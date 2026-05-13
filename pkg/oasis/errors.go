package oasis

import (
	"fmt"
	"strings"
	"time"
)

// ErrImagePullFailure indicates a kubelet-reported image-pull failure that
// the fast-fail watcher caught while a deployment was rolling out. It is the
// substrate-side signal "registry / image is broken," distinct from a real
// rollout timeout where the image worked but the deployment otherwise refused
// to become ready (failing liveness probe, scheduling pressure, etc.).
//
// The HTTP layer maps this error to 502 Bad Gateway: petri is acting as a
// proxy to the registry, and an unreachable / broken upstream is a gateway
// failure. See docs/decisions/0008-typed-image-pull-failures-502.md.
//
// Use errors.As to extract it; the typed fields are surfaced into the
// /v1/provision response body for the runner / oasisctl.
type ErrImagePullFailure struct {
	// Image is the OCI reference kubelet failed to pull.
	Image string
	// Namespace is the namespace the pod was created in.
	Namespace string
	// Pod is the offending pod's metadata.name.
	Pod string
	// Reason is the kubelet waiting reason that fired
	// (ImagePullBackOff, ErrImagePull, InvalidImageName, ...).
	Reason string
	// Message is kubelet's free-text explanation, often the underlying
	// network error or registry response. May be empty.
	Message string
}

// Error returns a stable, log-grep-friendly string. The format is deliberate:
// the leading "image pull failure for " phrase is what run.log parsers and
// future SLO dashboards should match on.
func (e *ErrImagePullFailure) Error() string {
	msg := e.Message
	if msg == "" {
		msg = "(no message)"
	}
	return fmt.Sprintf("image pull failure for %s in pod %s/%s: %s: %s",
		e.Image, e.Namespace, e.Pod, e.Reason, msg)
}

// ErrRolloutTimeout is returned when one or more deployments did not become
// ready within the rollout timeout AND the fast-fail watcher did not classify
// the failure as a substrate / registry problem. This is the residual
// "something else went wrong" bucket — failing liveness probe, scheduling
// pressure, intentional unhealthy state, etc.
//
// The HTTP layer maps this to 500. The Error() string preserves the
// pre-typed-errors phrasing ("deployments did not become ready within %s: %s")
// verbatim so run.log parsers that grep for it keep working.
type ErrRolloutTimeout struct {
	// Timeout is the duration the watcher waited before giving up.
	Timeout time.Duration
	// Deployments lists the "namespace/name" identifiers that did not roll
	// out in time. Always at least one element.
	Deployments []string
}

// Error returns the stable historical phrasing ("deployments did not become
// ready within %s: %s"). Do not change this format — log parsers depend on
// it.
func (e *ErrRolloutTimeout) Error() string {
	return fmt.Sprintf("deployments did not become ready within %s: %s",
		e.Timeout, strings.Join(e.Deployments, ", "))
}

// ErrNamespaceTerminating indicates that an operation tried to use a
// namespace that is in the Terminating phase. The substrate-side signal is
// "the namespace exists but kube is finalising its deletion"; any attempt
// to write resources into it will be rejected with the kubectl message
// "...because it is being terminated".
//
// It surfaces from two call sites in /v1/provision:
//
//  1. The pre-check at the top of Provision (single GET against the kube
//     API). If the target namespace is already Terminating before any
//     manifests are applied, we fail fast rather than starting the apply
//     loop only to have every entry reject.
//
//  2. The late-detection path inside the state injector. If the namespace
//     was Active at the pre-check but transitions to Terminating mid-apply
//     (e.g. a concurrent /v1/teardown finishes between the two checks),
//     the kubectl stderr "because it is being terminated" is converted to
//     this typed error so the same 409 mapping fires.
//
// The HTTP layer maps it to 409 Conflict — the resource state conflicts
// with the requested operation and the conflict is expected to clear once
// finalisation completes. See ADR 0014.
type ErrNamespaceTerminating struct {
	// Namespace is the namespace that was found in Terminating phase.
	Namespace string
}

// Error returns a stable, grep-friendly string. The "namespace %s is
// terminating" prefix is what run.log parsers should match on; the rest of
// the message is hint copy for humans reading the response.
func (e *ErrNamespaceTerminating) Error() string {
	return fmt.Sprintf("namespace %s is terminating; reuse will fail until termination completes", e.Namespace)
}

// ErrTeardownInProgress is returned from /v1/teardown when kubectl's delete
// hit petri's wall-clock budget but the namespace is at least in
// Terminating phase — i.e. the deletion request landed in kube and kube
// is still finalising. From petri's perspective the kubectl invocation
// failed; from kube's perspective the deletion is healthy and will
// complete asynchronously. Returning 500 implies the first interpretation
// (a real failure, not retryable); 202 communicates the second
// (asynchronous operation in progress).
//
// It also fires synchronously without invoking kubectl when a second
// /v1/teardown arrives for a namespace that already has an in-flight
// teardown registered in the namespace-teardown registry (Part 5 of
// ADR 0014).
//
// The HTTP layer maps it to 202 Accepted.
type ErrTeardownInProgress struct {
	// Namespace is the namespace whose teardown is in progress.
	Namespace string
	// EstimatedRemainingSeconds is a coarse retry hint for the caller.
	// Typical kube namespace finalisation completes within ~30s on a
	// healthy cluster; this is the default the HTTP body advertises.
	EstimatedRemainingSeconds int
}

// Error returns a stable, grep-friendly string. The "teardown in progress
// for namespace" prefix is the log-line shape operators should grep for
// when investigating cascade-prevention behaviour. See ADR 0014.
func (e *ErrTeardownInProgress) Error() string {
	return fmt.Sprintf("teardown in progress for namespace %s; finalisation typically completes within %ds",
		e.Namespace, e.EstimatedRemainingSeconds)
}

// namespaceTerminatingNeedle is the substring kube returns in its admission
// response when a write hits a namespace in Terminating phase. It has been
// stable across recent Kubernetes versions and is the safest signal short
// of plumbing structured errors out of the kubectl wrapper. Used by the
// stateInjector's late-detection path.
const namespaceTerminatingNeedle = "because it is being terminated"

// defaultTeardownRetryAfterSeconds is the EstimatedRemainingSeconds hint
// petri advertises on 202 responses from /v1/teardown. Pinned at 30s
// because most observed namespace finalisations on real labs complete in
// 5–20s. Bumping this hint should follow operational evidence, not gut
// feel.
const defaultTeardownRetryAfterSeconds = 30
