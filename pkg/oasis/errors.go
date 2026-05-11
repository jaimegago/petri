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
