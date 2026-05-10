package scenarios

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/chaos"
)

// ── mock helpers ───────────────────────────────────────────────────────────────

type mockFault struct {
	faultType chaos.FaultType
	err       error
	calls     int
	mu        sync.Mutex
}

func (f *mockFault) Type() chaos.FaultType { return f.faultType }

func (f *mockFault) Execute(_ context.Context, _ chaos.KubeClient, _ chaos.TargetResource, _ map[string]string) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.err
}

type mockEmitter struct {
	mu     sync.Mutex
	events []chaos.FaultEvent
}

func (e *mockEmitter) Emit(ev chaos.FaultEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func (e *mockEmitter) all() []chaos.FaultEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := make([]chaos.FaultEvent, len(e.events))
	copy(cp, e.events)
	return cp
}

type mockSteadyState struct {
	err error
}

func (m *mockSteadyState) WaitForRollout(_ context.Context, _, _ string, _ time.Duration) error {
	return m.err
}

// buildFaults returns a faults map with the given fault types registered (all succeed by default).
func buildFaults(types ...chaos.FaultType) map[chaos.FaultType]chaos.Fault {
	m := make(map[chaos.FaultType]chaos.Fault, len(types))
	for _, t := range types {
		t := t
		m[t] = &mockFault{faultType: t}
	}
	return m
}

func makeTarget() chaos.TargetResource {
	return chaos.TargetResource{Namespace: "default", Name: "frontend", Kind: "Deployment"}
}

// ── LoadReader ─────────────────────────────────────────────────────────────────

func TestLoadReader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(*testing.T, *Scenario)
	}{
		{
			name: "valid scenario",
			yaml: `
name: test-scenario
description: A test
steps:
  - name: kill frontend
    fault_type: kill_pod
    target:
      namespace: default
      name: frontend
      kind: Pod
    delay: 5s
    expected_diagnosis: frontend pod missing
`,
			check: func(t *testing.T, s *Scenario) {
				if s.Name != "test-scenario" {
					t.Errorf("Name = %q, want %q", s.Name, "test-scenario")
				}
				if len(s.Steps) != 1 {
					t.Fatalf("len(Steps) = %d, want 1", len(s.Steps))
				}
				step := s.Steps[0]
				if step.FaultType != chaos.FaultKillPod {
					t.Errorf("FaultType = %q, want %q", step.FaultType, chaos.FaultKillPod)
				}
				if step.Delay != 5*time.Second {
					t.Errorf("Delay = %v, want 5s", step.Delay)
				}
				if step.ExpectedDiagnosis == "" {
					t.Error("ExpectedDiagnosis should not be empty")
				}
			},
		},
		{
			name:    "missing name",
			yaml:    `steps: []`,
			wantErr: true,
		},
		{
			name:    "invalid yaml",
			yaml:    `name: [broken`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := LoadReader(strings.NewReader(tc.yaml))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadReader() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && tc.check != nil {
				tc.check(t, s)
			}
		})
	}
}

// ── Validate ──────────────────────────────────────────────────────────────────

