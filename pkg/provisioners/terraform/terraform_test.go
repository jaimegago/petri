package terraform

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── mock runner ──────────────────────────────────────────────────────────────

type mockRunner struct {
	execFn    func(ctx context.Context, workDir string, env []string, args ...string) error
	captureFn func(ctx context.Context, workDir string, env []string, args ...string) (string, error)
	lastCmd   string // first arg of most recent call
}

func (m *mockRunner) exec(ctx context.Context, workDir string, env []string, args ...string) error {
	if len(args) > 0 {
		m.lastCmd = args[0]
	}
	if m.execFn != nil {
		return m.execFn(ctx, workDir, env, args...)
	}
	return nil
}

func (m *mockRunner) capture(ctx context.Context, workDir string, env []string, args ...string) (string, error) {
	if len(args) > 0 {
		m.lastCmd = args[0]
	}
	if m.captureFn != nil {
		return m.captureFn(ctx, workDir, env, args...)
	}
	return "", nil
}

func okRunner() *mockRunner { return &mockRunner{} }

// ─── Init tests ───────────────────────────────────────────────────────────────

func TestInit(t *testing.T) {
	tests := []struct {
		name    string
		opts    InitOptions
		execFn  func(ctx context.Context, workDir string, env []string, args ...string) error
		wantErr bool
		checkFn func(t *testing.T, workDir string)
	}{
		{
			name: "success no backend",
			opts: InitOptions{WorkDir: t.TempDir()},
		},
		{
			name: "success with S3 backend writes override file",
			opts: InitOptions{
				WorkDir: t.TempDir(),
				BackendConfig: &BackendConfig{
					Type:     BackendS3,
					S3Bucket: "my-bucket",
					S3Key:    "terraform/state",
					S3Region: "us-east-1",
				},
			},
			checkFn: func(t *testing.T, workDir string) {
				content, err := os.ReadFile(filepath.Join(workDir, "_petri_override.tf"))
				if err != nil {
					t.Fatalf("override file not written: %v", err)
				}
				if !strings.Contains(string(content), `"s3"`) {
					t.Errorf("override missing s3 backend: %s", content)
				}
				if !strings.Contains(string(content), "my-bucket") {
					t.Errorf("override missing bucket name: %s", content)
				}
			},
		},
		{
			name:    "terraform init error propagated",
			opts:    InitOptions{WorkDir: t.TempDir()},
			execFn:  func(_ context.Context, _ string, _ []string, _ ...string) error { return errors.New("no module") },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{execFn: tt.execFn}
			p := newWithRunner(r)

			err := p.Init(context.Background(), tt.opts)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if r.lastCmd != "init" {
					t.Errorf("expected 'init' command, got %q", r.lastCmd)
				}
				if tt.checkFn != nil {
					tt.checkFn(t, tt.opts.WorkDir)
				}
			}
		})
	}
}

// ─── Plan tests ───────────────────────────────────────────────────────────────

func TestPlan(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		captureFn   func(ctx context.Context, workDir string, env []string, args ...string) (string, error)
		wantChanges bool
		wantSummary string
		wantErr     bool
	}{
		{
			name:        "no changes",
			output:      "No changes. Your infrastructure matches the configuration.",
			wantChanges: false,
			wantSummary: "No changes. Your infrastructure matches the configuration.",
		},
		{
			name:        "has changes",
			output:      "Terraform will perform the following actions:\n\nPlan: 3 to add, 0 to change, 0 to destroy.",
			wantChanges: true,
			wantSummary: "Plan: 3 to add, 0 to change, 0 to destroy.",
		},
		{
			name: "terraform error",
			captureFn: func(_ context.Context, _ string, _ []string, _ ...string) (string, error) {
				return "", errors.New("configuration error")
			},
			wantErr: true,
		},
		{
			name:        "var file passed through",
			output:      "No changes. Your infrastructure matches the configuration.",
			wantChanges: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{
				captureFn: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
					if tt.captureFn != nil {
						return tt.captureFn(context.Background(), "", nil, args...)
					}
					return tt.output, nil
				},
			}
			p := newWithRunner(r)

			result, err := p.Plan(context.Background(), PlanOptions{WorkDir: t.TempDir()})

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.HasChanges != tt.wantChanges {
				t.Errorf("HasChanges = %v, want %v", result.HasChanges, tt.wantChanges)
			}
			if tt.wantSummary != "" && result.Summary != tt.wantSummary {
				t.Errorf("Summary = %q, want %q", result.Summary, tt.wantSummary)
			}
		})
	}
}

// ─── Apply tests ──────────────────────────────────────────────────────────────

