package instrumentation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/chaos"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func faultEvent(ft chaos.FaultType, ns, name string, success bool, startedAt time.Time, diag string) chaos.FaultEvent {
	return chaos.FaultEvent{
		ID:                "evt-" + string(ft),
		FaultType:         ft,
		Target:            chaos.TargetResource{Namespace: ns, Name: name, Kind: "Deployment"},
		StartedAt:         startedAt,
		EndedAt:           startedAt.Add(100 * time.Millisecond),
		Success:           success,
		Metadata:          map[string]string{},
		ExpectedDiagnosis: diag,
	}
}

// simpleAction implements Action for tests.
type simpleAction struct {
	ts   time.Time
	desc string
}

func (a simpleAction) Timestamp() time.Time { return a.ts }
func (a simpleAction) Description() string  { return a.desc }

func action(ts time.Time, desc string) Action {
	return simpleAction{ts: ts, desc: desc}
}

// ── Collector ─────────────────────────────────────────────────────────────────

func TestCollector_Emit_And_Timeline(t *testing.T) {
	t.Parallel()

	c := NewCollector()
	now := time.Now()

	ev1 := faultEvent(chaos.FaultKillPod, "prod", "frontend", true, now, "")
	ev2 := faultEvent(chaos.FaultScaleToZero, "prod", "backend", false, now.Add(time.Second), "")

	c.Emit(ev1)
	c.Emit(ev2)

	tl := c.Timeline()
	if tl.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", tl.Len())
	}
	if tl.All()[0].Index != 0 {
		t.Errorf("first entry Index = %d, want 0", tl.All()[0].Index)
	}
	if tl.All()[1].Event.FaultType != chaos.FaultScaleToZero {
		t.Errorf("second event FaultType = %q", tl.All()[1].Event.FaultType)
	}
}

func TestCollector_Timeline_IsSnapshot(t *testing.T) {
	t.Parallel()

	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "pod", true, time.Now(), ""))
	tl := c.Timeline()

	// Emit after taking snapshot — should not affect the existing Timeline.
	c.Emit(faultEvent(chaos.FaultScaleToZero, "ns", "svc", true, time.Now(), ""))

	if tl.Len() != 1 {
		t.Errorf("snapshot should have 1 entry, got %d", tl.Len())
	}
}

// ── Timeline queries ──────────────────────────────────────────────────────────

func TestTimeline_ByFaultType(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "a", true, now, ""))
	c.Emit(faultEvent(chaos.FaultScaleToZero, "ns", "b", true, now.Add(time.Second), ""))
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "c", false, now.Add(2*time.Second), ""))

	tl := c.Timeline()
	kills := tl.ByFaultType(chaos.FaultKillPod)
	if len(kills) != 2 {
		t.Errorf("ByFaultType(kill_pod) = %d entries, want 2", len(kills))
	}
	scales := tl.ByFaultType(chaos.FaultScaleToZero)
	if len(scales) != 1 {
		t.Errorf("ByFaultType(scale_to_zero) = %d entries, want 1", len(scales))
	}
}

func TestTimeline_ByTarget(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "prod", "frontend", true, now, ""))
	c.Emit(faultEvent(chaos.FaultScaleToZero, "prod", "backend", true, now.Add(time.Second), ""))
	c.Emit(faultEvent(chaos.FaultKillPod, "staging", "frontend", false, now.Add(2*time.Second), ""))

	tl := c.Timeline()

	tests := []struct {
		ns, name, kind string
		want           int
	}{
		{"prod", "", "", 2},
		{"", "frontend", "", 2},
		{"prod", "frontend", "", 1},
		{"", "", "", 3}, // wildcard
	}
	for _, tc := range tests {
		got := tl.ByTarget(tc.ns, tc.name, tc.kind)
		if len(got) != tc.want {
			t.Errorf("ByTarget(%q,%q,%q) = %d, want %d", tc.ns, tc.name, tc.kind, len(got), tc.want)
		}
	}
}

func TestTimeline_InRange(t *testing.T) {
	t.Parallel()

	base := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "a", true, base, ""))
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "b", true, base.Add(30*time.Second), ""))
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "c", true, base.Add(90*time.Second), ""))

	tl := c.Timeline()

	got := tl.InRange(base.Add(10*time.Second), base.Add(60*time.Second))
	if len(got) != 1 {
		t.Errorf("InRange returned %d entries, want 1", len(got))
	}

	all := tl.InRange(time.Time{}, time.Time{})
	if len(all) != 3 {
		t.Errorf("unbounded InRange returned %d entries, want 3", len(all))
	}
}

// ── GenerateReport ────────────────────────────────────────────────────────────

