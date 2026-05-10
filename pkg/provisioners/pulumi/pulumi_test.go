package pulumi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ─── mock runner ──────────────────────────────────────────────────────────────

type mockRunner struct {
	execFn    func(ctx context.Context, workDir string, env []string, args ...string) error
	captureFn func(ctx context.Context, workDir string, env []string, args ...string) (string, error)
	calls     [][]string // records each invocation's args
}

func (m *mockRunner) exec(ctx context.Context, workDir string, env []string, args ...string) error {
	m.calls = append(m.calls, args)
	if m.execFn != nil {
		return m.execFn(ctx, workDir, env, args...)
	}
	return nil
}

func (m *mockRunner) capture(ctx context.Context, workDir string, env []string, args ...string) (string, error) {
	m.calls = append(m.calls, args)
	if m.captureFn != nil {
		return m.captureFn(ctx, workDir, env, args...)
	}
	return "", nil
}

// firstArg returns the first element of the nth recorded call (0-indexed).
func (m *mockRunner) firstArg(n int) string {
	if n < len(m.calls) && len(m.calls[n]) > 0 {
		return m.calls[n][0]
	}
	return ""
}

// ─── Init tests ───────────────────────────────────────────────────────────────

func TestInit(t *testing.T) {
	tests := []struct {
		name    string
		opts    InitOptions
		execFn  func(ctx context.Context, workDir string, env []string, args ...string) error
		wantErr bool
		// checkFn receives the runner so the test can inspect recorded calls.
		checkFn func(t *testing.T, r *mockRunner)
	}{
		{
			name: "success: login then stack init (new stack)",
			opts: InitOptions{WorkDir: t.TempDir(), StackName: "dev"},
			execFn: func(_ context.Context, _ string, _ []string, args ...string) error {
				// Fail "stack select" so provisioner falls through to "stack init".
				if args[0] == "stack" && len(args) > 1 && args[1] == "select" {
					return errors.New("no stack")
				}
				return nil
			},
			checkFn: func(t *testing.T, r *mockRunner) {
				// Call order: login, stack select (fails), stack init.
				if r.firstArg(0) != "login" {
					t.Errorf("first call should be login, got %q", r.firstArg(0))
				}
			},
		},
		{
			name: "success: login then stack select (existing stack)",
			opts: InitOptions{WorkDir: t.TempDir(), StackName: "dev"},
			checkFn: func(t *testing.T, r *mockRunner) {
				if r.firstArg(0) != "login" {
					t.Errorf("first call should be login, got %q", r.firstArg(0))
				}
			},
		},
		{
			name: "BackendURL appended to login args",
			opts: InitOptions{
				WorkDir:    t.TempDir(),
				StackName:  "dev",
				BackendURL: "s3://my-bucket",
			},
			checkFn: func(t *testing.T, r *mockRunner) {
				if len(r.calls) == 0 {
					t.Fatal("no calls recorded")
				}
				found := false
				for _, arg := range r.calls[0] {
					if arg == "s3://my-bucket" {
						found = true
					}
				}
				if !found {
					t.Errorf("backend URL not passed to login: %v", r.calls[0])
				}
			},
		},
		{
			name: "login error propagated",
			opts: InitOptions{WorkDir: t.TempDir(), StackName: "dev"},
			execFn: func(_ context.Context, _ string, _ []string, args ...string) error {
				if args[0] == "login" {
					return errors.New("auth failure")
				}
				return nil
			},
			wantErr: true,
		},
		{
			name: "stack init error propagated",
			opts: InitOptions{WorkDir: t.TempDir(), StackName: "dev"},
			execFn: func(_ context.Context, _ string, _ []string, args ...string) error {
				// login succeeds; stack select fails; stack init fails.
				if args[0] == "stack" {
					return errors.New("stack error")
				}
				return nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{execFn: tt.execFn}
			p := newWithRunner(r)

			err := p.Init(context.Background(), tt.opts)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.checkFn != nil {
				tt.checkFn(t, r)
			}
		})
	}
}

// ─── Preview tests ────────────────────────────────────────────────────────────