func TestApply(t *testing.T) {
	outputJSON := `{"cluster_endpoint":{"value":"https://1.2.3.4","type":"string","sensitive":false}}`
	showJSON := `{"values":{"root_module":{"resources":[{"address":"aws_vpc.main","type":"aws_vpc","name":"main","values":{"id":"vpc-abc123"}}]}}}`

	callCount := 0
	r := &mockRunner{
		execFn: func(_ context.Context, _ string, _ []string, _ ...string) error { return nil },
		captureFn: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
			callCount++
			if len(args) > 0 && args[0] == "output" {
				return outputJSON, nil
			}
			if len(args) > 0 && args[0] == "show" {
				return showJSON, nil
			}
			return "", nil
		},
	}
	p := newWithRunner(r)

	result, err := p.Apply(context.Background(), ApplyOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ApplyResult")
	}
	if _, ok := result.Outputs["cluster_endpoint"]; !ok {
		t.Error("expected cluster_endpoint in outputs")
	}
	if len(result.Resources) != 1 {
		t.Errorf("expected 1 resource, got %d", len(result.Resources))
	}
	if result.Resources[0].ResourceID != "vpc-abc123" {
		t.Errorf("ResourceID = %q, want vpc-abc123", result.Resources[0].ResourceID)
	}
}

