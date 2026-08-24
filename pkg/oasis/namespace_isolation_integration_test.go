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
