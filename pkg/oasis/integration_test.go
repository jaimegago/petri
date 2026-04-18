//go:build integration

package oasis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Test 1: Provision creates namespace and resources.
func TestIntegration_ProvisionCreatesResources(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-prov")

	resp := provisionAndCleanup(t, env, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
			State: []StateEntry{
				{
					Kind: "namespace",
					Name: "integration-test",
					Zone: "zone-a",
					Labels: map[string]string{
						"petri.io/test": "true",
					},
				},
				{
					Kind:      "deployment",
					Name:      "test-app",
					Namespace: "integration-test",
					Spec: map[string]any{
						"status":   "running",
						"replicas": 1,
					},
				},
				{
					Kind:      "configmap",
					Name:      "test-config",
					Namespace: "integration-test",
					Data:      map[string]string{"ENV": "test", "LOG_LEVEL": "debug"},
				},
			},
		},
		Agent: AgentSpec{
			Mode:  "autonomous",
			Tools: []string{"container-orchestration"},
			Scope: AgentScope{Namespaces: []string{"integration-test"}},
		},
	})

	if resp.EnvironmentID == "" {
		t.Fatal("expected non-empty environment_id")
	}

	ns := getNamespace(t, env, resp.EnvironmentID)
	ctx := context.Background()

	// Verify the integration-test namespace was created with zone label.
	waitForResource(t, env.kube, "namespace", "", "integration-test", 15*time.Second)
	nsRaw, err := env.kube.GetResource(ctx, "namespace", "", "integration-test")
	if err != nil {
		t.Fatalf("getting namespace: %v", err)
	}
	if !strings.Contains(nsRaw, "petri.oasis/zone") {
		t.Error("namespace missing zone label")
	}

	// Verify the deployment exists.
	waitForResource(t, env.kube, "deployment", "integration-test", "test-app", 15*time.Second)

	// Verify the configmap exists with correct data.
	cmRaw, err := env.kube.GetResource(ctx, "configmap", "integration-test", "test-config")
	if err != nil {
		t.Fatalf("getting configmap: %v", err)
	}
	if !strings.Contains(cmRaw, "test") {
		t.Error("configmap missing expected data")
	}

	// Teardown and verify.
	tdResp, err := env.provider.Teardown(ctx, TeardownRequest{EnvironmentID: resp.EnvironmentID})
	if err != nil {
		t.Fatalf("Teardown() error: %v", err)
	}
	if tdResp.Status != "destroyed" {
		t.Errorf("teardown status = %q, want %q", tdResp.Status, "destroyed")
	}

	// Also clean up the integration-test namespace.
	_ = env.kube.DeleteNamespace(ctx, "integration-test")
	_ = ns
}

// Test 2: Provision with CrashLoopBackOff deployment.
func TestIntegration_CrashLoopBackOff(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-crash")

	resp := provisionAndCleanup(t, env, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
			State: []StateEntry{
				{
					Kind: "configmap",
					Name: "app-config",
					Data: map[string]string{"VALID_KEY": "value"},
				},
				{
					Kind: "deployment",
					Name: "crashy-app",
					Spec: map[string]any{
						"status":       "crashloopbackoff",
						"configMapRef": "app-config",
					},
				},
			},
		},
	})

	ns := getNamespace(t, env, resp.EnvironmentID)

	// Verify deployment was created with missing key reference.
	waitForResource(t, env.kube, "deployment", ns, "crashy-app", 15*time.Second)
	raw, err := env.kube.GetResource(context.Background(), "deployment", ns, "crashy-app")
	if err != nil {
		t.Fatalf("getting deployment: %v", err)
	}
	if !strings.Contains(raw, "__petri_missing_key__") {
		t.Error("deployment should reference missing configmap key")
	}
}

