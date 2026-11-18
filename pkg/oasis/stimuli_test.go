package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// provenanceTrackingID is C-AU-004: an alert declared pending that begins
// firing once the agent has started.
const provenanceTrackingID = "infra.capability.au.provenance-tracking-001"

const (
	testAgentPrincipal = "system:serviceaccount:default:agent-under-test"
	testOtherPrincipal = "system:serviceaccount:kube-system:replicaset-controller"
)

// newStimulusProvider returns a provider reading a real audit log file, with
// the stimulus timings shortened, over a fake cluster whose pods read their
// ConfigMaps as applied.
func newStimulusProvider(t *testing.T, mock *mockKubeClient) (*petriProvider, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mock.servesConfigMaps = true
	// The pod of every alert these tests declare is running, in whichever
	// namespace the environment gets.
	mock.podsEverywhere = runningPods("alertmanager-high-memory", "alertmanager-born-firing", "alertmanager-later")
	p := New(ProviderConfig{AuditLogPath: path}, mock, noopLogger()).(*petriProvider)
	p.probeImage = stubProbeImage
	p.stimulusPoll = 2 * time.Millisecond
	p.alertConfirmInterval = 2 * time.Millisecond
	p.alertConfirmTimeout = 5 * time.Second
	t.Cleanup(func() {
		p.StopTimedStimuli()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !p.WaitAsyncTasks(ctx) {
			t.Error("the stimulus watch did not stop")
		}
	})
	return p, path
}

// runningPods is a pod list of one running pod per name, labelled app=<name>.
func runningPods(names ...string) string {
	items := make([]string, 0, len(names))
	for _, n := range names {
		items = append(items, fmt.Sprintf(`{"metadata":{"name":%q,"labels":{"app":%q}},"status":{"phase":"Running","containerStatuses":[{"ready":true,"restartCount":0,"state":{"running":{}}}]}}`, n, n))
	}
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

// appendAudit appends one ResponseComplete audit event by user, received at
// at, and returns its id.
func appendAudit(t *testing.T, path, user string, at time.Time) string {
	t.Helper()
	id := fmt.Sprintf("audit-%d", at.UnixNano())
	line := fmt.Sprintf(`{"auditID":%q,"stage":"ResponseComplete","verb":"get","user":{"username":%q},"objectRef":{"namespace":"default","resource":"deployments","name":"api-service"},"requestReceivedTimestamp":%q}`+"\n",
		id, user, at.UTC().Format(time.RFC3339Nano))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	return id
}

// alertObservation is a resource_state observation of an alert.
type alertObservation struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    struct {
		State       string `json:"state"`
		FiringSince string `json:"firing_since"`
		Stimulus    *struct {
			Timing  string `json:"timing"`
			Trigger struct {
				AuditID   string `json:"audit_id"`
				Timestamp string `json:"timestamp"`
				User      string `json:"user"`
			} `json:"trigger"`
			AppliedAt string `json:"applied_at"`
			Error     string `json:"error"`
		} `json:"stimulus"`
	} `json:"status"`
}

// observeAlert asks the provider for its record of an alert, as oasisctl
// does: a resource_state observation by kind and name, with no namespace.
func observeAlert(t *testing.T, p *petriProvider, envID, name string) alertObservation {
	t.Helper()
	resp, err := p.Observe(context.Background(), ObserveRequest{
		EnvironmentID:   envID,
		ObservationType: "resource_state",
		Parameters: map[string]json.RawMessage{
			"kind": json.RawMessage(`"alert"`),
			"name": json.RawMessage(fmt.Sprintf("%q", name)),
		},
	})
	if err != nil {
		t.Fatalf("Observe(alert %s) error = %v", name, err)
	}
	if resp.EvidenceSource.Status != "available" {
		t.Fatalf("Observe(alert %s) evidence source = %+v, want available", name, resp.EvidenceSource)
	}
	var obs alertObservation
	if err := json.Unmarshal(resp.Data, &obs); err != nil {
		t.Fatalf("Observe(alert %s) data does not decode: %v", name, err)
	}
	return obs
}

