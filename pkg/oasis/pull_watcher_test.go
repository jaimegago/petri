package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// podListJSON builds a kubectl-style pod list JSON for the given pods,
// each owned by a ReplicaSet of the given Deployment.
func podListJSON(deployment string, pods []podFixture) string {
	type owner struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	type meta struct {
		Name            string  `json:"name"`
		OwnerReferences []owner `json:"ownerReferences"`
	}
	type container struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	}
	type waiting struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	type state struct {
		Waiting *waiting `json:"waiting,omitempty"`
	}
	type containerStatus struct {
		Name    string `json:"name"`
		Image   string `json:"image,omitempty"`
		ImageID string `json:"imageID,omitempty"`
		State   state  `json:"state"`
	}
	type spec struct {
		Containers []container `json:"containers"`
	}
	type status struct {
		Phase             string            `json:"phase,omitempty"`
		ContainerStatuses []containerStatus `json:"containerStatuses,omitempty"`
	}
	type pod struct {
		Metadata meta   `json:"metadata"`
		Spec     spec   `json:"spec"`
		Status   status `json:"status"`
	}
	out := struct {
		Items []pod `json:"items"`
	}{}

	for _, p := range pods {
		rsName := deployment + "-abc123"
		if p.ownerOverride != "" {
			rsName = p.ownerOverride
		}
		entry := pod{
			Metadata: meta{
				Name: p.name,
				OwnerReferences: []owner{
					{Kind: "ReplicaSet", Name: rsName},
				},
			},
			Spec: spec{Containers: []container{{Name: "app", Image: p.image}}},
		}
		if p.waitingReason != "" || p.phase != "" || p.imageID != "" {
			cs := containerStatus{Name: "app", Image: p.image, ImageID: p.imageID}
			if p.waitingReason != "" {
				cs.State.Waiting = &waiting{Reason: p.waitingReason, Message: p.waitingMessage}
			}
			entry.Status = status{Phase: p.phase, ContainerStatuses: []containerStatus{cs}}
		}
		out.Items = append(out.Items, entry)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

type podFixture struct {
	name           string
	image          string
	phase          string
	waitingReason  string
	waitingMessage string
	// imageID mirrors kubelet's runtime-reported image identifier. Empty
	// means "not yet resolved on the node", which is what separates a pod
	// that is still pulling from one that is waiting on something else.
	imageID string
	// ownerOverride lets a test plant a pod with an owner that does NOT
	// match the deployment prefix (cross-contamination test).
	ownerOverride string
}

func TestWaitForRolloutWithFastFail_ImagePullDetected(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.waitRolloutBlock = true // block until ctx cancelled
	mock.resources["pods/frontend"] = podListJSON("web-app", []podFixture{
		{
			name:           "web-app-abc123-xyz",
			image:          "example.invalid/missing:1.0",
			phase:          "Pending",
			waitingReason:  "ImagePullBackOff",
			waitingMessage: "Back-off pulling image \"example.invalid/missing:1.0\"",
		},
	})
	p := newTestProvider(mock)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := p.waitForRolloutWithFastFail(ctx, "frontend", "web-app", 60*time.Second, 5*time.Minute)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pull *ErrImagePullFailure
	if !errors.As(err, &pull) {
		t.Fatalf("expected *ErrImagePullFailure, got %T: %v", err, err)
	}
	if pull.Image != "example.invalid/missing:1.0" {
		t.Errorf("Image = %q, want %q", pull.Image, "example.invalid/missing:1.0")
	}
	if pull.Namespace != "frontend" {
		t.Errorf("Namespace = %q, want %q", pull.Namespace, "frontend")
	}
	if pull.Pod != "web-app-abc123-xyz" {
		t.Errorf("Pod = %q, want %q", pull.Pod, "web-app-abc123-xyz")
	}
	if pull.Reason != "ImagePullBackOff" {
		t.Errorf("Reason = %q, want %q", pull.Reason, "ImagePullBackOff")
	}
	// Should fast-fail well under the 60s rollout timeout. The watcher does
	// an immediate first scan, so detection should be near-instant.
	if elapsed > 5*time.Second {
		t.Errorf("fast-fail took %v, expected <5s", elapsed)
	}
}

func TestWaitForRolloutWithFastFail_DoesNotBlockOnSlowRollout(t *testing.T) {
	t.Parallel()

	// Regression test: the watcher-fires-first branch must return
	// immediately after detecting a pull failure. Previously the code
	// drained rolloutCh, which made this function block until `kubectl
	// rollout status` exited — empirically ~5s after ctx cancellation —
	// adding a directly user-visible gap between detection and the HTTP
	// 502 response.
	mock := newMockKube()
	mock.waitRolloutDelay = 5 * time.Second
	mock.waitRolloutIgnoreCancel = true // simulate kubectl ignoring cancel
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := p.waitForRolloutWithFastFail(ctx, "frontend", "web-app", 60*time.Second, 5*time.Minute)
	elapsed := time.Since(start)

	var pull *ErrImagePullFailure
	if !errors.As(err, &pull) {
		t.Fatalf("expected *ErrImagePullFailure, got %T: %v", err, err)
	}
	// The watcher's immediate-scan path detects the failure on the first
	// list call; the function should return well under a second. The slow
	// rollout (5s, ignoring cancel) is still running in the background but
	// must not block our return.
	if elapsed > 500*time.Millisecond {
		t.Errorf("expected fast return (<500ms) on pull-failure path, got %v — function is blocked on the slow rollout goroutine", elapsed)
	}
}

func TestWaitForRolloutWithFastFail_RolloutTimeoutNotShortCircuited(t *testing.T) {
	t.Parallel()

	// Pod is Running but failing a liveness probe — no image-pull events.
	// The rollout error must surface as ErrRolloutTimeout, not ErrImagePullFailure.
	mock := newMockKube()
	mock.waitRolloutDelay = 200 * time.Millisecond
	mock.waitRolloutErr = map[string]error{
		"frontend/web-app": fmt.Errorf("timed out waiting for rollout"),
	}
	mock.resources["pods/frontend"] = podListJSON("web-app", []podFixture{
		{
			name:  "web-app-abc123-xyz",
			image: "registry.k8s.io/nginx-slim:0.27",
			phase: "Running",
		},
	})
	p := newTestProvider(mock)

	err := p.waitForRolloutWithFastFail(context.Background(), "frontend", "web-app", 1*time.Second, 5*time.Minute)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pull *ErrImagePullFailure
	if errors.As(err, &pull) {
		t.Fatalf("must not return *ErrImagePullFailure for a non-pull rollout failure, got %v", err)
	}
	var timeout *ErrRolloutTimeout
	if !errors.As(err, &timeout) {
		t.Fatalf("expected *ErrRolloutTimeout, got %T: %v", err, err)
	}
	if len(timeout.Deployments) != 1 || timeout.Deployments[0] != "frontend/web-app" {
		t.Errorf("Deployments = %v, want [frontend/web-app]", timeout.Deployments)
	}
}

func TestWaitForRolloutWithFastFail_Success(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	// WaitForRollout returns nil immediately → success.
	mock.resources["pods/frontend"] = podListJSON("web-app", []podFixture{
		{name: "web-app-abc-1", image: "registry.k8s.io/nginx-slim:0.27", phase: "Running"},
	})
	p := newTestProvider(mock)

	if err := p.waitForRolloutWithFastFail(context.Background(), "frontend", "web-app", 60*time.Second, 5*time.Minute); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestWaitForRolloutWithFastFail_ContextCancellation(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.waitRolloutBlock = true
	mock.resources["pods/frontend"] = podListJSON("web-app", []podFixture{
		{name: "web-app-abc-1", image: "registry.k8s.io/nginx-slim:0.27", phase: "Pending"},
	})
	p := newTestProvider(mock)

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- p.waitForRolloutWithFastFail(ctx, "frontend", "web-app", 60*time.Second, 5*time.Minute)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-doneCh:
		// Should return whatever the cancelled rollout produced (wrapped
		// as ErrRolloutTimeout because the rollout returned ctx.Err()).
		// Just verify it returned within the cancellation budget.
		_ = err
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not stop within 3s of ctx cancel")
	}
}

func TestParsePodsForDeployment_FiltersByOwner(t *testing.T) {
	t.Parallel()

	raw := podListJSON("web-app", []podFixture{
		{name: "web-app-abc-1", image: "img:1", waitingReason: "ImagePullBackOff"},
		{name: "other-pod", image: "img:2", waitingReason: "ImagePullBackOff", ownerOverride: "other-app-xyz"},
	})

	pods, err := parsePodsForDeployment(raw, "web-app")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod (owned by web-app-*), got %d", len(pods))
	}
	if pods[0].name != "web-app-abc-1" {
		t.Errorf("got pod %q, want web-app-abc-1", pods[0].name)
	}
}

