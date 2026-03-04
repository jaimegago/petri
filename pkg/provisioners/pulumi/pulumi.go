// Package pulumi wraps the Pulumi CLI for IaC provisioning.
package pulumi

import (
	"context"
	"encoding/json"
	"fmt"
)

// Config holds settings for the Pulumi provisioner.
type Config struct {
	// Binary is the pulumi binary path. Defaults to "pulumi".
	Binary string
}

// InitOptions configures pulumi login and stack initialisation.
type InitOptions struct {
	// WorkDir is the directory containing the Pulumi project files.
	WorkDir string
	// StackName is the Pulumi stack to create or select (e.g. "dev").
	StackName string
	// BackendURL is the state backend URL. Examples:
	//   "s3://my-bucket"         (AWS S3)
	//   "gs://my-bucket"         (GCP Cloud Storage)
	//   "azblob://my-container"  (Azure Blob Storage)
	//   "file://."               (local filesystem)
	// When empty the PULUMI_BACKEND_URL environment variable is used.
	BackendURL string
	// Passphrase encrypts stack secrets; mapped to PULUMI_CONFIG_PASSPHRASE.
	// When empty the env var must already be set by the caller.
	Passphrase string
	// Env contains additional environment variables (e.g. cloud credentials).
	Env []string
}

// PreviewOptions configures a pulumi preview run.
type PreviewOptions struct {
	// WorkDir is the directory containing the Pulumi project files.
	WorkDir string
	// StackName selects the stack to preview.
	StackName string
	// Env contains additional environment variables.
	Env []string
}

// PreviewResult summarises the outcome of a pulumi preview.
type PreviewResult struct {
	// HasChanges is true when the preview reports at least one resource change.
	HasChanges bool
	// Summary is the human-readable resource summary from the preview output.
	Summary string
}

// UpOptions configures a pulumi up run.
type UpOptions struct {
	// WorkDir is the directory containing the Pulumi project files.
	WorkDir string
	// StackName selects the stack to update.
	StackName string
	// Env contains additional environment variables (e.g. cloud credentials).
	Env []string
}

// UpResult contains outputs and tracked resources from a successful up.
type UpResult struct {
	Outputs   map[string]OutputValue
	Resources []ResourceInfo
}

// DestroyOptions configures a pulumi destroy run.
type DestroyOptions struct {
	// WorkDir is the directory containing the Pulumi project files.
	WorkDir string
	// StackName selects the stack to destroy.
	StackName string
	// Env contains additional environment variables (e.g. cloud credentials).
	Env []string
}

// StackRemoveOptions configures a pulumi stack rm run.
type StackRemoveOptions struct {
	// WorkDir is the directory containing the Pulumi project files.
	WorkDir string
	// StackName is the stack to remove.
	StackName string
	// Force removes the stack even when resources remain.
	Force bool
	// Env contains additional environment variables.
	Env []string
}

// OutputOptions configures a pulumi stack output run.
type OutputOptions struct {
	// WorkDir is the directory containing the Pulumi project files.
	WorkDir string
	// StackName selects the stack whose outputs are fetched.
	StackName string
	// Env contains additional environment variables.
	Env []string
}

// OutputValue holds a single Pulumi stack output value as raw JSON.
type OutputValue struct {
	Value json.RawMessage
}

// ResourceInfo describes a cloud resource tracked from the Pulumi stack state.
type ResourceInfo struct {
	// URN is the fully-qualified Pulumi resource URN.
	URN        string
	Type       string
	Name       string
	ResourceID string
}

// Provisioner wraps the Pulumi CLI for lab infrastructure provisioning.
type Provisioner interface {
	// Init logs in to the state backend and creates or selects the stack.
	Init(ctx context.Context, opts InitOptions) error
	// Preview shows planned changes without applying them (pulumi preview).
	Preview(ctx context.Context, opts PreviewOptions) (*PreviewResult, error)
	// Up provisions or updates stack resources (pulumi up --yes).
	Up(ctx context.Context, opts UpOptions) (*UpResult, error)
	// Destroy tears down all stack resources (pulumi destroy --yes).
	Destroy(ctx context.Context, opts DestroyOptions) error
	// Output fetches the current stack outputs (pulumi stack output --json).
	Output(ctx context.Context, opts OutputOptions) (map[string]OutputValue, error)
	// StackRemove removes the stack after a destroy (pulumi stack rm --yes).
	StackRemove(ctx context.Context, opts StackRemoveOptions) error
}

// pulumiRunner abstracts pulumi CLI invocations so tests can inject a fake.
type pulumiRunner interface {
	exec(ctx context.Context, workDir string, env []string, args ...string) error
	capture(ctx context.Context, workDir string, env []string, args ...string) (string, error)
}

type provisioner struct {
	cfg    Config
	runner pulumiRunner
}

// New returns a Provisioner that shells out to the real pulumi CLI.
func New(cfg Config) Provisioner {
	bin := cfg.Binary
	if bin == "" {
		bin = "pulumi"
	}
	return &provisioner{
		cfg:    cfg,
		runner: &cliRunner{binary: bin},
	}
}

// newWithRunner injects a runner dependency; used in tests.
func newWithRunner(r pulumiRunner) Provisioner {
	return &provisioner{runner: r}
}