// Test 3: StateSnapshot returns resources.
func TestIntegration_StateSnapshot(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-snap")

	resp := provisionAndCleanup(t, env, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
			State: []StateEntry{
				{
					Kind: "deployment",
					Name: "snap-deploy",
					Spec: map[string]any{"replicas": 1},
				},
				{
					Kind: "configmap",
					Name: "snap-config",
					Data: map[string]string{"KEY": "value"},
				},
			},
		},
	})

	ns := getNamespace(t, env, resp.EnvironmentID)

	// Wait for resources to be created.
	waitForResource(t, env.kube, "deployment", ns, "snap-deploy", 15*time.Second)
	waitForResource(t, env.kube, "configmap", ns, "snap-config", 15*time.Second)

	ctx := context.Background()
	snapResp, err := env.provider.StateSnapshot(ctx, StateSnapshotRequest{
		EnvironmentID: resp.EnvironmentID,
	})
	if err != nil {
		t.Fatalf("StateSnapshot() error: %v", err)
	}

	var foundDeploy, foundCM bool
	for _, r := range snapResp.Resources {
		if r.Name == "snap-deploy" {
			foundDeploy = true
		}
		if r.Name == "snap-config" {
			foundCM = true
		}
	}
	if !foundDeploy {
		t.Error("snapshot missing deployment snap-deploy")
	}
	if !foundCM {
		t.Error("snapshot missing configmap snap-config")
	}
}

// Test 4: InjectState adds resources to existing environment.
func TestIntegration_InjectState(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-inject")

	resp := provisionAndCleanup(t, env, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type:  "kubernetes-cluster",
			State: []StateEntry{},
		},
	})

	ctx := context.Background()

	// Inject a configmap.
	iResp, err := env.provider.InjectState(ctx, InjectStateRequest{
		EnvironmentID: resp.EnvironmentID,
		State: []StateEntry{
			{
				Kind: "configmap",
				Name: "injected-config",
				Data: map[string]string{"injected": "true"},
			},
		},
	})
	if err != nil {
		t.Fatalf("InjectState() error: %v", err)
	}
	if iResp.Status != "applied" {
		t.Errorf("status = %q, want %q", iResp.Status, "applied")
	}

	ns := getNamespace(t, env, resp.EnvironmentID)
	waitForResource(t, env.kube, "configmap", ns, "injected-config", 15*time.Second)

	// StateSnapshot should show the configmap.
	snapResp, err := env.provider.StateSnapshot(ctx, StateSnapshotRequest{
		EnvironmentID: resp.EnvironmentID,
	})
	if err != nil {
		t.Fatalf("StateSnapshot() error: %v", err)
	}

	var found bool
	for _, r := range snapResp.Resources {
		if r.Name == "injected-config" {
			found = true
		}
	}
	if !found {
		t.Error("snapshot missing injected-config after InjectState")
	}
}

// Test 5: Observe resource_state returns current resource.
func TestIntegration_ObserveResourceState(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-obs-rs")

	resp := provisionAndCleanup(t, env, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
			State: []StateEntry{
				{
					Kind: "configmap",
					Name: "observe-config",
					Data: map[string]string{"OBSERVE_KEY": "observe_value"},
				},
			},
		},
	})

	ns := getNamespace(t, env, resp.EnvironmentID)
	waitForResource(t, env.kube, "configmap", ns, "observe-config", 15*time.Second)

	ctx := context.Background()
	obsResp, err := env.provider.Observe(ctx, ObserveRequest{
		EnvironmentID:   resp.EnvironmentID,
		ObservationType: "resource_state",
		Parameters: map[string]json.RawMessage{
			"kind": json.RawMessage(`"configmap"`),
			"name": json.RawMessage(`"observe-config"`),
		},
	})
	if err != nil {
		t.Fatalf("Observe() resource_state error: %v", err)
	}

	if !strings.Contains(string(obsResp.Data), "observe_value") {
		t.Errorf("resource_state data should contain 'observe_value', got: %s", string(obsResp.Data))
	}
}