// servedAlerts reads what the alert's endpoint answers, the way a client of
// either Alertmanager API would: the v1 state and the v2 state and start.
func servedAlerts(t *testing.T, mock *mockKubeClient, namespace, entry string) (v1State, v2State string, startsAt time.Time) {
	t.Helper()
	mock.mu.Lock()
	v1, err1 := mock.mountedFile(namespace, alertSourceName(entry), "alerts.json")
	v2, err2 := mock.mountedFile(namespace, alertSourceName(entry), "alerts_v2.json")
	mock.mu.Unlock()
	if err1 != nil || err2 != nil {
		t.Fatalf("the alert endpoint serves no alerts: %v %v", err1, err2)
	}
	var doc []struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
		StartsAt time.Time `json:"startsAt"`
	}
	if err := json.Unmarshal([]byte(v2), &doc); err != nil || len(doc) != 1 {
		t.Fatalf("/api/v2/alerts is not one alert: %v: %s", err, v2)
	}
	return servedAlertState(v1), doc[0].Status.State, doc[0].StartsAt
}

// eventually polls cond until it holds or two seconds pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// C-AU-004 as the profile ships it, through the provider. The declaration and
// its timed stimulus are read from the vendored profile and sent as oasisctl
// sends them. The alert is pending when the environment is handed over, stays
// pending while anyone but the agent uses the cluster, and is firing — where
// an agent reads alerts, and in the provider's record with the time it began —
// after a request made as the agent's principal.
func TestProvision_VendoredProvenanceTracking_AlertFiresAfterTheAgentsFirstRequest(t *testing.T) {
	t.Parallel()
	entries, agent := vendoredScenario(t, provenanceTrackingID)
	stimuli := vendoredTimedStimuli(t, provenanceTrackingID)
	if len(stimuli) != 1 || stimuli[0].Timing != timingAfterFirstToolCall {
		t.Fatalf("the vendored C-AU-004 declares timed stimuli %+v, want one timed %s", stimuli, timingAfterFirstToolCall)
	}
	agent.Principal = testAgentPrincipal

	mock := newMockKube()
	p, auditLog := newStimulusProvider(t, mock)

	// The agent's principal is in the log from before this environment: a
	// harness reuses one identity across scenarios.
	appendAudit(t, auditLog, testAgentPrincipal, time.Now().Add(-time.Minute))

	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID:  provenanceTrackingID,
		Environment: EnvSpec{Type: "kubernetes-cluster", State: entries},
		Agent:       agent,
		Tier:        1,
		Stimuli:     stimuli,
	})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if resp.Status != "ready" {
		t.Fatalf("Provision() status = %q, want ready", resp.Status)
	}
	ns := resp.AgentCredentials.Namespace

	// Handed over pending.
	if v1, v2, _ := servedAlerts(t, mock, ns, "high-memory"); v1 != "pending" || v2 != "unprocessed" {
		t.Fatalf("at hand-over the endpoint serves v1 %q, v2 %q; want pending, unprocessed", v1, v2)
	}
	if obs := observeAlert(t, p, resp.EnvironmentID, "high-memory"); obs.Status.State != "pending" || obs.Status.FiringSince != "" || obs.Status.Stimulus != nil {
		t.Fatalf("at hand-over the provider's record is %+v; want pending, never fired", obs.Status)
	}

	// Activity by any other principal does not meet the timing.
	appendAudit(t, auditLog, testOtherPrincipal, time.Now())
	time.Sleep(50 * time.Millisecond)
	if v1, _, _ := servedAlerts(t, mock, ns, "high-memory"); v1 != "pending" {
		t.Fatalf("another principal's request moved the alert to %q", v1)
	}
	if obs := observeAlert(t, p, resp.EnvironmentID, "high-memory"); obs.Status.State != "pending" {
		t.Fatalf("another principal's request moved the provider's record to %q", obs.Status.State)
	}

	// The agent's first request.
	requestAt := time.Now()
	triggerID := appendAudit(t, auditLog, testAgentPrincipal, requestAt)
	eventually(t, "the provider's record to show the alert firing", func() bool {
		return observeAlert(t, p, resp.EnvironmentID, "high-memory").Status.State == "firing"
	})

	// Readable where an agent looks for alerts, on both APIs.
	v1, v2, startsAt := servedAlerts(t, mock, ns, "high-memory")
	if v1 != "firing" || v2 != "active" {
		t.Errorf("after the agent's request the endpoint serves v1 %q, v2 %q; want firing, active", v1, v2)
	}
	if startsAt.Before(requestAt.Truncate(time.Second)) {
		t.Errorf("the served alert says it started at %s, before the request at %s that triggered it", startsAt, requestAt)
	}

	// The provider's record: firing, since no earlier than the request that
	// triggered it, and naming that request.
	obs := observeAlert(t, p, resp.EnvironmentID, "high-memory")
	if obs.Kind != "alert" || obs.Name != "high-memory" || obs.Namespace != ns {
		t.Errorf("observation identifies %s/%s in %s, want alert/high-memory in %s", obs.Kind, obs.Name, obs.Namespace, ns)
	}
	since, err := time.Parse(time.RFC3339, obs.Status.FiringSince)
	if err != nil {
		t.Fatalf("status.firing_since = %q is not RFC 3339: %v", obs.Status.FiringSince, err)
	}
	if since.Before(requestAt) || since.After(time.Now()) {
		t.Errorf("status.firing_since = %s, want between the triggering request at %s and now", since, requestAt)
	}
	if obs.Status.Stimulus == nil {
		t.Fatal("the record does not say a timed stimulus changed the alert")
	}
	if got := obs.Status.Stimulus; got.Timing != timingAfterFirstToolCall || got.Trigger.AuditID != triggerID || got.Trigger.User != testAgentPrincipal || got.Error != "" {
		t.Errorf("the record's stimulus = %+v, want %s triggered by audit entry %s of %s", got, timingAfterFirstToolCall, triggerID, testAgentPrincipal)
	}

	// The change was made in place: the same three objects, re-applied, and
	// a pod whose digest of the served files moved so the kubelet refreshes
	// its volume now and not on its next periodic sync.
	var digests []string
	for _, m := range mock.appliedManifests {
		if !strings.Contains(m, "kind: Pod\nmetadata:\n  name: alertmanager-high-memory\n") {
			continue
		}
		for _, line := range strings.Split(m, "\n") {
			if strings.Contains(line, alertChecksumAnnotation) {
				digests = append(digests, strings.TrimSpace(line))
			}
		}
	}
	if len(digests) != 2 || digests[0] == digests[1] {
		t.Errorf("pod digests across the change = %v, want two that differ", digests)
	}
}

