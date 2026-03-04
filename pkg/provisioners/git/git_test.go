package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/generators/commits"
	"github.com/jaimegago/petri/pkg/types"
)

// ─── mock helpers ─────────────────────────────────────────────────────────────

type mockGitHubClient struct {
	createRepoFn    func(ctx context.Context, req createRepoRequest) (*RepoInfo, error)
	deleteRepoFn    func(ctx context.Context, owner, name string) error
	deleteCallCount int
}

func (m *mockGitHubClient) createRepo(ctx context.Context, req createRepoRequest) (*RepoInfo, error) {
	return m.createRepoFn(ctx, req)
}

func (m *mockGitHubClient) deleteRepo(ctx context.Context, owner, name string) error {
	m.deleteCallCount++
	if m.deleteRepoFn != nil {
		return m.deleteRepoFn(ctx, owner, name)
	}
	return nil
}

// successRepoInfo returns a stub RepoInfo for happy-path mocks.
func successRepoInfo(owner, name string) *RepoInfo {
	return &RepoInfo{
		Name:     name,
		FullName: owner + "/" + name,
		CloneURL: "https://github.com/" + owner + "/" + name + ".git",
		SSHURL:   "git@github.com:" + owner + "/" + name + ".git",
	}
}

type mockRepoOps struct {
	initFn      func(ctx context.Context, dir, branch string) error
	addRemoteFn func(ctx context.Context, dir, name, url string) error
	addAllFn    func(ctx context.Context, dir string) error
	commitFn    func(ctx context.Context, dir string, spec commits.CommitSpec) error
	pushFn      func(ctx context.Context, dir, remote, branch string) error
}

func (m *mockRepoOps) init(ctx context.Context, dir, branch string) error {
	if m.initFn != nil {
		return m.initFn(ctx, dir, branch)
	}
	return nil
}

func (m *mockRepoOps) addRemote(ctx context.Context, dir, name, url string) error {
	if m.addRemoteFn != nil {
		return m.addRemoteFn(ctx, dir, name, url)
	}
	return nil
}

func (m *mockRepoOps) addAll(ctx context.Context, dir string) error {
	if m.addAllFn != nil {
		return m.addAllFn(ctx, dir)
	}
	return nil
}

func (m *mockRepoOps) commit(ctx context.Context, dir string, spec commits.CommitSpec) error {
	if m.commitFn != nil {
		return m.commitFn(ctx, dir, spec)
	}
	return nil
}

func (m *mockRepoOps) push(ctx context.Context, dir, remote, branch string) error {
	if m.pushFn != nil {
		return m.pushFn(ctx, dir, remote, branch)
	}
	return nil
}

// noopOps returns a mockRepoOps that succeeds for all operations.
func noopOps() *mockRepoOps { return &mockRepoOps{} }

// ─── Provisioner tests ────────────────────────────────────────────────────────