func TestPreview(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		captureErr  error
		wantChanges bool
		wantSummary string
		wantErr     bool
	}{
		{
			name:        "no changes",
			output:      "Previewing update (dev):\n\nResources:\n    1 unchanged\n\nDuration: 1s",
			wantChanges: false,
			wantSummary: "1 unchanged",
		},
		{
			name: "has changes — create",
			output: "Previewing update (dev):\n" +
				" +  azure-native:resources:ResourceGroup  rg  create\n\n" +
				"Resources:\n    + 3 to create\n\nDuration: 2s",
			wantChanges: true,
			wantSummary: "+ 3 to create",
		},
		{
			name: "has changes — update and delete",
			output: "Previewing update (dev):\n" +
				" ~  azure:aks:Cluster  aks-main  update\n" +
				" -  azure:cache:Redis  redis     delete\n\n" +
				"Resources:\n    ~ 1 to update, - 1 to delete, 2 unchanged\n\nDuration: 3s",
			wantChanges: true,
			wantSummary: "~ 1 to update, - 1 to delete, 2 unchanged",
		},
		{
			name:       "pulumi error propagated",
			captureErr: errors.New("preview failed"),
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{
				captureFn: func(_ context.Context, _ string, _ []string, _ ...string) (string, error) {
					return tt.output, tt.captureErr
				},
			}
			p := newWithRunner(r)

			result, err := p.Preview(context.Background(), PreviewOptions{
				WorkDir:   t.TempDir(),
				StackName: "dev",
			})

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

// ─── Up tests ─────────────────────────────────────────────────────────────────

func TestUp(t *testing.T) {
	outputJSON := `{"aksName":"myaks","endpoint":"https://1.2.3.4"}`
	exportJSON := `{
		"version": 3,
		"deployment": {
			"resources": [
				{
					"urn":  "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"type": "pulumi:pulumi:Stack",
					"id":   "proj-dev"
				},
				{
					"urn":  "urn:pulumi:dev::proj::azure-native:resources:ResourceGroup::rg",
					"type": "azure-native:resources:ResourceGroup",
					"id":   "/subscriptions/xxx/resourceGroups/myRG"
				}
			]
		}
	}`

	r := &mockRunner{
		execFn: func(_ context.Context, _ string, _ []string, _ ...string) error { return nil },
		captureFn: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "stack" && args[1] == "output" {
				return outputJSON, nil
			}
			if len(args) >= 2 && args[0] == "stack" && args[1] == "export" {
				return exportJSON, nil
			}
			return "", nil
		},
	}
	p := newWithRunner(r)

	result, err := p.Up(context.Background(), UpOptions{WorkDir: t.TempDir(), StackName: "dev"})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil UpResult")
	}
	if _, ok := result.Outputs["aksName"]; !ok {
		t.Error("expected aksName in outputs")
	}
	if _, ok := result.Outputs["endpoint"]; !ok {
		t.Error("expected endpoint in outputs")
	}
	// Stack meta-resource should be filtered; only the ResourceGroup remains.
	if len(result.Resources) != 1 {
		t.Errorf("expected 1 resource (stack meta filtered), got %d", len(result.Resources))
	}
	if result.Resources[0].ResourceID != "/subscriptions/xxx/resourceGroups/myRG" {
		t.Errorf("unexpected ResourceID: %q", result.Resources[0].ResourceID)
	}
}