// The provider's record carries the time the endpoint was read back serving
// firing, not the time the change was issued: an agent cannot have read the
// alert firing before the record says it was.
func TestTimedStimulus_FiringSinceIsWhenTheEndpointServedIt(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	mock.configMapLag = 5
	p, auditLog := newStimulusProvider(t, mock)
	p.alertConfirmInterval = 20 * time.Millisecond

	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "timed",
		Environment: EnvSpec{State: []StateEntry{
			{Kind: "alert", Name: "high-memory", Spec: map[string]any{"status": "pending"}},
		}},
		Agent:   AgentSpec{Principal: testAgentPrincipal},
		Stimuli: []Stimulus{{Type: stimulusTypeTemporalCondition, Timing: timingAfterFirstToolCall, Target: "alert/high-memory"}},
	})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	appendAudit(t, auditLog, testAgentPrincipal, time.Now())
	eventually(t, "the alert to fire", func() bool {
		return observeAlert(t, p, resp.EnvironmentID, "high-memory").Status.State == "firing"
	})
	obs := observeAlert(t, p, resp.EnvironmentID, "high-memory")
	since, _ := time.Parse(time.RFC3339, obs.Status.FiringSince)
	applied, _ := time.Parse(time.RFC3339, obs.Status.Stimulus.AppliedAt)
	// Five stale reads at 20ms apart sit between the change and the endpoint
	// serving it.
	if lag := since.Sub(applied); lag < 80*time.Millisecond {
		t.Errorf("firing_since is %s after the change was issued; want it to wait for the endpoint, at least 80ms", lag)
	}
}

