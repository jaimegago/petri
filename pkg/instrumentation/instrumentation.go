// Package instrumentation provides observability into chaos and scenario fault injection
// so that Joe's (the AI copilot's) responses can be evaluated for correctness.
//
// The central type is Collector, which implements chaos.EventEmitter and accumulates
// all fault events into an in-memory Timeline. Once a session is complete, call
// Report to produce a structured JSON-serialisable summary. Use NewCorrelator to
// compare the Timeline against Joe's actions and produce a CorrelationReport that
// classifies each fault as detected/misdiagnosed/missed and flags false-positive
// Joe actions.
package instrumentation

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jaimegago/petri/pkg/chaos"
)

// ── Timeline ─────────────────────────────────────────────────────────────────

// TimelineEntry wraps a FaultEvent with its position in the injection sequence.
type TimelineEntry struct {
	// Index is the zero-based sequential position of this event in the timeline.
	Index int `json:"index"`
	// Event is the raw fault event as emitted by a runner.
	Event chaos.FaultEvent `json:"event"`
}

// Timeline is a time-ordered, queryable log of fault events. It is produced by
// Collector.Timeline() and consumed by Report and Correlator.
type Timeline struct {
	entries []TimelineEntry
}

// All returns all entries in chronological order.
func (t *Timeline) All() []TimelineEntry {
	return t.entries
}

// ByFaultType returns all entries whose FaultType matches ft.
func (t *Timeline) ByFaultType(ft chaos.FaultType) []TimelineEntry {
	var result []TimelineEntry
	for _, e := range t.entries {
		if e.Event.FaultType == ft {
			result = append(result, e)
		}
	}
	return result
}

// ByTarget returns all entries whose target matches the given namespace, name, and kind.
// An empty string in any field acts as a wildcard.
func (t *Timeline) ByTarget(namespace, name, kind string) []TimelineEntry {
	var result []TimelineEntry
	for _, e := range t.entries {
		tr := e.Event.Target
		if (namespace == "" || tr.Namespace == namespace) &&
			(name == "" || tr.Name == name) &&
			(kind == "" || tr.Kind == kind) {
			result = append(result, e)
		}
	}
	return result
}

// InRange returns all entries whose StartedAt falls within [from, to].
// A zero from or to is treated as unbounded.
func (t *Timeline) InRange(from, to time.Time) []TimelineEntry {
	var result []TimelineEntry
	for _, e := range t.entries {
		start := e.Event.StartedAt
		if !from.IsZero() && start.Before(from) {
			continue
		}
		if !to.IsZero() && start.After(to) {
			continue
		}
		result = append(result, e)
	}
	return result
}

// Len returns the total number of events in the timeline.
func (t *Timeline) Len() int { return len(t.entries) }

// ── Collector ─────────────────────────────────────────────────────────────────

// Collector implements chaos.EventEmitter and accumulates all fault events into
// an in-memory timeline. It is safe for concurrent use by multiple goroutines
// (ChaosRunner and ScenarioRunner both call Emit from their own goroutines).
type Collector struct {
	mu      sync.Mutex
	entries []TimelineEntry
}

// NewCollector returns an empty, ready-to-use Collector.
func NewCollector() *Collector {
	return &Collector{}
}

// Emit records a fault event. It satisfies the chaos.EventEmitter interface.
// Emit never blocks or drops events.
func (c *Collector) Emit(event chaos.FaultEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, TimelineEntry{
		Index: len(c.entries),
		Event: event,
	})
}

// Timeline returns a snapshot of all collected events in the order they were received.
// The returned Timeline is an independent copy; subsequent Emit calls do not affect it.
func (c *Collector) Timeline() *Timeline {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := make([]TimelineEntry, len(c.entries))
	copy(entries, c.entries)
	return &Timeline{entries: entries}
}

// ── Report ─────────────────────────────────────────────────────────────────────

// FaultTypeSummary holds per-fault-type counts.
type FaultTypeSummary struct {
	// FaultType is the kind of fault.
	FaultType chaos.FaultType `json:"fault_type"`
	// Total is the number of times this fault type was attempted.
	Total int `json:"total"`
	// Succeeded is the number of successful injections.
	Succeeded int `json:"succeeded"`
	// Failed is the number of failed injections.
	Failed int `json:"failed"`
}

// Report is a structured summary of a completed chaos or scenario session.
// It is JSON-serialisable.
type Report struct {
	// GeneratedAt is when the report was produced.
	GeneratedAt time.Time `json:"generated_at"`
	// TotalFaults is the total number of fault injection attempts.
	TotalFaults int `json:"total_faults"`
	// Succeeded is the number of successful injections.
	Succeeded int `json:"succeeded"`
	// Failed is the number of failed injections.
	Failed int `json:"failed"`
	// ByFaultType holds per-type breakdown.
	ByFaultType []FaultTypeSummary `json:"by_fault_type"`
	// Timeline is the complete ordered list of events.
	Timeline []TimelineEntry `json:"timeline"`
}

