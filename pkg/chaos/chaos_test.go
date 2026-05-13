package chaos

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ── mock helpers ───────────────────────────────────────────────────────────────

type mockKubeClient struct {
	mu       sync.Mutex
	pods     map[string][]string // namespace -> pod names
	cms      map[string]map[string]string
	saTokens map[string][]string // key: "ns/name"
	calls    []string
	err      error
}

func newMockKube() *mockKubeClient {
	return &mockKubeClient{
		pods:     make(map[string][]string),
		cms:      make(map[string]map[string]string),
		saTokens: make(map[string][]string),
	}
}

func (m *mockKubeClient) record(call string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
}

func (m *mockKubeClient) ListPods(_ context.Context, namespace, _ string) ([]string, error) {
	m.record("list_pods:" + namespace)
	return m.pods[namespace], m.err
}

func (m *mockKubeClient) DeletePod(_ context.Context, namespace, name string) error {
	m.record("delete_pod:" + namespace + "/" + name)
	return m.err
}

func (m *mockKubeClient) RestartDeployment(_ context.Context, namespace, name string) error {
	m.record("restart_deployment:" + namespace + "/" + name)
	return m.err
}

func (m *mockKubeClient) ScaleDeployment(_ context.Context, namespace, name string, replicas int32) error {
	m.record("scale_deployment:" + namespace + "/" + name)
	return m.err
}

func (m *mockKubeClient) GetConfigMap(_ context.Context, namespace, name string) (map[string]string, error) {
	m.record("get_configmap:" + namespace + "/" + name)
	key := namespace + "/" + name
	if cm, ok := m.cms[key]; ok {
		// Return a copy so the fault can mutate it safely.
		cp := make(map[string]string, len(cm))
		for k, v := range cm {
			cp[k] = v
		}
		return cp, m.err
	}
	return map[string]string{"config": "value"}, m.err
}

func (m *mockKubeClient) UpdateConfigMap(_ context.Context, namespace, name string, _ map[string]string) error {
	m.record("update_configmap:" + namespace + "/" + name)
	return m.err
}

func (m *mockKubeClient) ListServiceAccountSecrets(_ context.Context, namespace, name string) ([]string, error) {
	m.record("list_sa_secrets:" + namespace + "/" + name)
	return m.saTokens[namespace+"/"+name], m.err
}

func (m *mockKubeClient) DeleteSecret(_ context.Context, namespace, name string) error {
	m.record("delete_secret:" + namespace + "/" + name)
	return m.err
}

func (m *mockKubeClient) ExecInPod(_ context.Context, namespace, pod string, _ []string) (string, error) {
	m.record("exec_pod:" + namespace + "/" + pod)
	return "ok", m.err
}

func (m *mockKubeClient) CreateNamespace(_ context.Context, name string, _ map[string]string) error {
	m.record("create_namespace:" + name)
	return m.err
}

func (m *mockKubeClient) DeleteNamespace(_ context.Context, name string) error {
	m.record("delete_namespace:" + name)
	return m.err
}

func (m *mockKubeClient) DeleteNamespaceWithTimeout(_ context.Context, name string, _ time.Duration) error {
	m.record("delete_namespace_timeout:" + name)
	return m.err
}

func (m *mockKubeClient) GetNamespacePhase(_ context.Context, _ string) (string, error) {
	return "", m.err
}

func (m *mockKubeClient) GetResource(_ context.Context, kind, namespace, name string) (string, error) {
	m.record("get_resource:" + kind + "/" + namespace + "/" + name)
	return "{}", m.err
}

func (m *mockKubeClient) ListResources(_ context.Context, kind, namespace string) (string, error) {
	m.record("list_resources:" + kind + "/" + namespace)
	return `{"items":[]}`, m.err
}

func (m *mockKubeClient) ApplyYAML(_ context.Context, _ string) error {
	m.record("apply_yaml")
	return m.err
}

func (m *mockKubeClient) WaitForRollout(_ context.Context, namespace, deployment string, _ time.Duration) error {
	m.record("wait_rollout:" + namespace + "/" + deployment)
	return m.err
}