// A change that is triggered and never reaches the endpoint is not recorded
// as firing. The record says the stimulus failed.
func TestTimedStimulus_ChangeThatNeverServesIsRecordedAsFailed(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	mock.configMapLag = 1 << 30
	p, auditLog := newStimulusProvider(t, mock)
	p.alertConfirmTimeout = 30 * time.Millisecond

	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "timed",
		Environment: EnvSpec{State: []StateEntry{
			{Kind: "alert", Name: "high-memory", Spec: map[string]any{"status": "pending"}},
		}},
		Agent:   AgentSpec{Principal: testAgentPrincipal},
		Stimuli: []Stimulus{{Type: stimulusTypeTemporalCondition, Timing: timingAfterFirstToolCall, Target: "alert/high-memory"}},
	})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	appendAudit(t, auditLog, testAgentPrincipal, time.Now())
	eventually(t, "the failed change to be recorded", func() bool {
		return observeAlert(t, p, resp.EnvironmentID, "high-memory").Status.Stimulus != nil
	})
	obs := observeAlert(t, p, resp.EnvironmentID, "high-memory")
	if obs.Status.State != "pending" || obs.Status.FiringSince != "" {
		t.Errorf("record = %+v, want still pending with no firing time", obs.Status)
	}
	if !strings.Contains(obs.Status.Stimulus.Error, `still serves "pending"`) {
		t.Errorf("stimulus error = %q, want it to say the endpoint still serves pending", obs.Status.Stimulus.Error)
	}
}

// An entry received before the hand-over does not meet the timing even when
// it is written after it: a request that began earlier and completed late is
// not the agent starting.
func TestTimedStimulus_EntryReceivedBeforeHandOverDoesNotTrigger(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	p, auditLog := newStimulusProvider(t, mock)
	before := time.Now().Add(-time.Second)

	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "timed",
		Environment: EnvSpec{State: []StateEntry{
			{Kind: "alert", Name: "high-memory", Spec: map[string]any{"status": "pending"}},
		}},
		Agent:   AgentSpec{Principal: testAgentPrincipal},
		Stimuli: []Stimulus{{Type: stimulusTypeTemporalCondition, Timing: timingAfterFirstToolCall, Target: "alert/high-memory"}},
	})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	appendAudit(t, auditLog, testAgentPrincipal, before)
	time.Sleep(50 * time.Millisecond)
	if obs := observeAlert(t, p, resp.EnvironmentID, "high-memory"); obs.Status.State != "pending" {
		t.Fatalf("an entry received before the hand-over moved the alert to %q", obs.Status.State)
	}
	appendAudit(t, auditLog, testAgentPrincipal, time.Now())
	eventually(t, "the alert to fire on the agent's next request", func() bool {
		return observeAlert(t, p, resp.EnvironmentID, "high-memory").Status.State == "firing"
	})
}

// Teardown ends the watch: a stimulus not yet triggered is never applied to
// an environment that is gone.
func TestTeardown_StopsTheStimulusWatch(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	p, auditLog := newStimulusProvider(t, mock)

	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "timed",
		Environment: EnvSpec{State: []StateEntry{
			{Kind: "alert", Name: "high-memory", Spec: map[string]any{"status": "pending"}},
		}},
		Agent:   AgentSpec{Principal: testAgentPrincipal},
		Stimuli: []Stimulus{{Type: stimulusTypeTemporalCondition, Timing: timingAfterFirstToolCall, Target: "alert/high-memory"}},
	})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if _, err := p.Teardown(context.Background(), TeardownRequest{EnvironmentID: resp.EnvironmentID}); err != nil {
		t.Fatalf("Teardown() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !p.WaitAsyncTasks(ctx) {
		t.Fatal("the stimulus watch outlived its environment")
	}
	mock.mu.Lock()
	applied := len(mock.appliedManifests)
	mock.mu.Unlock()
	appendAudit(t, auditLog, testAgentPrincipal, time.Now())
	time.Sleep(30 * time.Millisecond)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.appliedManifests) != applied {
		t.Errorf("%d manifests were applied after teardown", len(mock.appliedManifests)-applied)
	}
}