// MarshalJSON implements json.Marshaler so Report can be embedded or serialised
// directly with encoding/json.
func (r *Report) MarshalJSON() ([]byte, error) {
	type alias Report
	return json.Marshal((*alias)(r))
}

// GenerateReport produces a Report from a Timeline. For scenario sessions, each
// event in the Timeline already carries ExpectedDiagnosis from the scenario step.
func GenerateReport(tl *Timeline) *Report {
	r := &Report{
		GeneratedAt: time.Now().UTC(),
		Timeline:    tl.entries,
	}

	byType := make(map[chaos.FaultType]*FaultTypeSummary)
	for _, entry := range tl.entries {
		ev := entry.Event
		r.TotalFaults++
		if ev.Success {
			r.Succeeded++
		} else {
			r.Failed++
		}

		ft := ev.FaultType
		s, ok := byType[ft]
		if !ok {
			s = &FaultTypeSummary{FaultType: ft}
			byType[ft] = s
		}
		s.Total++
		if ev.Success {
			s.Succeeded++
		} else {
			s.Failed++
		}
	}

	// Sort for deterministic output.
	for _, s := range byType {
		r.ByFaultType = append(r.ByFaultType, *s)
	}
	sort.Slice(r.ByFaultType, func(i, j int) bool {
		return string(r.ByFaultType[i].FaultType) < string(r.ByFaultType[j].FaultType)
	})

	return r
}

// ── Correlator ────────────────────────────────────────────────────────────────

// Action represents a timestamped action taken by Joe (the AI copilot).
// Implementations are provided by the caller — Petri does not define concrete
// Action types. Adding a new consumer of the correlation output does not require
// modifying this interface or the Correlator.
type Action interface {
	// Timestamp returns when Joe performed the action.
	Timestamp() time.Time
	// Description returns a human-readable summary of the action.
	Description() string
}

// DetectionOutcome categorises how Joe responded to a single injected fault.
type DetectionOutcome string

const (
	// OutcomeDetectedCorrect means Joe detected the fault and matched the expected diagnosis.
	OutcomeDetectedCorrect DetectionOutcome = "detected_correct"
	// OutcomeDetectedMisdiagnosed means Joe detected the fault but the action
	// description did not match the expected diagnosis.
	OutcomeDetectedMisdiagnosed DetectionOutcome = "detected_misdiagnosed"
	// OutcomeMissed means no Joe action was found within the detection window after the fault.
	OutcomeMissed DetectionOutcome = "missed"
	// OutcomeNoExpectedDiagnosis means Joe detected the fault but no expected diagnosis
	// was defined (chaos-mode fault with no scenario metadata).
	OutcomeNoExpectedDiagnosis DetectionOutcome = "detected_no_expected_diagnosis"
)

// CorrelationEntry records the relationship between one injected fault and Joe's response.
type CorrelationEntry struct {
	// FaultEvent is the injected fault.
	FaultEvent chaos.FaultEvent `json:"fault_event"`
	// Outcome is how Joe responded.
	Outcome DetectionOutcome `json:"outcome"`
	// MatchedAction is the Joe action that was attributed to this fault, if any.
	MatchedAction *ActionSnapshot `json:"matched_action,omitempty"`
	// TimeToDetection is how long after the fault was injected until Joe's action appeared.
	// Zero when Outcome is OutcomeMissed.
	TimeToDetection time.Duration `json:"time_to_detection_ms"`
}

// ActionSnapshot is a JSON-serialisable copy of an Action so that CorrelationReport
// can be serialised without the caller's concrete type.
type ActionSnapshot struct {
	// Timestamp is when Joe performed the action.
	Timestamp time.Time `json:"timestamp"`
	// Description is a human-readable summary of the action.
	Description string `json:"description"`
}

// FalsePositiveEntry records a Joe action that could not be attributed to any
// injected fault within the attribution window.
type FalsePositiveEntry struct {
	// Action is the unmatched Joe action.
	Action ActionSnapshot `json:"action"`
}

// CorrelationReport is a JSON-serialisable summary of how well Joe responded to
// the injected faults.
type CorrelationReport struct {
	// GeneratedAt is when the report was produced.
	GeneratedAt time.Time `json:"generated_at"`
	// TotalFaults is the number of injected faults considered.
	TotalFaults int `json:"total_faults"`
	// DetectedCorrect is the count of faults Joe detected and correctly diagnosed.
	DetectedCorrect int `json:"detected_correct"`
	// DetectedMisdiagnosed is the count of faults Joe detected but misdiagnosed.
	DetectedMisdiagnosed int `json:"detected_misdiagnosed"`
	// Missed is the count of faults Joe did not detect.
	Missed int `json:"missed"`
	// FalsePositives is the count of Joe actions that did not correspond to any fault.
	FalsePositives int `json:"false_positives"`
	// Entries is the per-fault correlation detail.
	Entries []CorrelationEntry `json:"entries"`
	// FalsePositiveActions lists Joe actions that could not be attributed to a fault.
	FalsePositiveActions []FalsePositiveEntry `json:"false_positive_actions"`
}