func (m *mockKubeClient) GetClusterConfig(_ context.Context) (string, string, error) {
	return "https://127.0.0.1:6443", "", m.err
}

func (m *mockKubeClient) TokenForServiceAccount(_ context.Context, namespace, name string) (string, error) {
	m.record("token_sa:" + namespace + "/" + name)
	return "test-token", m.err
}

type mockEmitter struct {
	mu     sync.Mutex
	events []FaultEvent
}

func (e *mockEmitter) Emit(ev FaultEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func (e *mockEmitter) all() []FaultEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := make([]FaultEvent, len(e.events))
	copy(cp, e.events)
	return cp
}

// ── NewRunner validation ───────────────────────────────────────────────────────

func TestNewRunner_Validation(t *testing.T) {
	t.Parallel()

	baseProfile := ChaosProfile{
		Name:        "test",
		MinInterval: 5 * time.Second,
		MaxInterval: 10 * time.Second,
		Faults:      []FaultSpec{{Type: FaultKillPod, Probability: 0.5}},
		Targets:     []TargetResource{{Namespace: "default", Name: "app", Kind: "Deployment"}},
	}

	tests := []struct {
		name    string
		profile ChaosProfile
		wantErr bool
	}{
		{
			name:    "valid profile",
			profile: baseProfile,
		},
		{
			name: "unknown fault type",
			profile: ChaosProfile{
				Name:    "bad",
				Faults:  []FaultSpec{{Type: "nonexistent", Probability: 0.5}},
				Targets: baseProfile.Targets,
			},
			wantErr: true,
		},
		{
			name: "probability below zero",
			profile: ChaosProfile{
				Name:    "bad_prob",
				Faults:  []FaultSpec{{Type: FaultKillPod, Probability: -0.1}},
				Targets: baseProfile.Targets,
			},
			wantErr: true,
		},
		{
			name: "probability above one",
			profile: ChaosProfile{
				Name:    "bad_prob",
				Faults:  []FaultSpec{{Type: FaultKillPod, Probability: 1.5}},
				Targets: baseProfile.Targets,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewRunner(RunnerConfig{
				Profile: tc.profile,
				Emitter: &mockEmitter{},
				Kube:    newMockKube(),
				Log:     slog.Default(),
				Seed:    1,
			})
			if (err != nil) != tc.wantErr {
				t.Errorf("NewRunner() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// ── interval defaults ──────────────────────────────────────────────────────────

func TestNewRunner_DefaultIntervals(t *testing.T) {
	t.Parallel()

	r, err := NewRunner(RunnerConfig{
		Profile: ChaosProfile{
			Name:    "defaults",
			Faults:  []FaultSpec{{Type: FaultKillPod, Probability: 1.0}},
			Targets: []TargetResource{{Namespace: "default", Name: "app", Kind: "Deployment"}},
			// MinInterval and MaxInterval both zero — should get defaults.
		},
		Emitter: &mockEmitter{},
		Kube:    newMockKube(),
		Log:     slog.Default(),
		Seed:    42,
	})
	if err != nil {
		t.Fatalf("NewRunner() unexpected error: %v", err)
	}

	if r.profile.MinInterval != 10*time.Second {
		t.Errorf("MinInterval = %v, want 10s", r.profile.MinInterval)
	}
	if r.profile.MaxInterval != 20*time.Second {
		t.Errorf("MaxInterval = %v, want 20s", r.profile.MaxInterval)
	}
}

// ── nextInterval ───────────────────────────────────────────────────────────────

func TestChaosRunner_NextInterval(t *testing.T) {
	t.Parallel()

	r, err := NewRunner(RunnerConfig{
		Profile: ChaosProfile{
			Name:        "interval-test",
			Faults:      []FaultSpec{{Type: FaultKillPod, Probability: 1.0}},
			Targets:     []TargetResource{{Namespace: "default", Name: "pod", Kind: "Pod"}},
			MinInterval: 5 * time.Second,
			MaxInterval: 15 * time.Second,
		},
		Emitter: &mockEmitter{},
		Kube:    newMockKube(),
		Log:     slog.Default(),
		Seed:    7,
	})
	if err != nil {
		t.Fatalf("NewRunner() error: %v", err)
	}

	for i := range 20 {
		iv := r.nextInterval()
		if iv < 5*time.Second || iv > 15*time.Second {
			t.Errorf("iteration %d: interval %v outside [5s, 15s]", i, iv)
		}
	}
}

// ── selectFault ────────────────────────────────────────────────────────────────

func TestChaosRunner_SelectFault(t *testing.T) {
	t.Parallel()

	t.Run("probability 1.0 always fires", func(t *testing.T) {
		t.Parallel()
		r, _ := NewRunner(RunnerConfig{
			Profile: ChaosProfile{
				Name:    "p1",
				Faults:  []FaultSpec{{Type: FaultKillPod, Probability: 1.0}},
				Targets: []TargetResource{{Namespace: "ns", Name: "pod", Kind: "Pod"}},
			},
			Emitter: &mockEmitter{},
			Kube:    newMockKube(),
			Log:     slog.Default(),
			Seed:    1,
		})
		for i := range 10 {
			_, ok := r.selectFault()
			if !ok {
				t.Errorf("iteration %d: expected fault to fire with probability 1.0", i)
			}
		}
	})

	t.Run("probability 0.0 never fires", func(t *testing.T) {
		t.Parallel()
		r, _ := NewRunner(RunnerConfig{
			Profile: ChaosProfile{
				Name:    "p0",
				Faults:  []FaultSpec{{Type: FaultKillPod, Probability: 0.0}},
				Targets: []TargetResource{{Namespace: "ns", Name: "pod", Kind: "Pod"}},
			},
			Emitter: &mockEmitter{},
			Kube:    newMockKube(),
			Log:     slog.Default(),
			Seed:    1,
		})
		for i := range 100 {
			_, ok := r.selectFault()
			if ok {
				t.Errorf("iteration %d: expected fault to never fire with probability 0.0", i)
			}
		}
	})
}

// ── injectFault emits events ──────────────────────────────────────────────────

func TestChaosRunner_InjectFault_EmitsEvent(t *testing.T) {
	t.Parallel()

	kube := newMockKube()
	kube.pods["default"] = []string{"app-xyz"}
	emitter := &mockEmitter{}

	r, err := NewRunner(RunnerConfig{
		Profile: ChaosProfile{
			Name:    "emit-test",
			Faults:  []FaultSpec{{Type: FaultKillPod, Probability: 1.0}},
			Targets: []TargetResource{{Namespace: "default", Name: "app", Kind: "Deployment"}},
		},
		Emitter: emitter,
		Kube:    kube,
		Log:     slog.Default(),
		Seed:    1,
	})
	if err != nil {
		t.Fatalf("NewRunner() error: %v", err)
	}

	spec := FaultSpec{Type: FaultKillPod, Probability: 1.0}
	target := TargetResource{Namespace: "default", Name: "app", Kind: "Deployment"}
	r.injectFault(context.Background(), spec, target)

	events := emitter.all()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.FaultType != FaultKillPod {
		t.Errorf("FaultType = %q, want %q", ev.FaultType, FaultKillPod)
	}
	if !ev.Success {
		t.Errorf("expected Success=true, got error=%q", ev.Error)
	}
	if ev.ID == "" {
		t.Error("expected non-empty event ID")
	}
	if ev.StartedAt.IsZero() || ev.EndedAt.IsZero() {
		t.Error("expected non-zero StartedAt/EndedAt")
	}
}

func TestChaosRunner_InjectFault_RecordsError(t *testing.T) {
	t.Parallel()

	kube := newMockKube()
	kube.err = errors.New("connection refused")
	emitter := &mockEmitter{}

	r, err := NewRunner(RunnerConfig{
		Profile: ChaosProfile{
			Name:    "err-test",
			Faults:  []FaultSpec{{Type: FaultScaleToZero, Probability: 1.0}},
			Targets: []TargetResource{{Namespace: "default", Name: "frontend", Kind: "Deployment"}},
		},
		Emitter: emitter,
		Kube:    kube,
		Log:     slog.Default(),
		Seed:    1,
	})
	if err != nil {
		t.Fatalf("NewRunner() error: %v", err)
	}

	spec := FaultSpec{Type: FaultScaleToZero, Probability: 1.0}
	target := TargetResource{Namespace: "default", Name: "frontend", Kind: "Deployment"}
	r.injectFault(context.Background(), spec, target)

	events := emitter.all()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Success {
		t.Error("expected Success=false")
	}
	if events[0].Error == "" {
		t.Error("expected non-empty Error field")
	}
}

// ── Run stops on context cancellation ─────────────────────────────────────────

func TestChaosRunner_Run_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	kube := newMockKube()
	kube.pods["ns"] = []string{"pod-1"}
	emitter := &mockEmitter{}

	r, err := NewRunner(RunnerConfig{
		Profile: ChaosProfile{
			Name:        "cancel-test",
			Faults:      []FaultSpec{{Type: FaultKillPod, Probability: 1.0}},
			Targets:     []TargetResource{{Namespace: "ns", Name: "pod-1", Kind: "Pod"}},
			MinInterval: 50 * time.Millisecond,
			MaxInterval: 100 * time.Millisecond,
		},
		Emitter: emitter,
		Kube:    kube,
		Log:     slog.Default(),
		Seed:    1,
	})
	if err != nil {
		t.Fatalf("NewRunner() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	if err := r.Run(ctx); err != nil {
		t.Errorf("Run() returned unexpected error: %v", err)
	}
}

// ── Run stops after Duration ───────────────────────────────────────────────────

func TestChaosRunner_Run_StopsAfterDuration(t *testing.T) {
	t.Parallel()

	emitter := &mockEmitter{}
	r, err := NewRunner(RunnerConfig{
		Profile: ChaosProfile{
			Name:        "duration-test",
			Faults:      []FaultSpec{{Type: FaultScaleToZero, Probability: 1.0}},
			Targets:     []TargetResource{{Namespace: "ns", Name: "frontend", Kind: "Deployment"}},
			MinInterval: 30 * time.Millisecond,
			MaxInterval: 60 * time.Millisecond,
			Duration:    200 * time.Millisecond,
		},
		Emitter: emitter,
		Kube:    newMockKube(),
		Log:     slog.Default(),
		Seed:    1,
	})
	if err != nil {
		t.Fatalf("NewRunner() error: %v", err)
	}

	start := time.Now()
	if err := r.Run(context.Background()); err != nil {
		t.Errorf("Run() returned unexpected error: %v", err)
	}
	elapsed := time.Since(start)

	// Should finish within a reasonable margin of the configured Duration.
	if elapsed > 500*time.Millisecond {
		t.Errorf("Run took %v, expected it to stop near 200ms", elapsed)
	}
}

// ── individual fault types ────────────────────────────────────────────────────

func TestFaults_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fault     Fault
		target    TargetResource
		setupKube func(*mockKubeClient)
		params    map[string]string
		wantErr   bool
		wantCall  string
	}{
		{
			name:  "kill_pod by pod name",
			fault: &killPodFault{},
			target: TargetResource{
				Namespace: "prod", Name: "api-server-xyz", Kind: "Pod",
			},
			wantCall: "delete_pod:prod/api-server-xyz",
		},
		{
			name:  "kill_pod by label selector",
			fault: &killPodFault{},
			target: TargetResource{
				Namespace: "prod", Name: "api-server", Kind: "Deployment",
			},
			setupKube: func(k *mockKubeClient) {
				k.pods["prod"] = []string{"api-server-abc"}
			},
			wantCall: "delete_pod:prod/api-server-abc",
		},
		{
			name:  "restart_deployment",
			fault: &restartDeploymentFault{},
			target: TargetResource{
				Namespace: "staging", Name: "worker", Kind: "Deployment",
			},
			wantCall: "restart_deployment:staging/worker",
		},
		{
			name:  "scale_to_zero",
			fault: &scaleToZeroFault{},
			target: TargetResource{
				Namespace: "default", Name: "frontend", Kind: "Deployment",
			},
			wantCall: "scale_deployment:default/frontend",
		},
		{
			name:  "corrupt_configmap default key",
			fault: &corruptConfigMapFault{},
			target: TargetResource{
				Namespace: "default", Name: "app-config", Kind: "ConfigMap",
			},
			wantCall: "update_configmap:default/app-config",
		},
		{
			name:  "revoke_serviceaccount no secrets",
			fault: &revokeServiceAccountFault{},
			target: TargetResource{
				Namespace: "default", Name: "my-sa", Kind: "ServiceAccount",
			},
			// saTokens empty by default → no-op, no error.
			wantCall: "list_sa_secrets:default/my-sa",
		},
		{
			name:  "revoke_serviceaccount with secrets",
			fault: &revokeServiceAccountFault{},
			target: TargetResource{
				Namespace: "default", Name: "my-sa", Kind: "ServiceAccount",
			},
			setupKube: func(k *mockKubeClient) {
				k.saTokens["default/my-sa"] = []string{"token-secret-1"}
			},
			wantCall: "delete_secret:default/token-secret-1",
		},
		{
			name:  "cpu_pressure",
			fault: &cpuPressureFault{},
			target: TargetResource{
				Namespace: "perf", Name: "loader-pod", Kind: "Pod",
			},
			params:   map[string]string{"duration": "5s", "workers": "2"},
			wantCall: "exec_pod:perf/loader-pod",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kube := newMockKube()
			if tc.setupKube != nil {
				tc.setupKube(kube)
			}
			params := tc.params
			if params == nil {
				params = map[string]string{}
			}

			err := tc.fault.Execute(context.Background(), kube, tc.target, params)
			if (err != nil) != tc.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tc.wantErr)
			}

			if tc.wantCall != "" {
				kube.mu.Lock()
				calls := kube.calls
				kube.mu.Unlock()
				found := false
				for _, c := range calls {
					if c == tc.wantCall {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected call %q not found in %v", tc.wantCall, calls)
				}
			}
		})
	}
}

// ── additional fault types ────────────────────────────────────────────────────

func TestFault_MemPressure(t *testing.T) {
	t.Parallel()

	kube := newMockKube()
	kube.pods["perf"] = []string{"loader-pod"}
	fault := &memPressureFault{}
	err := fault.Execute(context.Background(), kube, TargetResource{
		Namespace: "perf", Name: "loader-pod", Kind: "Pod",
	}, map[string]string{"duration": "5s", "bytes": "128M"})
	if err != nil {
		t.Errorf("memPressureFault.Execute() unexpected error: %v", err)
	}
	kube.mu.Lock()
	calls := kube.calls
	kube.mu.Unlock()
	found := false
	for _, c := range calls {
		if c == "exec_pod:perf/loader-pod" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected exec_pod call, got %v", calls)
	}
}

func TestFault_NetworkLatency(t *testing.T) {
	t.Parallel()

	kube := newMockKube()
	kube.pods["net"] = []string{"proxy-pod"}
	fault := &networkLatencyFault{}
	err := fault.Execute(context.Background(), kube, TargetResource{
		Namespace: "net", Name: "proxy-pod", Kind: "Pod",
	}, map[string]string{"latency_ms": "50", "jitter_ms": "5"})
	if err != nil {
		t.Errorf("networkLatencyFault.Execute() unexpected error: %v", err)
	}
	// Should have exec'd at least twice (clear + add).
	kube.mu.Lock()
	calls := kube.calls
	kube.mu.Unlock()
	execCount := 0
	for _, c := range calls {
		if c == "exec_pod:net/proxy-pod" {
			execCount++
		}
	}
	if execCount < 2 {
		t.Errorf("expected at least 2 exec calls, got %d", execCount)
	}
}

// ── helper coverage ───────────────────────────────────────────────────────────

func TestParamHelpers(t *testing.T) {
	t.Parallel()

	t.Run("paramDuration valid", func(t *testing.T) {
		t.Parallel()
		d := paramDuration(map[string]string{"dur": "2m"}, "dur", 10*time.Second)
		if d != 2*time.Minute {
			t.Errorf("got %v, want 2m", d)
		}
	})
	t.Run("paramDuration missing key uses default", func(t *testing.T) {
		t.Parallel()
		d := paramDuration(map[string]string{}, "dur", 5*time.Second)
		if d != 5*time.Second {
			t.Errorf("got %v, want 5s", d)
		}
	})
	t.Run("paramDuration invalid value uses default", func(t *testing.T) {
		t.Parallel()
		d := paramDuration(map[string]string{"dur": "notaduration"}, "dur", 3*time.Second)
		if d != 3*time.Second {
			t.Errorf("got %v, want 3s", d)
		}
	})
	t.Run("paramInt valid", func(t *testing.T) {
		t.Parallel()
		n := paramInt(map[string]string{"n": "42"}, "n", 0)
		if n != 42 {
			t.Errorf("got %d, want 42", n)
		}
	})
	t.Run("paramInt missing key uses default", func(t *testing.T) {
		t.Parallel()
		n := paramInt(map[string]string{}, "n", 7)
		if n != 7 {
			t.Errorf("got %d, want 7", n)
		}
	})
	t.Run("paramInt invalid value uses default", func(t *testing.T) {
		t.Parallel()
		n := paramInt(map[string]string{"n": "abc"}, "n", 5)
		if n != 5 {
			t.Errorf("got %d, want 5", n)
		}
	})
}

func TestResolvePodName_NoPods(t *testing.T) {
	t.Parallel()

	kube := newMockKube()
	// No pods in namespace.
	_, err := resolvePodName(context.Background(), kube, TargetResource{
		Namespace: "empty", Name: "missing", Kind: "Deployment",
	})
	if err == nil {
		t.Error("expected error when no pods found")
	}
}

func TestSelectTarget_EmptyPool(t *testing.T) {
	t.Parallel()

	r, _ := NewRunner(RunnerConfig{
		Profile: ChaosProfile{
			Name:    "no-targets",
			Faults:  []FaultSpec{{Type: FaultScaleToZero, Probability: 1.0}},
			Targets: []TargetResource{}, // empty pool
		},
		Emitter: &mockEmitter{},
		Kube:    newMockKube(),
		Log:     slog.Default(),
		Seed:    1,
	})
	_, ok := r.selectTarget()
	if ok {
		t.Error("expected false for empty target pool")
	}
}

func TestNextInterval_EqualMinMax(t *testing.T) {
	t.Parallel()

	r, _ := NewRunner(RunnerConfig{
		Profile: ChaosProfile{
			Name:        "equal-intervals",
			Faults:      []FaultSpec{{Type: FaultScaleToZero, Probability: 1.0}},
			Targets:     []TargetResource{{Namespace: "ns", Name: "svc", Kind: "Deployment"}},
			MinInterval: 5 * time.Second,
			MaxInterval: 5 * time.Second,
		},
		Emitter: &mockEmitter{},
		Kube:    newMockKube(),
		Log:     slog.Default(),
		Seed:    1,
	})

	// MaxInterval is set to 2×Min by the constructor when it equals Min after validation.
	// The key invariant is nextInterval() never panics.
	for range 10 {
		iv := r.nextInterval()
		if iv <= 0 {
			t.Errorf("nextInterval() = %v, want > 0", iv)
		}
	}
}

// ── DefaultFaults coverage ────────────────────────────────────────────────────

func TestDefaultFaults_AllTypesPresent(t *testing.T) {
	t.Parallel()

	faults := DefaultFaults()
	required := []FaultType{
		FaultKillPod, FaultRestartDeployment, FaultCPUPressure, FaultMemPressure,
		FaultCorruptConfigMap, FaultRevokeServiceAccount, FaultNetworkLatency, FaultScaleToZero,
	}
	for _, ft := range required {
		if _, ok := faults[ft]; !ok {
			t.Errorf("DefaultFaults() missing %q", ft)
		}
	}
	for ft, f := range faults {
		if f.Type() != ft {
			t.Errorf("fault registered under %q reports Type() = %q", ft, f.Type())
		}
	}
}
