package oasis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// healthyDeploymentEntries builds N StateEntry items with kind=Deployment and
// status=running, each in its own namespace so the mock's pod-list lookup is
// keyed predictably.
func healthyDeploymentEntries(n int) []StateEntry {
	out := make([]StateEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, StateEntry{
			Kind:      "Deployment",
			Name:      fmt.Sprintf("app-%d", i),
			Namespace: fmt.Sprintf("ns-%d", i),
			Spec:      map[string]any{"status": "running"},
		})
	}
	return out
}

// TestWaitForHealthyDeployments_RunsInParallel asserts that three deployments
// each taking ~rolloutDelay to roll out complete in roughly that single
// duration, not the sum. This is the headline parallel-wait property: a
// scenario with N healthy deployments must not block the client for N ×
// rolloutTimeout.
func TestWaitForHealthyDeployments_RunsInParallel(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.waitRolloutDelay = 200 * time.Millisecond
	p := newTestProvider(mock)

	entries := healthyDeploymentEntries(3)
	start := time.Now()
	if err := p.waitForHealthyDeployments(context.Background(), entries, "ignored"); err != nil {
		t.Fatalf("waitForHealthyDeployments error: %v", err)
	}
	elapsed := time.Since(start)

	// Sequential would be ~600ms; parallel should land near the slowest single
	// rollout. Allow generous headroom for CI scheduling jitter while still
	// catching a regression to serial execution.
	if elapsed > 400*time.Millisecond {
		t.Errorf("waitForHealthyDeployments took %v; expected ~200ms (parallel), not ~600ms (serial)", elapsed)
	}
}

// TestWaitForHealthyDeployments_ImagePullFailureWins asserts the function
// returns a typed *ErrImagePullFailure within a small budget of the watcher
// firing — not after waiting for sibling deployments to complete.
func TestWaitForHealthyDeployments_ImagePullFailureWins(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	// Sibling deployments block on a long rollout so we can prove the
	// function returned without waiting for them. They must respond to ctx
	// cancellation (default block path does) — otherwise the test would
	// stall on shutdown.
	mock.waitRolloutBlock = true
	// Plant the pull-failure pod listing only on the second deployment's
	// namespace; the other two see an empty pod list.
	mock.resources["pods/ns-1"] = podListJSON("app-1", []podFixture{
		{
			name:           "app-1-abc-1",
			image:          "example.invalid/broken:1.0",
			phase:          "Pending",
			waitingReason:  "ImagePullBackOff",
			waitingMessage: "Back-off pulling image",
		},
	})
	p := newTestProvider(mock)

	entries := healthyDeploymentEntries(3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := p.waitForHealthyDeployments(ctx, entries, "ignored")
	elapsed := time.Since(start)

	var pull *ErrImagePullFailure
	if !errors.As(err, &pull) {
		t.Fatalf("expected *ErrImagePullFailure, got %T: %v", err, err)
	}
	if pull.Namespace != "ns-1" {
		t.Errorf("pull.Namespace = %q, want ns-1", pull.Namespace)
	}
	// Watcher fires on the immediate first scan. Allow generous headroom for
	// scheduler jitter, but assert we did NOT wait the full rollout timeout.
	if elapsed > 2*time.Second {
		t.Errorf("expected fast-fail in <2s, got %v — function blocked on sibling waits", elapsed)
	}
}

// TestWaitForHealthyDeployments_SiblingsCancelOnFailure asserts that when one
// deployment fast-fails, the goroutines for sibling deployments are
// cancelled and exit promptly — the mock's WaitForRollout invocations
// terminate (via ctx cancellation) rather than running to completion.
func TestWaitForHealthyDeployments_SiblingsCancelOnFailure(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.waitRolloutBlock = true
	mock.resources["pods/ns-1"] = podListJSON("app-1", []podFixture{
		{
			name:           "app-1-abc-1",
			image:          "example.invalid/broken:1.0",
			phase:          "Pending",
			waitingReason:  "ImagePullBackOff",
			waitingMessage: "Back-off pulling image",
		},
	})
	p := newTestProvider(mock)

	entries := healthyDeploymentEntries(3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- p.waitForHealthyDeployments(ctx, entries, "ignored")
	}()

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForHealthyDeployments did not return within 5s — siblings not cancelled")
	}

	// Sibling WaitForRollout calls must have unblocked (their goroutines
	// returned). Give them a short grace window for ctx propagation.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mock.mu.Lock()
		inflight := mock.waitRolloutInflight
		mock.mu.Unlock()
		if inflight == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mock.mu.Lock()
	inflight := mock.waitRolloutInflight
	mock.mu.Unlock()
	t.Fatalf("expected sibling WaitForRollout goroutines to exit after fast-fail, still %d inflight", inflight)
}