func TestGenerateReport(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "a", true, now, "pod missing"))
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "b", false, now.Add(time.Second), ""))
	c.Emit(faultEvent(chaos.FaultScaleToZero, "ns", "c", true, now.Add(2*time.Second), ""))

	r := GenerateReport(c.Timeline())

	if r.TotalFaults != 3 {
		t.Errorf("TotalFaults = %d, want 3", r.TotalFaults)
	}
	if r.Succeeded != 2 {
		t.Errorf("Succeeded = %d, want 2", r.Succeeded)
	}
	if r.Failed != 1 {
		t.Errorf("Failed = %d, want 1", r.Failed)
	}
	if len(r.ByFaultType) != 2 {
		t.Errorf("ByFaultType has %d entries, want 2", len(r.ByFaultType))
	}
	// ByFaultType should be sorted alphabetically.
	if r.ByFaultType[0].FaultType > r.ByFaultType[1].FaultType {
		t.Errorf("ByFaultType not sorted: %q > %q", r.ByFaultType[0].FaultType, r.ByFaultType[1].FaultType)
	}
	if len(r.Timeline) != 3 {
		t.Errorf("Timeline has %d entries, want 3", len(r.Timeline))
	}
}

func TestReport_JSONSerializable(t *testing.T) {
	t.Parallel()

	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "pod", true, time.Now(), "some diagnosis"))

	r := GenerateReport(c.Timeline())
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal(Report) error: %v", err)
	}
	if len(data) == 0 {
		t.Error("JSON output is empty")
	}

	// Round-trip: unmarshal and verify a key field.
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if decoded["total_faults"] != float64(1) {
		t.Errorf("decoded total_faults = %v, want 1", decoded["total_faults"])
	}
}

// ── Correlator ────────────────────────────────────────────────────────────────

func TestCorrelator_DetectedCorrect(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "prod", "frontend", true, now, "frontend pod missing"))

	tl := c.Timeline()
	corr := NewCorrelator(CorrelatorConfig{DetectionWindow: 5 * time.Minute})

	// Joe action with description containing the expected diagnosis.
	actions := []Action{
		action(now.Add(2*time.Minute), "detected: frontend pod missing from prod namespace"),
	}

	report, err := corr.Correlate(tl, actions)
	if err != nil {
		t.Fatalf("Correlate() error: %v", err)
	}

	if report.DetectedCorrect != 1 {
		t.Errorf("DetectedCorrect = %d, want 1", report.DetectedCorrect)
	}
	if report.Missed != 0 {
		t.Errorf("Missed = %d, want 0", report.Missed)
	}
	if report.FalsePositives != 0 {
		t.Errorf("FalsePositives = %d, want 0", report.FalsePositives)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("Entries len = %d, want 1", len(report.Entries))
	}
	entry := report.Entries[0]
	if entry.Outcome != OutcomeDetectedCorrect {
		t.Errorf("Outcome = %q, want %q", entry.Outcome, OutcomeDetectedCorrect)
	}
	if entry.TimeToDetection != 2*time.Minute {
		t.Errorf("TimeToDetection = %v, want 2m", entry.TimeToDetection)
	}
}

func TestCorrelator_DetectedMisdiagnosed(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "prod", "frontend", true, now, "frontend pod missing"))

	tl := c.Timeline()
	corr := NewCorrelator(CorrelatorConfig{DetectionWindow: 5 * time.Minute})

	// Joe action but with wrong description.
	actions := []Action{
		action(now.Add(time.Minute), "CPU usage spike detected on backend service"),
	}

	report, _ := corr.Correlate(tl, actions)

	if report.DetectedMisdiagnosed != 1 {
		t.Errorf("DetectedMisdiagnosed = %d, want 1", report.DetectedMisdiagnosed)
	}
	if report.Entries[0].Outcome != OutcomeDetectedMisdiagnosed {
		t.Errorf("Outcome = %q, want %q", report.Entries[0].Outcome, OutcomeDetectedMisdiagnosed)
	}
}

func TestCorrelator_Missed(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultScaleToZero, "prod", "api", true, now, "api deployment scaled to zero"))

	tl := c.Timeline()
	corr := NewCorrelator(CorrelatorConfig{DetectionWindow: time.Minute})

	// Joe action arrives after the detection window.
	actions := []Action{
		action(now.Add(10*time.Minute), "late response: api deployment scaled to zero"),
	}

	report, _ := corr.Correlate(tl, actions)

	if report.Missed != 1 {
		t.Errorf("Missed = %d, want 1", report.Missed)
	}
	if report.FalsePositives != 1 {
		t.Errorf("FalsePositives = %d, want 1 (late action is unattributed)", report.FalsePositives)
	}
}

func TestCorrelator_FalsePositive(t *testing.T) {
	t.Parallel()

	// No faults injected, but Joe fires an action.
	c := NewCollector()
	tl := c.Timeline()
	corr := NewCorrelator(CorrelatorConfig{})

	actions := []Action{
		action(time.Now(), "Joe spontaneously restarted a healthy deployment"),
	}

	report, _ := corr.Correlate(tl, actions)

	if report.FalsePositives != 1 {
		t.Errorf("FalsePositives = %d, want 1", report.FalsePositives)
	}
	if len(report.FalsePositiveActions) != 1 {
		t.Fatalf("FalsePositiveActions len = %d, want 1", len(report.FalsePositiveActions))
	}
}

