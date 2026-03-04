package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jaimegago/petri/pkg/generators/commits"
)

// cliRepo implements repoOps by shelling out to the git CLI.
type cliRepo struct{}

// init initialises a new git repository and sets the default branch name.
// Supports Git 2.28+ (-b flag) with a fallback via symbolic-ref for older versions.
func (r *cliRepo) init(ctx context.Context, dir, branch string) error {
	// Try modern --initial-branch flag (Git 2.28+).
	if err := r.run(ctx, dir, nil, "git", "init", "-b", branch); err != nil {
		// Fall back: init then repoint HEAD manually.
		if err2 := r.run(ctx, dir, nil, "git", "init"); err2 != nil {
			return fmt.Errorf("git init: %w", err2)
		}
		if err3 := r.run(ctx, dir, nil, "git", "symbolic-ref", "HEAD", "refs/heads/"+branch); err3 != nil {
			return fmt.Errorf("setting default branch: %w", err3)
		}
	}
	// Set a local identity so commits work without a global git config.
	_ = r.run(ctx, dir, nil, "git", "config", "user.email", "petri@lab.local")
	_ = r.run(ctx, dir, nil, "git", "config", "user.name", "Petri")
	return nil
}

// addRemote runs `git remote add <name> <url>`.
func (r *cliRepo) addRemote(ctx context.Context, dir, name, url string) error {
	if err := r.run(ctx, dir, nil, "git", "remote", "add", name, url); err != nil {
		return fmt.Errorf("git remote add %s: %w", name, err)
	}
	return nil
}

// addAll stages all changes in the working tree.
func (r *cliRepo) addAll(ctx context.Context, dir string) error {
	if err := r.run(ctx, dir, nil, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add -A: %w", err)
	}
	return nil
}

// commit creates a git commit using the author and timestamp from spec.
// GIT_AUTHOR_DATE and GIT_COMMITTER_DATE are both set to produce accurate
// backdated history.
func (r *cliRepo) commit(ctx context.Context, dir string, spec commits.CommitSpec) error {
	name := spec.Author.Name
	email := spec.Author.Email
	if name == "" {
		name = "Petri Bot"
	}
	if email == "" {
		email = "petri@lab.local"
	}

	ts := spec.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	dateStr := ts.Format(time.RFC3339)
	authorStr := fmt.Sprintf("%s <%s>", name, email)

	extra := []string{
		"GIT_AUTHOR_DATE=" + dateStr,
		"GIT_COMMITTER_DATE=" + dateStr,
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}

	if err := r.run(ctx, dir, extra,
		"git", "commit",
		"--author", authorStr,
		"--date", dateStr,
		"-m", spec.Message,
	); err != nil {
		return fmt.Errorf("git commit %q: %w", spec.Message, err)
	}
	return nil
}

// push pushes local branch to the named remote.
func (r *cliRepo) push(ctx context.Context, dir, remote, branch string) error {
	if err := r.run(ctx, dir, nil, "git", "push", remote, branch); err != nil {
		return fmt.Errorf("git push %s %s: %w", remote, branch, err)
	}
	return nil
}

// run executes a command in dir with optional extra environment variables.
// GIT_TERMINAL_PROMPT=0 is always set to prevent credential prompts from
// hanging the process.
func (r *cliRepo) run(ctx context.Context, dir string, extraEnv []string, name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, extraEnv...)

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%s %v: %w: %s", name, args, err, msg)
		}
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}
