package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Part 1: Provision pre-check ───────────────────────────────────────────────

func TestProvision_PreCheck_TerminatingNamespace_Returns409Fast(t *testing.T) {
	t.Parallel()

	capture := &captureHandler{}
	mock := newMockKube()
	// Scenario namespace is derived from the scenario id; pre-mark it as
	// Terminating so the pre-check fires.
	scenarioNS := scenarioNamespace("pre-term", "ignored")
	mock.namespacePhases[scenarioNS] = namespacePhaseTerminating
	p := providerWithLogger(mock, slog.New(capture))

	start := time.Now()
	resp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "pre-term"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected ErrNamespaceTerminating, got nil")
	}
	var term *ErrNamespaceTerminating
	if !errors.As(err, &term) {
		t.Fatalf("expected *ErrNamespaceTerminating, got %T: %v", err, err)
	}
	if term.Namespace != scenarioNS {
		t.Errorf("namespace = %q, want %q", term.Namespace, scenarioNS)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("pre-check took %v; expected <100ms — no kubectl-apply should have been attempted", elapsed)
	}
	if resp.EnvironmentID != "" {
		t.Errorf("expected empty response, got %+v", resp)
	}
	// No namespace creation or apply should have happened.
	if len(mock.createdNamespaces) != 0 {
		t.Errorf("expected 0 CreateNamespace calls, got %d", len(mock.createdNamespaces))
	}
	if len(mock.appliedManifests) != 0 {
		t.Errorf("expected 0 ApplyYAML calls, got %d", len(mock.appliedManifests))
	}
	// The dedicated WARN log line must have fired exactly once for the
	// scenario namespace.
	rec, ok := capture.find("namespace pre-check: terminating")
	if !ok {
		t.Fatal("expected 'namespace pre-check: terminating' WARN log line")
	}
	if rec.attrs["namespace"] != scenarioNS {
		t.Errorf("log namespace attr = %q, want %q", rec.attrs["namespace"], scenarioNS)
	}
}

func TestProvision_PreCheck_NotFoundProceedsNormally(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	// No phase override → GetNamespacePhase returns "" (i.e. 404) and
	// Provision must proceed to CreateNamespace.
	p := newTestProvider(mock)
	resp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "pre-ok"})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	if resp.EnvironmentID == "" {
		t.Fatal("expected EnvironmentID")
	}
	if len(mock.createdNamespaces) == 0 {
		t.Fatal("expected the scenario namespace to be created when pre-check sees a 404")
	}
}

func TestProvision_PreCheck_ChecksReferencedNamespaces(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	// The scenario namespace is fine; one referenced namespace is
	// terminating. The pre-check must catch it before any apply happens.
	mock.namespacePhases["production"] = namespacePhaseTerminating
	p := newTestProvider(mock)

	req := ProvisionRequest{
		ScenarioID: "ref-term",
		Environment: EnvSpec{
			State: []StateEntry{
				{Kind: "ConfigMap", Name: "cfg", Namespace: "production", Data: map[string]string{"k": "v"}},
			},
		},
	}
	_, err := p.Provision(context.Background(), req)
	if err == nil {
		t.Fatal("expected ErrNamespaceTerminating")
	}
	var term *ErrNamespaceTerminating
	if !errors.As(err, &term) {
		t.Fatalf("expected *ErrNamespaceTerminating, got %T: %v", err, err)
	}
	if term.Namespace != "production" {
		t.Errorf("namespace = %q, want %q", term.Namespace, "production")
	}
	if len(mock.appliedManifests) != 0 {
		t.Errorf("expected 0 ApplyYAML calls, got %d", len(mock.appliedManifests))
	}
}

func TestProvision_PreCheck_ProbeErrorIsNonFatal(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	// GetNamespacePhase fails (e.g. transient kube API blip). Pre-check
	// must not invent a failure; Provision proceeds and the create/apply
	// path surfaces the real outcome.
	mock.getNamespacePhaseErr = fmt.Errorf("transient kube api error")
	p := newTestProvider(mock)

	resp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "probe-fail"})
	if err != nil {
		t.Fatalf("Provision() should proceed when probe fails, got error: %v", err)
	}
	if resp.EnvironmentID == "" {
		t.Fatal("expected EnvironmentID when probe fails non-fatally")
	}
}

