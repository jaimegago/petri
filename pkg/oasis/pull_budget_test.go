package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The budget tests drive watchPullBudget directly rather than going through
// waitForRolloutWithFastFail. That is deliberate: the wrapper hardcodes
// pullWatchInterval at 3s, and a test that had to wait multiple 3s ticks to
// observe a budget decision would add ~30s to the unit suite to prove nothing
// the shorter interval does not already prove. The wrapper's own wiring is
// covered by the existing tests in pull_watcher_test.go.

const (
	testTick = 10 * time.Millisecond
	// testGenerous is any budget the test does not want to fire.
	testGenerous = 60 * time.Second
)

// TestWatchPullBudget_PullDoesNotConsumeRolloutBudget is the regression guard
// for the defect this whole change exists for: on a cold node the first image
// pull exceeded the 60s rollout deadline and the scenario was failed as a
// rollout timeout. Time spent pulling must be charged to the pull budget and
// to nothing else.
func TestWatchPullBudget_PullDoesNotConsumeRolloutBudget(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["pods/production"] = podListJSON("payment-gateway", []podFixture{
		{
			name:          "payment-gateway-abc123-xyz",
			image:         "registry.k8s.io/nginx-slim:0.27",
			phase:         "Pending",
			waitingReason: "ContainerCreating",
			// imageID empty: kubelet has not resolved the image yet.
		},
	})
	p := newTestProvider(mock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A rollout budget far too small to survive the pull, and a pull budget
	// that will fire. If pull time were charged to the rollout budget — the
	// old behaviour — this would come back ErrRolloutTimeout almost at once.
	err := p.watchPullBudget(ctx, "production", "payment-gateway", testTick,
		200*time.Millisecond, 50*time.Millisecond)

	var rollout *ErrRolloutTimeout
	if errors.As(err, &rollout) {
		t.Fatalf("pull time was charged to the rollout budget: %v", err)
	}
	var pullTimeout *ErrImagePullTimeout
	if !errors.As(err, &pullTimeout) {
		t.Fatalf("expected *ErrImagePullTimeout, got %T: %v", err, err)
	}
	if pullTimeout.Namespace != "production" || pullTimeout.Deployment != "payment-gateway" {
		t.Errorf("wrong workload identified: %s/%s", pullTimeout.Namespace, pullTimeout.Deployment)
	}
	if len(pullTimeout.Images) != 1 || pullTimeout.Images[0] != "registry.k8s.io/nginx-slim:0.27" {
		t.Errorf("expected the pulling image to be named, got %v", pullTimeout.Images)
	}
}

// TestWatchPullBudget_StuckRolloutStillFailsFast is the other half, and the
// one that matters most: the change promised to weaken nothing. A Deployment
// whose images are resolved and which simply is not converging must still hit
// its own budget, unextended by the pull budget beside it.
func TestWatchPullBudget_StuckRolloutStillFailsFast(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["pods/production"] = podListJSON("payment-gateway", []podFixture{
		{
			name:          "payment-gateway-abc123-xyz",
			image:         "registry.k8s.io/nginx-slim:0.27",
			imageID:       "sha256:deadbeef",
			phase:         "Running",
			waitingReason: "CrashLoopBackOff",
		},
	})
	p := newTestProvider(mock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := p.watchPullBudget(ctx, "production", "payment-gateway", testTick,
		testGenerous, 100*time.Millisecond)
	elapsed := time.Since(start)

	var rollout *ErrRolloutTimeout
	if !errors.As(err, &rollout) {
		t.Fatalf("expected *ErrRolloutTimeout, got %T: %v", err, err)
	}
	// The generous pull budget must not have delayed this at all.
	if elapsed > 2*time.Second {
		t.Errorf("stuck rollout took %s to fail; the pull budget leaked into it", elapsed)
	}
	if rollout.Timeout != 100*time.Millisecond {
		t.Errorf("reported timeout %s, want the rollout budget", rollout.Timeout)
	}
}

// TestWatchPullBudget_UnschedulablePodChargesRolloutBudget guards the narrow
// definition of imagePullProgressReasons. A pod that cannot be scheduled
// reports no container status at all. Treating that absence as "maybe
// pulling" would hand every unschedulable Deployment the pull budget on top
// of its own, which is exactly the fast-failure regression this change must
// not introduce.
func TestWatchPullBudget_UnschedulablePodChargesRolloutBudget(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["pods/production"] = podListJSON("payment-gateway", []podFixture{
		{
			name:  "payment-gateway-abc123-xyz",
			image: "registry.k8s.io/nginx-slim:0.27",
			phase: "Pending",
			// No waitingReason: no containerStatuses are reported.
		},
	})
	p := newTestProvider(mock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := p.watchPullBudget(ctx, "production", "payment-gateway", testTick,
		testGenerous, 100*time.Millisecond)

	var rollout *ErrRolloutTimeout
	if !errors.As(err, &rollout) {
		t.Fatalf("expected *ErrRolloutTimeout for an unschedulable pod, got %T: %v", err, err)
	}
}

// TestWatchPullBudget_PullFailureStillFastFails confirms the pre-existing
// fast-fail is untouched by the accounting added around it. A kubelet-
// reported pull failure is terminal and must not wait out either budget.
func TestWatchPullBudget_PullFailureStillFastFails(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["pods/production"] = podListJSON("payment-gateway", []podFixture{
		{
			name:           "payment-gateway-abc123-xyz",
			image:          "example.invalid/missing:1.0",
			phase:          "Pending",
			waitingReason:  "ImagePullBackOff",
			waitingMessage: "Back-off pulling image",
		},
	})
	p := newTestProvider(mock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := p.watchPullBudget(ctx, "production", "payment-gateway", testTick,
		testGenerous, testGenerous)
	elapsed := time.Since(start)

	var pull *ErrImagePullFailure
	if !errors.As(err, &pull) {
		t.Fatalf("expected *ErrImagePullFailure, got %T: %v", err, err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("pull failure took %s to surface; it must not wait out a budget", elapsed)
	}
}

// TestWatchPullBudget_CancelledContextReturnsNil pins the contract
// waitForRolloutWithFastFail relies on to distinguish "the watcher decided
// something" from "the parent went away".
func TestWatchPullBudget_CancelledContextReturnsNil(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["pods/production"] = podListJSON("payment-gateway", []podFixture{
		{name: "payment-gateway-abc123-xyz", image: "img:1", phase: "Pending", waitingReason: "ContainerCreating"},
	})
	p := newTestProvider(mock)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- p.watchPullBudget(ctx, "production", "payment-gateway", testTick, testGenerous, testGenerous)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil on cancellation, got %T: %v", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not return after cancellation")
	}
}

func TestIsPullInProgress(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		c    parsedContainer
		want bool
	}{
		{"pulling: ContainerCreating with no imageID", parsedContainer{reason: "ContainerCreating"}, true},
		{"pulling: PodInitializing with no imageID", parsedContainer{reason: "PodInitializing"}, true},
		{
			"not pulling: imageID resolved, so a ContainerCreating wait is something else",
			parsedContainer{reason: "ContainerCreating", imageID: "sha256:abc"},
			false,
		},
		{"not pulling: no waiting reason at all (unschedulable)", parsedContainer{}, false},
		{"not pulling: crashlooping", parsedContainer{reason: "CrashLoopBackOff"}, false},
		{"not pulling: a pull failure is terminal, not progress", parsedContainer{reason: "ImagePullBackOff"}, false},
		{"not pulling: running", parsedContainer{imageID: "sha256:abc"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPullInProgress(tc.c); got != tc.want {
				t.Errorf("isPullInProgress(%+v) = %v, want %v", tc.c, got, tc.want)
			}
		})
	}
}

// TestErrImagePullTimeout_DoesNotReuseRolloutPhrasing guards the log-parser
// contract. Run.log consumers count rollout failures by grepping for
// "deployments did not become ready within"; a pull timeout that reused that
// string would refill the bucket this type was split out of.
func TestErrImagePullTimeout_DoesNotReuseRolloutPhrasing(t *testing.T) {
	t.Parallel()

	e := &ErrImagePullTimeout{
		Timeout:    5 * time.Minute,
		Namespace:  "production",
		Deployment: "payment-gateway",
		Images:     []string{"registry.k8s.io/nginx-slim:0.27"},
	}
	got := e.Error()
	if strings.Contains(got, "deployments did not become ready within") {
		t.Errorf("pull timeout reuses the rollout phrasing: %q", got)
	}
	for _, want := range []string{"image pull did not complete within", "5m0s", "production/payment-gateway", "registry.k8s.io/nginx-slim:0.27"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing %q", got, want)
		}
	}

	bare := &ErrImagePullTimeout{Timeout: time.Minute, Namespace: "ns", Deployment: "d"}
	if strings.Contains(bare.Error(), "still pulling") {
		t.Errorf("empty Images should omit the parenthetical, got %q", bare.Error())
	}
}

func TestAsyncCleanupReason_ImagePullTimeout(t *testing.T) {
	t.Parallel()

	err := &ErrImagePullTimeout{Timeout: time.Minute, Namespace: "ns", Deployment: "d"}
	if got := asyncCleanupReason(err); got != "image-pull-timeout" {
		t.Errorf("asyncCleanupReason = %q, want %q", got, "image-pull-timeout")
	}
}

// TestProvisionError_ImagePullTimeoutMapsTo502 pins the classification half of
// the change: the new condition reaches the HTTP layer as a recognised typed
// error rather than defaulting into the generic 500 envelope.
//
// It shares 502 with ErrImagePullFailure on purpose — both are substrate-side
// and the runner already routes 502 that way — so the reason field is what
// carries the distinction, and this test guards that rather than the code.
func TestProvisionError_ImagePullTimeoutMapsTo502(t *testing.T) {
	t.Parallel()

	mock := &mockOASISProvider{err: &ErrImagePullTimeout{
		Timeout:    5 * time.Minute,
		Namespace:  "production",
		Deployment: "payment-gateway",
		Images:     []string{"registry.k8s.io/nginx-slim:0.27"},
	}}
	srv := NewServer(mock, noopLogger())
	w := postJSON(t, srv.Handler(), "/v1/provision", ProvisionRequest{})

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	var body imagePullTimeoutResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Reason != "ImagePullTimeout" {
		t.Errorf("reason = %q, want %q — this is what distinguishes it from a pull failure", body.Reason, "ImagePullTimeout")
	}
	if body.Namespace != "production" || body.Deployment != "payment-gateway" {
		t.Errorf("workload = %s/%s", body.Namespace, body.Deployment)
	}
	if body.RetryAfterSeconds <= 0 {
		t.Errorf("retry_after_seconds = %d; a partial pull is resumable and must carry a hint", body.RetryAfterSeconds)
	}
}

// TestWaitForRolloutWithFastFail_UsesSeparateBudgets confirms the wrapper
// actually threads both budgets through, rather than the accounting being
// correct in a function nothing calls with the right arguments.
func TestWaitForRolloutWithFastFail_UsesSeparateBudgets(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.waitRolloutBlock = true // never completes; the watcher must decide
	mock.resources["pods/production"] = podListJSON("payment-gateway", []podFixture{
		{
			name:          "payment-gateway-abc123-xyz",
			image:         "registry.k8s.io/nginx-slim:0.27",
			phase:         "Pending",
			waitingReason: "ContainerCreating",
		},
	})
	p := newTestProvider(mock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Rollout budget of 1s against a pod that is pulling: the old single
	// deadline would have failed this as a rollout timeout. pullWatchInterval
	// is 3s, so the first accounting tick lands after the rollout budget has
	// nominally passed — which is the point.
	err := p.waitForRolloutWithFastFail(ctx, "production", "payment-gateway",
		1*time.Second, 4*time.Second)

	var rollout *ErrRolloutTimeout
	if errors.As(err, &rollout) {
		t.Fatalf("pulling pod failed against the rollout budget: %v", err)
	}
	var pullTimeout *ErrImagePullTimeout
	if !errors.As(err, &pullTimeout) {
		t.Fatalf("expected *ErrImagePullTimeout, got %T: %v", err, err)
	}
}