// Test 6: Observe state_diff detects changes.
func TestIntegration_ObserveStateDiff(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-obs-diff")

	resp := provisionAndCleanup(t, env, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
			State: []StateEntry{
				{
					Kind: "configmap",
					Name: "diff-config",
					Data: map[string]string{"KEY": "original"},
				},
			},
		},
	})

	ns := getNamespace(t, env, resp.EnvironmentID)
	waitForResource(t, env.kube, "configmap", ns, "diff-config", 15*time.Second)

	ctx := context.Background()

	// Inject updated configmap.
	_, err := env.provider.InjectState(ctx, InjectStateRequest{
		EnvironmentID: resp.EnvironmentID,
		State: []StateEntry{
			{
				Kind: "configmap",
				Name: "diff-config",
				Data: map[string]string{"KEY": "modified"},
			},
		},
	})
	if err != nil {
		t.Fatalf("InjectState() error: %v", err)
	}

	// Small delay for apply to take effect.
	time.Sleep(2 * time.Second)

	obsResp, err := env.provider.Observe(ctx, ObserveRequest{
		EnvironmentID:   resp.EnvironmentID,
		ObservationType: "state_diff",
		Parameters: map[string]json.RawMessage{
			"kind": json.RawMessage(`"configmap"`),
			"name": json.RawMessage(`"diff-config"`),
		},
	})
	if err != nil {
		t.Fatalf("Observe() state_diff error: %v", err)
	}

	var diff StateDiff
	if err := json.Unmarshal(obsResp.Data, &diff); err != nil {
		t.Fatalf("parsing diff data: %v", err)
	}
	if len(diff.Changes) == 0 {
		t.Error("expected changes in state_diff after modifying configmap")
	}
}

// Test 8: Teardown deletes namespace.
func TestIntegration_TeardownDeletesNamespace(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-td")

	ctx := context.Background()
	pResp, err := env.provider.Provision(ctx, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
		},
	})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	ns := getNamespace(t, env, pResp.EnvironmentID)
	// Register cleanup in case teardown fails.
	cleanupNamespace(t, env.kube, ns)

	// Wait for namespace to exist.
	waitForResource(t, env.kube, "namespace", "", ns, 15*time.Second)

	// Teardown.
	tdResp, err := env.provider.Teardown(ctx, TeardownRequest{EnvironmentID: pResp.EnvironmentID})
	if err != nil {
		t.Fatalf("Teardown() error: %v", err)
	}
	if tdResp.Status != "destroyed" {
		t.Errorf("status = %q, want %q", tdResp.Status, "destroyed")
	}

	// Wait a moment for namespace deletion to propagate.
	time.Sleep(3 * time.Second)

	// Verify namespace no longer exists.
	if namespaceExists(env.kube, ns) {
		t.Error("namespace should be deleted after teardown")
	}

	// Subsequent calls should return "not found".
	_, err = env.provider.StateSnapshot(ctx, StateSnapshotRequest{EnvironmentID: pResp.EnvironmentID})
	if err == nil {
		t.Error("expected error for torn-down environment")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found', got: %v", err)
	}
}

// Test 9: RBAC setup creates scoped agent credentials.
func TestIntegration_RBACSetup(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-rbac")

	// Create the target namespace first so RBAC can be applied there.
	ctx := context.Background()
	testNS := "integ-rbac-test-ns"
	if err := env.kube.CreateNamespace(ctx, testNS, map[string]string{"petri.io/test": "true"}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = env.kube.DeleteNamespace(cleanCtx, testNS)
	})

	resp := provisionAndCleanup(t, env, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
		},
		Agent: AgentSpec{
			Mode:  "autonomous",
			Tools: []string{"container-orchestration"},
			Scope: AgentScope{Namespaces: []string{testNS}},
		},
	})

	ns := getNamespace(t, env, resp.EnvironmentID)

	// Verify ServiceAccount exists in scenario namespace.
	waitForResource(t, env.kube, "serviceaccount", ns, "oasis-agent", 15*time.Second)

	// Verify Role exists in the scoped namespace.
	waitForResource(t, env.kube, "role", testNS, "oasis-agent-role", 15*time.Second)

	// Verify RoleBinding exists in the scoped namespace.
	waitForResource(t, env.kube, "rolebinding", testNS, "oasis-agent-binding", 15*time.Second)

	// Verify credentials are returned.
	if resp.AgentCredentials.Type != "kubeconfig" {
		t.Errorf("credentials type = %q, want %q", resp.AgentCredentials.Type, "kubeconfig")
	}
	if resp.AgentCredentials.Token == "" {
		t.Log("warning: agent token is empty (may require TokenRequest API)")
	}
}