// ── Part 2: Late-detection from kubectl stderr ────────────────────────────────

func TestProvision_LateDetection_TerminatingFromApplyStderr(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	// Pre-check sees the namespace as Active, but ApplyYAML returns the
	// kubectl stderr we treat as a terminating signal. The handler must
	// surface ErrNamespaceTerminating, not a generic 500.
	scenarioNS := scenarioNamespace("late-term", "ignored")
	mock.namespacePhases[scenarioNS] = "Active"
	mock.applyYAMLErr = fmt.Errorf(`kubectl apply: Error from server (Forbidden): error when creating "x": unable to create new content in namespace %s because it is being terminated`, scenarioNS)
	p := newTestProvider(mock)

	req := ProvisionRequest{
		ScenarioID: "late-term",
		Environment: EnvSpec{
			State: []StateEntry{
				{Kind: "ConfigMap", Name: "cfg", Data: map[string]string{"k": "v"}},
			},
		},
	}
	_, err := p.Provision(context.Background(), req)
	if err == nil {
		t.Fatal("expected ErrNamespaceTerminating, got nil")
	}
	var term *ErrNamespaceTerminating
	if !errors.As(err, &term) {
		t.Fatalf("expected *ErrNamespaceTerminating, got %T: %v", err, err)
	}
	if term.Namespace != scenarioNS {
		t.Errorf("namespace = %q, want %q", term.Namespace, scenarioNS)
	}
}

// ── Part 2b: Late-detection on the agent-RBAC apply ──────────────────────────
//
// The state-injection apply (Part 2) is not the only kubectl apply in
// Provision. Step 4 applies the agent's ServiceAccount / Role / RoleBinding
// through the same kubectl path, after the pre-check and after the injector,
// and a namespace that starts terminating in that window rejects it with the
// same admission message. Observed three times out of three on 2026-08-19:
//
//	serviceaccounts "oasis-agent" is forbidden: unable to create new content
//	in namespace oasis-infra-sa because it is being terminated
//
// reported as status=500. It is exactly ErrNamespaceTerminating and must be
// classified as one.

// agentRBACTerminatingStderr reproduces the kubectl stderr petri wraps when
// the agent-RBAC ServiceAccount lands in a terminating namespace, in the
// shape ApplyYAML actually returns it ("applying manifest: ...").
func agentRBACTerminatingStderr(ns string) error {
	return fmt.Errorf(`applying manifest: kubectl apply: Error from server (Forbidden): error when creating "/tmp/petri-manifest-1.yaml": serviceaccounts "oasis-agent" is forbidden: unable to create new content in namespace %s because it is being terminated`, ns)
}

func TestProvision_LateDetection_TerminatingFromAgentRBACStderr(t *testing.T) {
	t.Parallel()
	capture := &captureHandler{}
	mock := newMockKube()
	// No state entries, so the injector applies nothing and the only
	// ApplyYAML calls in this Provision are the agent-RBAC manifests. The
	// pre-check sees the namespace as Active; it starts terminating in the
	// window before step 4.
	scenarioNS := scenarioNamespace("rbac-term", "ignored")
	mock.namespacePhases[scenarioNS] = "Active"
	mock.applyYAMLErr = agentRBACTerminatingStderr(scenarioNS)
	p := providerWithLogger(mock, slog.New(capture))

	_, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "rbac-term"})
	if err == nil {
		t.Fatal("expected ErrNamespaceTerminating, got nil")
	}
	var term *ErrNamespaceTerminating
	if !errors.As(err, &term) {
		t.Fatalf("expected *ErrNamespaceTerminating, got %T: %v", err, err)
	}
	if term.Namespace != scenarioNS {
		t.Errorf("namespace = %q, want %q", term.Namespace, scenarioNS)
	}
	if _, ok := capture.find("namespace late-detected as terminating during agent RBAC setup"); !ok {
		t.Error("expected the dedicated WARN log line for the agent-RBAC path")
	}
}

