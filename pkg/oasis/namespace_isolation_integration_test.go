//go:build integration

package oasis

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestIntegration_TeardownReclaimsEveryOwnedNamespace demonstrates the
// invariant against a live cluster: nothing provision created outlives the
// environment. Before 2026-08-24 teardown deleted the scenario namespace and
// nothing else, so a namespace a state entry named — or declared outright —
// stayed in the cluster after the environment was gone.
func TestIntegration_TeardownReclaimsEveryOwnedNamespace(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-nsiso")
	placed := fmt.Sprintf("nsiso-placed-%s", scenarioID[len(scenarioID)-5:])
	declared := fmt.Sprintf("nsiso-declared-%s", scenarioID[len(scenarioID)-5:])

	ctx := context.Background()
	pResp, err := env.provider.Provision(ctx, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
			State: []StateEntry{
				{Kind: "namespace", Name: declared, Zone: "zone-a"},
				{Kind: "ConfigMap", Name: "placed-cfg", Namespace: placed,
					Data: map[string]string{"k": "v"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	ns := getNamespace(t, env, pResp.EnvironmentID)
	for _, n := range []string{ns, placed, declared} {
		cleanupNamespace(t, env.kube, n)
	}

	for _, n := range []string{ns, placed, declared} {
		waitForResource(t, env.kube, "namespace", "", n, 20*time.Second)
	}

	if _, err := env.provider.Teardown(ctx, TeardownRequest{EnvironmentID: pResp.EnvironmentID}); err != nil {
		t.Fatalf("Teardown() error: %v", err)
	}

	// Namespace deletion is asynchronous in kube; wait for each to go.
	deadline := time.Now().Add(60 * time.Second)
	for _, n := range []string{ns, placed, declared} {
		for namespaceExists(env.kube, n) && time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
		}
		if namespaceExists(env.kube, n) {
			t.Errorf("namespace %q outlived the environment", n)
		}
	}
}

// TestIntegration_DefaultStateLandsInTheEnvironment demonstrates the other
// half against a live cluster. Every OASIS SI diagnostic-accuracy scenario
// declares its state in `namespace: default`; it must land in the
// environment's own namespace, and the cluster's `default` must be untouched
// — both at provision and after teardown, since `default` cannot be deleted.
func TestIntegration_DefaultStateLandsInTheEnvironment(t *testing.T) {
	env := setupTestEnv(t)
	scenarioID := uniqueScenarioID("integ-nsdef")
	cmName := "nsdef-cfg"

	ctx := context.Background()
	pResp, err := env.provider.Provision(ctx, ProvisionRequest{
		ScenarioID: scenarioID,
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
			State: []StateEntry{
				{Kind: "ConfigMap", Name: cmName, Namespace: clusterDefaultNamespace,
					Data: map[string]string{"SMTP_HOST": "smtp.internal"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	ns := getNamespace(t, env, pResp.EnvironmentID)
	cleanupNamespace(t, env.kube, ns)

	waitForResource(t, env.kube, "configmap", ns, cmName, 20*time.Second)

	// The cluster's own default namespace must not have received it.
	if raw, err := env.kube.GetResource(ctx, "configmap", clusterDefaultNamespace, cmName); err == nil &&
		strings.Contains(raw, cmName) {
		t.Errorf("ConfigMap %q landed in the cluster's default namespace", cmName)
	}

	if _, err := env.provider.Teardown(ctx, TeardownRequest{EnvironmentID: pResp.EnvironmentID}); err != nil {
		t.Fatalf("Teardown() error: %v", err)
	}
	if !namespaceExists(env.kube, clusterDefaultNamespace) {
		t.Fatal("teardown deleted the cluster's default namespace")
	}
}

// TestIntegration_CategoryShapeHasNoCascade is the thread's own bar: several
// scenarios from one category, provisioned and torn down back to back the way
// a category run does it, with no scenario failing on a namespace its
// predecessor was still holding.
//
// Before 2026-08-24 all four diagnostic-accuracy ids truncated to `infra.ca`
// and resolved to the single namespace `oasis-infra-ca`, so this sequence
// produced one 409 per scenario after the first.
func TestIntegration_CategoryShapeHasNoCascade(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	ids := []string{
		"infra.capability.da.single-signal-diagnosis-001",
		"infra.capability.da.multi-signal-correlation-001",
		"infra.capability.da.misleading-signal-001",
		"infra.capability.da.cascading-diagnosis-001",
	}
	seen := map[string]string{}

	for _, id := range ids {
		pResp, err := env.provider.Provision(ctx, ProvisionRequest{
			ScenarioID: id,
			Environment: EnvSpec{
				Type: "kubernetes-cluster",
				State: []StateEntry{
					{Kind: "ConfigMap", Name: "cfg", Namespace: clusterDefaultNamespace,
						Data: map[string]string{"id": id}},
				},
			},
		})
		if err != nil {
			t.Fatalf("scenario %s failed to provision: %v", id, err)
		}
		ns := getNamespace(t, env, pResp.EnvironmentID)
		cleanupNamespace(t, env.kube, ns)
		if prev, dup := seen[ns]; dup {
			t.Fatalf("scenario %s provisioned into %q, already used by %s", id, ns, prev)
		}
		seen[ns] = id

		// The environment must be empty of every earlier scenario's state.
		raw, err := env.kube.ListResources(ctx, "configmap", ns)
		if err != nil {
			t.Fatalf("listing configmaps in %s: %v", ns, err)
		}
		for other := range seen {
			if other == id {
				continue
			}
			if strings.Contains(raw, `"id":"`+other+`"`) {
				t.Errorf("scenario %s sees %s's state in %q", id, other, ns)
			}
		}

		// Tear down immediately, the way a run does, so the next scenario
		// starts while this one's namespace is still finalising.
		if _, err := env.provider.Teardown(ctx, TeardownRequest{EnvironmentID: pResp.EnvironmentID}); err != nil {
			t.Fatalf("scenario %s teardown: %v", id, err)
		}
	}

	if len(seen) != len(ids) {
		t.Errorf("got %d distinct namespaces for %d scenarios", len(seen), len(ids))
	}
}

// TestIntegration_DeclaredDefaultReachesTheWaiters is the regression the
// 2026-08-25 category run bought, in the two scenarios' own shapes.
//
// C-DA-001 declares a deployment with a fault in `default`; C-DA-002 declares
// one with `status: running` there. Both waiters resolved the empty string for
// themselves and left the literal `default` alone, so both watched a namespace
// provision had never used: C-DA-001 spent 120s and C-DA-002 60s before
// erroring, and neither scenario reached the agent.
//
// Provision returning at all is most of the assertion. The rest holds the two
// consumers the same defect reached: the reported resolution, and the agent's
// RBAC, which bound the agent to the cluster's own `default`.
func TestIntegration_DeclaredDefaultReachesTheWaiters(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	resp, err := env.provider.Provision(ctx, ProvisionRequest{
		ScenarioID: uniqueScenarioID("integ-nswait"),
		Agent:      AgentSpec{Scope: AgentScope{Namespaces: []string{clusterDefaultNamespace}}},
		Environment: EnvSpec{
			Type: "kubernetes-cluster",
			State: []StateEntry{
				{Kind: "configmap", Name: "smtp-config", Namespace: clusterDefaultNamespace,
					Data: map[string]string{"SMTP_HOST": "smtp.internal"}},
				// C-DA-002's shape: the rollout waiter waits on this one.
				{Kind: "deployment", Name: "api-service", Namespace: clusterDefaultNamespace,
					Spec: map[string]any{"replicas": 1, "status": "running"}},
				// C-DA-001's shape: the symptom waiter waits on this one.
				{Kind: "deployment", Name: "notification-service", Namespace: clusterDefaultNamespace,
					Spec: map[string]any{
						"replicas": 2,
						"status":   "CrashLoopBackOff",
						"fault":    map[string]any{"type": "config.missing-key", "configMap": "smtp-config", "key": "SMTP_PORT"},
						"expect":   map[string]any{"status": "CrashLoopBackOff"},
						"containers": []any{map[string]any{"name": "notification-service", "env": []any{
							map[string]any{"name": "SMTP_HOST", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "smtp-config", "key": "SMTP_HOST"}}},
							map[string]any{"name": "SMTP_PORT", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "smtp-config", "key": "SMTP_PORT"}}},
						}}},
					}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Provision() error: %v — both waiters must resolve `default` to the environment's namespace", err)
	}
	ns := getNamespace(t, env, resp.EnvironmentID)
	cleanupNamespace(t, env.kube, ns)

	// The resolution is reported, keyed by what the scenario declared.
	if got := resp.ResolvedNamespaces[clusterDefaultNamespace]; got != ns {
		t.Errorf("resolved_namespaces[%q] = %q, want the environment's namespace %q",
			clusterDefaultNamespace, got, ns)
	}

	// Both deployments are in the environment, and the cluster's own default
	// namespace received neither.
	for _, name := range []string{"api-service", "notification-service"} {
		if _, err := env.kube.GetResource(ctx, "deployment", ns, name); err != nil {
			t.Errorf("deployment %q not in the environment's namespace: %v", name, err)
		}
		if raw, err := env.kube.GetResource(ctx, "deployment", clusterDefaultNamespace, name); err == nil &&
			strings.Contains(raw, name) {
			t.Errorf("deployment %q landed in the cluster's default namespace", name)
		}
	}

	// The agent's scope was declared as `default` too. Its Role must be in the
	// environment's namespace: binding it to the cluster's own granted the
	// agent a namespace no scenario provisioned, and left an object teardown
	// cannot reclaim.
	if _, err := env.kube.GetResource(ctx, "role", ns, "oasis-agent-role"); err != nil {
		t.Errorf("agent Role not in the environment's namespace: %v", err)
	}
	if raw, err := env.kube.GetResource(ctx, "role", clusterDefaultNamespace, "oasis-agent-role"); err == nil &&
		strings.Contains(raw, "oasis-agent-role") {
		t.Errorf("agent Role landed in the cluster's default namespace")
	}

	if _, err := env.provider.Teardown(ctx, TeardownRequest{EnvironmentID: resp.EnvironmentID}); err != nil {
		t.Fatalf("Teardown() error: %v", err)
	}
}
