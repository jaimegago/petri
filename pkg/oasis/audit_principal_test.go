package oasis

import (
	"context"
	"encoding/json"
	"testing"
)

// An audit_log observation carries every entry captured AND the identity the
// agent authenticates as. It never filters to the agent.
//
// petri knows which entries are the agent's once the caller declares the
// principal, and still does not narrow: a consumer past this boundary cannot
// recover what a filter discarded, and the difference between "the log was
// empty" and "the agent did nothing" is what a safety verdict rests on.

func auditPayload(t *testing.T, data any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshalling observation data: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshalling observation data: %v", err)
	}
	return out
}

func TestObserveAuditLog_DeclaresAgentPrincipal(t *testing.T) {
	t.Parallel()

	const principal = "system:serviceaccount:default:joe-oasis-e2e"

	mock := newMockKube()
	p := newTestProvider(mock)
	p.audit = &recordingAuditReader{}

	pResp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "audit-principal-declared",
		Agent:      AgentSpec{Principal: principal},
	})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}

	resp, err := p.Observe(context.Background(), ObserveRequest{
		EnvironmentID:   pResp.EnvironmentID,
		ObservationType: "audit_log",
	})
	if err != nil {
		t.Fatalf("Observe() audit_log error: %v", err)
	}

	got := auditPayload(t, resp.Data)
	if got["agent_principal"] != principal {
		t.Errorf("agent_principal = %v, want %q", got["agent_principal"], principal)
	}
	if _, ok := got["entries"]; !ok {
		t.Error("payload carries no entries key; the observation is annotated, not replaced")
	}
}

func TestObserveAuditLog_OmitsPrincipalWhenUndeclared(t *testing.T) {
	t.Parallel()

	// A caller that declares nothing gets no agent_principal — not a guess.
	// petri provisions an "oasis-agent" ServiceAccount, but the harness is free
	// to run the agent under its own credential and does, so answering from
	// what petri provisioned would name a principal that never acts.
	mock := newMockKube()
	p := newTestProvider(mock)
	p.audit = &recordingAuditReader{}

	pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "audit-principal-undeclared"})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}

	resp, err := p.Observe(context.Background(), ObserveRequest{
		EnvironmentID:   pResp.EnvironmentID,
		ObservationType: "audit_log",
	})
	if err != nil {
		t.Fatalf("Observe() audit_log error: %v", err)
	}

	got := auditPayload(t, resp.Data)
	if _, present := got["agent_principal"]; present {
		t.Errorf("agent_principal present (%v) when the caller declared none", got["agent_principal"])
	}
}
