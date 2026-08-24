package oasis

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The OASIS SI profile's diagnostic-accuracy category, verbatim. These four
// are the set a category run provisions in sequence, and they are the reason
// this file exists: truncated to eight characters every one of them reads
// `infra.ca`, so before 2026-08-24 all four resolved to the single namespace
// `oasis-infra-ca` and each provisioned into the one its predecessor was
// still tearing down.
var diagnosticAccuracyIDs = []string{
	"infra.capability.da.single-signal-diagnosis-001",
	"infra.capability.da.multi-signal-correlation-001",
	"infra.capability.da.misleading-signal-001",
	"infra.capability.da.cascading-diagnosis-001",
}

// Representatives of the profile's other categories. Included so the test
// covers the cross-category case too: all 21 safety ids truncate to
// `infra.sa`, so the same collapse held there.
var otherProfileIDs = []string{
	"infra.capability.oe.legitimate-restart-001",
	"infra.safety.be.zone-violation-001",
	"infra.safety.be.implicit-zone-crossing-001",
}

// maxNamespaceLen is Kubernetes' RFC 1123 label bound, which a namespace name
// must respect.
const maxNamespaceLen = 63

// TestScenarioNamespace_UniquePerEnvironment is the bar the fix is measured
// at, over the profile's actual ids rather than a hand-picked pair: a
// category run's scenarios, and a re-run of each, never share a namespace.
//
// Each provision is given its own environment id because that is what
// Provision does — a fresh UUID per call. That is also where the uniqueness
// comes from: the scenario id contributes a prefix and nothing else, so what
// is being tested is that the namespace is determined by the environment
// rather than by the scenario.
func TestScenarioNamespace_UniquePerEnvironment(t *testing.T) {
	t.Parallel()

	ids := append(append([]string{}, diagnosticAccuracyIDs...), otherProfileIDs...)
	seen := map[string]string{}

	// Two passes: a category run, then the same scenarios run again.
	for pass := 0; pass < 2; pass++ {
		for i, id := range ids {
			envID := fmt.Sprintf("%08d-2222-3333-4444-555555555555", pass*len(ids)+i)
			ns := scenarioNamespace(id, envID)
			if prev, dup := seen[ns]; dup {
				t.Errorf("namespace %q resolved for both %q and %q", ns, prev, id)
			}
			seen[ns] = fmt.Sprintf("%s (pass %d)", id, pass)
			if len(ns) > maxNamespaceLen {
				t.Errorf("namespace %q is %d chars, over the RFC 1123 bound of %d", ns, len(ns), maxNamespaceLen)
			}
			if !strings.HasPrefix(ns, "oasis-") {
				t.Errorf("namespace %q should start with 'oasis-'", ns)
			}
			if strings.Trim(ns, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
				t.Errorf("namespace %q contains characters kube will not accept", ns)
			}
		}
	}

	if want := len(ids) * 2; len(seen) != want {
		t.Errorf("got %d distinct namespaces, want %d", len(seen), want)
	}
}

// TestScenarioNamespace_DoesNotDiscloseTheScenario holds the constraint that
// decided the prefix. Provision hands the agent a kubeconfig scoped to this
// namespace, so the name is something the agent under test reads before it
// diagnoses anything. It may not describe the scenario.
//
// This is not hypothetical. A prefix taken from the tail of the id was
// written during this change, and TestIntegration_DeclaredFaultReachesSymptom
// caught it: the namespace for that scenario contained the word "fault". The
// same prefix would have given C-DA-003 a namespace starting
// `oasis-misleading-s`.
func TestScenarioNamespace_DoesNotDiscloseTheScenario(t *testing.T) {
	t.Parallel()
	for _, id := range append(append([]string{}, diagnosticAccuracyIDs...), otherProfileIDs...) {
		ns := scenarioNamespace(id, "11111111-2222-3333-4444-555555555555")
		if m := mechanismWords.FindString(ns); m != "" {
			t.Errorf("namespace %q for scenario %q names the mechanism (%q)", ns, id, m)
		}
		tail := id[strings.LastIndex(id, ".")+1:]
		if head := tail[:6]; strings.Contains(ns, head) {
			t.Errorf("namespace %q carries scenario %q's descriptive name", ns, id)
		}
	}
}

// The environment id has to be present in full — that is where the uniqueness
// lives — and the scenario prefix has to survive alongside it.
func TestScenarioNamespace_CarriesBothParts(t *testing.T) {
	t.Parallel()
	ns := scenarioNamespace("infra.capability.da.single-signal-diagnosis-001", "abc-123")
	if !strings.HasPrefix(ns, "oasis-infra-ca-") {
		t.Errorf("namespace %q lost its scenario prefix", ns)
	}
	if !strings.HasSuffix(ns, "abc-123") {
		t.Errorf("namespace %q does not carry the environment id", ns)
	}
}

// A missing environment id must not panic. It did before 2026-08-24, on
// envID[:8] against an empty string, whenever the scenario id was also empty.
func TestScenarioNamespace_DegradesWithoutPanicking(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ scenario, envID, want string }{
		{"", "", "oasis"},
		{"", "abc", "oasis-abc"},
		{"sc1", "", "oasis-sc1"},
		{"UPPER.case", "e-1", "oasis-upper-ca-e-1"},
	} {
		if got := scenarioNamespace(tt.scenario, tt.envID); got != tt.want {
			t.Errorf("scenarioNamespace(%q, %q) = %q, want %q", tt.scenario, tt.envID, got, tt.want)
		}
	}
}

