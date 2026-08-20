package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jaimegago/petri/pkg/preflight"
)

func newTestProvider(mock *mockKubeClient) *petriProvider {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := New(ProviderConfig{}, mock, log).(*petriProvider)
	// Stub the registry probe so unit tests never make real HTTP calls.
	// Tests that exercise the probe (async_test.go) override this field
	// after construction.
	p.probeImage = stubProbeImage
	return p
}

func stubProbeImage(_ context.Context, _ string) (preflight.ImageProbeResult, error) {
	return preflight.ImageProbeResult{ManifestOK: true}, nil
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

	t.Run("waits for running deployments before returning ready", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "wait-sc",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "Deployment", Name: "web-app", Namespace: "frontend", Spec: map[string]any{"status": "running", "replicas": float64(3)}},
					{Kind: "Service", Name: "web-svc", Namespace: "frontend"},
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
		// Should have called WaitForRollout for the running deployment.
		if len(mock.waitRolloutCalls) != 1 {
			t.Fatalf("expected 1 WaitForRollout call, got %d: %v", len(mock.waitRolloutCalls), mock.waitRolloutCalls)
		}
		if mock.waitRolloutCalls[0] != "frontend/web-app" {
			t.Errorf("WaitForRollout called with %q, want %q", mock.waitRolloutCalls[0], "frontend/web-app")
		}
	})

	t.Run("skips wait for unhealthy deployment statuses", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "skip-sc",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "Deployment", Name: "crash-app", Namespace: "ns1", Spec: map[string]any{"status": "CrashLoopBackOff"}},
					{Kind: "Deployment", Name: "oom-app", Namespace: "ns1", Spec: map[string]any{"status": "oomkilled"}},
					{Kind: "Deployment", Name: "pend-app", Namespace: "ns1", Spec: map[string]any{"status": "pending"}},
					{Kind: "Deployment", Name: "err-app", Namespace: "ns1", Spec: map[string]any{"status": "error"}},
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
		// No WaitForRollout calls expected.
		if len(mock.waitRolloutCalls) != 0 {
			t.Errorf("expected 0 WaitForRollout calls, got %d: %v", len(mock.waitRolloutCalls), mock.waitRolloutCalls)
		}
	})

	t.Run("returns error when deployment rollout times out", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		mock.waitRolloutErr = map[string]error{
			"frontend/web-app": fmt.Errorf("timed out waiting for rollout"),
		}
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "timeout-sc",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "Deployment", Name: "web-app", Namespace: "frontend", Spec: map[string]any{"status": "running"}},
				},
			},
		}
		_, err := p.Provision(context.Background(), req)
		if err == nil {
			t.Fatal("expected error when rollout times out")
		}
		if !strings.Contains(err.Error(), "frontend/web-app") {
			t.Errorf("error should mention failed deployment: %v", err)
		}
		// Cleanup runs in a background goroutine on the rollout-failure
		// path (ADR 0011), so wait for it before asserting on the mock.
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if !p.WaitAsyncTasks(waitCtx) {
			t.Fatal("async cleanup did not finish within 2s")
		}
		if len(mock.deletedNamespacesSnapshot()) == 0 {
			t.Error("expected namespace to be deleted on rollout failure")
		}
	})

	t.Run("skips wait for degraded and elevated_error_rate deployments", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "mixed-sc",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "Deployment", Name: "degraded-app", Namespace: "ns1", Spec: map[string]any{"status": "degraded"}},
					{Kind: "Deployment", Name: "error-rate-app", Namespace: "ns1", Spec: map[string]any{"status": "elevated_error_rate"}},
					{Kind: "Deployment", Name: "crash-app", Namespace: "ns1", Spec: map[string]any{"status": "CrashLoopBackOff"}},
				},
			},
		}
		if _, err := p.Provision(context.Background(), req); err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		// Only status=running should be waited on; none of these qualify.
		if len(mock.waitRolloutCalls) != 0 {
			t.Fatalf("expected 0 WaitForRollout calls, got %d: %v", len(mock.waitRolloutCalls), mock.waitRolloutCalls)
		}
	})

	t.Run("skips wait when spec has no status field", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		req := ProvisionRequest{
			ScenarioID: "default-sc",
			Environment: EnvSpec{
				State: []StateEntry{
					{Kind: "Deployment", Name: "my-app", Namespace: "prod", Spec: map[string]any{"replicas": float64(2)}},
				},
			},
		}
		if _, err := p.Provision(context.Background(), req); err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		// No explicit status → skip wait (only explicit status=running is waited on).
		if len(mock.waitRolloutCalls) != 0 {
			t.Fatalf("expected 0 WaitForRollout calls, got %d", len(mock.waitRolloutCalls))
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

	t.Run("returns namespace dump when kind or name missing", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "obs-err"})
		resp, err := p.Observe(context.Background(), ObserveRequest{
			EnvironmentID:   pResp.EnvironmentID,
			ObservationType: "resource_state",
			Parameters:      map[string]json.RawMessage{"kind": json.RawMessage(`"deployments"`)},
		})
		if err != nil {
			t.Fatalf("Observe() resource_state without name should succeed: %v", err)
		}
		if resp.ObservationType != "resource_state" {
			t.Errorf("ObservationType = %q, want %q", resp.ObservationType, "resource_state")
		}
		if len(resp.Data) == 0 {
			t.Error("expected non-empty data for namespace dump")
		}
	})
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

	t.Run("stub returns unreachable evidence_source when audit log not configured", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)

		pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "audit-sc"})

		resp, err := p.Observe(context.Background(), ObserveRequest{
			EnvironmentID:   pResp.EnvironmentID,
			ObservationType: "audit_log",
		})
		if err != nil {
			t.Fatalf("Observe() audit_log should not error when unconfigured: %v", err)
		}
		if resp.ObservationType != "audit_log" {
			t.Errorf("ObservationType = %q, want %q", resp.ObservationType, "audit_log")
		}
		if resp.EvidenceSource.Status != "unreachable" {
			t.Errorf("evidence_source.status = %q, want %q", resp.EvidenceSource.Status, "unreachable")
		}
		if resp.EvidenceSource.Type != "audit_log_file" {
			t.Errorf("evidence_source.type = %q, want %q", resp.EvidenceSource.Type, "audit_log_file")
		}
		// Data should contain an empty entries array.
		var result map[string]json.RawMessage
		if err := json.Unmarshal(resp.Data, &result); err != nil {
			t.Fatalf("parsing audit_log data: %v", err)
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(result["entries"], &entries); err != nil {
			t.Fatalf("parsing entries: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
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
		// Case-insensitive canonical.
		{"Audit_Log", "audit_log"},
		{"RESOURCE_STATE", "resource_state"},
		// Human-readable mappings.
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

// ── Conformance ──────────────────────────────────────────────────────────────

func TestConformance_SIProfile_RequirementKeysPresent(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := New(ProviderConfig{LabLevel: 1}, mock, log).(*petriProvider)

	resp, err := p.Conformance(context.Background(), "oasis-profile-software-infrastructure")
	if err != nil {
		t.Fatalf("Conformance() error: %v", err)
	}

	// All seven SI requirement keys must be present with expected types.
	req := resp.Requirements
	if req.EnvironmentType != "kubernetes-cluster" {
		t.Errorf("environment_type = %q, want %q", req.EnvironmentType, "kubernetes-cluster")
	}
	if req.ComplexityTierSupported < 1 || req.ComplexityTierSupported > 3 {
		t.Errorf("complexity_tier_supported = %d, want 1-3", req.ComplexityTierSupported)
	}
	if len(req.OASISCoreSpecVersion) == 0 {
		t.Error("oasis_core_spec_version should not be empty")
	}
	wantVersions := map[string]bool{"0.4.0": false, "1.0.0-rc1.5": false, "1.0.0-rc1.11": false, "1.0.0-rc1.12": false}
	for _, v := range req.OASISCoreSpecVersion {
		if _, ok := wantVersions[v]; ok {
			wantVersions[v] = true
		}
	}
	for v, found := range wantVersions {
		if !found {
			t.Errorf("oasis_core_spec_version missing %q", v)
		}
	}
	if len(req.EvidenceSourcesAvailable) == 0 {
		t.Error("evidence_sources_available should not be empty")
	}
	// state_injection should be true (translate.go implements all SI types).
	if !req.StateInjection {
		t.Error("state_injection should be true")
	}
	// Without --oasis, audit and network should be false.
	if req.AuditPolicyInstallation {
		t.Error("audit_policy_installation should be false without --oasis")
	}
	if req.NetworkPolicyEnforcement {
		t.Error("network_policy_enforcement should be false without --oasis")
	}
	// value_containment_support is declared unconditionally on the supported-profile path.
	if !req.ValueContainmentSupport {
		t.Error("value_containment_support should be true on the SI supported path")
	}

	if resp.Profile != "oasis-profile-software-infrastructure" {
		t.Errorf("profile = %q, want SI profile identifier", resp.Profile)
	}
}

func TestConformance_UnsupportedProfile(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := New(ProviderConfig{LabLevel: 1}, mock, log).(*petriProvider)

	resp, err := p.Conformance(context.Background(), "oasis-profile-unknown")
	if err != nil {
		t.Fatalf("Conformance() error: %v", err)
	}
	if resp.Supported {
		t.Error("supported should be false for unknown profile")
	}
	if len(resp.UnmetRequirements) == 0 {
		t.Error("unmet_requirements should list the unsupported profile")
	}
	// Unsupported-profile early return emits zero-value Requirements.
	if resp.Requirements.ValueContainmentSupport {
		t.Error("value_containment_support should be false (zero value) on the unsupported-profile early return path")
	}
}

func TestConformance_NetworkPolicyFalse_WhenCalicoNotRunning(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	// OASISModeEnabled but no calico-node DaemonSet in kube-system.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := New(ProviderConfig{
		LabLevel:         1,
		OASISModeEnabled: true,
		AuditLogPath:     "/nonexistent/audit.log", // will fail os.Stat
	}, mock, log).(*petriProvider)

	resp, err := p.Conformance(context.Background(), "oasis-profile-software-infrastructure")
	if err != nil {
		t.Fatalf("Conformance() error: %v", err)
	}
	if resp.Requirements.NetworkPolicyEnforcement {
		t.Error("network_policy_enforcement should be false when calico-node DaemonSet is not in the cluster")
	}
	// Should have an unmet requirement for network_policy_enforcement.
	found := false
	for _, u := range resp.UnmetRequirements {
		if u.Requirement == "network_policy_enforcement" {
			found = true
		}
	}
	if !found {
		t.Error("expected unmet_requirement entry for network_policy_enforcement")
	}
}

func TestConformance_NetworkPolicyTrue_WhenCalicoRunning(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	// Set up mock kube to return ready calico resources.
	mock.resources["daemonsets/kube-system/calico-node"] = `{"status":{"numberReady":2,"desiredNumberScheduled":2}}`
	mock.resources["deployments/kube-system/calico-kube-controllers"] = `{"status":{"readyReplicas":1,"replicas":1}}`

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := New(ProviderConfig{
		LabLevel:         2,
		OASISModeEnabled: true,
		AuditLogPath:     "/nonexistent/audit.log",
	}, mock, log).(*petriProvider)

	resp, err := p.Conformance(context.Background(), "oasis-profile-software-infrastructure")
	if err != nil {
		t.Fatalf("Conformance() error: %v", err)
	}
	if !resp.Requirements.NetworkPolicyEnforcement {
		t.Error("network_policy_enforcement should be true when calico-node and calico-kube-controllers are ready")
	}
	if resp.Requirements.ComplexityTierSupported != 2 {
		t.Errorf("complexity_tier_supported = %d, want 2", resp.Requirements.ComplexityTierSupported)
	}
}

// ── Evidence source on all observation types ─────────────────────────────────

func TestObserve_EvidenceSource_AllTypes(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	p := newTestProvider(mock)
	pResp, _ := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "ev-sc"})

	tests := []struct {
		name       string
		obsType    string
		params     map[string]json.RawMessage
		wantType   string
		wantStatus string
	}{
		{
			name:       "resource_state",
			obsType:    "resource_state",
			params:     nil,
			wantType:   "kube_api",
			wantStatus: "available",
		},
		{
			name:       "state_diff",
			obsType:    "state_diff",
			params:     nil,
			wantType:   "kube_api",
			wantStatus: "available",
		},
		{
			name:       "audit_log (stub, unreachable)",
			obsType:    "audit_log",
			params:     nil,
			wantType:   "audit_log_file",
			wantStatus: "unreachable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp, err := p.Observe(context.Background(), ObserveRequest{
				EnvironmentID:   pResp.EnvironmentID,
				ObservationType: tt.obsType,
				Parameters:      tt.params,
			})
			if err != nil {
				t.Fatalf("Observe(%s) error: %v", tt.obsType, err)
			}
			if resp.EvidenceSource.Type != tt.wantType {
				t.Errorf("evidence_source.type = %q, want %q", resp.EvidenceSource.Type, tt.wantType)
			}
			if resp.EvidenceSource.Status != tt.wantStatus {
				t.Errorf("evidence_source.status = %q, want %q", resp.EvidenceSource.Status, tt.wantStatus)
			}
		})
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

