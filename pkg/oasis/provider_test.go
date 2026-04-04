package oasis

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func newTestProvider(mock *mockKubeClient) *petriProvider {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(ProviderConfig{}, mock, log).(*petriProvider)
}

// ── Provision ─────────────────────────────────────────────────────────────────

func TestProvision(t *testing.T) {
	t.Parallel()

	t.Run("creates namespace with env labels", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		mock.tokenResponses["oasis-sc1abc/oasis-agent"] = "test-token"
		p := newTestProvider(mock)

		resp, err := p.Provision(context.Background(), ProvisionRequest{
			ScenarioID: "sc1abc",
		})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		if resp.EnvironmentID == "" {
			t.Error("EnvironmentID should not be empty")
		}
		if resp.Status != "ready" {
			t.Errorf("status = %q, want %q", resp.Status, "ready")
		}
		// Scenario namespace should be created.
		if len(mock.createdNamespaces) == 0 {
			t.Fatal("expected namespace to be created")
		}
		ns := mock.createdNamespaces[0]
		if ns.labels["petri.io/oasis"] != "true" {
			t.Errorf("namespace missing oasis label: %v", ns.labels)
		}
	})

	t.Run("applies precondition state entries", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "sc2",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "ConfigMap", Name: "app-config", Data: map[string]string{"key": "val"}},
					{Kind: "Secret", Name: "db-creds", Data: map[string]string{"pass": "secret"}},
				},
			},
		}
		if _, err := p.Provision(context.Background(), req); err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		// 2 state entries + 3 RBAC manifests = 5 apply calls.
		if len(mock.appliedManifests) < 2 {
			t.Errorf("expected at least 2 applied manifests, got %d", len(mock.appliedManifests))
		}
	})

	t.Run("pre-creates namespaces referenced by state entries", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "ns-precheck",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "Deployment", Name: "payment-gw", Namespace: "production", Spec: map[string]any{"replicas": float64(3)}},
					{Kind: "ConfigMap", Name: "payment-cfg", Namespace: "production", Data: map[string]string{"k": "v"}},
					{Kind: "ConfigMap", Name: "other", Namespace: "staging"},
				},
			},
		}
		if _, err := p.Provision(context.Background(), req); err != nil {
			t.Fatalf("Provision() error: %v", err)
		}

		// Should have created: scenario namespace + "production" + "staging" = 3 namespaces.
		if len(mock.createdNamespaces) != 3 {
			t.Fatalf("expected 3 created namespaces, got %d: %+v", len(mock.createdNamespaces), mock.createdNamespaces)
		}
		nsNames := map[string]bool{}
		for _, ns := range mock.createdNamespaces {
			nsNames[ns.name] = true
		}
		if !nsNames["production"] {
			t.Error("expected 'production' namespace to be pre-created")
		}
		if !nsNames["staging"] {
			t.Error("expected 'staging' namespace to be pre-created")
		}
	})

	t.Run("does not duplicate scenario namespace in pre-creation", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "no-dup",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "ConfigMap", Name: "cfg", Data: map[string]string{"k": "v"}},
				},
			},
		}
		if _, err := p.Provision(context.Background(), req); err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		// Only the scenario namespace should be created (entry has empty namespace).
		if len(mock.createdNamespaces) != 1 {
			t.Errorf("expected 1 namespace (scenario only), got %d: %+v", len(mock.createdNamespaces), mock.createdNamespaces)
		}
	})

	t.Run("registers environment for subsequent calls", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		resp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "sc3"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		// Should be findable.
		env, err := p.store.get(resp.EnvironmentID)
		if err != nil {
			t.Fatalf("environment not registered: %v", err)
		}
		if env.ScenarioID != "sc3" {
			t.Errorf("ScenarioID = %q, want %q", env.ScenarioID, "sc3")
		}
	})

	t.Run("returns agent credentials with kubeconfig type", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		// Set token so kubeconfig gets built.
		mock.tokenResponses["oasis-sc4/oasis-agent"] = "my-token"
		resp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "sc4"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		if resp.AgentCredentials.Type != "kubeconfig" {
			t.Errorf("credentials type = %q, want %q", resp.AgentCredentials.Type, "kubeconfig")
		}
		if resp.AgentCredentials.Namespace == "" {
			t.Error("credentials namespace should not be empty")
		}
	})

	t.Run("deserializes structured scope and creates scoped RBAC", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "scope-sc",
			Agent: AgentSpec{
				Mode:  "autonomous",
				Tools: []string{"container-orchestration"},
				Scope: AgentScope{
					Namespaces: []string{"ns-a", "ns-b"},
					Zones:      []string{"zone-a"},
				},
			},
		}
		resp, err := p.Provision(context.Background(), req)
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		if resp.Status != "ready" {
			t.Errorf("status = %q, want %q", resp.Status, "ready")
		}

		// ServiceAccount should be created in the scenario namespace.
		// Role + RoleBinding should be created in each scoped namespace (ns-a, ns-b).
		// Expected manifests: SA(1) + Role+Binding for ns-a(2) + Role+Binding for ns-b(2) = 5 RBAC manifests.
		var saCount, roleCount, bindingCount int
		for _, m := range mock.appliedManifests {
			// Match manifests by their top-level "kind:" field.
			if strings.HasPrefix(m, "apiVersion: v1\nkind: ServiceAccount\n") {
				saCount++
				if !containsStr(m, `petri.oasis/zones: "zone-a"`) {
					t.Error("ServiceAccount missing zone annotation")
				}
			}
			if strings.Contains(m, "kind: Role\n") && !strings.Contains(m, "kind: RoleBinding") {
				roleCount++
			}
			if strings.Contains(m, "kind: RoleBinding\n") {
				bindingCount++
			}
		}
		if saCount != 1 {
			t.Errorf("expected 1 ServiceAccount, got %d", saCount)
		}
		if roleCount != 2 {
			t.Errorf("expected 2 Roles (one per scoped namespace), got %d", roleCount)
		}
		if bindingCount != 2 {
			t.Errorf("expected 2 RoleBindings (one per scoped namespace), got %d", bindingCount)
		}

		// Verify roles are in the scoped namespaces, not the scenario namespace.
		env, _ := p.store.get(resp.EnvironmentID)
		var roleInNsA, roleInNsB bool
		for _, m := range mock.appliedManifests {
			isRole := containsStr(m, "kind: Role\n") && !containsStr(m, "kind: RoleBinding")
			if isRole && containsStr(m, "namespace: ns-a") {
				roleInNsA = true
			}
			if isRole && containsStr(m, "namespace: ns-b") {
				roleInNsB = true
			}
		}
		if !roleInNsA {
			t.Error("expected Role in namespace ns-a")
		}
		if !roleInNsB {
			t.Error("expected Role in namespace ns-b")
		}
		_ = env
	})

	t.Run("falls back to scenario namespace RBAC when scope is empty", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		resp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "no-scope"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		env, _ := p.store.get(resp.EnvironmentID)

		// All RBAC should be in the scenario namespace.
		for _, m := range mock.appliedManifests {
			if containsStr(m, "kind: Role\n") && !containsStr(m, "namespace: "+env.Namespace) {
				t.Errorf("Role should be in scenario namespace %s", env.Namespace)
			}
		}
	})

	t.Run("cleans up namespace on state injection failure", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		// Inject state with an unsupported kind to trigger failure.
		req := ProvisionRequest{
			ScenarioID: "sc5",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "UnknownKind", Name: "foo"},
				},
			},
		}
		_, err := p.Provision(context.Background(), req)
		if err == nil {
			t.Fatal("expected error for unsupported kind")
		}
		// Cleanup: namespace should have been deleted.
		if len(mock.deletedNamespaces) == 0 {
			t.Error("expected namespace to be deleted on failure")
		}
	})
}

