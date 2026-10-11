// Package workloadstate provisions a Kubernetes workload — a Deployment, or a
// bare Pod — that is born exhibiting a named operational state — healthy ("running") or one of several
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

	"github.com/jaimegago/petri/pkg/fault"
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
	// start, driving the pod into CrashLoopBackOff. With a Fault declared the
	// container is the application failing its own startup validation;
	// without one it is a symptom-only exit.
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
	// returns 5xx for a configurable percentage of requests. The server is
	// HTTPServerImage, never a Docker Hub image.
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

// Kind is the Kubernetes workload kind a Spec renders as.
type Kind string

const (
	// KindDeployment renders a Deployment. It is what an empty Spec.Kind
	// means.
	KindDeployment Kind = "Deployment"
	// KindPod renders a bare Pod: one replica, no controller. A state that
	// is a property of a replica set rather than of one pod (degraded) is
	// refused for it rather than rendered as something else.
	KindPod Kind = "Pod"
)

// Spec describes the workload to synthesise. Callers populate it; the
// package does not reach back into any provider model. Defaulting that belongs
// to the caller's domain (e.g. resolving a registry-safe default image) must
// be done before constructing the Spec — workloadstate only applies the
// state-specific defaults documented on each field.
type Spec struct {
	// Kind is the workload kind to render. Empty means KindDeployment.
	Kind Kind
	// Name is the workload metadata.name.
	Name string
	// Namespace is the workload metadata.namespace.
	Namespace string
	// Replicas is the desired replica count. Values < 1 are treated as 1,
	// except StateDegraded which forces a minimum of 2. A Pod is one
	// replica, and any other count is refused.
	Replicas int
	// NodeName, for a Pod only, binds it to a node, bypassing the
	// scheduler. A Pod bound this way is refused the pending state, which
	// needs the scheduler to refuse it.
	NodeName string
	// Image is the container image for states that run a real workload
	// (running, pending, degraded, and the ConfigMap variant of
	// crashloopbackoff). Ignored by states that supply their own image
	// (oomkilled, error, the exit-1 variant of crashloopbackoff, and
	// elevated_error_rate).
	Image string
	// Labels are extra pod/template labels merged over the selector labels.
	Labels map[string]string
	// Annotations are the workload's metadata.annotations.
	Annotations map[string]string
	// ManagedBy, when non-empty, adds an app.kubernetes.io/managed-by
	// annotation.
	ManagedBy string
	// MatchLabels is the selector. When empty, {"app": Name} is used. A Pod
	// carries it as labels, so it is selected the same way.
	MatchLabels map[string]string
	// State is the requested operational state as a raw string. Empty
	// resolves to running. Matching is case-insensitive and trims spaces;
	// aliases (e.g. "elevated-error-rate") are accepted. A non-empty value
	// outside the recognised set is rejected by Render/Provision.
	State string

	// Env is the container environment as the scenario declares it. When
	// non-empty it is rendered onto the container verbatim: the scenario's
	// own key names are what the agent under evaluation reads.
	Env []EnvVar

	// Fault, when set, materialises the state through the application
	// rather than a synthetic container: the Deployment runs AppImage with
	// the declared Env, and the misconfiguration Fault names is what makes
	// it fail. The state must be the one the fault's class produces. ConfigMap
	// references are rendered optional so the absence reaches the
	// application, whose own validation is the failure the agent sees.
	Fault *fault.Spec
	// AppImage overrides the pinned application image when Fault is set.
	// Empty falls back to fault.AppImage.
	AppImage string
	// ErrorRate is the percentage [1,100] of requests that return 5xx for
	// elevated_error_rate. Values < 1 default to 50.
	ErrorRate int
	// UtilImage is the small shell image used by states that need an
	// exit-1 / busy loop (crashloopbackoff exit-1 variant, oomkilled). When
	// empty it falls back to DefaultUtilImage.
	UtilImage string
	// ContainerName is the name of the workload's single container, as the
	// caller declares it. It is rendered verbatim, so an agent reading the
	// cluster finds the identifier the scenario declared. Empty falls back
	// to DefaultContainerName; a non-empty value must be a valid Kubernetes
	// container name (an RFC 1123 label) or Render refuses it.
	ContainerName string
}