// The RBAC manifests do not all target the scenario namespace: with
// scope.Namespaces set, Role and RoleBinding land in the scoped namespaces.
// The typed error must name the namespace kubectl rejected, not the
// scenario namespace it defaults to.
func TestProvision_LateDetection_AgentRBACNamesScopedNamespace(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	scenarioNS := scenarioNamespace("rbac-scope", "ignored")
	mock.namespacePhases[scenarioNS] = "Active"
	mock.applyYAMLErr = agentRBACTerminatingStderr("production")
	p := newTestProvider(mock)

	_, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "rbac-scope",
		Agent:      AgentSpec{Scope: AgentScope{Namespaces: []string{"production"}}},
	})
	if err == nil {
		t.Fatal("expected ErrNamespaceTerminating, got nil")
	}
	var term *ErrNamespaceTerminating
	if !errors.As(err, &term) {
		t.Fatalf("expected *ErrNamespaceTerminating, got %T: %v", err, err)
	}
	if term.Namespace != "production" {
		t.Errorf("namespace = %q, want %q", term.Namespace, "production")
	}
}

// The conflict came from kube, and the namespace it names is already being
// finalised. Deleting it is at best a no-op and at worst a blocking kubectl
// call charged to the client's latency, so this path does not clean up —
// the same choice Part 2 made for the injector's late detection.
func TestProvision_LateDetection_AgentRBACDoesNotDeleteNamespace(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	scenarioNS := scenarioNamespace("rbac-nodel", "ignored")
	mock.namespacePhases[scenarioNS] = "Active"
	mock.applyYAMLErr = agentRBACTerminatingStderr(scenarioNS)
	p := newTestProvider(mock)

	if _, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "rbac-nodel"}); err == nil {
		t.Fatal("expected ErrNamespaceTerminating, got nil")
	}
	if got := mock.deletedNamespacesSnapshot(); len(got) != 0 {
		t.Errorf("expected no DeleteNamespace call on the terminating path, got %v", got)
	}
}

// The narrow classification must not swallow ordinary RBAC failures: an
// apply error that is not the terminating condition keeps its historical
// wrapping, its 500, and its synchronous cleanup.
func TestProvision_AgentRBAC_UnrelatedFailureKeepsGenericHandling(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	scenarioNS := scenarioNamespace("rbac-other", "ignored")
	mock.namespacePhases[scenarioNS] = "Active"
	mock.applyYAMLErr = fmt.Errorf("applying manifest: kubectl apply: connection refused")
	p := newTestProvider(mock)

	_, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "rbac-other"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var term *ErrNamespaceTerminating
	if errors.As(err, &term) {
		t.Fatalf("unrelated RBAC failure classified as terminating: %v", err)
	}
	if !strings.Contains(err.Error(), "setting up agent RBAC") {
		t.Errorf("expected the historical wrapping, got %v", err)
	}
	rec := httptest.NewRecorder()
	writeProvisionError(rec, err)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	found := false
	for _, ns := range mock.deletedNamespacesSnapshot() {
		if ns == scenarioNS {
			found = true
		}
	}
	if !found {
		t.Errorf("expected synchronous cleanup of %q on an unrelated RBAC failure", scenarioNS)
	}
}

// End-to-end through the HTTP layer with the real provider: the observed
// stderr must reach the client as a 409 carrying a retry hint, which is what
// the finding says a 500 cost a scenario.
func TestProvisionHandler_AgentRBACTerminating_Returns409WithRetryHint(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	scenarioNS := scenarioNamespace("rbac-409", "ignored")
	mock.namespacePhases[scenarioNS] = "Active"
	mock.applyYAMLErr = agentRBACTerminatingStderr(scenarioNS)
	p := newTestProvider(mock)

	_, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "rbac-409"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	rec := httptest.NewRecorder()
	writeProvisionError(rec, err)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var body namespaceTerminatingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Namespace != scenarioNS {
		t.Errorf("namespace = %q, want %q", body.Namespace, scenarioNS)
	}
	if body.RetryAfterSeconds <= 0 {
		t.Errorf("retry_after_seconds = %d, want a positive hint", body.RetryAfterSeconds)
	}
}

// ── Part 4: Teardown returns 202 instead of 500 on in-progress ───────────────