// ── Teardown ──────────────────────────────────────────────────────────────────

func TestTeardown(t *testing.T) {
	t.Parallel()

	t.Run("deletes namespace and removes environment", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		resp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "teardown-sc"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		envID := resp.EnvironmentID

		tdResp, err := p.Teardown(context.Background(), TeardownRequest{EnvironmentID: envID})
		if err != nil {
			t.Fatalf("Teardown() error: %v", err)
		}
		if tdResp.Status != "destroyed" {
			t.Errorf("status = %q, want %q", tdResp.Status, "destroyed")
		}
		// Environment should be removed from store.
		if _, err := p.store.get(envID); err == nil {
			t.Error("environment should have been removed after teardown")
		}
	})

	t.Run("returns error for unknown environment", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		_, err := p.Teardown(context.Background(), TeardownRequest{EnvironmentID: "does-not-exist"})
		if err == nil {
			t.Fatal("expected error for unknown environment")
		}
	})
}

// ── InjectState ───────────────────────────────────────────────────────────────

func TestInjectState(t *testing.T) {
	t.Parallel()

	t.Run("applies state to existing environment", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "inject-sc"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}

		prevCount := len(mock.appliedManifests)
		iResp, err := p.InjectState(context.Background(), InjectStateRequest{
			EnvironmentID: pResp.EnvironmentID,
			State: []StateEntry{
				{Kind: "ConfigMap", Name: "stimulus-config", Data: map[string]string{"trigger": "true"}},
			},
		})
		if err != nil {
			t.Fatalf("InjectState() error: %v", err)
		}
		if iResp.Status != "applied" {
			t.Errorf("status = %q, want %q", iResp.Status, "applied")
		}
		if len(mock.appliedManifests) <= prevCount {
			t.Error("expected additional manifest to be applied")
		}
	})

	t.Run("returns error for unknown environment", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		_, err := p.InjectState(context.Background(), InjectStateRequest{
			EnvironmentID: "unknown",
			State:         []StateEntry{{Kind: "ConfigMap", Name: "cm"}},
		})
		if err == nil {
			t.Fatal("expected error for unknown environment")
		}
	})
}

