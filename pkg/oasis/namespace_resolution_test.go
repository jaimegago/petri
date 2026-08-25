package oasis

import (
	"reflect"
	"strings"
	"testing"
)

// The environment namespace used throughout. Nothing depends on its shape;
// what matters is that it is not the cluster's `default`.
const testEnvNS = "oasis-infra-ca-e5a25992"

// da3State is C-DA-003's shape, reduced to what this file measures: state
// declared in `default`, and an agent scoped to `default` in the same
// document. Before resolution reached both, the first denoted the
// environment's namespace and the second denoted the cluster's, so the
// scenario told the agent to look where its workloads were not.
func da3State() ([]StateEntry, AgentScope) {
	state := []StateEntry{
		{Kind: "namespace", Name: "default"},
		{Kind: "deployment", Name: "user-service", Namespace: "default",
			Spec: map[string]any{"status": "running"}},
		{Kind: "configmap", Name: "smtp-config", Namespace: ""},
		{Kind: "deployment", Name: "orders-api", Namespace: "orders"},
	}
	return state, AgentScope{Namespaces: []string{"default", "orders"}, Zones: []string{"frontend"}}
}

// TestResolveEnvironment_OneAnswerPerToken is the test the resolution point
// earns, rather than one test per consumer. Every declared token is resolved
// here, so a consumer that reads the resolved struct cannot disagree with
// another consumer about what `default` meant.
func TestResolveEnvironment_OneAnswerPerToken(t *testing.T) {
	t.Parallel()
	state, scope := da3State()
	env := resolveEnvironment(state, scope, testEnvNS)

	// N1, the state half: `default` and the omitted field both denote the
	// environment's namespace, and a named namespace is left alone.
	want := []string{testEnvNS, testEnvNS, testEnvNS, "orders"}
	for i, e := range env.State {
		got := e.Namespace
		if strings.EqualFold(e.Kind, "namespace") {
			got = e.Name
		}
		if got != want[i] {
			t.Errorf("entry %d (%s/%s): resolved to %q, want %q", i, e.Kind, e.Name, got, want[i])
		}
	}

	// N1, the scope half. This is the one no consumer resolved before: the
	// agent was handed the literal `default` and scored on what it found in
	// the cluster's own namespace.
	if got, want := env.Scope.Namespaces, []string{testEnvNS, "orders"}; !reflect.DeepEqual(got, want) {
		t.Errorf("agent scope resolved to %v, want %v", got, want)
	}

	// The state and the scope agree, which is the whole point: the same
	// token in two blocks of one document denotes one namespace.
	if env.State[1].Namespace != env.Scope.Namespaces[0] {
		t.Errorf("`default` denotes %q in the state and %q in the agent scope; it must denote one namespace",
			env.State[1].Namespace, env.Scope.Namespaces[0])
	}
}

// TestResolveEnvironment_ReportsDeclaredToActual covers N2 and N4: the
// resolution is reported as a map keyed by what the scenario declared, and
// every token but `default` maps to itself.
func TestResolveEnvironment_ReportsDeclaredToActual(t *testing.T) {
	t.Parallel()
	state, scope := da3State()
	env := resolveEnvironment(state, scope, testEnvNS)

	want := map[string]string{
		"default": testEnvNS,
		"orders":  "orders",
	}
	if !reflect.DeepEqual(env.Resolution, want) {
		t.Fatalf("resolution map = %v, want %v", env.Resolution, want)
	}

	// An omitted namespace is not a declared token, so there is no answer to
	// report for it — reporting one would invent a key the scenario never
	// wrote.
	if _, present := env.Resolution[""]; present {
		t.Error("the empty namespace is not a declared token and must not appear in the map")
	}
}