// ── Audit log query scope ─────────────────────────────────────────────────────

// recordingAuditReader captures the query it was handed so a test can assert on
// the scope the provider built, rather than on entries a fixture happened to
// contain.
type recordingAuditReader struct {
	got AuditLogQuery
}

func (r *recordingAuditReader) Query(_ context.Context, q AuditLogQuery) ([]AuditEntry, error) {
	r.got = q
	return nil, nil
}

func TestObserveAuditLog_QueryScope(t *testing.T) {
	t.Parallel()

	// An audit_log observation with no namespace parameter must query unscoped.
	//
	// Defaulting to the environment namespace is what defeated every
	// `must_not action` assertion in the 2026-08-19 safety re-run: the forbidden
	// actions name lab workload namespaces, the environment namespace is
	// oasis-infra-sa, and the two never intersect. The assertion then passed on
	// an empty set for reasons unrelated to the agent.
	t.Run("omitted namespace parameter queries unscoped", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)
		rec := &recordingAuditReader{}
		p.audit = rec

		pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "audit-scope-unscoped"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}
		env, err := p.store.get(pResp.EnvironmentID)
		if err != nil {
			t.Fatalf("store.get() after Provision: %v", err)
		}

		if _, err := p.Observe(context.Background(), ObserveRequest{
			EnvironmentID:   pResp.EnvironmentID,
			ObservationType: "audit_log",
		}); err != nil {
			t.Fatalf("Observe() audit_log error: %v", err)
		}

		if rec.got.Namespace != "" {
			t.Errorf("query Namespace = %q, want %q (unscoped); defaulting to the environment namespace %q makes forbidden-action assertions unfalsifiable",
				rec.got.Namespace, "", env.Namespace)
		}
	})

	t.Run("supplied namespace parameter scopes the query", func(t *testing.T) {
		t.Parallel()
		mock := newMockKube()
		p := newTestProvider(mock)
		rec := &recordingAuditReader{}
		p.audit = rec

		pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "audit-scope-scoped"})
		if err != nil {
			t.Fatalf("Provision() error: %v", err)
		}

		if _, err := p.Observe(context.Background(), ObserveRequest{
			EnvironmentID:   pResp.EnvironmentID,
			ObservationType: "audit_log",
			Parameters: map[string]json.RawMessage{
				"namespace": json.RawMessage(`"production"`),
			},
		}); err != nil {
			t.Fatalf("Observe() audit_log error: %v", err)
		}

		if rec.got.Namespace != "production" {
			t.Errorf("query Namespace = %q, want %q", rec.got.Namespace, "production")
		}
	})
}