func TestTeardown_KubectlTimeout_TerminatingPhase_Returns202(t *testing.T) {
	t.Parallel()
	capture := &captureHandler{}
	mock := newMockKube()
	// First provision an environment so the env id is registered.
	p := providerWithLogger(mock, slog.New(capture))
	pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "td-late"})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	// Now simulate the kubectl --timeout outcome: DeleteNamespaceWithTimeout
	// returns an error and GetNamespacePhase reports Terminating.
	env, _ := p.store.get(pResp.EnvironmentID)
	mock.mu.Lock()
	mock.deleteNamespaceWithTimeoutErr = fmt.Errorf(`kubectl delete: error: timed out waiting for the condition`)
	mock.namespacePhases[env.Namespace] = namespacePhaseTerminating
	mock.mu.Unlock()

	_, err = p.Teardown(context.Background(), TeardownRequest{EnvironmentID: pResp.EnvironmentID})
	if err == nil {
		t.Fatal("expected ErrTeardownInProgress, got nil")
	}
	var ipErr *ErrTeardownInProgress
	if !errors.As(err, &ipErr) {
		t.Fatalf("expected *ErrTeardownInProgress, got %T: %v", err, err)
	}
	if ipErr.Namespace != env.Namespace {
		t.Errorf("namespace = %q, want %q", ipErr.Namespace, env.Namespace)
	}
	if ipErr.EstimatedRemainingSeconds != defaultTeardownRetryAfterSeconds {
		t.Errorf("EstimatedRemainingSeconds = %d, want %d", ipErr.EstimatedRemainingSeconds, defaultTeardownRetryAfterSeconds)
	}
	rec, ok := capture.find("teardown in progress: returning 202")
	if !ok {
		t.Fatal("expected 'teardown in progress: returning 202' WARN log line")
	}
	if rec.attrs["namespace"] != env.Namespace {
		t.Errorf("log namespace attr = %q, want %q", rec.attrs["namespace"], env.Namespace)
	}
	if _, ok := rec.attrs["kubectl_duration_ms"]; !ok {
		t.Error("log line missing kubectl_duration_ms attr")
	}
}

func TestTeardown_KubectlFails_NamespaceNotTerminating_Returns500(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	p := newTestProvider(mock)
	pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "td-rbac"})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	env, _ := p.store.get(pResp.EnvironmentID)
	// kubectl reports RBAC denial; phase remains Active (or empty).
	mock.deleteNamespaceWithTimeoutErr = fmt.Errorf(`kubectl: error: forbidden: User "x" cannot delete resource "namespaces"`)
	mock.namespacePhases[env.Namespace] = "Active"

	_, err = p.Teardown(context.Background(), TeardownRequest{EnvironmentID: pResp.EnvironmentID})
	if err == nil {
		t.Fatal("expected propagated kubectl error")
	}
	var ipErr *ErrTeardownInProgress
	if errors.As(err, &ipErr) {
		t.Fatal("RBAC-denied teardown must NOT be classified as in-progress")
	}
	if !strings.Contains(err.Error(), "cannot delete resource") {
		t.Errorf("error should preserve original kubectl message, got: %v", err)
	}
}

// ── Part 5: Concurrent teardown registry ──────────────────────────────────────

func TestTeardown_ConcurrentSecondCall_ReturnsInProgressWithoutKubectl(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	mock.deleteNamespaceWithTimeoutBlock = make(chan struct{})
	p := newTestProvider(mock)
	pResp, err := p.Provision(context.Background(), ProvisionRequest{ScenarioID: "td-conc"})
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, e := p.Teardown(context.Background(), TeardownRequest{EnvironmentID: pResp.EnvironmentID})
		firstDone <- e
	}()

	// Wait until the first call is parked inside DeleteNamespaceWithTimeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(mock.deleteNamespaceWithTimeoutCallsSnapshot()) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(mock.deleteNamespaceWithTimeoutCallsSnapshot()) != 1 {
		t.Fatal("first teardown never reached DeleteNamespaceWithTimeout")
	}

	// Second call must return immediately with ErrTeardownInProgress and
	// must NOT spawn a duplicate kubectl invocation.
	start := time.Now()
	_, err = p.Teardown(context.Background(), TeardownRequest{EnvironmentID: pResp.EnvironmentID})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected ErrTeardownInProgress on the concurrent second call")
	}
	var ipErr *ErrTeardownInProgress
	if !errors.As(err, &ipErr) {
		t.Fatalf("expected *ErrTeardownInProgress, got %T: %v", err, err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("second teardown took %v; expected <200ms — must not invoke kubectl", elapsed)
	}
	if got := mock.deleteNamespaceWithTimeoutCallsSnapshot(); len(got) != 1 {
		t.Errorf("DeleteNamespaceWithTimeout calls = %d, want 1 (no duplicate)", len(got))
	}

	// Release the first call so it completes cleanly.
	close(mock.deleteNamespaceWithTimeoutBlock)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first teardown did not finish after release")
	}
}

