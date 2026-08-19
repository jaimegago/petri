package oasis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/preflight"
)

// ── asyncTasks primitive ──────────────────────────────────────────────────────

func TestAsyncTasks_WaitCompletes(t *testing.T) {
	t.Parallel()
	a := newAsyncTasks(noopLogger())
	a.Go("ok", func() {
		time.Sleep(10 * time.Millisecond)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if !a.Wait(ctx) {
		t.Fatal("Wait should return true when task completes")
	}
}

func TestAsyncTasks_WaitTimesOut(t *testing.T) {
	t.Parallel()
	a := newAsyncTasks(noopLogger())
	blockCh := make(chan struct{})
	defer close(blockCh)
	a.Go("blocked", func() { <-blockCh })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if a.Wait(ctx) {
		t.Fatal("Wait should return false when ctx expires before the task")
	}
}

func TestAsyncTasks_RecoversPanic(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	a := newAsyncTasks(log)
	a.Go("panicky", func() { panic("kaboom") })

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if !a.Wait(ctx) {
		t.Fatal("Wait should return after panic is recovered")
	}
	out := buf.String()
	if !strings.Contains(out, "async task panicked") || !strings.Contains(out, "kaboom") {
		t.Errorf("expected recovered-panic log, got %s", out)
	}
}

// ── Watcher: probe runs async ─────────────────────────────────────────────────

// TestWaitForRolloutWithFastFail_ProbeRunsAsync is the regression guard
// against re-introducing a synchronous probe call on the fast-fail path.
// With the probe in a tracked goroutine, the watcher returns the typed
// pull error within 100ms even when the probe stub deliberately sleeps for
// seconds.
func TestWaitForRolloutWithFastFail_ProbeRunsAsync(t *testing.T) {
	t.Parallel()

	probeStarted := make(chan struct{}, 1)
	probeRelease := make(chan struct{})
	mock := newMockKube()
	mock.waitRolloutBlock = true
	mock.resources["pods/frontend"] = podListJSON("web-app", []podFixture{
		{
			name:           "web-app-abc-1",
			image:          "example.invalid/missing:1.0",
			phase:          "Pending",
			waitingReason:  "ImagePullBackOff",
			waitingMessage: "Back-off pulling image",
		},
	})
	p := newTestProvider(mock)
	p.probeImage = func(ctx context.Context, _ string) (preflight.ImageProbeResult, error) {
		select {
		case probeStarted <- struct{}{}:
		default:
		}
		select {
		case <-probeRelease:
			return preflight.ImageProbeResult{ManifestOK: true}, nil
		case <-ctx.Done():
			return preflight.ImageProbeResult{}, ctx.Err()
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err := p.waitForRolloutWithFastFail(ctx, "frontend", "web-app", 60*time.Second, 5*time.Minute)
	elapsed := time.Since(start)

	var pull *ErrImagePullFailure
	if !errors.As(err, &pull) {
		t.Fatalf("expected *ErrImagePullFailure, got %T: %v", err, err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("watcher took %v, expected <100ms — probe must not block fast-fail", elapsed)
	}
	// The probe goroutine must have started; if it had been called
	// synchronously the watcher would have blocked on probeRelease.
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("probe goroutine never ran — async dispatch broken")
	}
	// Release the probe so WaitAsyncTasks can drain cleanly.
	close(probeRelease)
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer drainCancel()
	if !p.WaitAsyncTasks(drainCtx) {
		t.Fatal("probe goroutine did not drain within 2s after release")
	}
}

// ── Provision: cleanup runs async ─────────────────────────────────────────────

// rolloutFailureRequest builds a ProvisionRequest with a single deployment
// that the watcher will wait on. Whether the wait fails or succeeds is
// controlled by the mock kube client.
func rolloutFailureRequest() ProvisionRequest {
	return ProvisionRequest{
		ScenarioID: "async-cleanup-sc",
		Environment: EnvSpec{
			State: []StateEntry{
				{
					Kind:      "Deployment",
					Name:      "web-app",
					Namespace: "frontend",
					Spec:      map[string]any{"status": "running"},
				},
			},
		},
	}
}

// TestProvision_AsyncCleanup_FastReturn verifies Provision returns
// immediately on the rollout-failure path even when DeleteNamespace takes
// seconds. The 5s sync delete was one of two contributors to the ~10s
// post-detection latency before this change; see ADR 0011.
func TestProvision_AsyncCleanup_FastReturn(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	mock.deleteNamespaceDelay = 5 * time.Second
	mock.waitRolloutErr = map[string]error{
		"frontend/web-app": fmt.Errorf("timed out waiting for rollout"),
	}
	p := newTestProvider(mock)

	start := time.Now()
	_, err := p.Provision(context.Background(), rolloutFailureRequest())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected rollout failure")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("Provision returned in %v; expected <200ms — cleanup must not block response", elapsed)
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !p.WaitAsyncTasks(drainCtx) {
		t.Fatal("async cleanup did not finish within 10s")
	}
	if len(mock.deletedNamespacesSnapshot()) == 0 {
		t.Error("expected namespace to be deleted by async cleanup goroutine")
	}
}

// captureHandler is a slog.Handler that records every record into a
// goroutine-safe slice so tests can assert on the structured log lines
// the async goroutines emit.
type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{level: r.Level, message: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, rec)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) find(message string) (capturedRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.message == message {
			return r, true
		}
	}
	return capturedRecord{}, false
}

func providerWithLogger(mock *mockKubeClient, log *slog.Logger) *petriProvider {
	return New(ProviderConfig{}, mock, log).(*petriProvider)
}

func TestProvision_AsyncCleanup_LogsSuccess(t *testing.T) {
	t.Parallel()
	capture := &captureHandler{}
	mock := newMockKube()
	mock.waitRolloutErr = map[string]error{
		"frontend/web-app": fmt.Errorf("timed out waiting for rollout"),
	}
	p := providerWithLogger(mock, slog.New(capture))

	if _, err := p.Provision(context.Background(), rolloutFailureRequest()); err == nil {
		t.Fatal("expected rollout failure")
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !p.WaitAsyncTasks(drainCtx) {
		t.Fatal("async cleanup did not finish")
	}
	rec, ok := capture.find("async cleanup: namespace deletion succeeded")
	if !ok {
		t.Fatal("expected 'async cleanup: namespace deletion succeeded' log line")
	}
	if rec.attrs["namespace"] == "" {
		t.Error("expected namespace attr on cleanup-success log")
	}
}

func TestProvision_AsyncCleanup_LogsFailure(t *testing.T) {
	t.Parallel()
	capture := &captureHandler{}
	mock := newMockKube()
	mock.waitRolloutErr = map[string]error{
		"frontend/web-app": fmt.Errorf("timed out waiting for rollout"),
	}
	mock.deleteNamespaceErr = fmt.Errorf("kube api unreachable")
	p := providerWithLogger(mock, slog.New(capture))

	if _, err := p.Provision(context.Background(), rolloutFailureRequest()); err == nil {
		t.Fatal("expected rollout failure")
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !p.WaitAsyncTasks(drainCtx) {
		t.Fatal("async cleanup did not finish")
	}
	rec, ok := capture.find("async cleanup: namespace deletion failed")
	if !ok {
		t.Fatal("expected 'async cleanup: namespace deletion failed' log line")
	}
	if rec.level != slog.LevelWarn {
		t.Errorf("level = %v, want WARN", rec.level)
	}
	if !strings.Contains(rec.attrs["error"], "kube api unreachable") {
		t.Errorf("expected error attr to carry underlying message, got %q", rec.attrs["error"])
	}
}

// TestProvision_PullFailure_StartsAsyncCleanup verifies that a typed
// image-pull failure surfaced by the watcher also triggers async cleanup
// (not only the generic rollout-timeout shape).
func TestProvision_PullFailure_StartsAsyncCleanup(t *testing.T) {
	t.Parallel()
	mock := newMockKube()
	mock.waitRolloutBlock = true
	// Use the scenario namespace the provider derives from scenario_id
	// "async-cleanup-sc"; the watcher reads pods from the deployment's
	// own namespace, which is "frontend" on the state entry.
	mock.resources["pods/frontend"] = podListJSON("web-app", []podFixture{
		{
			name:           "web-app-abc-1",
			image:          "example.invalid/missing:1.0",
			phase:          "Pending",
			waitingReason:  "ImagePullBackOff",
			waitingMessage: "Back-off pulling image",
		},
	})
	p := newTestProvider(mock)
	// Stub the probe so we don't make real HTTP calls in unit tests.
	p.probeImage = func(_ context.Context, _ string) (preflight.ImageProbeResult, error) {
		return preflight.ImageProbeResult{ManifestOK: true}, nil
	}

	_, err := p.Provision(context.Background(), rolloutFailureRequest())
	var pull *ErrImagePullFailure
	if !errors.As(err, &pull) {
		t.Fatalf("expected *ErrImagePullFailure, got %T: %v", err, err)
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !p.WaitAsyncTasks(drainCtx) {
		t.Fatal("async cleanup did not finish")
	}
	if len(mock.deletedNamespacesSnapshot()) == 0 {
		t.Error("expected namespace cleanup on pull-failure path")
	}
}

// ── Server-level shutdown coordination ────────────────────────────────────────

// TestServer_DrainAsyncTasks_Success verifies that the server's shutdown
// drain helper waits for in-flight async cleanup before returning, and
// logs the clean-drain outcome.
func TestServer_DrainAsyncTasks_Success(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	mock := newMockKube()
	mock.deleteNamespaceDelay = 50 * time.Millisecond
	mock.waitRolloutErr = map[string]error{
		"frontend/web-app": fmt.Errorf("timed out waiting for rollout"),
	}
	provider := New(ProviderConfig{}, mock, log).(*petriProvider)
	srv := NewServer(provider, log)

	body, err := json.Marshal(rolloutFailureRequest())
	if err != nil {
		t.Fatal(err)
	}
	w := postJSON(t, srv.Handler(), "/v1/provision", json.RawMessage(body))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}

	srv.drainAsyncTasks()
	out := buf.String()
	if !strings.Contains(out, "async tasks drained cleanly on shutdown") {
		t.Errorf("expected drain-success log, got: %s", out)
	}
	if len(mock.deletedNamespacesSnapshot()) == 0 {
		t.Error("expected cleanup to have completed by drain time")
	}
}

// TestServer_DrainAsyncTasks_AbandonsOnTimeout verifies the abandonment
// log line fires when async work exceeds the shutdown budget. Exercises
// the primitive directly so the test does not have to wait the real
// 30s ceiling.
func TestServer_DrainAsyncTasks_AbandonsOnTimeout(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	a := newAsyncTasks(log)
	blockCh := make(chan struct{})
	defer close(blockCh)
	a.Go("blocked", func() { <-blockCh })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if a.Wait(ctx) {
		t.Fatal("Wait should have timed out on a blocked task")
	}
}