func TestProvisionerCreate(t *testing.T) {
	baseCommits := []commits.CommitSpec{
		{
			Message:   "init: setup",
			Author:    types.Author{Name: "Alice", Email: "alice@example.com"},
			Timestamp: time.Now().Add(-24 * time.Hour),
			Files:     map[string]string{"README.md": "hello"},
		},
	}

	tests := []struct {
		name            string
		opts            CreateOptions
		clientFn        func() *mockGitHubClient
		opsFn           func() *mockRepoOps
		wantErr         bool
		wantDeleteCalls int
	}{
		{
			name: "success with commits",
			opts: CreateOptions{
				Owner:   "octocat",
				Name:    "my-repo",
				Commits: baseCommits,
			},
			clientFn: func() *mockGitHubClient {
				return &mockGitHubClient{
					createRepoFn: func(_ context.Context, req createRepoRequest) (*RepoInfo, error) {
						return successRepoInfo(req.Owner, req.Name), nil
					},
				}
			},
			opsFn:           noopOps,
			wantErr:         false,
			wantDeleteCalls: 0,
		},
		{
			name: "success with no commits uses bootstrap",
			opts: CreateOptions{Owner: "octocat", Name: "empty-repo"},
			clientFn: func() *mockGitHubClient {
				return &mockGitHubClient{
					createRepoFn: func(_ context.Context, req createRepoRequest) (*RepoInfo, error) {
						return successRepoInfo(req.Owner, req.Name), nil
					},
				}
			},
			opsFn:           noopOps,
			wantErr:         false,
			wantDeleteCalls: 0,
		},
		{
			name: "github create fails - no cleanup needed",
			opts: CreateOptions{Owner: "octocat", Name: "fail-repo"},
			clientFn: func() *mockGitHubClient {
				return &mockGitHubClient{
					createRepoFn: func(_ context.Context, _ createRepoRequest) (*RepoInfo, error) {
						return nil, errors.New("bad credentials")
					},
				}
			},
			opsFn:           noopOps,
			wantErr:         true,
			wantDeleteCalls: 0,
		},
		{
			name: "git init fails - repo is deleted",
			opts: CreateOptions{Owner: "octocat", Name: "init-fail"},
			clientFn: func() *mockGitHubClient {
				return &mockGitHubClient{
					createRepoFn: func(_ context.Context, req createRepoRequest) (*RepoInfo, error) {
						return successRepoInfo(req.Owner, req.Name), nil
					},
				}
			},
			opsFn: func() *mockRepoOps {
				return &mockRepoOps{
					initFn: func(_ context.Context, _, _ string) error {
						return errors.New("git not found")
					},
				}
			},
			wantErr:         true,
			wantDeleteCalls: 1,
		},
		{
			name: "git commit fails - repo is deleted",
			opts: CreateOptions{
				Owner:   "octocat",
				Name:    "commit-fail",
				Commits: baseCommits,
			},
			clientFn: func() *mockGitHubClient {
				return &mockGitHubClient{
					createRepoFn: func(_ context.Context, req createRepoRequest) (*RepoInfo, error) {
						return successRepoInfo(req.Owner, req.Name), nil
					},
				}
			},
			opsFn: func() *mockRepoOps {
				return &mockRepoOps{
					commitFn: func(_ context.Context, _ string, _ commits.CommitSpec) error {
						return errors.New("commit failed")
					},
				}
			},
			wantErr:         true,
			wantDeleteCalls: 1,
		},
		{
			name: "git push fails - repo is deleted",
			opts: CreateOptions{
				Owner:   "octocat",
				Name:    "push-fail",
				Commits: baseCommits,
			},
			clientFn: func() *mockGitHubClient {
				return &mockGitHubClient{
					createRepoFn: func(_ context.Context, req createRepoRequest) (*RepoInfo, error) {
						return successRepoInfo(req.Owner, req.Name), nil
					},
				}
			},
			opsFn: func() *mockRepoOps {
				return &mockRepoOps{
					pushFn: func(_ context.Context, _, _, _ string) error {
						return errors.New("push rejected")
					},
				}
			},
			wantErr:         true,
			wantDeleteCalls: 1,
		},
		{
			name: "uses org endpoint when IsOrg true",
			opts: CreateOptions{Owner: "my-org", Name: "org-repo", IsOrg: true},
			clientFn: func() *mockGitHubClient {
				return &mockGitHubClient{
					createRepoFn: func(_ context.Context, req createRepoRequest) (*RepoInfo, error) {
						if !req.IsOrg {
							return nil, fmt.Errorf("expected IsOrg=true")
						}
						return successRepoInfo(req.Owner, req.Name), nil
					},
				}
			},
			opsFn:           noopOps,
			wantErr:         false,
			wantDeleteCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.clientFn()
			ops := tt.opsFn()
			p := newWithDeps(Config{Token: "tok"}, client, ops)

			info, err := p.Create(context.Background(), tt.opts)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if info == nil {
					t.Fatal("expected non-nil RepoInfo")
				}
				if info.DefaultBranch == "" {
					t.Error("DefaultBranch should be set")
				}
			}

			if client.deleteCallCount != tt.wantDeleteCalls {
				t.Errorf("deleteRepo called %d times, want %d",
					client.deleteCallCount, tt.wantDeleteCalls)
			}
		})
	}
}