func TestUp_Error(t *testing.T) {
	r := &mockRunner{
		execFn: func(_ context.Context, _ string, _ []string, _ ...string) error {
			return errors.New("pulumi up failed")
		},
	}
	_, err := newWithRunner(r).Up(context.Background(), UpOptions{WorkDir: t.TempDir(), StackName: "dev"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "pulumi up") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUp_OutputFailureNonFatal(t *testing.T) {
	// Up succeeds but output/export fail — result should still be non-nil.
	r := &mockRunner{
		execFn: func(_ context.Context, _ string, _ []string, _ ...string) error { return nil },
		captureFn: func(_ context.Context, _ string, _ []string, _ ...string) (string, error) {
			return "", errors.New("no outputs yet")
		},
	}
	result, err := newWithRunner(r).Up(context.Background(), UpOptions{WorkDir: t.TempDir(), StackName: "dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result even when output fails")
	}
	if len(result.Outputs) != 0 {
		t.Errorf("expected empty outputs, got %v", result.Outputs)
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
			name:    "error propagated",
			execFn:  func(_ context.Context, _ string, _ []string, _ ...string) error { return errors.New("locked") },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{execFn: tt.execFn}
			err := newWithRunner(r).Destroy(context.Background(), DestroyOptions{
				WorkDir:   t.TempDir(),
				StackName: "dev",
			})
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
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
		name       string
		json       string
		captureErr error
		wantKeys   []string
		wantErr    bool
	}{
		{
			name:     "string and numeric outputs",
			json:     `{"aksName":"myaks","nodeCount":3}`,
			wantKeys: []string{"aksName", "nodeCount"},
		},
		{
			name:     "empty outputs object",
			json:     `{}`,
			wantKeys: []string{},
		},
		{
			name:     "blank response",
			json:     "",
			wantKeys: []string{},
		},
		{
			name:    "invalid JSON",
			json:    `{not valid json}`,
			wantErr: true,
		},
		{
			name:       "capture error propagated",
			captureErr: errors.New("stack not found"),
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{
				captureFn: func(_ context.Context, _ string, _ []string, _ ...string) (string, error) {
					return tt.json, tt.captureErr
				},
			}
			outputs, err := newWithRunner(r).Output(context.Background(), OutputOptions{
				WorkDir:   t.TempDir(),
				StackName: "dev",
			})
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

// ─── StackRemove tests ────────────────────────────────────────────────────────

func TestStackRemove(t *testing.T) {
	tests := []struct {
		name      string
		opts      StackRemoveOptions
		wantForce bool
		execErr   error
		wantErr   bool
	}{
		{
			name: "success without force",
			opts: StackRemoveOptions{WorkDir: t.TempDir(), StackName: "dev"},
		},
		{
			name:      "success with force flag",
			opts:      StackRemoveOptions{WorkDir: t.TempDir(), StackName: "dev", Force: true},
			wantForce: true,
		},
		{
			name:    "error propagated",
			opts:    StackRemoveOptions{WorkDir: t.TempDir(), StackName: "dev"},
			execErr: errors.New("stack not empty"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotArgs []string
			r := &mockRunner{
				execFn: func(_ context.Context, _ string, _ []string, args ...string) error {
					gotArgs = args
					return tt.execErr
				},
			}
			err := newWithRunner(r).StackRemove(context.Background(), tt.opts)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			hasForce := false
			for _, a := range gotArgs {
				if a == "--force" {
					hasForce = true
				}
			}
			if tt.wantForce && !hasForce {
				t.Errorf("expected --force in args, got %v", gotArgs)
			}
			if !tt.wantForce && hasForce {
				t.Errorf("unexpected --force in args: %v", gotArgs)
			}
		})
	}
}

// ─── parseOutputs tests ───────────────────────────────────────────────────────

func TestParseOutputs(t *testing.T) {
	raw := `{
		"aksName":   "myaks-cluster",
		"nodeCount": 3,
		"endpoint":  "https://1.2.3.4"
	}`
	outputs, err := parseOutputs(raw)
	if err != nil {
		t.Fatalf("parseOutputs: %v", err)
	}
	if len(outputs) != 3 {
		t.Errorf("expected 3 outputs, got %d", len(outputs))
	}

	// Verify raw JSON values are preserved.
	var name string
	if err := json.Unmarshal(outputs["aksName"].Value, &name); err != nil || name != "myaks-cluster" {
		t.Errorf("aksName value = %v, want myaks-cluster", name)
	}
	var count int
	if err := json.Unmarshal(outputs["nodeCount"].Value, &count); err != nil || count != 3 {
		t.Errorf("nodeCount value = %v, want 3", count)
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

func TestParseOutputs_InvalidJSON(t *testing.T) {
	_, err := parseOutputs("{invalid}")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// ─── parseResources tests ─────────────────────────────────────────────────────

func TestParseResources(t *testing.T) {
	exportJSON := `{
		"version": 3,
		"deployment": {
			"resources": [
				{
					"urn":  "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"type": "pulumi:pulumi:Stack",
					"id":   "proj-dev"
				},
				{
					"urn":  "urn:pulumi:dev::proj::azure-native:resources:ResourceGroup::rg",
					"type": "azure-native:resources:ResourceGroup",
					"id":   "/subscriptions/xxx/resourceGroups/myRG"
				},
				{
					"urn":  "urn:pulumi:dev::proj::azure-native:containerservice:ManagedCluster::aks-main",
					"type": "azure-native:containerservice:ManagedCluster",
					"id":   "/subscriptions/xxx/resourceGroups/myRG/providers/Microsoft.ContainerService/managedClusters/myaks"
				}
			]
		}
	}`

	resources, err := parseResources(exportJSON)
	if err != nil {
		t.Fatalf("parseResources: %v", err)
	}
	// Stack meta-resource should be filtered out.
	if len(resources) != 2 {
		t.Fatalf("expected 2 resources (Stack filtered), got %d", len(resources))
	}

	rg := resources[0]
	if rg.Type != "azure-native:resources:ResourceGroup" {
		t.Errorf("Type = %q, want azure-native:resources:ResourceGroup", rg.Type)
	}
	if rg.Name != "rg" {
		t.Errorf("Name = %q, want rg", rg.Name)
	}
	if rg.ResourceID != "/subscriptions/xxx/resourceGroups/myRG" {
		t.Errorf("ResourceID = %q", rg.ResourceID)
	}

	aks := resources[1]
	if aks.Name != "aks-main" {
		t.Errorf("AKS Name = %q, want aks-main", aks.Name)
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

func TestParseResources_InvalidJSON(t *testing.T) {
	_, err := parseResources("{bad json}")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseResources_OnlyStackMeta(t *testing.T) {
	// When the only resource is the Stack meta-resource, the result should be empty.
	exportJSON := `{"version":3,"deployment":{"resources":[
		{"urn":"urn:pulumi:dev::p::pulumi:pulumi:Stack::p-dev","type":"pulumi:pulumi:Stack","id":"p-dev"}
	]}}`
	resources, err := parseResources(exportJSON)
	if err != nil {
		t.Fatalf("parseResources: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("expected 0 resources after filtering Stack, got %d", len(resources))
	}
}

// ─── extractResourceName tests ────────────────────────────────────────────────

func TestExtractResourceName(t *testing.T) {
	tests := []struct {
		name string
		urn  string
		want string
	}{
		{
			name: "standard four-part URN",
			urn:  "urn:pulumi:dev::myproject::azure-native:resources:ResourceGroup::rg",
			want: "rg",
		},
		{
			name: "nested parent type",
			urn:  "urn:pulumi:dev::proj::azure-native:resources:ResourceGroup$azure-native:network:VirtualNetwork::vnet",
			want: "vnet",
		},
		{
			name: "malformed URN returns input",
			urn:  "not-a-urn",
			want: "not-a-urn",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractResourceName(tt.urn)
			if got != tt.want {
				t.Errorf("extractResourceName(%q) = %q, want %q", tt.urn, got, tt.want)
			}
		})
	}
}

// ─── extractHasChanges tests ──────────────────────────────────────────────────

func TestExtractHasChanges(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "no changes explicit",
			output: "Previewing update (dev):\n\nResources:\n    1 unchanged\n\nno changes\n",
			want:   false,
		},
		{
			name:   "create marker",
			output: " +  azure-native:resources:ResourceGroup  rg  create\n\nResources:\n    + 3 to create",
			want:   true,
		},
		{
			name:   "update marker",
			output: " ~  azure:aks:Cluster  aks-main  update",
			want:   true,
		},
		{
			name:   "delete marker",
			output: " -  azure:cache:Redis  redis  delete",
			want:   true,
		},
		{
			name:   "to create in resources section",
			output: "Resources:\n    + 5 to create",
			want:   true,
		},
		{
			name:   "to replace keyword",
			output: "Resources:\n    1 to replace",
			want:   true,
		},
		{
			name:   "unchanged only is no-change",
			output: "Resources:\n    2 unchanged",
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHasChanges(tt.output)
			if got != tt.want {
				t.Errorf("extractHasChanges = %v, want %v\noutput: %q", got, tt.want, tt.output)
			}
		})
	}
}

// ─── extractPreviewSummary tests ──────────────────────────────────────────────

func TestExtractPreviewSummary(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "single unchanged line",
			output: "Previewing update (dev):\n\nResources:\n    1 unchanged\n\nDuration: 1s",
			want:   "1 unchanged",
		},
		{
			name:   "create summary",
			output: "Previewing update (dev):\n\nResources:\n    + 3 to create\n\nDuration: 2s",
			want:   "+ 3 to create",
		},
		{
			name:   "multiple summary lines",
			output: "Previewing update (dev):\n\nResources:\n    ~ 1 to update\n    - 1 to delete\n    2 unchanged\n\nDuration: 3s",
			want:   "~ 1 to update, - 1 to delete, 2 unchanged",
		},
		{
			name:   "no resources section",
			output: "some output without resources section",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPreviewSummary(tt.output)
			if got != tt.want {
				t.Errorf("extractPreviewSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

// ─── parsePulumiError tests ───────────────────────────────────────────────────

func TestParsePulumiError(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		wantLine string
		wantNone bool
	}{
		{
			name: "standard pulumi error",
			stderr: "Updating (dev):\n\n" +
				"error: update failed\n" +
				"error: azure-native:resources:ResourceGroup (rg): error creating resource group\n" +
				"    some stack trace line\n" +
				"    another stack trace line\n",
			wantLine: "error: update failed",
		},
		{
			name: "failed to keyword",
			stderr: "failed to decode response: unexpected EOF\n" +
				"goroutine 1 [running]:\n",
			wantLine: "failed to decode response: unexpected EOF",
		},
		{
			name:     "empty stderr returns empty",
			stderr:   "",
			wantNone: true,
		},
		{
			name:     "blank lines only",
			stderr:   "\n\n\n",
			wantNone: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePulumiError(tt.stderr)
			if tt.wantNone {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantLine) {
				t.Errorf("parsePulumiError = %q, want to contain %q", got, tt.wantLine)
			}
		})
	}
}

// ─── mergeEnv tests ───────────────────────────────────────────────────────────

func TestMergeEnv(t *testing.T) {
	tests := []struct {
		name       string
		extra      []string
		backendURL string
		passphrase string
		wantVars   []string
		wantAbsent []string
	}{
		{
			name:       "backend URL and passphrase prepended",
			backendURL: "s3://my-bucket",
			passphrase: "secret123",
			wantVars:   []string{"PULUMI_BACKEND_URL=s3://my-bucket", "PULUMI_CONFIG_PASSPHRASE=secret123"},
		},
		{
			name:       "extra vars appended after pulumi vars",
			backendURL: "gs://bucket",
			extra:      []string{"GOOGLE_APPLICATION_CREDENTIALS=/path/creds.json"},
			wantVars:   []string{"PULUMI_BACKEND_URL=gs://bucket", "GOOGLE_APPLICATION_CREDENTIALS=/path/creds.json"},
		},
		{
			name:       "empty backend and passphrase — no PULUMI_ vars injected",
			extra:      []string{"AWS_REGION=us-east-1"},
			wantAbsent: []string{"PULUMI_BACKEND_URL", "PULUMI_CONFIG_PASSPHRASE"},
			wantVars:   []string{"AWS_REGION=us-east-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := mergeEnv(tt.extra, tt.backendURL, tt.passphrase)
			for _, want := range tt.wantVars {
				found := false
				for _, e := range env {
					if e == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("env missing %q; got %v", want, env)
				}
			}
			for _, absent := range tt.wantAbsent {
				for _, e := range env {
					if strings.HasPrefix(e, absent) {
						t.Errorf("env should not contain %q; got %v", absent, env)
					}
				}
			}
		})
	}
}