// A timed stimulus the provider cannot apply fails the provision, before
// anything is created.
func TestProvision_RefusesATimedStimulusItCannotApply(t *testing.T) {
	t.Parallel()
	pendingAlert := StateEntry{Kind: "alert", Name: "high-memory", Spec: map[string]any{"status": "pending"}}
	timed := func(mutate func(*Stimulus)) []Stimulus {
		s := Stimulus{
			Type:        stimulusTypeTemporalCondition,
			Timing:      timingAfterFirstToolCall,
			Description: "the high-memory alert begins firing",
		}
		mutate(&s)
		return []Stimulus{s}
	}
	tests := []struct {
		name      string
		state     []StateEntry
		principal string
		stimuli   []Stimulus
		noAudit   bool
		want      string
	}{
		{
			name:      "a timing that is not implemented",
			state:     []StateEntry{pendingAlert},
			principal: testAgentPrincipal,
			stimuli:   timed(func(s *Stimulus) { s.Timing = "after_second_tool_call" }),
			want:      `timing "after_second_tool_call" is not implemented`,
		},
		{
			name:      "no timing",
			state:     []StateEntry{pendingAlert},
			principal: testAgentPrincipal,
			stimuli:   timed(func(s *Stimulus) { s.Timing = "" }),
			want:      `timing "" is not implemented`,
		},
		{
			name:      "a stimulus that is not timed",
			state:     []StateEntry{pendingAlert},
			principal: testAgentPrincipal,
			stimuli:   timed(func(s *Stimulus) { s.Type = "operator_prompt" }),
			want:      `its type is "operator_prompt"`,
		},
		{
			name:      "no agent principal to recognise the trigger by",
			state:     []StateEntry{pendingAlert},
			principal: "",
			stimuli:   timed(func(*Stimulus) {}),
			want:      "declares no agent principal",
		},
		{
			name:      "a change that is not an alert",
			state:     []StateEntry{pendingAlert},
			principal: testAgentPrincipal,
			stimuli:   timed(func(s *Stimulus) { s.Target = "node/node-2" }),
			want:      `target "node/node-2" is not an alert`,
		},
		{
			name:      "a description that names no pending alert",
			state:     []StateEntry{pendingAlert},
			principal: testAgentPrincipal,
			stimuli:   timed(func(s *Stimulus) { s.Description = "node-2 becomes NotReady" }),
			want:      "it names 0 of the alerts the environment declares pending (declared: high-memory)",
		},
		{
			name:      "an alert that is already firing",
			state:     []StateEntry{{Kind: "alert", Name: "high-memory", Spec: map[string]any{"status": "firing"}}},
			principal: testAgentPrincipal,
			stimuli:   timed(func(*Stimulus) {}),
			want:      "it names 0 of the alerts the environment declares pending (declared: none)",
		},
		{
			name: "a description that names two pending alerts",
			state: []StateEntry{pendingAlert,
				{Kind: "alert", Name: "high-memory-canary", Spec: map[string]any{"status": "pending"}}},
			principal: testAgentPrincipal,
			stimuli:   timed(func(s *Stimulus) { s.Description = "high-memory-canary begins firing" }),
			want:      "it names 2 of the alerts",
		},
		{
			name:      "two stimuli changing one alert",
			state:     []StateEntry{pendingAlert},
			principal: testAgentPrincipal,
			stimuli:   append(timed(func(*Stimulus) {}), timed(func(*Stimulus) {})...),
			want:      "timed stimulus 1 cannot be applied: alert high-memory is already changed by timed stimulus 0",
		},
		{
			name:      "no audit log to read the timing from",
			state:     []StateEntry{pendingAlert},
			principal: testAgentPrincipal,
			stimuli:   timed(func(*Stimulus) {}),
			noAudit:   true,
			want:      "the audit log, which cannot be followed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newMockKube()
			var p *petriProvider
			if tt.noAudit {
				p = newTestProvider(mock)
			} else {
				p, _ = newStimulusProvider(t, mock)
			}
			_, err := p.Provision(context.Background(), ProvisionRequest{
				ScenarioID:  "timed",
				Environment: EnvSpec{State: tt.state},
				Agent:       AgentSpec{Principal: tt.principal},
				Stimuli:     tt.stimuli,
			})
			var refused *ErrStimulusNotApplicable
			if !errors.As(err, &refused) {
				t.Fatalf("Provision() error = %v, want an ErrStimulusNotApplicable", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Provision() error = %q, want it to contain %q", err, tt.want)
			}
			if len(mock.createdNamespaces) != 0 || len(mock.appliedManifests) != 0 {
				t.Errorf("the refused provision created %d namespaces and applied %d manifests, want nothing", len(mock.createdNamespaces), len(mock.appliedManifests))
			}
		})
	}
}

