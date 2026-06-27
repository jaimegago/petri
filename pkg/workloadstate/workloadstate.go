// Package workloadstate provisions a Kubernetes Deployment that is born
// exhibiting a named operational state — healthy ("running") or one of several
// intentionally unhealthy states (CrashLoopBackOff, OOMKilled, Pending,
// degraded, elevated error rate, image-pull error).
//
// It is the provision-time sibling of pkg/chaos. Where chaos perturbs an
// already-running resource at runtime (kill a pod, corrupt a ConfigMap),
// workloadstate synthesises a workload that materialises directly into the
// requested state — no healthy intermediate. The two packages share a shape:
// each owns a narrow KubeClient interface defined at the point of use and an
// Execute-style entry point that takes a context, that client, and a spec.
//
// The package is OASIS-agnostic. OASIS is one consumer that drives it from
// state-entry specs, but nothing here imports or knows about OASIS.
package workloadstate

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// State is a recognised workload operational state. A Spec carries the
// requested state as a raw string (see Spec.State); the recognised vocabulary
// is enumerated here and validated by normalizeState.
type State string

const (
	// StateRunning is a healthy Deployment whose pods reach Ready. It is the
	// state an omitted or empty Spec.State resolves to.
	StateRunning State = "running"
	// StateCrashLoopBackOff is a Deployment whose container exits non-zero on
	// start (or fails to resolve a required ConfigMap key), driving the pod
	// into CrashLoopBackOff.
	StateCrashLoopBackOff State = "crashloopbackoff"
	// StateOOMKilled is a Deployment with a tiny memory limit whose container
	// allocates until the kernel OOM-kills it.
	StateOOMKilled State = "oomkilled"
	// StatePending is a Deployment with an unschedulable CPU request so its
	// pods stay Pending with "Insufficient cpu".
	StatePending State = "pending"
	// StateDegraded is a Deployment (≥2 replicas) with a failing readiness
	// probe so a subset of pods never become Ready.
	StateDegraded State = "degraded"
	// StateElevatedErrorRate is a Deployment running an HTTP server that
	// returns 5xx for a configurable percentage of requests.
	StateElevatedErrorRate State = "elevated_error_rate"
	// StateError is a Deployment referencing a non-existent image, producing
	// ErrImagePull / ImagePullBackOff.
	StateError State = "error"
)

// stateAlias maps accepted spelling variants onto their canonical State.
var stateAlias = map[string]State{
	"elevated-error-rate": StateElevatedErrorRate,
}

// recognizedStates is the canonical set in a stable, documented order. It is
// the single source of truth for what normalizeState accepts and for the
// accepted-states list surfaced in validation errors and AcceptedStates.
var recognizedStates = []State{
	StateRunning,
	StateCrashLoopBackOff,
	StateOOMKilled,
	StatePending,
	StateDegraded,
	StateElevatedErrorRate,
	StateError,
}

// AcceptedStates returns the recognised state vocabulary, including the
// canonical spelling of each state. Callers (and docs) can enumerate it
// without hardcoding a count.
func AcceptedStates() []State {
	out := make([]State, len(recognizedStates))
	copy(out, recognizedStates)
	return out
}

// Spec describes the Deployment to synthesise. Callers populate it; the
// package does not reach back into any provider model. Defaulting that belongs
// to the caller's domain (e.g. resolving a registry-safe default image) must
// be done before constructing the Spec — workloadstate only applies the
// state-specific defaults documented on each field.
type Spec struct {
	// Name is the Deployment metadata.name.
	Name string
	// Namespace is the Deployment metadata.namespace.
	Namespace string
	// Replicas is the desired replica count. Values < 1 are treated as 1,
	// except StateDegraded which forces a minimum of 2.
	Replicas int
	// Image is the container image for states that run a real workload
	// (running, pending, degraded, and the ConfigMap variant of
	// crashloopbackoff). Ignored by states that supply their own image
	// (oomkilled, error, the exit-1 variant of crashloopbackoff, and
	// elevated_error_rate).
	Image string
	// Labels are extra pod/template labels merged over the selector labels.
	Labels map[string]string
	// Annotations are Deployment metadata.annotations.
	Annotations map[string]string
	// ManagedBy, when non-empty, adds an app.kubernetes.io/managed-by
	// annotation.
	ManagedBy string
	// MatchLabels is the selector. When empty, {"app": Name} is used.
	MatchLabels map[string]string
	// State is the requested operational state as a raw string. Empty
	// resolves to running. Matching is case-insensitive and trims spaces;
	// aliases (e.g. "elevated-error-rate") are accepted. A non-empty value
	// outside the recognised set is rejected by Render/Provision.
	State string

	// ConfigMapRef selects the ConfigMap-key variant of crashloopbackoff:
	// when set, the container references a missing key in this ConfigMap.
	ConfigMapRef string
	// ErrorRate is the percentage [1,100] of requests that return 5xx for
	// elevated_error_rate. Values < 1 default to 50.
	ErrorRate int
	// UtilImage is the small shell image used by states that need an
	// exit-1 / busy loop (crashloopbackoff exit-1 variant, oomkilled). When
	// empty it falls back to DefaultUtilImage.
	UtilImage string
}

// KubeClient is the narrow cluster surface workloadstate needs: it applies a
// rendered manifest. It is defined here, at the point of use, so any apply
// implementation (the OASIS kube client, a test double) satisfies it without
// importing this package's provider. Mirrors how pkg/chaos owns its KubeClient.
type KubeClient interface {
	// ApplyYAML applies a YAML manifest string (e.g. via kubectl apply).
	ApplyYAML(ctx context.Context, manifest string) error
}

// Provision renders the Deployment manifest for spec and applies it via kube.
// It fails loud: a non-empty Spec.State outside the recognised vocabulary
// returns a descriptive error from Render and nothing is applied.
func Provision(ctx context.Context, kube KubeClient, spec Spec) error {
	manifest, err := Render(spec)
	if err != nil {
		return err
	}
	if err := kube.ApplyYAML(ctx, manifest); err != nil {
		return fmt.Errorf("applying %s workload %q: %w", spec.normalizedStateForError(), spec.Name, err)
	}
	return nil
}

// normalizeState resolves the raw Spec.State to a canonical State. An empty
// (or whitespace-only) value resolves to StateRunning. A non-empty value
// outside the recognised set (after lowercasing, trimming, and alias
// resolution) returns a descriptive error naming the resource and the
// offending value and enumerating the accepted states.
func (s Spec) normalizeState() (State, error) {
	raw := strings.ToLower(strings.TrimSpace(s.State))
	if raw == "" {
		return StateRunning, nil
	}
	if canonical, ok := stateAlias[raw]; ok {
		return canonical, nil
	}
	for _, known := range recognizedStates {
		if raw == string(known) {
			return known, nil
		}
	}
	return "", fmt.Errorf(
		"workload %q: unrecognized state %q; accepted states are %s",
		s.Name, s.State, acceptedStatesList(),
	)
}

// normalizedStateForError returns the canonical state for error messages,
// falling back to the raw value when the state is invalid (Provision only
// reaches the apply path after a successful Render, so this is the valid one).
func (s Spec) normalizedStateForError() string {
	st, err := s.normalizeState()
	if err != nil {
		return s.State
	}
	return string(st)
}

// acceptedStatesList renders the recognised states as a sorted, comma-joined
// string for error messages.
func acceptedStatesList() string {
	names := make([]string, len(recognizedStates))
	for i, st := range recognizedStates {
		names[i] = string(st)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
