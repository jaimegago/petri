package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// localProvisioner implements Provisioner using the local filesystem.
// Repositories are created as regular git repos under a base directory
// and exposed via file:// clone URLs. Used as a fallback for local labs
// when no GitHub token is available.
type localProvisioner struct {
	baseDir string
	ops     repoOps
}

// NewLocalFS returns a Provisioner that creates git repositories on the local
// filesystem under baseDir. Each repo is accessible via a file:// URL.
func NewLocalFS(baseDir string) Provisioner {
	return &localProvisioner{baseDir: baseDir, ops: &cliRepo{}}
}

// Create initialises a local git repository, writes commit history, and
// returns a RepoInfo with a file:// clone URL.
func (p *localProvisioner) Create(ctx context.Context, opts CreateOptions) (*RepoInfo, error) {
	if err := os.MkdirAll(p.baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating repos base dir: %w", err)
	}

	repoDir := filepath.Join(p.baseDir, opts.Name)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating repo dir: %w", err)
	}

	branch := opts.DefaultBranch
	if branch == "" {
		branch = "main"
	}

	if err := p.ops.init(ctx, repoDir, branch); err != nil {
		return nil, fmt.Errorf("git init: %w", err)
	}

	commitList := opts.Commits
	if len(commitList) == 0 {
		commitList = bootstrapCommits()
	}

	for i, spec := range commitList {
		if err := writeCommitFiles(repoDir, spec, i); err != nil {
			_ = os.RemoveAll(repoDir)
			return nil, fmt.Errorf("writing files for commit %d: %w", i, err)
		}
		if err := p.ops.addAll(ctx, repoDir); err != nil {
			_ = os.RemoveAll(repoDir)
			return nil, fmt.Errorf("git add for commit %d: %w", i, err)
		}
		if err := p.ops.commit(ctx, repoDir, spec); err != nil {
			_ = os.RemoveAll(repoDir)
			return nil, fmt.Errorf("git commit %d %q: %w", i, spec.Message, err)
		}
	}

	cloneURL := "file://" + repoDir
	return &RepoInfo{
		Name:          opts.Name,
		FullName:      opts.Name,
		CloneURL:      cloneURL,
		SSHURL:        cloneURL,
		DefaultBranch: branch,
	}, nil
}

// Delete removes the local repository directory.
func (p *localProvisioner) Delete(_ context.Context, opts DeleteOptions) error {
	repoDir := filepath.Join(p.baseDir, opts.Name)
	if err := os.RemoveAll(repoDir); err != nil {
		return fmt.Errorf("removing local repo %s: %w", repoDir, err)
	}
	return nil
}