func TestParsePodsForDeployment_CarriesContainerState(t *testing.T) {
	t.Parallel()

	raw := podListJSON("web-app", []podFixture{
		{
			name:           "web-app-abc-1",
			image:          "registry.k8s.io/nginx:1.27",
			waitingReason:  "ImagePullBackOff",
			waitingMessage: "Back-off pulling",
		},
	})
	pods, err := parsePodsForDeployment(raw, "web-app")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(pods) != 1 || len(pods[0].containers) != 1 {
		t.Fatalf("unexpected shape: %+v", pods)
	}
	c := pods[0].containers[0]
	if c.image != "registry.k8s.io/nginx:1.27" {
		t.Errorf("image = %q", c.image)
	}
	if c.reason != "ImagePullBackOff" {
		t.Errorf("reason = %q", c.reason)
	}
	if c.message != "Back-off pulling" {
		t.Errorf("message = %q", c.message)
	}
}

func TestIsImagePullReason(t *testing.T) {
	t.Parallel()

	pull := []string{
		"ImagePullBackOff", "ErrImagePull", "ErrImageNeverPull",
		"InvalidImageName", "CreateContainerConfigError", "RegistryUnavailable",
	}
	for _, r := range pull {
		if !isImagePullReason(r) {
			t.Errorf("expected %q to be classified as image-pull failure", r)
		}
	}
	notPull := []string{
		"CrashLoopBackOff", "ContainerCreating", "PodInitializing", "",
	}
	for _, r := range notPull {
		if isImagePullReason(r) {
			t.Errorf("expected %q NOT to be classified as image-pull failure", r)
		}
	}
}

func TestErrImagePullFailure_Error(t *testing.T) {
	t.Parallel()

	e := &ErrImagePullFailure{
		Image:     "example.invalid/x:1.0",
		Namespace: "ns",
		Pod:       "web-abc-1",
		Reason:    "ImagePullBackOff",
		Message:   "back-off pulling",
	}
	got := e.Error()
	for _, want := range []string{
		"image pull failure for example.invalid/x:1.0",
		"in pod ns/web-abc-1",
		"ImagePullBackOff",
		"back-off pulling",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing substring %q", got, want)
		}
	}
}

func TestErrRolloutTimeout_PreservesHistoricalPhrasing(t *testing.T) {
	t.Parallel()

	e := &ErrRolloutTimeout{Timeout: 60 * time.Second, Deployments: []string{"frontend/web-app"}}
	got := e.Error()
	// Historical phrasing log parsers depend on: "deployments did not become
	// ready within ..." — do not change this format.
	if !strings.Contains(got, "deployments did not become ready within 1m0s") {
		t.Errorf("Error() = %q, expected historical phrasing", got)
	}
	if !strings.Contains(got, "frontend/web-app") {
		t.Errorf("Error() = %q, expected deployment id", got)
	}
}