// DefaultContainerName is the container name rendered when Spec.ContainerName
// is empty.
const DefaultContainerName = "app"

// EnvVar is one container environment variable. Exactly one source applies:
// a literal Value, a ConfigMapKeyRef sourcing it from a ConfigMap key, or a
// SecretKeyRef sourcing it from a Secret key. A key reference renders as a
// required reference, so a missing key stops the container from starting
// rather than leaving the variable unset — that is what makes a deliberately
// omitted key visible to an agent as a named cause.
type EnvVar struct {
	// Name is the environment variable name.
	Name string
	// Value is a literal value. Ignored when ConfigMapKeyRef is set.
	Value string
	// ConfigMapKeyRef sources the value from a ConfigMap key.
	ConfigMapKeyRef *ConfigMapKeySelector
	// SecretKeyRef sources the value from a Secret key.
	SecretKeyRef *SecretKeySelector
}

// SecretKeySelector names a Secret and a key within it.
type SecretKeySelector struct {
	// Name is the Secret metadata.name.
	Name string
	// Key is the key within the Secret's data.
	Key string
}

// ConfigMapKeySelector names a ConfigMap and a key within it.
type ConfigMapKeySelector struct {
	// Name is the ConfigMap metadata.name.
	Name string
	// Key is the key within the ConfigMap's data.
	Key string
}

// KubeClient is the narrow cluster surface workloadstate needs: it applies a
// rendered manifest. It is defined here, at the point of use, so any apply
// implementation (the OASIS kube client, a test double) satisfies it without
// importing this package's provider. Mirrors how pkg/chaos owns its KubeClient.
type KubeClient interface {
	// ApplyYAML applies a YAML manifest string (e.g. via kubectl apply).
	ApplyYAML(ctx context.Context, manifest string) error
}

// Provision renders the workload manifest for spec and applies it via kube.
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

// kind resolves Spec.Kind, an empty value meaning KindDeployment.
func (s Spec) kind() (Kind, error) {
	switch s.Kind {
	case "", KindDeployment:
		return KindDeployment, nil
	case KindPod:
		return KindPod, nil
	default:
		return "", fmt.Errorf("workload %q: unsupported kind %q; supported kinds are %s and %s", s.Name, s.Kind, KindDeployment, KindPod)
	}
}

// resolve validates everything Render and Convergence both depend on: the
// state, the kind, and the combinations a kind cannot materialise. A
// combination it refuses would otherwise render as some other state.
func (s Spec) resolve() (Kind, State, error) {
	state, err := s.normalizeState()
	if err != nil {
		return "", "", err
	}
	kind, err := s.kind()
	if err != nil {
		return "", "", err
	}
	if kind == KindDeployment {
		if s.NodeName != "" {
			return "", "", fmt.Errorf("workload %q: a node binding is supported for a %s, not a %s", s.Name, KindPod, KindDeployment)
		}
		return kind, state, nil
	}
	if s.Replicas > 1 {
		return "", "", fmt.Errorf("workload %q: a %s is one replica, not %d; declare a deployment for more", s.Name, KindPod, s.Replicas)
	}
	if s.Fault != nil {
		return "", "", fmt.Errorf("workload %q: a fault is materialised through a %s, not a bare %s", s.Name, KindDeployment, KindPod)
	}
	switch {
	case state == StateDegraded:
		return "", "", fmt.Errorf("workload %q: state %q is a replica set some of whose pods are not Ready, which a bare %s cannot be; declare a deployment", s.Name, state, KindPod)
	case state == StatePending && s.NodeName != "":
		return "", "", fmt.Errorf("workload %q: state %q needs the scheduler to refuse the pod, and a pod bound to a node bypasses it", s.Name, state)
	}
	return kind, state, nil
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