func TestApply_Error(t *testing.T) {
	r := &mockRunner{
		execFn: func(_ context.Context, _ string, _ []string, _ ...string) error {
			return errors.New("error creating resource")
		},
	}
	_, err := newWithRunner(r).Apply(context.Background(), ApplyOptions{WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "terraform apply") {
		t.Errorf("unexpected error format: %v", err)
	}
}

func TestApply_OutputFailureNonFatal(t *testing.T) {
	// Apply succeeds but output/show fail — result should still be non-nil with empty outputs.
	r := &mockRunner{
		execFn: func(_ context.Context, _ string, _ []string, _ ...string) error { return nil },
		captureFn: func(_ context.Context, _ string, _ []string, _ ...string) (string, error) {
			return "", errors.New("no outputs yet")
		},
	}
	result, err := newWithRunner(r).Apply(context.Background(), ApplyOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result even when output fails")
	}
}

// ─── Destroy tests ────────────────────────────────────────────────────────────

func TestDestroy(t *testing.T) {
	tests := []struct {
		name    string
		execFn  func(ctx context.Context, workDir string, env []string, args ...string) error
		wantErr bool
	}{
		{name: "success"},
		{
			name:    "error",
			execFn:  func(_ context.Context, _ string, _ []string, _ ...string) error { return errors.New("locked") },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{execFn: tt.execFn}
			err := newWithRunner(r).Destroy(context.Background(), DestroyOptions{WorkDir: t.TempDir()})
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ─── Output tests ─────────────────────────────────────────────────────────────

func TestOutput(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		wantKeys  []string
		wantErr   bool
	}{
		{
			name:     "single string output",
			json:     `{"endpoint":{"value":"https://k8s.example.com","type":"string","sensitive":false}}`,
			wantKeys: []string{"endpoint"},
		},
		{
			name:     "empty outputs",
			json:     `{}`,
			wantKeys: []string{},
		},
		{
			name:     "blank response",
			json:     "",
			wantKeys: []string{},
		},
		{
			name:    "invalid json",
			json:    `{not valid}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{
				captureFn: func(_ context.Context, _ string, _ []string, _ ...string) (string, error) {
					return tt.json, nil
				},
			}
			outputs, err := newWithRunner(r).Output(context.Background(), t.TempDir(), nil)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, k := range tt.wantKeys {
				if _, ok := outputs[k]; !ok {
					t.Errorf("missing expected output key %q", k)
				}
			}
		})
	}
}

// ─── parseOutputs tests ───────────────────────────────────────────────────────

func TestParseOutputs(t *testing.T) {
	raw := `{
		"vpc_id":           {"value":"vpc-123","type":"string","sensitive":false},
		"cluster_name":     {"value":"my-cluster","type":"string","sensitive":false},
		"db_password":      {"value":"secret","type":"string","sensitive":true}
	}`
	outputs, err := parseOutputs(raw)
	if err != nil {
		t.Fatalf("parseOutputs: %v", err)
	}
	if len(outputs) != 3 {
		t.Errorf("expected 3 outputs, got %d", len(outputs))
	}
	if !outputs["db_password"].Sensitive {
		t.Error("db_password should be sensitive")
	}
	// Verify value is preserved as raw JSON.
	var vpcID string
	if err := json.Unmarshal(outputs["vpc_id"].Value, &vpcID); err != nil || vpcID != "vpc-123" {
		t.Errorf("vpc_id value = %v, want vpc-123", vpcID)
	}
}

func TestParseOutputs_Empty(t *testing.T) {
	out, err := parseOutputs("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty map, got %v", out)
	}
}

// ─── parseResources tests ─────────────────────────────────────────────────────

func TestParseResources(t *testing.T) {
	showJSON := `{
		"values": {
			"root_module": {
				"resources": [
					{
						"address": "aws_vpc.main",
						"type":    "aws_vpc",
						"name":    "main",
						"values":  {"id": "vpc-abc123", "cidr_block": "10.0.0.0/16"}
					},
					{
						"address": "aws_eks_cluster.main",
						"type":    "aws_eks_cluster",
						"name":    "main",
						"values":  {"id": "my-cluster", "arn": "arn:aws:eks:::cluster/my-cluster"}
					}
				]
			}
		}
	}`

	resources, err := parseResources(showJSON)
	if err != nil {
		t.Fatalf("parseResources: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(resources))
	}

	vpc := resources[0]
	if vpc.Address != "aws_vpc.main" {
		t.Errorf("Address = %q, want aws_vpc.main", vpc.Address)
	}
	if vpc.ResourceID != "vpc-abc123" {
		t.Errorf("ResourceID = %q, want vpc-abc123", vpc.ResourceID)
	}

	// EKS cluster: "id" is preferred over "arn".
	eks := resources[1]
	if eks.ResourceID != "my-cluster" {
		t.Errorf("EKS ResourceID = %q, want my-cluster", eks.ResourceID)
	}
}

func TestParseResources_Empty(t *testing.T) {
	res, err := parseResources("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != nil {
		t.Errorf("expected nil, got %v", res)
	}
}

func TestExtractResourceID(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]json.RawMessage
		want   string
	}{
		{
			name:   "prefers id over arn",
			values: map[string]json.RawMessage{"id": json.RawMessage(`"vpc-123"`), "arn": json.RawMessage(`"arn:aws:..."`)},
			want:   "vpc-123",
		},
		{
			name:   "falls back to arn when no id",
			values: map[string]json.RawMessage{"arn": json.RawMessage(`"arn:aws:..."`)},
			want:   "arn:aws:...",
		},
		{
			name:   "falls back to name",
			values: map[string]json.RawMessage{"name": json.RawMessage(`"my-resource"`)},
			want:   "my-resource",
		},
		{
			name:   "empty values",
			values: map[string]json.RawMessage{},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractResourceID(tt.values)
			if got != tt.want {
				t.Errorf("extractResourceID = %q, want %q", got, tt.want)
			}
		})
	}
}

// ─── extractPlanSummary tests ─────────────────────────────────────────────────

func TestExtractPlanSummary(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "no changes",
			output: "Refreshing state...\n\nNo changes. Your infrastructure matches the configuration.",
			want:   "No changes. Your infrastructure matches the configuration.",
		},
		{
			name:   "with changes",
			output: "Terraform will perform:\n\nPlan: 5 to add, 2 to change, 1 to destroy.",
			want:   "Plan: 5 to add, 2 to change, 1 to destroy.",
		},
		{
			name:   "no summary line",
			output: "some output without summary",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanSummary(tt.output)
			if got != tt.want {
				t.Errorf("extractPlanSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

// ─── backend HCL tests ────────────────────────────────────────────────────────

func TestBuildBackendHCL(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *BackendConfig
		wantStrs []string
	}{
		{
			name: "S3",
			cfg: &BackendConfig{
				Type:     BackendS3,
				S3Bucket: "petri-state",
				S3Key:    "labs/test.tfstate",
				S3Region: "us-west-2",
			},
			wantStrs: []string{`"s3"`, "petri-state", "labs/test.tfstate", "us-west-2"},
		},
		{
			name: "GCS",
			cfg: &BackendConfig{
				Type:      BackendGCS,
				GCSBucket: "petri-gcs",
				GCSPrefix: "terraform/labs",
			},
			wantStrs: []string{`"gcs"`, "petri-gcs", "terraform/labs"},
		},
		{
			name: "AzureRM",
			cfg: &BackendConfig{
				Type:                BackendAzureRM,
				AzureStorageAccount: "petristate",
				AzureContainer:      "tfstate",
				AzureKey:            "test.tfstate",
			},
			wantStrs: []string{`"azurerm"`, "petristate", "tfstate", "test.tfstate"},
		},
		{
			name:     "local fallback",
			cfg:      &BackendConfig{Type: BackendLocal},
			wantStrs: []string{`"local"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hcl := buildBackendHCL(tt.cfg)
			if !strings.Contains(hcl, "terraform {") {
				t.Error("missing terraform block")
			}
			for _, s := range tt.wantStrs {
				if !strings.Contains(hcl, s) {
					t.Errorf("HCL missing %q:\n%s", s, hcl)
				}
			}
		})
	}
}

func TestWriteBackendOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := &BackendConfig{Type: BackendS3, S3Bucket: "b", S3Key: "k", S3Region: "r"}

	if err := writeBackendOverride(dir, cfg); err != nil {
		t.Fatalf("writeBackendOverride: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "_petri_override.tf"))
	if err != nil {
		t.Fatalf("reading override file: %v", err)
	}
	if !strings.Contains(string(content), "Managed by Petri") {
		t.Error("override file missing header comment")
	}
}

// ─── parseTerraformError tests ────────────────────────────────────────────────

func TestParseTerraformError(t *testing.T) {
	stderr := `╷
│ Error: Invalid resource type
│
│   on main.tf line 5, in resource "aws_bad" "example":
│    5: resource "aws_bad" "example" {
│
│ There is no resource type named "aws_bad".
╵`
	result := parseTerraformError(stderr)
	if result == "" {
		t.Fatal("expected non-empty error message")
	}
	if strings.Contains(result, "│") {
		t.Errorf("result still contains box chars: %s", result)
	}
	if !strings.Contains(result, "Error: Invalid resource type") {
		t.Errorf("result missing error message: %s", result)
	}
}