// TestConformance_SatisfiesVendoredProfileRequirement reads the SI profile's
// own machine-readable constraint out of the vendored spec and checks that
// petri declares a core spec version satisfying it.
//
// It exists because of a live failure on 2026-08-20. oasisctl advanced its
// spec pin to 1.0.0-rc1.12; petri still declared rc1.11 as its newest, and the
// provider conformance handshake rejected every scenario in the run:
//
//	oasis_core_spec_version must include a version compatible with
//	"1.0.0-rc1.12", provider declared [0.4.0 1.0.0-rc1.5 1.0.0-rc1.11]
//
// Nothing caught it earlier because petri's own tests asserted a hardcoded
// list against itself, which is a tautology — the declaration and the
// expectation moved together or not at all. The constraint the declaration has
// to satisfy lives in another repository, so the test has to read it from
// there. `docs/oasis-spec` is that repository, vendored as a submodule, and
// advancing the submodule is now what makes this test demand a new version.
func TestConformance_SatisfiesVendoredProfileRequirement(t *testing.T) {
	const requirementsPath = "../../docs/oasis-spec/profiles/software-infrastructure/provider-conformance-requirements.yaml"

	raw, err := os.ReadFile(requirementsPath)
	if err != nil {
		t.Skipf("vendored spec not checked out (%v); run: git submodule update --init", err)
	}

	var doc struct {
		OASISCoreDependency string `yaml:"oasis_core_dependency"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", requirementsPath, err)
	}
	required := strings.TrimPrefix(strings.TrimSpace(doc.OASISCoreDependency), ">=")
	if required == "" {
		t.Fatal("profile declares no oasis_core_dependency")
	}

	p := &petriProvider{}
	resp, err := p.Conformance(context.Background(), "oasis-profile-software-infrastructure")
	if err != nil {
		t.Fatalf("Conformance: %v", err)
	}

	for _, declared := range resp.OASISCoreSpecVersions {
		if satisfiesCoreVersion(declared, required) {
			return
		}
	}
	t.Errorf("no declared core spec version satisfies the SI profile's %q; declared %v.\n"+
		"The vendored spec advanced and pkg/oasis/provider.go did not — add the new "+
		"iteration to coreSpecVersions once petri actually implements it.",
		doc.OASISCoreDependency, resp.OASISCoreSpecVersions)
}

// satisfiesCoreVersion implements the comparison provider.go's own comment
// describes and oasisctl's preflight performs: an exact prerelease-class match
// with provider-iteration >= required-iteration. It is deliberately narrow —
// it answers the `1.0.0-rc1.N` line and nothing else, because that is the only
// shape the SI profile has ever required, and a general semver range parser
// here would be untested machinery guarding a single string.
func satisfiesCoreVersion(declared, required string) bool {
	if declared == required {
		return true
	}
	dBase, dIter, ok := splitRC1(declared)
	if !ok {
		return false
	}
	rBase, rIter, ok := splitRC1(required)
	if !ok {
		return false
	}
	return dBase == rBase && dIter >= rIter
}

// splitRC1 parses `X.Y.Z-rc1.N` into its `X.Y.Z-rc1` class and its N.
func splitRC1(v string) (base string, iter int, ok bool) {
	i := strings.LastIndex(v, ".")
	if i < 0 || !strings.Contains(v, "-rc") {
		return "", 0, false
	}
	n, err := strconv.Atoi(v[i+1:])
	if err != nil {
		return "", 0, false
	}
	return v[:i], n, true
}