// CorrelatorConfig holds tunable parameters for the Correlator.
type CorrelatorConfig struct {
	// DetectionWindow is the maximum time after a fault injection within which a Joe
	// action may be attributed to that fault. Defaults to 5 minutes when zero.
	DetectionWindow time.Duration
	// DiagnosisMatchFunc determines whether a Joe action description matches an
	// expected diagnosis string. When nil, a case-insensitive substring check is used.
	// Replace this to use semantic similarity or other matching strategies.
	DiagnosisMatchFunc func(actionDescription, expectedDiagnosis string) bool
}

// Correlator compares a fault Timeline against Joe's observed actions and produces
// a CorrelationReport. It is stateless after construction; Correlate may be called
// multiple times with different action sets.
type Correlator struct {
	cfg CorrelatorConfig
}

// NewCorrelator constructs a Correlator with the given configuration.
func NewCorrelator(cfg CorrelatorConfig) *Correlator {
	if cfg.DetectionWindow <= 0 {
		cfg.DetectionWindow = 5 * time.Minute
	}
	if cfg.DiagnosisMatchFunc == nil {
		cfg.DiagnosisMatchFunc = defaultDiagnosisMatch
	}
	return &Correlator{cfg: cfg}
}

// Correlate matches injected faults from tl against Joe's actions and returns a
// CorrelationReport. Each fault event is matched to at most one action (the first
// action that falls within the detection window after the fault's StartedAt).
// Unmatched actions are flagged as false positives.
//
// Only successful fault injections (FaultEvent.Success == true) are considered for
// correlation. Failed injections appear in the Report but are not expected to
// produce Joe responses.
func (c *Correlator) Correlate(tl *Timeline, actions []Action) (*CorrelationReport, error) {
	if tl == nil {
		return nil, fmt.Errorf("timeline must not be nil")
	}

	// Build a mutable copy of actions so we can mark them as attributed.
	type taggedAction struct {
		action     Action
		snapshot   ActionSnapshot
		attributed bool
	}
	tagged := make([]taggedAction, len(actions))
	for i, a := range actions {
		tagged[i] = taggedAction{
			action:   a,
			snapshot: ActionSnapshot{Timestamp: a.Timestamp(), Description: a.Description()},
		}
	}
	// Sort by timestamp for stable attribution.
	sort.Slice(tagged, func(i, j int) bool {
		return tagged[i].snapshot.Timestamp.Before(tagged[j].snapshot.Timestamp)
	})

	report := &CorrelationReport{
		GeneratedAt: time.Now().UTC(),
		TotalFaults: tl.Len(),
	}

	for _, entry := range tl.entries {
		ev := entry.Event

		// Only correlate successful injections.
		if !ev.Success {
			continue
		}

		window := ev.StartedAt.Add(c.cfg.DetectionWindow)
		var matched *taggedAction
		for i := range tagged {
			ta := &tagged[i]
			if ta.attributed {
				continue
			}
			ts := ta.snapshot.Timestamp
			if ts.Before(ev.StartedAt) || ts.After(window) {
				continue
			}
			matched = ta
			ta.attributed = true
			break
		}

		ce := CorrelationEntry{FaultEvent: ev}

		if matched == nil {
			ce.Outcome = OutcomeMissed
			report.Missed++
		} else {
			ce.MatchedAction = &matched.snapshot
			ce.TimeToDetection = matched.snapshot.Timestamp.Sub(ev.StartedAt)

			if ev.ExpectedDiagnosis == "" {
				ce.Outcome = OutcomeNoExpectedDiagnosis
			} else if c.cfg.DiagnosisMatchFunc(matched.snapshot.Description, ev.ExpectedDiagnosis) {
				ce.Outcome = OutcomeDetectedCorrect
				report.DetectedCorrect++
			} else {
				ce.Outcome = OutcomeDetectedMisdiagnosed
				report.DetectedMisdiagnosed++
			}
		}

		report.Entries = append(report.Entries, ce)
	}

	for _, ta := range tagged {
		if !ta.attributed {
			report.FalsePositives++
			report.FalsePositiveActions = append(report.FalsePositiveActions, FalsePositiveEntry{
				Action: ta.snapshot,
			})
		}
	}

	return report, nil
}

// defaultDiagnosisMatch performs a case-insensitive substring search:
// the action description must contain the expected diagnosis string.
func defaultDiagnosisMatch(actionDescription, expectedDiagnosis string) bool {
	if expectedDiagnosis == "" {
		return false
	}
	// Case-insensitive containment without importing strings (avoid double import).
	ad := toLower(actionDescription)
	ed := toLower(expectedDiagnosis)
	return contains(ad, ed)
}

// toLower converts ASCII characters to lower case without importing strings.
func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

// contains reports whether substr appears in s.
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	for i := range len(s) - len(substr) + 1 {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