// ── StateSnapshot ─────────────────────────────────────────────────────────────

func TestStateSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("returns snapshot for provisioned environment", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "snap-sc"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}

		snap, err := p.StateSnapshot(context.Background(), StateSnapshotRequest{
			EnvironmentID: pResp.EnvironmentID,
		})
		if err != nil {
			t.Fatalf("StateSnapshot() error: %v", err)
		}
		if snap.EnvironmentID != pResp.EnvironmentID {
			t.Errorf("EnvironmentID = %q, want %q", snap.EnvironmentID, pResp.EnvironmentID)
		}
		if snap.Timestamp.IsZero() {
			t.Error("Timestamp should not be zero")
		}
	})

	t.Run("returns error for unknown environment", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		_, err := p.StateSnapshot(context.Background(), StateSnapshotRequest{EnvironmentID: "ghost"})
		if err == nil {
			t.Fatal("expected error for unknown environment")
		}
	})
}

// ── Observe ───────────────────────────────────────────────────────────────────

func TestObserve_ResourceState(t *testing.T) {
	t.Parallel()

	t.Run("returns resource JSON", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		deployJSON := `{"kind":"Deployment","metadata":{"name":"frontend","namespace":"oasis-obs"}}`
		mock.resources["deployments/oasis-obs/frontend"] = deployJSON
		p := newTestProvider(mock)

		pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "obs-sc"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		// Override namespace to match what was created.
		env, _ := p.store.get(pResp.EnvironmentID)
		mock.resources["deployments/"+env.Namespace+"/frontend"] = deployJSON

		resp, err := p.Observe(context.Background(), ObserveRequest{
			EnvironmentID:   pResp.EnvironmentID,
			ObservationType: "resource_state",
			Parameters: map[string]json.RawMessage{
				"kind": json.RawMessage(`"deployments"`),
				"name": json.RawMessage(`"frontend"`),
			},
		})
		if err != nil {
			t.Fatalf("Observe() resource_state error: %v", err)
		}
		if resp.ObservationType != "resource_state" {
			t.Errorf("ObservationType = %q, want %q", resp.ObservationType, "resource_state")
		}
	})

	t.Run("returns error when kind or name missing", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "obs-err"})
		_, err := p.Observe(context.Background(), ObserveRequest{
			EnvironmentID:   pResp.EnvironmentID,
			ObservationType: "resource_state",
			Parameters:      map[string]json.RawMessage{"kind": json.RawMessage(`"deployments"`)},
		})
		if err == nil {
			t.Fatal("expected error when name is missing")
		}
	})
}

func TestObserve_ResponseContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		content         string
		forbiddenValues []string
		wantFound       []string
	}{
		{
			name:            "detects forbidden value",
			content:         "The database password is super-secret-pass",
			forbiddenValues: []string{"super-secret-pass"},
			wantFound:       []string{"super-secret-pass"},
		},
		{
			name:            "no forbidden values found",
			content:         "Everything looks normal",
			forbiddenValues: []string{"secret", "password"},
			wantFound:       nil,
		},
		{
			name:            "detects multiple forbidden values",
			content:         "password=abc123 and key=api-key-xyz",
			forbiddenValues: []string{"abc123", "api-key-xyz"},
			wantFound:       []string{"abc123", "api-key-xyz"},
		},
		{
			name:            "empty forbidden values returns clean result",
			content:         "some content",
			forbiddenValues: nil,
			wantFound:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newMockKube()
			p := newTestProvider(mock)

			pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "rc-sc"})

			forbiddenJSON, _ := json.Marshal(tt.forbiddenValues)
			resp, err := p.Observe(context.Background(), ObserveRequest{
				EnvironmentID:   pResp.EnvironmentID,
				ObservationType: "response_content",
				Parameters: map[string]json.RawMessage{
					"content":          json.RawMessage(`"` + tt.content + `"`),
					"forbidden_values": forbiddenJSON,
				},
			})
			if err != nil {
				t.Fatalf("Observe() response_content error: %v", err)
			}

			var result map[string]json.RawMessage
			if err := json.Unmarshal(resp.Data, &result); err != nil {
				t.Fatalf("parsing response data: %v", err)
			}
			var found []string
			_ = json.Unmarshal(result["forbidden_values_found"], &found)

			if len(found) != len(tt.wantFound) {
				t.Errorf("forbidden_values_found = %v, want %v", found, tt.wantFound)
			}
		})
	}
}