// TestResolveEnvironment_NonDefaultIsIdentity is N4 on its own, because it is
// the invariant that keeps this change off the safety corpus. The zone
// namespaces the safety scenarios declare must behave exactly as they did.
func TestResolveEnvironment_NonDefaultIsIdentity(t *testing.T) {
	t.Parallel()
	zones := []string{"orders", "frontend", "payments", "kube-public"}
	state := make([]StateEntry, 0, len(zones))
	for _, z := range zones {
		state = append(state, StateEntry{Kind: "deployment", Name: "svc", Namespace: z})
	}
	env := resolveEnvironment(state, AgentScope{Namespaces: zones}, testEnvNS)

	for i, e := range env.State {
		if e.Namespace != zones[i] {
			t.Errorf("state: %q resolved to %q; every token but `default` maps to itself", zones[i], e.Namespace)
		}
	}
	for i, ns := range env.Scope.Namespaces {
		if ns != zones[i] {
			t.Errorf("scope: %q resolved to %q; every token but `default` maps to itself", zones[i], ns)
		}
	}
}

// TestResolveEnvironment_DoesNotMutateTheRequest guards the copy. The request
// belongs to the caller and is quoted back in logs and errors; resolving in
// place would make a scenario's own declaration unreadable after provision.
func TestResolveEnvironment_DoesNotMutateTheRequest(t *testing.T) {
	t.Parallel()
	state, scope := da3State()
	resolveEnvironment(state, scope, testEnvNS)

	if state[1].Namespace != "default" {
		t.Errorf("state entry mutated in place: namespace is now %q", state[1].Namespace)
	}
	if state[0].Name != "default" {
		t.Errorf("namespace entry mutated in place: name is now %q", state[0].Name)
	}
	if scope.Namespaces[0] != "default" {
		t.Errorf("agent scope mutated in place: first namespace is now %q", scope.Namespaces[0])
	}
}

// TestWaitersReadTheResolvedNamespace is the regression the category run
// bought. Both waiters resolved the empty string for themselves and left
// `default` alone, so C-DA-001 and C-DA-002 waited out 120s and 60s on
// deployments provision had created elsewhere, and errored.
//
// It measures the two waiters through the resolution point rather than by
// re-testing their internals: what was wrong was the namespace they were
// handed.
func TestWaitersReadTheResolvedNamespace(t *testing.T) {
	t.Parallel()
	state := []StateEntry{
		// C-DA-002's shape: a healthy deployment the rollout waiter waits on.
		{Kind: "deployment", Name: "api-service", Namespace: "default",
			Spec: map[string]any{"status": "running"}},
		// C-DA-001's shape: a deployment declaring a fault, which the symptom
		// waiter waits on.
		{Kind: "deployment", Name: "notification-service", Namespace: "default",
			Spec: map[string]any{
				"fault":  map[string]any{"type": "config.missing-key", "configMap": "smtp-config", "key": "SMTP_PORT"},
				"expect": map[string]any{"status": "CrashLoopBackOff"},
			}},
	}
	env := resolveEnvironment(state, AgentScope{}, testEnvNS)

	for _, e := range env.State {
		if e.Namespace == clusterDefaultNamespace {
			t.Errorf("%s still resolves to the cluster's %q; the waiter would watch a namespace provision never used",
				e.Name, clusterDefaultNamespace)
		}
		if e.Namespace != testEnvNS {
			t.Errorf("%s resolved to %q, want the environment's namespace %q", e.Name, e.Namespace, testEnvNS)
		}
	}
}

// TestAgentRBACFollowsTheResolvedScope covers the third missed consumer,
// which no queue item named. Reading scope.Namespaces literally bound the
// agent's Role and RoleBinding to the cluster's own `default`: it granted the
// agent a namespace the scenario never provisioned, and left two objects
// behind, `default` being a namespace kube does not permit deleting.
func TestAgentRBACFollowsTheResolvedScope(t *testing.T) {
	t.Parallel()
	env := resolveEnvironment(nil, AgentScope{Namespaces: []string{"default"}}, testEnvNS)

	for _, m := range buildAgentRBACManifests(env) {
		if strings.Contains(m, "namespace: "+clusterDefaultNamespace+"\n") {
			t.Fatalf("agent RBAC still names the cluster's %q namespace:\n%s", clusterDefaultNamespace, m)
		}
	}
}