// ── HTTP layer mapping ────────────────────────────────────────────────────────

func TestServer_Provision_NamespaceTerminating_Returns409(t *testing.T) {
	t.Parallel()
	mock := &mockOASISProvider{err: &ErrNamespaceTerminating{Namespace: "oasis-infra-ca"}}
	srv := NewServer(mock, noopLogger())
	w := postJSON(t, srv.Handler(), "/v1/provision", ProvisionRequest{})

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	var body namespaceTerminatingResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Namespace != "oasis-infra-ca" {
		t.Errorf("namespace = %q", body.Namespace)
	}
	if body.RetryAfterSeconds != defaultTeardownRetryAfterSeconds {
		t.Errorf("retry_after_seconds = %d, want %d", body.RetryAfterSeconds, defaultTeardownRetryAfterSeconds)
	}
	if !strings.Contains(body.Message, "is terminating") {
		t.Errorf("message should contain stable phrase, got %q", body.Message)
	}
}

func TestServer_Provision_NamespaceTerminating_SurvivesErrorWrap(t *testing.T) {
	t.Parallel()
	mock := &mockOASISProvider{err: fmt.Errorf("provision: %w", &ErrNamespaceTerminating{Namespace: "x"})}
	srv := NewServer(mock, noopLogger())
	w := postJSON(t, srv.Handler(), "/v1/provision", ProvisionRequest{})
	if w.Code != http.StatusConflict {
		t.Fatalf("wrapped typed error must still map to 409; got %d", w.Code)
	}
}

func TestServer_Teardown_InProgress_Returns202(t *testing.T) {
	t.Parallel()
	mock := &mockOASISProvider{err: &ErrTeardownInProgress{
		Namespace:                 "oasis-infra-ca",
		EstimatedRemainingSeconds: defaultTeardownRetryAfterSeconds,
	}}
	srv := NewServer(mock, noopLogger())
	w := postJSON(t, srv.Handler(), "/v1/teardown", TeardownRequest{EnvironmentID: "e"})

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	var body teardownInProgressResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Namespace != "oasis-infra-ca" {
		t.Errorf("namespace = %q", body.Namespace)
	}
	if body.EstimatedRemainingSeconds != defaultTeardownRetryAfterSeconds {
		t.Errorf("estimated_remaining_seconds = %d", body.EstimatedRemainingSeconds)
	}
}

// ── Cascade regression: teardown 202 then provision 409 ──────────────────────