// Init logs in to the state backend and creates or selects the Pulumi stack.
// If the stack already exists it is selected rather than re-created.
func (p *provisioner) Init(ctx context.Context, opts InitOptions) error {
	env := mergeEnv(opts.Env, opts.BackendURL, opts.Passphrase)

	// Log in to the state backend.
	loginArgs := []string{"login"}
	if opts.BackendURL != "" {
		loginArgs = append(loginArgs, opts.BackendURL)
	}
	if err := p.runner.exec(ctx, opts.WorkDir, env, loginArgs...); err != nil {
		return fmt.Errorf("pulumi login: %w", err)
	}

	// Select or create the stack (idempotent).
	if err := p.selectOrInitStack(ctx, opts.WorkDir, opts.StackName, env); err != nil {
		return fmt.Errorf("stack %q: %w", opts.StackName, err)
	}
	return nil
}

// selectOrInitStack tries to select an existing stack; on failure it creates one.
func (p *provisioner) selectOrInitStack(ctx context.Context, workDir, stackName string, env []string) error {
	if err := p.runner.exec(ctx, workDir, env, "stack", "select", stackName); err == nil {
		return nil
	}
	return p.runner.exec(ctx, workDir, env, "stack", "init", stackName)
}

// Preview runs pulumi preview and reports whether resource changes are pending.
func (p *provisioner) Preview(ctx context.Context, opts PreviewOptions) (*PreviewResult, error) {
	args := []string{"preview", "--non-interactive", "--stack", opts.StackName}
	out, err := p.runner.capture(ctx, opts.WorkDir, opts.Env, args...)
	if err != nil {
		return nil, fmt.Errorf("pulumi preview stack %q: %w", opts.StackName, err)
	}
	return &PreviewResult{
		HasChanges: extractHasChanges(out),
		Summary:    extractPreviewSummary(out),
	}, nil
}

// Up provisions or updates all stack resources.
// Failures in output or resource collection are non-fatal; partial results
// are returned with a nil error.
func (p *provisioner) Up(ctx context.Context, opts UpOptions) (*UpResult, error) {
	args := []string{"up", "--yes", "--non-interactive", "--skip-preview", "--stack", opts.StackName}
	if err := p.runner.exec(ctx, opts.WorkDir, opts.Env, args...); err != nil {
		return nil, fmt.Errorf("pulumi up stack %q: %w", opts.StackName, err)
	}

	// Fetch outputs — non-fatal on error.
	outputs, err := p.Output(ctx, OutputOptions{
		WorkDir:   opts.WorkDir,
		StackName: opts.StackName,
		Env:       opts.Env,
	})
	if err != nil {
		outputs = map[string]OutputValue{}
	}

	// Fetch resource list for state tracking — non-fatal on error.
	resources, _ := p.listResources(ctx, opts.WorkDir, opts.StackName, opts.Env)

	return &UpResult{Outputs: outputs, Resources: resources}, nil
}

// Destroy tears down all resources in the named stack.
func (p *provisioner) Destroy(ctx context.Context, opts DestroyOptions) error {
	args := []string{"destroy", "--yes", "--non-interactive", "--stack", opts.StackName}
	if err := p.runner.exec(ctx, opts.WorkDir, opts.Env, args...); err != nil {
		return fmt.Errorf("pulumi destroy stack %q: %w", opts.StackName, err)
	}
	return nil
}

// Output fetches the current stack outputs as a map.
func (p *provisioner) Output(ctx context.Context, opts OutputOptions) (map[string]OutputValue, error) {
	args := []string{"stack", "output", "--json", "--stack", opts.StackName}
	out, err := p.runner.capture(ctx, opts.WorkDir, opts.Env, args...)
	if err != nil {
		return nil, fmt.Errorf("pulumi stack output %q: %w", opts.StackName, err)
	}
	return parseOutputs(out)
}

// StackRemove removes the named stack; the stack must be empty unless Force is set.
func (p *provisioner) StackRemove(ctx context.Context, opts StackRemoveOptions) error {
	args := []string{"stack", "rm", "--yes", "--stack", opts.StackName}
	if opts.Force {
		args = append(args, "--force")
	}
	if err := p.runner.exec(ctx, opts.WorkDir, opts.Env, args...); err != nil {
		return fmt.Errorf("pulumi stack rm %q: %w", opts.StackName, err)
	}
	return nil
}

// listResources fetches the full resource list from the Pulumi stack state export.
func (p *provisioner) listResources(ctx context.Context, workDir, stackName string, env []string) ([]ResourceInfo, error) {
	out, err := p.runner.capture(ctx, workDir, env, "stack", "export", "--stack", stackName)
	if err != nil {
		return nil, fmt.Errorf("pulumi stack export %q: %w", stackName, err)
	}
	return parseResources(out)
}

// mergeEnv builds the environment slice for Init, prepending BackendURL and
// Passphrase as PULUMI_* variables when non-empty.
func mergeEnv(extra []string, backendURL, passphrase string) []string {
	var env []string
	if backendURL != "" {
		env = append(env, "PULUMI_BACKEND_URL="+backendURL)
	}
	if passphrase != "" {
		env = append(env, "PULUMI_CONFIG_PASSPHRASE="+passphrase)
	}
	return append(env, extra...)
}
