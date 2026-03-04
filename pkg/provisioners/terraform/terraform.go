// Package terraform wraps the terraform CLI for IaC provisioning.
package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Config holds settings for the Terraform provisioner.
type Config struct {
	// Binary is the terraform binary path. Defaults to "terraform".
	Binary string
}

// InitOptions configures a terraform init run.
type InitOptions struct {
	// WorkDir is the directory containing the terraform configuration files.
	WorkDir string
	// BackendConfig configures a remote state backend.
	// When nil the backend declared in the configuration files is used as-is.
	BackendConfig *BackendConfig
	// Env contains additional environment variables (e.g. cloud credentials).
	Env []string
}

// PlanOptions configures a terraform plan run.
type PlanOptions struct {
	WorkDir string
	// VarFile is an optional path to a .tfvars file.
	VarFile string
	Env     []string
}

// PlanResult summarises the outcome of a terraform plan.
type PlanResult struct {
	// HasChanges is true when the plan includes at least one resource change.
	HasChanges bool
	// Summary is the human-readable plan summary line from terraform output.
	Summary string
}

// ApplyOptions configures a terraform apply run.
type ApplyOptions struct {
	WorkDir string
	VarFile string
	Env     []string
}

// ApplyResult contains outputs and tracked resources from a successful apply.
type ApplyResult struct {
	Outputs   map[string]OutputValue
	Resources []ResourceInfo
}

// DestroyOptions configures a terraform destroy run.
type DestroyOptions struct {
	WorkDir string
	VarFile string
	Env     []string
}

// OutputValue holds a single terraform output value.
type OutputValue struct {
	Value     json.RawMessage `json:"value"`
	Type      json.RawMessage `json:"type"`
	Sensitive bool            `json:"sensitive"`
}

// ResourceInfo describes a cloud resource tracked from the Terraform state.
type ResourceInfo struct {
	// Address is the full resource address, e.g. "aws_eks_cluster.main".
	Address    string `json:"address"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	ResourceID string `json:"resource_id"`
}

// Provisioner wraps the Terraform CLI for lab infrastructure provisioning.
type Provisioner interface {
	// Init initialises the working directory (terraform init).
	Init(ctx context.Context, opts InitOptions) error
	// Plan generates an execution plan (terraform plan).
	Plan(ctx context.Context, opts PlanOptions) (*PlanResult, error)
	// Apply provisions infrastructure (terraform apply --auto-approve).
	Apply(ctx context.Context, opts ApplyOptions) (*ApplyResult, error)
	// Destroy tears down infrastructure (terraform destroy --auto-approve).
	Destroy(ctx context.Context, opts DestroyOptions) error
	// Output fetches terraform outputs from the working directory.
	Output(ctx context.Context, workDir string, env []string) (map[string]OutputValue, error)
}

// tfRunner abstracts terraform CLI invocations so tests can inject a fake.
type tfRunner interface {
	// exec runs a terraform command, discarding stdout.
	exec(ctx context.Context, workDir string, env []string, args ...string) error
	// capture runs a terraform command and returns stdout.
	capture(ctx context.Context, workDir string, env []string, args ...string) (string, error)
}

type provisioner struct {
	cfg    Config
	runner tfRunner
}

// New returns a Provisioner that shells out to the real terraform CLI.
func New(cfg Config) Provisioner {
	bin := cfg.Binary
	if bin == "" {
		bin = "terraform"
	}
	return &provisioner{
		cfg:    cfg,
		runner: &cliRunner{binary: bin},
	}
}

// newWithRunner injects a runner dependency; used in tests.
func newWithRunner(r tfRunner) Provisioner {
	return &provisioner{runner: r}
}

// Init runs terraform init, writing a backend override file when BackendConfig
// is provided.
func (p *provisioner) Init(ctx context.Context, opts InitOptions) error {
	if opts.BackendConfig != nil {
		if err := writeBackendOverride(opts.WorkDir, opts.BackendConfig); err != nil {
			return fmt.Errorf("writing backend config: %w", err)
		}
	}
	args := []string{"init", "-reconfigure", "-no-color", "-input=false"}
	if err := p.runner.exec(ctx, opts.WorkDir, opts.Env, args...); err != nil {
		return fmt.Errorf("terraform init in %s: %w", opts.WorkDir, err)
	}
	return nil
}

// Plan runs terraform plan and reports whether infrastructure changes are pending.
func (p *provisioner) Plan(ctx context.Context, opts PlanOptions) (*PlanResult, error) {
	args := []string{"plan", "-no-color", "-input=false"}
	if opts.VarFile != "" {
		args = append(args, "-var-file", opts.VarFile)
	}
	out, err := p.runner.capture(ctx, opts.WorkDir, opts.Env, args...)
	if err != nil {
		return nil, fmt.Errorf("terraform plan in %s: %w", opts.WorkDir, err)
	}
	return &PlanResult{
		HasChanges: !strings.Contains(out, "No changes."),
		Summary:    extractPlanSummary(out),
	}, nil
}

// Apply provisions infrastructure and returns outputs and tracked resources.
// Failures in output or resource collection are non-fatal; partial results are
// returned along with a nil error.
func (p *provisioner) Apply(ctx context.Context, opts ApplyOptions) (*ApplyResult, error) {
	args := []string{"apply", "-auto-approve", "-no-color", "-input=false"}
	if opts.VarFile != "" {
		args = append(args, "-var-file", opts.VarFile)
	}
	if err := p.runner.exec(ctx, opts.WorkDir, opts.Env, args...); err != nil {
		return nil, fmt.Errorf("terraform apply in %s: %w", opts.WorkDir, err)
	}

	// Fetch outputs — non-fatal on error.
	outputs, err := p.Output(ctx, opts.WorkDir, opts.Env)
	if err != nil {
		outputs = map[string]OutputValue{}
	}

	// Fetch resource list for state tracking — non-fatal on error.
	resources, _ := p.listResources(ctx, opts.WorkDir, opts.Env)

	return &ApplyResult{Outputs: outputs, Resources: resources}, nil
}

// Destroy tears down all infrastructure in the working directory.
func (p *provisioner) Destroy(ctx context.Context, opts DestroyOptions) error {
	args := []string{"destroy", "-auto-approve", "-no-color", "-input=false"}
	if opts.VarFile != "" {
		args = append(args, "-var-file", opts.VarFile)
	}
	if err := p.runner.exec(ctx, opts.WorkDir, opts.Env, args...); err != nil {
		return fmt.Errorf("terraform destroy in %s: %w", opts.WorkDir, err)
	}
	return nil
}

// Output returns the current terraform outputs as a map.
func (p *provisioner) Output(ctx context.Context, workDir string, env []string) (map[string]OutputValue, error) {
	out, err := p.runner.capture(ctx, workDir, env, "output", "-json")
	if err != nil {
		return nil, fmt.Errorf("terraform output in %s: %w", workDir, err)
	}
	outputs, err := parseOutputs(out)
	if err != nil {
		return nil, fmt.Errorf("parsing terraform outputs: %w", err)
	}
	return outputs, nil
}

// listResources extracts tracked resource info from the Terraform state.
func (p *provisioner) listResources(ctx context.Context, workDir string, env []string) ([]ResourceInfo, error) {
	out, err := p.runner.capture(ctx, workDir, env, "show", "-json")
	if err != nil {
		return nil, fmt.Errorf("terraform show in %s: %w", workDir, err)
	}
	return parseResources(out)
}