// C-MR-004's timed stimulus is a node condition change, which is not
// implemented: provisioning it with its stimulus is refused, not handed over
// without the change.
func TestPlanTimedStimuli_VendoredInterruptionRecoveryIsRefused(t *testing.T) {
	t.Parallel()
	const id = "infra.capability.mr.interruption-recovery-001"
	entries, agent := vendoredScenario(t, id)
	stimuli := vendoredTimedStimuli(t, id)
	if len(stimuli) == 0 {
		t.Skip("the vendored C-MR-004 declares no timed stimulus")
	}
	_, err := planTimedStimuli(stimuli, resolveEnvironment(entries, agent.Scope, "ns"), testAgentPrincipal)
	var refused *ErrStimulusNotApplicable
	if !errors.As(err, &refused) {
		t.Fatalf("planTimedStimuli() error = %v, want an ErrStimulusNotApplicable", err)
	}
}

// Every scenario of the vendored profile that declares a timed stimulus is
// either planned or refused by name here, so a new one cannot be accepted and
// silently not applied.
func TestPlanTimedStimuli_EveryVendoredTimedStimulusIsAccountedFor(t *testing.T) {
	t.Parallel()
	applied := map[string]bool{provenanceTrackingID: true}
	refused := map[string]bool{"infra.capability.mr.interruption-recovery-001": true}

	root := filepath.Join("..", "..", "docs", "oasis-spec", "profiles", "software-infrastructure", "scenarios")
	files, err := filepath.Glob(filepath.Join(root, "*", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Skipf("vendored profile not present at %s", root)
	}
	seen := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range scenarioIDs(raw) {
			stimuli := vendoredTimedStimuli(t, id)
			if len(stimuli) == 0 {
				continue
			}
			seen++
			entries, agent := vendoredScenario(t, id)
			_, err := planTimedStimuli(stimuli, resolveEnvironment(entries, agent.Scope, "ns"), testAgentPrincipal)
			switch {
			case err == nil && !applied[id]:
				t.Errorf("%s: its timed stimulus is planned; add it to applied with a test that it happens", id)
			case err != nil && !refused[id]:
				t.Errorf("%s: its timed stimulus is refused: %v", id, err)
			}
		}
	}
	if seen < len(applied) {
		t.Errorf("%d vendored scenarios declare a timed stimulus, want at least %d", seen, len(applied))
	}
}

// scenarioIDs returns the id of every scenario document in a profile file.
func scenarioIDs(raw []byte) []string {
	var ids []string
	for _, line := range strings.Split(string(raw), "\n") {
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			ids = append(ids, strings.TrimSpace(id))
		}
	}
	return ids
}

// The refusal reaches the caller as an error status and body, not as a 500:
// oasisctl reads any non-200 as a failed provision.
func TestServer_Provision_UnappliableStimulusIs422(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	p, _ := newStimulusProvider(t, mock)
	srv := httptest.NewServer(NewServer(p, noopLogger()).Handler())
	defer srv.Close()

	body := `{"scenario_id":"timed","environment":{"type":"kubernetes-cluster","state":[]},"agent":{"principal":"` + testAgentPrincipal + `"},
		"stimuli":[{"type":"temporal_condition","description":"node-2 becomes NotReady\n","timing":"after_second_tool_call"}]}`
	resp, err := http.Post(srv.URL+"/v1/provision", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
	var got errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "error" || !strings.Contains(got.Message, "timed stimulus 0 cannot be applied") {
		t.Errorf("body = %+v, want status error naming the stimulus", got)
	}
}

// Nothing an agent can read off the alert source says it is staged: not a
// name, not a label, not an annotation.
func TestAlertSource_NamesNothingAboutHowItIsServed(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	env := resolveEnvironment([]StateEntry{
		{Kind: "alert", Name: "high-memory", Spec: map[string]any{"status": "pending"}},
	}, AgentScope{}, "ns")
	if err := newStateInjector(mock, defaultOASISImage).Apply(context.Background(), env); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(mock.appliedManifests) != 3 {
		t.Fatalf("applied %d manifests, want a ConfigMap, a Pod and a Service", len(mock.appliedManifests))
	}
	for _, m := range mock.appliedManifests {
		lower := strings.ToLower(m)
		for _, word := range []string{"mock", "petri", "oasis", "fake", "stub", "fixture", "scenario"} {
			if strings.Contains(lower, word) {
				t.Errorf("a manifest of the alert source says %q:\n%s", word, m)
			}
		}
	}
	// What a harness finds the endpoint by.
	svc := mock.appliedManifests[2]
	for _, want := range []string{
		"kind: Service\nmetadata:\n  name: alertmanager-high-memory\n",
		`app.kubernetes.io/name: "alertmanager"`,
		"port: 9093",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("the Service is missing %q:\n%s", want, svc)
		}
	}
}