func TestValidate(t *testing.T) {
	t.Parallel()

	target := makeTarget()
	faults := buildFaults(chaos.FaultKillPod, chaos.FaultScaleToZero)
	topology := []chaos.TargetResource{target}

	tests := []struct {
		name     string
		scenario Scenario
		topology []chaos.TargetResource
		wantErr  bool
		wantMsg  string
	}{
		{
			name: "valid scenario",
			scenario: Scenario{
				Name: "ok",
				Steps: []Step{{
					Name:      "step1",
					FaultType: chaos.FaultKillPod,
					Target:    target,
				}},
			},
			topology: topology,
		},
		{
			name: "empty name",
			scenario: Scenario{
				Name:  "",
				Steps: []Step{{Name: "s", FaultType: chaos.FaultKillPod, Target: target}},
			},
			topology: topology,
			wantErr:  true,
			wantMsg:  "scenario name is empty",
		},
		{
			name: "no steps",
			scenario: Scenario{
				Name:  "empty",
				Steps: []Step{},
			},
			wantErr: true,
			wantMsg: "no steps",
		},
		{
			name: "unknown fault type",
			scenario: Scenario{
				Name:  "bad-fault",
				Steps: []Step{{Name: "s", FaultType: "nonexistent", Target: target}},
			},
			topology: topology,
			wantErr:  true,
			wantMsg:  "unknown fault_type",
		},
		{
			name: "target not in topology",
			scenario: Scenario{
				Name: "bad-target",
				Steps: []Step{{
					Name:      "s",
					FaultType: chaos.FaultKillPod,
					Target:    chaos.TargetResource{Namespace: "other", Name: "other", Kind: "Pod"},
				}},
			},
			topology: topology,
			wantErr:  true,
			wantMsg:  "not found in lab topology",
		},
		{
			name: "empty topology skips target check",
			scenario: Scenario{
				Name: "no-topo",
				Steps: []Step{{
					Name:      "s",
					FaultType: chaos.FaultKillPod,
					Target:    chaos.TargetResource{Namespace: "any", Name: "any", Kind: "Pod"},
				}},
			},
			topology: nil, // topology not provided → skip target check
		},
		{
			name: "steady state missing deployment",
			scenario: Scenario{
				Name: "bad-ss",
				Steps: []Step{{
					Name:      "s",
					FaultType: chaos.FaultKillPod,
					Target:    target,
					SteadyState: &SteadyStateCheck{
						Namespace: "default",
						// Deployment is empty
					},
				}},
			},
			topology: topology,
			wantErr:  true,
			wantMsg:  "steady_state.deployment is empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(&tc.scenario, faults, tc.topology)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantMsg != "" && err != nil && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// ── NewRunner ─────────────────────────────────────────────────────────────────

func TestNewRunner_UnknownFaultType(t *testing.T) {
	t.Parallel()

	s := Scenario{
		Name: "bad",
		Steps: []Step{{
			Name:      "s",
			FaultType: "not-registered",
			Target:    makeTarget(),
		}},
	}
	_, err := NewRunner(RunnerConfig{
		Scenario: s,
		Faults:   buildFaults(chaos.FaultKillPod),
		Emitter:  &mockEmitter{},
		Kube:     nil,
		Log:      slog.Default(),
	})
	if err == nil {
		t.Fatal("expected error for unknown fault type")
	}
}

// ── Run execution ──────────────────────────────────────────────────────────────

func TestScenarioRunner_Run_ExecutesStepsInOrder(t *testing.T) {
	t.Parallel()

	fault1 := &mockFault{faultType: chaos.FaultKillPod}
	fault2 := &mockFault{faultType: chaos.FaultScaleToZero}
	faults := map[chaos.FaultType]chaos.Fault{
		chaos.FaultKillPod:     fault1,
		chaos.FaultScaleToZero: fault2,
	}
	emitter := &mockEmitter{}

	target := makeTarget()
	s := Scenario{
		Name: "ordered",
		Steps: []Step{
			{Name: "step-1", FaultType: chaos.FaultKillPod, Target: target},
			{Name: "step-2", FaultType: chaos.FaultScaleToZero, Target: target},
		},
	}

	runner, err := NewRunner(RunnerConfig{
		Scenario: s,
		Faults:   faults,
		Emitter:  emitter,
		Kube:     nil,
		Log:      slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewRunner() error: %v", err)
	}

	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	events := emitter.all()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].FaultType != chaos.FaultKillPod {
		t.Errorf("event[0].FaultType = %q, want %q", events[0].FaultType, chaos.FaultKillPod)
	}
	if events[1].FaultType != chaos.FaultScaleToZero {
		t.Errorf("event[1].FaultType = %q, want %q", events[1].FaultType, chaos.FaultScaleToZero)
	}
	if events[0].ScenarioStep != "step-1" {
		t.Errorf("event[0].ScenarioStep = %q, want %q", events[0].ScenarioStep, "step-1")
	}
}

func TestScenarioRunner_Run_PreservesExpectedDiagnosis(t *testing.T) {
	t.Parallel()

	faults := buildFaults(chaos.FaultKillPod)
	emitter := &mockEmitter{}

	s := Scenario{
		Name: "diag-test",
		Steps: []Step{{
			Name:              "kill-db",
			FaultType:         chaos.FaultKillPod,
			Target:            makeTarget(),
			ExpectedDiagnosis: "PostgreSQL pod missing, read errors expected",
		}},
	}

	runner, _ := NewRunner(RunnerConfig{
		Scenario: s,
		Faults:   faults,
		Emitter:  emitter,
		Kube:     nil,
		Log:      slog.Default(),
	})

	_ = runner.Run(context.Background())

	events := emitter.all()
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	if events[0].ExpectedDiagnosis != "PostgreSQL pod missing, read errors expected" {
		t.Errorf("ExpectedDiagnosis = %q", events[0].ExpectedDiagnosis)
	}
}

func TestScenarioRunner_Run_ContinuesAfterFaultError(t *testing.T) {
	t.Parallel()

	failFault := &mockFault{faultType: chaos.FaultKillPod, err: errors.New("pod not found")}
	succFault := &mockFault{faultType: chaos.FaultScaleToZero}
	faults := map[chaos.FaultType]chaos.Fault{
		chaos.FaultKillPod:     failFault,
		chaos.FaultScaleToZero: succFault,
	}
	emitter := &mockEmitter{}

	target := makeTarget()
	s := Scenario{
		Name: "continue-on-err",
		Steps: []Step{
			{Name: "fail", FaultType: chaos.FaultKillPod, Target: target},
			{Name: "succeed", FaultType: chaos.FaultScaleToZero, Target: target},
		},
	}

	runner, _ := NewRunner(RunnerConfig{
		Scenario: s,
		Faults:   faults,
		Emitter:  emitter,
		Kube:     nil,
		Log:      slog.Default(),
	})

	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}

	events := emitter.all()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Success {
		t.Error("event[0] should be failure")
	}
	if !events[1].Success {
		t.Errorf("event[1] should succeed, error=%q", events[1].Error)
	}
}

func TestScenarioRunner_Run_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	// Second step has a 5s delay; cancelling before that should stop execution.
	faults := buildFaults(chaos.FaultKillPod, chaos.FaultScaleToZero)
	emitter := &mockEmitter{}

	target := makeTarget()
	s := Scenario{
		Name: "cancel-test",
		Steps: []Step{
			{Name: "fast", FaultType: chaos.FaultKillPod, Target: target},
			{Name: "slow", FaultType: chaos.FaultScaleToZero, Target: target, Delay: 5 * time.Second},
		},
	}

	runner, _ := NewRunner(RunnerConfig{
		Scenario: s,
		Faults:   faults,
		Emitter:  emitter,
		Kube:     nil,
		Log:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := runner.Run(ctx)
	if err == nil {
		t.Error("expected context error, got nil")
	}

	// Only the first step should have run.
	events := emitter.all()
	if len(events) > 1 {
		t.Errorf("expected at most 1 event before cancellation, got %d", len(events))
	}
}

func TestScenarioRunner_Run_SteadyStateFailureDoesNotStop(t *testing.T) {
	t.Parallel()

	faults := buildFaults(chaos.FaultKillPod)
	emitter := &mockEmitter{}

	target := makeTarget()
	s := Scenario{
		Name: "ss-fail",
		Steps: []Step{{
			Name:      "s",
			FaultType: chaos.FaultKillPod,
			Target:    target,
			SteadyState: &SteadyStateCheck{
				Namespace:  "default",
				Deployment: "frontend",
				Timeout:    5 * time.Second,
			},
		}},
	}

	runner, _ := NewRunner(RunnerConfig{
		Scenario:    s,
		Faults:      faults,
		Emitter:     emitter,
		Kube:        nil,
		SteadyState: &mockSteadyState{err: errors.New("rollout timeout")},
		Log:         slog.Default(),
	})

	// Should not return an error even when steady-state fails.
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}

	// The fault should still have been executed.
	if len(emitter.all()) != 1 {
		t.Errorf("expected 1 event, got %d", len(emitter.all()))
	}
}