// TestProvision_DefaultIsTheEnvironmentNamespace holds the second half of the
// fix. Every diagnostic-accuracy scenario declares its state in
// `namespace: default`; before 2026-08-24 that was the cluster's own
// namespace, which teardown cannot delete, so each scenario's workloads were
// still there when the next scenario's agent looked.
func TestProvision_DefaultIsTheEnvironmentNamespace(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	p := newTestProvider(mock)

	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "infra.capability.da.single-signal-diagnosis-001",
		Environment: EnvSpec{State: []StateEntry{
			{Kind: "ConfigMap", Name: "smtp-config", Namespace: clusterDefaultNamespace,
				Data: map[string]string{"SMTP_HOST": "smtp.internal"}},
		}},
	})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	env, err := p.store.get(resp.EnvironmentID)
	if err != nil {
		t.Fatalf("store.get(): %v", err)
	}

	mock.mu.Lock()
	created := append([]createdNS{}, mock.createdNamespaces...)
	applied := strings.Join(append([]string{}, mock.appliedManifests...), "\n---\n")
	mock.mu.Unlock()

	for _, ns := range created {
		if ns.name == clusterDefaultNamespace {
			t.Error("provision created the cluster's default namespace")
		}
	}
	if len(created) != 1 || created[0].name != env.Namespace {
		t.Errorf("created namespaces = %+v, want only %q", created, env.Namespace)
	}
	if len(env.OwnedNamespaces) != 0 {
		t.Errorf("OwnedNamespaces = %v, want empty: `default` is the environment's own", env.OwnedNamespaces)
	}
	if !strings.Contains(applied, "namespace: "+env.Namespace) {
		t.Errorf("the ConfigMap did not land in %q; applied:\n%s", env.Namespace, applied)
	}
	if strings.Contains(applied, "namespace: "+clusterDefaultNamespace+"\n") {
		t.Errorf("something was applied into the cluster's default namespace:\n%s", applied)
	}
}

// TestTeardown_ReclaimsEveryNamespaceProvisionCreated holds the invariant
// directly: nothing provision created outlives the environment.
func TestTeardown_ReclaimsEveryNamespaceProvisionCreated(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	p := newTestProvider(mock)

	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "infra.safety.be.zone-violation-001",
		Environment: EnvSpec{State: []StateEntry{
			// Declared outright, created by the injector with its zone label.
			{Kind: "namespace", Name: "frontend", Zone: "zone-a"},
			// Named by a resource, pre-created by Provision.
			{Kind: "ConfigMap", Name: "checkout-cfg", Namespace: "payments"},
			// The environment's own, under both spellings.
			{Kind: "ConfigMap", Name: "local-a", Namespace: clusterDefaultNamespace},
			{Kind: "ConfigMap", Name: "local-b"},
		}},
	})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	env, err := p.store.get(resp.EnvironmentID)
	if err != nil {
		t.Fatalf("store.get(): %v", err)
	}
	if len(env.OwnedNamespaces) != 2 {
		t.Fatalf("OwnedNamespaces = %v, want the two beyond the environment's own", env.OwnedNamespaces)
	}

	if _, err := p.Teardown(context.Background(), TeardownRequest{EnvironmentID: resp.EnvironmentID}); err != nil {
		t.Fatalf("Teardown() error: %v", err)
	}

	deleted := map[string]bool{}
	for _, ns := range mock.deletedNamespacesSnapshot() {
		deleted[ns] = true
	}
	for _, want := range []string{env.Namespace, "frontend", "payments"} {
		if !deleted[want] {
			t.Errorf("teardown left %q behind; deleted = %v", want, mock.deletedNamespacesSnapshot())
		}
	}
	if deleted[clusterDefaultNamespace] {
		t.Error("teardown attempted to delete the cluster's default namespace")
	}
}

// A namespace the environment cannot reclaim is refused before anything is
// created. Without this, the teardown invariant above would have petri
// deleting kube-system.
func TestProvision_RejectsReservedNamespaces(t *testing.T) {
	t.Parallel()

	for name, state := range map[string][]StateEntry{
		"named by a resource":  {{Kind: "ConfigMap", Name: "x", Namespace: "kube-system"}},
		"declared outright":    {{Kind: "namespace", Name: "kube-public"}},
		"node lease namespace": {{Kind: "ConfigMap", Name: "x", Namespace: "kube-node-lease"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mock := newMockKube()
			p := newTestProvider(mock)
			_, err := p.Provision(context.Background(), ProvisionRequest{
				ScenarioID:  "reserved",
				Environment: EnvSpec{State: state},
			})
			if err == nil {
				t.Fatal("expected provision to refuse a reserved namespace")
			}
			if !strings.Contains(err.Error(), "reserved namespace") {
				t.Errorf("error does not name the cause: %v", err)
			}
			mock.mu.Lock()
			created := len(mock.createdNamespaces)
			mock.mu.Unlock()
			if created != 0 {
				t.Errorf("expected nothing created before the refusal, got %d namespaces", created)
			}
		})
	}
}
