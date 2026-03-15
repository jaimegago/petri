package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	gitprov "github.com/jaimegago/petri/pkg/provisioners/git"
	"github.com/jaimegago/petri/pkg/types"
)

// StartCleanupLoop runs a background goroutine that periodically destroys labs
// whose TTL has expired. It returns when ctx is cancelled.
func (o *Orchestrator) StartCleanupLoop(ctx context.Context, interval, gracePeriod time.Duration) {
	o.log.Info("Starting cleanup loop", "interval", interval, "grace_period", gracePeriod)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.log.Info("Cleanup loop stopped")
			return
		case <-ticker.C:
			o.runCleanup(ctx, gracePeriod)
		}
	}
}

// runCleanup finds expired labs and destroys them.
func (o *Orchestrator) runCleanup(ctx context.Context, gracePeriod time.Duration) {
	labs, err := o.deps.State.FindExpiredLabs(ctx, gracePeriod)
	if err != nil {
		o.log.Error("Cleanup: failed to find expired labs", "error", err)
		return
	}

	if len(labs) == 0 {
		return
	}

	o.log.Info("Cleanup: found expired labs", "count", len(labs))

	for _, lab := range labs {
		o.destroyExpiredLab(ctx, lab)
	}
}

// destroyExpiredLab transitions a single expired lab through DESTROYING → DESTROYED.
func (o *Orchestrator) destroyExpiredLab(ctx context.Context, lab *types.Lab) {
	log := o.log.With("lab", lab.Name)

	if !lab.CanTransitionTo(types.LabStatusDestroying) {
		log.Warn("Cleanup: lab cannot be destroyed; skipping", "status", string(lab.Status))
		return
	}

	lab.Status = types.LabStatusDestroying
	if err := o.deps.State.UpdateLab(ctx, lab); err != nil {
		log.Error("Cleanup: failed to mark lab as DESTROYING", "error", err)
		return
	}

	// We don't have company/spec context here, so we can only clean up
	// what we know from the lab metadata (cluster names, git repos).
	if err := o.destroyFromMetadata(ctx, lab); err != nil {
		log.Error("Cleanup: infrastructure teardown failed", "error", err)
		lab.Status = types.LabStatusError
		lab.Metadata.ErrorMessage = fmt.Sprintf("auto-cleanup failed: %v", err)
		_ = o.deps.State.UpdateLab(ctx, lab)
		return
	}

	_ = o.deps.State.DeleteResources(ctx, lab.ID)
	_ = o.deps.State.DeleteCredentials(ctx, lab.ID)
	o.removeLabWorkDir(lab.ID.String())

	lab.Status = types.LabStatusDestroyed
	if err := o.deps.State.UpdateLab(ctx, lab); err != nil {
		log.Error("Cleanup: failed to mark lab as DESTROYED", "error", err)
		return
	}

	log.Info("Cleanup: lab destroyed")
}

// destroyFromMetadata tears down resources using only lab metadata (no company profile needed).
// Used by the auto-cleanup loop which doesn't have access to the full company config.
func (o *Orchestrator) destroyFromMetadata(ctx context.Context, lab *types.Lab) error {
	var errs []string

	// Delete kind clusters for local labs.
	if lab.CloudProvider == types.CloudProviderLocal && o.deps.LocalProv != nil {
		for _, cluster := range lab.Metadata.Clusters {
			name := cluster.Name
			if name == "" {
				name = lab.Name
			}
			if err := o.deps.LocalProv.Delete(ctx, name); err != nil {
				errs = append(errs, fmt.Sprintf("delete cluster %s: %v", name, err))
			}
		}
	}

	// Delete git repositories. Local file:// repos are removed by removeLabWorkDir.
	if o.deps.GitProv != nil {
		for _, repo := range lab.Metadata.GitRepos {
			if strings.HasPrefix(repo.URL, "file://") {
				continue
			}
			owner := extractOwnerFromURL(repo.URL)
			if owner == "" {
				continue
			}
			if err := o.deps.GitProv.Delete(ctx, gitprov.DeleteOptions{Owner: owner, Name: repo.Name}); err != nil {
				errs = append(errs, fmt.Sprintf("delete repo %s: %v", repo.Name, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", joinErrs(errs))
	}
	return nil
}

// extractOwnerFromURL parses the GitHub owner from a clone URL.
// e.g. https://github.com/owner/repo.git → owner
func extractOwnerFromURL(cloneURL string) string {
	// Strip scheme and host: https://github.com/owner/repo.git
	const githubPrefix = "https://github.com/"
	if len(cloneURL) > len(githubPrefix) && cloneURL[:len(githubPrefix)] == githubPrefix {
		rest := cloneURL[len(githubPrefix):]
		if idx := indexByte(rest, '/'); idx > 0 {
			return rest[:idx]
		}
	}
	return ""
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func joinErrs(errs []string) string {
	result := ""
	for i, e := range errs {
		if i > 0 {
			result += "; "
		}
		result += e
	}
	return result
}