// TestWaitForHealthyDeployments_SimultaneousTimeoutsReturnSingleDeployment
// asserts the parallel-wait first-error-wins contract: two deployments both
// failing produces an *ErrRolloutTimeout listing exactly one deployment
// (whichever the errgroup recorded first). The other failure is visible only
// via the per-deployment WARN log line.
func TestWaitForHealthyDeployments_SimultaneousTimeoutsReturnSingleDeployment(t *testing.T) {
	t.Parallel()

	capture := &captureHandler{}
	mock := newMockKube()
	mock.waitRolloutDelay = 50 * time.Millisecond
	mock.waitRolloutErr = map[string]error{
		"ns-0/app-0": fmt.Errorf("timed out waiting for rollout"),
		"ns-1/app-1": fmt.Errorf("timed out waiting for rollout"),
	}
	p := providerWithLogger(mock, slog.New(capture))

	err := p.waitForHealthyDeployments(context.Background(), healthyDeploymentEntries(2), "ignored")
	if err == nil {
		t.Fatal("expected error from waitForHealthyDeployments")
	}
	// Image-pull failures must not appear; this is a generic timeout case.
	var pull *ErrImagePullFailure
	if errors.As(err, &pull) {
		t.Fatalf("must not return *ErrImagePullFailure, got %v", err)
	}
	var timeout *ErrRolloutTimeout
	if !errors.As(err, &timeout) {
		t.Fatalf("expected *ErrRolloutTimeout, got %T: %v", err, err)
	}
	if len(timeout.Deployments) != 1 {
		t.Errorf("Deployments = %v; expected exactly one entry (first-error-wins)", timeout.Deployments)
	}
	if got := timeout.Deployments[0]; got != "ns-0/app-0" && got != "ns-1/app-1" {
		t.Errorf("Deployments[0] = %q; want ns-0/app-0 or ns-1/app-1", got)
	}
	// Historical error phrasing preserved for log-grep compatibility.
	if !strings.Contains(err.Error(), "deployments did not become ready within") {
		t.Errorf("error message lost historical phrasing: %q", err.Error())
	}

	// Each failing deployment must emit its own "deployment rollout failed"
	// WARN line so operators can see all failures in run.log even though
	// only one is returned as the typed error.
	capture.mu.Lock()
	defer capture.mu.Unlock()
	seen := map[string]int{"ns-0/app-0": 0, "ns-1/app-1": 0}
	for _, rec := range capture.records {
		if rec.message != "deployment rollout failed" {
			continue
		}
		key := rec.attrs["namespace"] + "/" + rec.attrs["deployment"]
		seen[key]++
	}
	for k, v := range seen {
		if v == 0 {
			t.Errorf("expected a 'deployment rollout failed' WARN for %s; none seen", k)
		}
	}
}

// TestWaitForHealthyDeployments_RespectsConcurrencyCap asserts that with many
// deployments scheduled simultaneously the parallel wait never exceeds the
// rolloutWaitConcurrency limit. Prevents a future N-deployment scenario from
// spawning N kubectl subprocesses and overwhelming the kube API.
func TestWaitForHealthyDeployments_RespectsConcurrencyCap(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	// Per-call delay must be large enough that all goroutines released by
	// the semaphore overlap in time — otherwise the cap is enforced
	// trivially by speed of execution.
	mock.waitRolloutDelay = 100 * time.Millisecond
	p := newTestProvider(mock)

	entries := healthyDeploymentEntries(16)
	if err := p.waitForHealthyDeployments(context.Background(), entries, "ignored"); err != nil {
		t.Fatalf("waitForHealthyDeployments error: %v", err)
	}

	if got := mock.maxConcurrentWaitRollout(); got > rolloutWaitConcurrency {
		t.Errorf("observed concurrency %d exceeds cap %d", got, rolloutWaitConcurrency)
	}
}

// TestWaitForHealthyDeployments_SkipsNonRunningStatuses preserves the
// existing skip semantics: only deployments with status:"running" are waited
// on; intentionally unhealthy states are passed through.
func TestWaitForHealthyDeployments_SkipsNonRunningStatuses(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	p := newTestProvider(mock)

	entries := []StateEntry{
		{Kind: "Deployment", Name: "broken", Namespace: "ns-x", Spec: map[string]any{"status": "CrashLoopBackOff"}},
		{Kind: "Deployment", Name: "no-status", Namespace: "ns-y", Spec: map[string]any{}},
		{Kind: "Service", Name: "svc", Namespace: "ns-z", Spec: map[string]any{"status": "running"}},
	}
	if err := p.waitForHealthyDeployments(context.Background(), entries, "ignored"); err != nil {
		t.Fatalf("expected nil error when no entries are eligible, got %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.waitRolloutCalls) != 0 {
		t.Errorf("expected zero WaitForRollout calls, got %v", mock.waitRolloutCalls)
	}
}