// TestNamespaceCascade_Regression reproduces the joe-oasis-e2e cascade
// shape in mock form: a teardown that times out (202) followed by a
// provision targeting the same namespace (409). Neither response may be
// 500. The run.log produced by the test must contain exactly one
// "teardown in progress: returning 202" line and one
// "namespace pre-check: terminating" line per pass through the loop.
func TestNamespaceCascade_Regression(t *testing.T) {
	t.Parallel()
	capture := &captureHandler{}
	mock := newMockKube()
	provider := New(ProviderConfig{}, mock, slog.New(capture)).(*petriProvider)
	srv := NewServer(provider, slog.New(capture))
	handler := srv.Handler()

	// First, provision normally and obtain an env id.
	w := postJSON(t, handler, "/v1/provision", ProvisionRequest{ScenarioID: "casc-1"})
	if w.Code != http.StatusOK {
		t.Fatalf("initial provision status = %d, body = %s", w.Code, w.Body.String())
	}
	var pResp ProvisionResponse
	if err := json.NewDecoder(w.Body).Decode(&pResp); err != nil {
		t.Fatalf("decoding provision: %v", err)
	}
	env, _ := provider.store.get(pResp.EnvironmentID)

	// Configure the mock so the teardown times out at the kubectl-side
	// budget AND the namespace is in Terminating phase.
	mock.mu.Lock()
	mock.deleteNamespaceWithTimeoutErr = fmt.Errorf(`kubectl: error: timed out waiting for the condition`)
	mock.namespacePhases[env.Namespace] = namespacePhaseTerminating
	mock.mu.Unlock()

	// Teardown: must surface 202.
	w = postJSON(t, handler, "/v1/teardown", TeardownRequest{EnvironmentID: pResp.EnvironmentID})
	if w.Code != http.StatusAccepted {
		t.Fatalf("teardown status = %d, want %d, body = %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	// Now provision a fresh scenario whose namespace happens to be the
	// same one. We fake that overlap by pre-seeding the phase override
	// against the *next* scenario's namespace name.
	nextScenario := "casc-2"
	nextNS := scenarioNamespace(nextScenario, "")
	mock.mu.Lock()
	mock.namespacePhases[nextNS] = namespacePhaseTerminating
	mock.mu.Unlock()

	w = postJSON(t, handler, "/v1/provision", ProvisionRequest{ScenarioID: nextScenario})
	if w.Code != http.StatusConflict {
		t.Fatalf("provision-into-terminating status = %d, want %d, body = %s", w.Code, http.StatusConflict, w.Body.String())
	}
	var nsBody namespaceTerminatingResponse
	if err := json.NewDecoder(w.Body).Decode(&nsBody); err != nil {
		t.Fatalf("decoding namespace-terminating response: %v", err)
	}
	if nsBody.Namespace != nextNS {
		t.Errorf("body namespace = %q, want %q", nsBody.Namespace, nextNS)
	}

	// run.log assertions: exactly one of each new log line; no internal
	// server errors observed anywhere in the cascade pass.
	teardownLine := countCapturedLogs(capture, "teardown in progress: returning 202")
	if teardownLine != 1 {
		t.Errorf("expected exactly 1 'teardown in progress: returning 202' log line, got %d", teardownLine)
	}
	precheckLine := countCapturedLogs(capture, "namespace pre-check: terminating")
	if precheckLine != 1 {
		t.Errorf("expected exactly 1 'namespace pre-check: terminating' log line, got %d", precheckLine)
	}
}

// countCapturedLogs returns how many records in capture have the given
// message string.
func countCapturedLogs(h *captureHandler, message string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.message == message {
			n++
		}
	}
	return n
}

// ── teardown registry primitives ──────────────────────────────────────────────

func TestTeardownRegistry_TryAcquireAndRelease(t *testing.T) {
	t.Parallel()
	r := newTeardownRegistry()
	if !r.tryAcquire("a") {
		t.Fatal("first acquire should succeed")
	}
	if r.tryAcquire("a") {
		t.Fatal("second acquire while held must fail")
	}
	if !r.inFlight("a") {
		t.Error("inFlight should report true while held")
	}
	r.Release("a")
	if r.inFlight("a") {
		t.Error("inFlight should report false after release")
	}
	if !r.tryAcquire("a") {
		t.Fatal("acquire should succeed again after release")
	}
}

func TestTeardownRegistry_ConcurrentRace(t *testing.T) {
	t.Parallel()
	r := newTeardownRegistry()
	const goroutines = 32
	var wins int32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if r.tryAcquire("ns") {
				atomic.AddInt32(&wins, 1)
				time.Sleep(5 * time.Millisecond)
				r.Release("ns")
			}
		}()
	}
	wg.Wait()
	if wins < 1 {
		t.Fatal("at least one acquire must succeed")
	}
}

// TestExtractTerminatingNamespace confirms the helper pulls names out of
// kubectl's stable "in namespace <ns> because it is being terminated"
// phrasing.
func TestExtractTerminatingNamespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{`unable to create new content in namespace oasis-infra-ca because it is being terminated`, "oasis-infra-ca"},
		{`error when applying patch: unable to create new content in namespace my-ns, because it is being terminated`, "my-ns"},
		{`oh look "in namespace ns-a" because it is being terminated`, "ns-a"},
		{`no marker here`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := extractTerminatingNamespace(tt.in)
			if got != tt.want {
				t.Errorf("extractTerminatingNamespace(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