func TestProvisionerDelete(t *testing.T) {
	tests := []struct {
		name         string
		deleteRepoFn func(ctx context.Context, owner, name string) error
		wantErr      bool
	}{
		{
			name:         "success",
			deleteRepoFn: func(_ context.Context, _, _ string) error { return nil },
			wantErr:      false,
		},
		{
			name:         "api error",
			deleteRepoFn: func(_ context.Context, _, _ string) error { return errors.New("not found") },
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockGitHubClient{deleteRepoFn: tt.deleteRepoFn}
			p := newWithDeps(Config{}, client, noopOps())

			err := p.Delete(context.Background(), DeleteOptions{Owner: "o", Name: "r"})
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestProvisionerCreate_DefaultBranch(t *testing.T) {
	var capturedBranch string
	client := &mockGitHubClient{
		createRepoFn: func(_ context.Context, req createRepoRequest) (*RepoInfo, error) {
			return successRepoInfo(req.Owner, req.Name), nil
		},
	}
	ops := &mockRepoOps{
		initFn: func(_ context.Context, _, branch string) error {
			capturedBranch = branch
			return nil
		},
	}

	p := newWithDeps(Config{}, client, ops)
	_, _ = p.Create(context.Background(), CreateOptions{Owner: "o", Name: "r"})

	if capturedBranch != "main" {
		t.Errorf("expected default branch 'main', got %q", capturedBranch)
	}

	capturedBranch = ""
	_, _ = p.Create(context.Background(), CreateOptions{
		Owner: "o", Name: "r", DefaultBranch: "trunk",
	})
	if capturedBranch != "trunk" {
		t.Errorf("expected branch 'trunk', got %q", capturedBranch)
	}
}

// ─── helper function tests ────────────────────────────────────────────────────

func TestEmbedToken(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		token    string
		expected string
	}{
		{
			name:     "injects token",
			url:      "https://github.com/owner/repo.git",
			token:    "ghp_secret",
			expected: "https://ghp_secret@github.com/owner/repo.git",
		},
		{
			name:     "empty token returns url unchanged",
			url:      "https://github.com/owner/repo.git",
			token:    "",
			expected: "https://github.com/owner/repo.git",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := embedToken(tt.url, tt.token)
			if got != tt.expected {
				t.Errorf("embedToken(%q, %q) = %q, want %q", tt.url, tt.token, got, tt.expected)
			}
		})
	}
}

func TestWriteCommitFiles(t *testing.T) {
	dir := t.TempDir()

	spec := commits.CommitSpec{
		Message:   "init: test",
		Timestamp: time.Now(),
		Files: map[string]string{
			"src/main.go": "package main\n",
			"README.md":   "# Test\n",
		},
	}

	if err := writeCommitFiles(dir, spec, 0); err != nil {
		t.Fatalf("writeCommitFiles: %v", err)
	}

	// .petri-meta must exist with commit sequence.
	meta, err := os.ReadFile(filepath.Join(dir, ".petri-meta"))
	if err != nil {
		t.Fatalf("reading .petri-meta: %v", err)
	}
	if !strings.Contains(string(meta), "commit-sequence: 0") {
		t.Errorf(".petri-meta missing sequence: %s", meta)
	}

	// Other files must exist.
	for _, path := range []string{"src/main.go", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path))); err != nil {
			t.Errorf("expected file %s: %v", path, err)
		}
	}
}

func TestWriteCommitFiles_UniquePerIndex(t *testing.T) {
	dir := t.TempDir()
	spec := commits.CommitSpec{
		Message:   "chore: update",
		Timestamp: time.Now(),
		Files:     map[string]string{".petri-meta": "generated-by: petri\n"},
	}

	for i := 0; i < 3; i++ {
		if err := writeCommitFiles(dir, spec, i); err != nil {
			t.Fatalf("writeCommitFiles idx %d: %v", i, err)
		}
		content, _ := os.ReadFile(filepath.Join(dir, ".petri-meta"))
		seq := fmt.Sprintf("commit-sequence: %d", i)
		if !strings.Contains(string(content), seq) {
			t.Errorf("idx %d: .petri-meta missing %q: %s", i, seq, content)
		}
	}
}

func TestBootstrapCommits(t *testing.T) {
	cs := bootstrapCommits()
	if len(cs) != 1 {
		t.Fatalf("expected 1 bootstrap commit, got %d", len(cs))
	}
	if cs[0].Message == "" {
		t.Error("bootstrap commit has empty message")
	}
	if len(cs[0].Files) == 0 {
		t.Error("bootstrap commit has no files")
	}
}

// ─── GitHub API client tests ──────────────────────────────────────────────────

func githubRepoResponse(owner, name string) map[string]any {
	return map[string]any{
		"name":           name,
		"full_name":      owner + "/" + name,
		"clone_url":      "https://github.com/" + owner + "/" + name + ".git",
		"ssh_url":        "git@github.com:" + owner + "/" + name + ".git",
		"default_branch": "main",
	}
}

func TestAPIClientCreateRepo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/user/repos") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(githubRepoResponse("octocat", "hello"))
	}))
	defer srv.Close()

	c := newAPIClient("tok", srv.URL)
	info, err := c.createRepo(context.Background(), createRepoRequest{
		Owner: "octocat", Name: "hello",
	})
	if err != nil {
		t.Fatalf("createRepo: %v", err)
	}
	if info.Name != "hello" {
		t.Errorf("Name = %q, want %q", info.Name, "hello")
	}
	if info.FullName != "octocat/hello" {
		t.Errorf("FullName = %q, want %q", info.FullName, "octocat/hello")
	}
}