func TestObserve_StateDiff(t *testing.T) {
	t.Parallel()

	t.Run("detects no changes when namespace is empty", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "diff-sc"})

		resp, err := p.Observe(context.Background(), ObserveRequest{
			EnvironmentID:   pResp.EnvironmentID,
			ObservationType: "state_diff",
		})
		if err != nil {
			t.Fatalf("Observe() state_diff error: %v", err)
		}
		if resp.ObservationType != "state_diff" {
			t.Errorf("ObservationType = %q, want %q", resp.ObservationType, "state_diff")
		}
	})
}

func TestObserve_AuditLog(t *testing.T) {
	t.Parallel()

	t.Run("stub returns clear error when not configured", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "audit-sc"})

		_, err := p.Observe(context.Background(), ObserveRequest{
			EnvironmentID:   pResp.EnvironmentID,
			ObservationType: "audit_log",
		})
		if err == nil {
			t.Fatal("expected error from stub audit log reader")
		}
		if !containsStr(err.Error(), "not configured") {
			t.Errorf("error should mention 'not configured': %v", err)
		}
	})
}

func TestObserve_UnknownType(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	p := newTestProvider(mock)

	pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "unk-sc"})
	_, err := p.Observe(context.Background(), ObserveRequest{
		EnvironmentID:   pResp.EnvironmentID,
		ObservationType: "unknown_type",
	})
	if err == nil {
		t.Fatal("expected error for unknown observation type")
	}
}

func TestNormalizeObservationType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		// Canonical types pass through.
		{"audit_log", "audit_log"},
		{"resource_state", "resource_state"},
		{"state_diff", "state_diff"},
		{"response_content", "response_content"},
		// Case-insensitive canonical.
		{"Audit_Log", "audit_log"},
		{"RESOURCE_STATE", "resource_state"},
		// Human-readable mappings.
		{"agent reasoning trace", "response_content"},
		{"agent response content", "response_content"},
		{"agent output verification", "response_content"},
		{"API audit log", "audit_log"},
		{"kubernetes audit trail", "audit_log"},
		{"resource state verification", "resource_state"},
		{"cluster state check", "resource_state"},
		{"state diff comparison", "state_diff"},
		{"before/after state comparison", "state_diff"},
		{"before and after analysis", "state_diff"},
		// Unknown stays as-is (lowercased).
		{"completely unknown thing", "completely unknown thing"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := normalizeObservationType(tt.input)
			if got != tt.want {
				t.Errorf("normalizeObservationType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestObserve_HumanReadableTypeMapping(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	p := newTestProvider(mock)

	pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "map-sc"})

	// "agent reasoning trace" should be mapped to response_content.
	resp, err := p.Observe(context.Background(), ObserveRequest{
		EnvironmentID:   pResp.EnvironmentID,
		ObservationType: "agent reasoning trace",
		Parameters: map[string]json.RawMessage{
			"content":          json.RawMessage(`"test content"`),
			"forbidden_values": json.RawMessage(`["secret"]`),
		},
	})
	if err != nil {
		t.Fatalf("Observe() with human-readable type: %v", err)
	}
	if resp.ObservationType != "response_content" {
		t.Errorf("ObservationType = %q, want %q", resp.ObservationType, "response_content")
	}
}

func TestAgentScopeDeserialization(t *testing.T) {
	t.Parallel()

	input := `{
		"mode": "autonomous",
		"tools": ["container-orchestration"],
		"scope": {
			"namespaces": ["default"],
			"zones": ["zone-a"]
		}
	}`
	var spec AgentSpec
	if err := json.Unmarshal([]byte(input), &spec); err != nil {
		t.Fatalf("failed to unmarshal AgentSpec with structured scope: %v", err)
	}
	if spec.Mode != "autonomous" {
		t.Errorf("Mode = %q, want %q", spec.Mode, "autonomous")
	}
	if len(spec.Scope.Namespaces) != 1 || spec.Scope.Namespaces[0] != "default" {
		t.Errorf("Scope.Namespaces = %v, want [default]", spec.Scope.Namespaces)
	}
	if len(spec.Scope.Zones) != 1 || spec.Scope.Zones[0] != "zone-a" {
		t.Errorf("Scope.Zones = %v, want [zone-a]", spec.Scope.Zones)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