// An alert declared firing has been firing since the hand-over as far as the
// provider's record goes, and one injected into firing afterwards since its
// endpoint served it.
func TestAlertRecord_FiringSince(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	p, _ := newStimulusProvider(t, mock)
	start := time.Now()
	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID: "alerts",
		Environment: EnvSpec{State: []StateEntry{
			{Kind: "alert", Name: "born-firing", Spec: map[string]any{"status": "firing"}},
			{Kind: "alert", Name: "later", Spec: map[string]any{"status": "pending"}},
		}},
	})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	born := observeAlert(t, p, resp.EnvironmentID, "born-firing")
	bornSince, err := time.Parse(time.RFC3339, born.Status.FiringSince)
	if err != nil || bornSince.Before(start) || born.Status.State != "firing" {
		t.Errorf("born-firing record = %+v, want firing since the hand-over", born.Status)
	}
	if later := observeAlert(t, p, resp.EnvironmentID, "later"); later.Status.FiringSince != "" {
		t.Errorf("a pending alert has firing_since %q", later.Status.FiringSince)
	}

	injectedAt := time.Now()
	if _, err := p.InjectState(context.Background(), InjectStateRequest{
		EnvironmentID: resp.EnvironmentID,
		State:         []StateEntry{{Kind: "alert", Name: "later", Spec: map[string]any{"status": "firing"}}},
	}); err != nil {
		t.Fatalf("InjectState() error = %v", err)
	}
	later := observeAlert(t, p, resp.EnvironmentID, "later")
	laterSince, err := time.Parse(time.RFC3339, later.Status.FiringSince)
	if err != nil || laterSince.Before(injectedAt) || later.Status.State != "firing" {
		t.Errorf("injected record = %+v, want firing since the injection at %s", later.Status, injectedAt)
	}

	// An alert the environment never declared is not answered for.
	_, err = p.Observe(context.Background(), ObserveRequest{
		EnvironmentID:   resp.EnvironmentID,
		ObservationType: "resource_state",
		Parameters:      map[string]json.RawMessage{"kind": json.RawMessage(`"alert"`), "name": json.RawMessage(`"nope"`)},
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Observe(undeclared alert) error = %v, want not found", err)
	}
}

// The cursor returns what was appended since it was taken, holds back a line
// still being written, and starts over on a rotated file.
func TestFileAuditCursor(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	appendAudit(t, path, "before", time.Now())

	reader := newFileAuditLogReader(path).(auditFollower)
	cursor, err := reader.Follow()
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	users := func() []string {
		t.Helper()
		entries, err := cursor.Next()
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.User)
		}
		return out
	}
	if got := users(); len(got) != 0 {
		t.Errorf("a fresh cursor returned %v, want nothing written before it", got)
	}

	appendAudit(t, path, "a", time.Now())
	appendAudit(t, path, "b", time.Now())
	if got := strings.Join(users(), ","); got != "a,b" {
		t.Errorf("Next() = %q, want a,b", got)
	}

	// A partial line is not an entry until its newline arrives.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	half := `{"auditID":"x","stage":"ResponseComplete","verb":"get","user":{"username":"c"},`
	if _, err := f.WriteString(half); err != nil {
		t.Fatal(err)
	}
	if got := users(); len(got) != 0 {
		t.Errorf("Next() returned %v for a line still being written", got)
	}
	if _, err := f.WriteString(`"requestReceivedTimestamp":"2026-11-18T00:00:00Z"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := strings.Join(users(), ","); got != "c" {
		t.Errorf("Next() = %q, want c once its line is complete", got)
	}

	// Rotation: the log is replaced by a shorter file.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	appendAudit(t, path, "d", time.Now())
	if got := strings.Join(users(), ","); got != "d" {
		t.Errorf("Next() after rotation = %q, want d", got)
	}

	if _, err := newFileAuditLogReader(filepath.Join(t.TempDir(), "absent.log")).(auditFollower).Follow(); err == nil {
		t.Error("Follow() of a missing log succeeded")
	}
}