func TestAPIClientCreateRepo_OrgEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(githubRepoResponse("my-org", "repo"))
	}))
	defer srv.Close()

	c := newAPIClient("tok", srv.URL)
	_, err := c.createRepo(context.Background(), createRepoRequest{
		Owner: "my-org", Name: "repo", IsOrg: true,
	})
	if err != nil {
		t.Fatalf("createRepo: %v", err)
	}
	if gotPath != "/orgs/my-org/repos" {
		t.Errorf("expected /orgs/my-org/repos, got %s", gotPath)
	}
}

func TestAPIClientCreateRepo_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Repository creation failed.",
			"errors": []map[string]any{
				{"field": "name", "message": "name already exists on this account"},
			},
		})
	}))
	defer srv.Close()

	c := newAPIClient("tok", srv.URL)
	_, err := c.createRepo(context.Background(), createRepoRequest{Name: "dup"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "name already exists") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestAPIClientDeleteRepo_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newAPIClient("tok", srv.URL)
	if err := c.deleteRepo(context.Background(), "octocat", "hello"); err != nil {
		t.Fatalf("deleteRepo: %v", err)
	}
	if gotPath != "/repos/octocat/hello" {
		t.Errorf("path = %q, want /repos/octocat/hello", gotPath)
	}
}

func TestAPIClientDeleteRepo_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
	}))
	defer srv.Close()

	c := newAPIClient("tok", srv.URL)
	err := c.deleteRepo(context.Background(), "o", "missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAPIClientCheckRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "API rate limit exceeded",
		})
	}))
	defer srv.Close()

	c := newAPIClient("tok", srv.URL)
	_, err := c.createRepo(context.Background(), createRepoRequest{Name: "r"})
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAPIClientAuthHeaders(t *testing.T) {
	var gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(githubRepoResponse("u", "r"))
	}))
	defer srv.Close()

	c := newAPIClient("my-token", srv.URL)
	_, _ = c.createRepo(context.Background(), createRepoRequest{Name: "r"})

	if gotAuth != "Bearer my-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer my-token")
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want %q", gotAccept, "application/vnd.github+json")
	}
}

// ─── Integration tests (requires git CLI) ─────────────────────────────────────

// TestCLIRepo_IntegrationCreate tests a real local git repository is initialised
// and commits are created with the correct author and backdated timestamps.
// Skipped if the git CLI is not available.
func TestCLIRepo_IntegrationCreate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}

	dir := t.TempDir()
	r := &cliRepo{}
	ctx := context.Background()

	if err := r.init(ctx, dir, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Write a file.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.addAll(ctx, dir); err != nil {
		t.Fatalf("addAll: %v", err)
	}

	past := time.Now().Add(-7 * 24 * time.Hour)
	spec := commits.CommitSpec{
		Message:   "init: setup",
		Author:    types.Author{Name: "Alice", Email: "alice@example.com"},
		Timestamp: past,
	}
	if err := r.commit(ctx, dir, spec); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify git log shows the backdated commit.
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "log",
		"--format=%ae %ai", "-1").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	logLine := strings.TrimSpace(string(out))
	if !strings.Contains(logLine, "alice@example.com") {
		t.Errorf("expected alice@example.com in log, got: %s", logLine)
	}
	// The commit date should be roughly 7 days ago (within 1 hour tolerance).
	if !strings.Contains(logLine, past.Format("2006-01-02")) {
		t.Errorf("expected date %s in log, got: %s", past.Format("2006-01-02"), logLine)
	}
}

func TestCLIRepo_IntegrationMultipleCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}

	dir := t.TempDir()
	r := &cliRepo{}
	ctx := context.Background()

	if err := r.init(ctx, dir, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}

	authors := []types.Author{
		{Name: "Alice", Email: "alice@example.com"},
		{Name: "Bob", Email: "bob@example.com"},
	}
	now := time.Now()

	for i := 0; i < 3; i++ {
		ts := now.Add(time.Duration(-(3-i)) * 24 * time.Hour)
		spec := commits.CommitSpec{
			Message:   fmt.Sprintf("commit %d", i),
			Author:    authors[i%2],
			Timestamp: ts,
			Files:     map[string]string{fmt.Sprintf("file%d.txt", i): "content"},
		}
		if err := writeCommitFiles(dir, spec, i); err != nil {
			t.Fatalf("writeCommitFiles %d: %v", i, err)
		}
		if err := r.addAll(ctx, dir); err != nil {
			t.Fatalf("addAll %d: %v", i, err)
		}
		if err := r.commit(ctx, dir, spec); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	// Confirm 3 commits were created.
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-list: %v", err)
	}
	if strings.TrimSpace(string(out)) != "3" {
		t.Errorf("expected 3 commits, got: %s", strings.TrimSpace(string(out)))
	}
}