func TestCorrelator_FailedFaultNotCorrelated(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	// Fault injection failed — Joe should not be expected to respond.
	c.Emit(faultEvent(chaos.FaultKillPod, "prod", "db", false, now, "database pod killed"))

	tl := c.Timeline()
	corr := NewCorrelator(CorrelatorConfig{DetectionWindow: 5 * time.Minute})
	actions := []Action{
		action(now.Add(time.Minute), "database pod killed"),
	}

	report, _ := corr.Correlate(tl, actions)

	// Failed faults: 0 in Entries (not correlated), and the Joe action is a false positive.
	if len(report.Entries) != 0 {
		t.Errorf("Entries len = %d, want 0 (failed fault skipped)", len(report.Entries))
	}
	if report.FalsePositives != 1 {
		t.Errorf("FalsePositives = %d, want 1", report.FalsePositives)
	}
}

func TestCorrelator_NoExpectedDiagnosis(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	// Chaos-mode fault: no expected diagnosis.
	c.Emit(faultEvent(chaos.FaultRestartDeployment, "prod", "worker", true, now, ""))

	tl := c.Timeline()
	corr := NewCorrelator(CorrelatorConfig{DetectionWindow: 5 * time.Minute})
	actions := []Action{
		action(now.Add(time.Minute), "noticed worker deployment restarting"),
	}

	report, _ := corr.Correlate(tl, actions)

	if len(report.Entries) != 1 {
		t.Fatalf("Entries len = %d, want 1", len(report.Entries))
	}
	if report.Entries[0].Outcome != OutcomeNoExpectedDiagnosis {
		t.Errorf("Outcome = %q, want %q", report.Entries[0].Outcome, OutcomeNoExpectedDiagnosis)
	}
}

func TestCorrelator_NilTimeline(t *testing.T) {
	t.Parallel()

	corr := NewCorrelator(CorrelatorConfig{})
	_, err := corr.Correlate(nil, nil)
	if err == nil {
		t.Error("expected error for nil timeline")
	}
}

func TestCorrelationReport_JSONSerializable(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "prod", "frontend", true, now, "pod missing"))

	tl := c.Timeline()
	corr := NewCorrelator(CorrelatorConfig{DetectionWindow: 5 * time.Minute})
	actions := []Action{
		action(now.Add(time.Minute), "pod missing alert fired"),
	}

	report, err := corr.Correlate(tl, actions)
	if err != nil {
		t.Fatalf("Correlate() error: %v", err)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(CorrelationReport) error: %v", err)
	}
	if len(data) == 0 {
		t.Error("JSON output is empty")
	}
}

// ── defaultDiagnosisMatch ─────────────────────────────────────────────────────

func TestDefaultDiagnosisMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		action   string
		expected string
		want     bool
	}{
		{"frontend pod missing from prod", "frontend pod missing", true},
		{"FRONTEND POD MISSING", "frontend pod missing", true},
		{"something unrelated", "frontend pod missing", false},
		{"something unrelated", "", false},
		{"", "diagnosis", false},
	}

	for _, tc := range tests {
		got := defaultDiagnosisMatch(tc.action, tc.expected)
		if got != tc.want {
			t.Errorf("defaultDiagnosisMatch(%q, %q) = %v, want %v", tc.action, tc.expected, got, tc.want)
		}
	}
}

// ── custom DiagnosisMatchFunc ─────────────────────────────────────────────────

func TestCorrelator_CustomMatchFunc(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := NewCollector()
	c.Emit(faultEvent(chaos.FaultKillPod, "ns", "pod", true, now, "exact-token"))

	tl := c.Timeline()

	// Custom matcher: requires exact equality.
	exactMatch := func(desc, expected string) bool { return desc == expected }
	corr := NewCorrelator(CorrelatorConfig{
		DetectionWindow:    5 * time.Minute,
		DiagnosisMatchFunc: exactMatch,
	})

	t.Run("exact match succeeds", func(t *testing.T) {
		report, _ := corr.Correlate(tl, []Action{action(now.Add(time.Minute), "exact-token")})
		if report.DetectedCorrect != 1 {
			t.Errorf("DetectedCorrect = %d, want 1", report.DetectedCorrect)
		}
	})

	t.Run("partial match fails", func(t *testing.T) {
		report, _ := corr.Correlate(tl, []Action{action(now.Add(time.Minute), "prefix exact-token suffix")})
		if report.DetectedMisdiagnosed != 1 {
			t.Errorf("DetectedMisdiagnosed = %d, want 1", report.DetectedMisdiagnosed)
		}
	})
}
