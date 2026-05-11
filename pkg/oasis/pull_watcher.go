package oasis

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jaimegago/petri/pkg/preflight"
)

// pullWatchInterval is the cadence at which the watcher polls pod state. Set
// to 3s — roughly one kubelet event-flush cycle — so we detect kubelet-
// reported ImagePullBackOff within a few seconds of it firing without
// hammering the API server.
const pullWatchInterval = 3 * time.Second

// imagePullReasons enumerates the kubelet waiting reasons we treat as
// definitive image-pull failures. CrashLoopBackOff, ContainerCreating, and
// Pending-due-to-scheduling are deliberately NOT in this set: they are not
// substrate-side signals and would cause false fast-fails.
var imagePullReasons = map[string]struct{}{
	"ImagePullBackOff":           {},
	"ErrImagePull":               {},
	"ErrImageNeverPull":          {},
	"RegistryUnavailable":        {},
	"InvalidImageName":           {},
	"CreateContainerConfigError": {},
	"ImageInspectError":          {},
	"SignatureValidationFailed":  {},
}

// isImagePullReason returns true for kubelet waiting reasons that indicate a
// failed image pull (not an in-progress crash or scheduling delay).
func isImagePullReason(reason string) bool {
	_, ok := imagePullReasons[reason]
	return ok
}

// waitForRolloutWithFastFail waits for a single Deployment to roll out, with
// a concurrent pod-event watcher that short-circuits the wait the moment
// kubelet reports an image-pull failure. The two goroutines share a derived
// context; whichever returns first cancels the other.
//
// Return semantics:
//   - nil: the rollout completed successfully.
//   - *ErrImagePullFailure: the watcher detected an image-pull failure.
//     The rollout wait was cancelled.
//   - *ErrRolloutTimeout: the rollout wait returned an error and the watcher
//     did NOT classify it as a pull failure. The Deployments slice carries
//     the single "namespace/deployment" identifier.
func (p *petriProvider) waitForRolloutWithFastFail(
	ctx context.Context, namespace, deployment string, timeout time.Duration,
) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Both channels are buffered (cap 1) so the goroutines can always write
	// their single result without blocking on a reader. That property is what
	// lets us avoid draining the unselected channel below: an unread buffered
	// channel does not leak its writer — the goroutine writes, exits, and the
	// channel is collected once it falls out of scope. Draining would instead
	// stall this function on the loser goroutine's wind-down, and `kubectl
	// rollout status` takes ~5s to notice context cancellation. On the
	// pull-failure path the HTTP caller is waiting on us, so that ~5s gap is
	// directly user-visible.
	rolloutCh := make(chan error, 1)
	watcherCh := make(chan *ErrImagePullFailure, 1)

	go func() {
		rolloutCh <- p.kube.WaitForRollout(wctx, namespace, deployment, timeout)
	}()
	go func() {
		watcherCh <- p.watchPullFailures(wctx, namespace, deployment, pullWatchInterval)
	}()

	select {
	case err := <-rolloutCh:
		// Rollout finished (success or timeout). Cancel the watcher; it
		// exits within a tick of pullWatchInterval (its inner loop selects
		// on ctx.Done) and its buffered write does not block.
		cancel()
		if err != nil {
			return &ErrRolloutTimeout{
				Timeout:     timeout,
				Deployments: []string{namespace + "/" + deployment},
			}
		}
		return nil
	case pf := <-watcherCh:
		if pf != nil {
			// Watcher caught a pull failure. Cancel the rollout and return
			// immediately. The kubectl subprocess will exit on its own
			// schedule; we deliberately do not wait for it, because that
			// wait was the source of the ~5s gap between detection and the
			// HTTP 502 response.
			cancel()
			return pf
		}
		// Watcher returned nil only when wctx was cancelled by the parent
		// (watchPullFailures returns nil exclusively on ctx.Done). The
		// rollout goroutine sees the same cancellation and will return its
		// own ctx error. We *do* wait for it here — this is the one branch
		// where we want the rollout's result so a client-cancelled request
		// surfaces as a typed ErrRolloutTimeout rather than nil. Accepted
		// trade-off: a cancelled client may wait up to ~5s for kubectl to
		// exit. If that ever becomes a real problem, introduce a typed
		// "request cancelled" error and return it without waiting.
		err := <-rolloutCh
		if err != nil {
			return &ErrRolloutTimeout{
				Timeout:     timeout,
				Deployments: []string{namespace + "/" + deployment},
			}
		}
		return nil
	}
}

// watchPullFailures polls pod state for the given Deployment at interval and
// returns a typed image-pull error as soon as one is detected. Returns nil
// only when ctx is cancelled.
//
// The watcher scopes its listing to pods whose ownerReferences point at a
// ReplicaSet named "<deployment>-<hash>" — the standard Deployment-managed
// ReplicaSet naming convention. This isolation is important: a parallel-wait
// implementation (deferred to a future change, see ADR 0010) will run one
// watcher per deployment, and they must not cross-contaminate when several
// deployments share a namespace.
func (p *petriProvider) watchPullFailures(
	ctx context.Context, namespace, deployment string, interval time.Duration,
) *ErrImagePullFailure {
	// Do an immediate check before sleeping — kubelet may have already
	// reported the failure by the time we get here.
	if pf := p.scanPullFailures(ctx, namespace, deployment); pf != nil {
		return pf
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if pf := p.scanPullFailures(ctx, namespace, deployment); pf != nil {
				return pf
			}
		}
	}
}

// scanPullFailures performs a single pod-list and returns the first pull
// failure detected, or nil. List/parse errors are logged at DEBUG and
// treated as "no failure detected" — the watcher will try again on the next
// tick.
func (p *petriProvider) scanPullFailures(
	ctx context.Context, namespace, deployment string,
) *ErrImagePullFailure {
	raw, err := p.kube.ListResources(ctx, "pods", namespace)
	if err != nil {
		// Don't propagate: the rollout wait is authoritative; a transient
		// list error should not trigger a fast-fail.
		p.log.Debug("pod-event watcher: list pods failed",
			"namespace", namespace, "deployment", deployment, "error", err)
		return nil
	}
	pods, err := parsePodsForDeployment(raw, deployment)
	if err != nil {
		p.log.Debug("pod-event watcher: parse failed",
			"namespace", namespace, "deployment", deployment, "error", err)
		return nil
	}
	for _, pod := range pods {
		for _, cs := range pod.containers {
			if !isImagePullReason(cs.reason) {
				continue
			}
			pf := &ErrImagePullFailure{
				Image:     cs.image,
				Namespace: namespace,
				Pod:       pod.name,
				Reason:    cs.reason,
				Message:   cs.message,
			}
			p.log.Warn("image pull failure detected",
				"deployment", deployment,
				"namespace", namespace,
				"image", cs.image,
				"pod_name", pod.name,
				"reason", cs.reason,
				"message", cs.message,
			)
			p.logRegistryProbe(ctx, p.log, cs.image)
			return pf
		}
	}
	return nil
}

// logRegistryProbe runs ProbeImage against the failing reference and emits a
// follow-up structured log line so the operator can immediately tell whether
// the registry itself is unreachable or whether the failure is kubelet-
// specific (e.g. kind node missing a private-registry credential). The probe
// runs with a tight budget — failure to probe must not stall the fast-fail
// path.
func (p *petriProvider) logRegistryProbe(parent context.Context, log *slog.Logger, image string) {
	pctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	result, err := preflight.ProbeImage(pctx, http.DefaultClient, image)
	if err != nil {
		log.Warn("image pull failure: registry probe result",
			"image", image,
			"probe_outcome", "invalid-ref",
			"probe_detail", err.Error(),
		)
		return
	}
	log.Warn("image pull failure: registry probe result",
		"image", image,
		"probe_outcome", result.Outcome(),
		"probe_detail", result.Detail(),
	)
}

// ── pod-list parsing ─────────────────────────────────────────────────────────

// parsedPod is the narrow view of a Pod the watcher cares about: name plus
// per-container waiting-state. We unmarshal kubectl's JSON list output rather
// than depend on client-go so the watcher works against the existing
// KubeClient.ListResources surface.
type parsedPod struct {
	name       string
	containers []parsedContainer
}

type parsedContainer struct {
	name    string
	image   string
	reason  string
	message string
}

type podListRaw struct {
	Items []podItemRaw `json:"items"`
}

type podItemRaw struct {
	Metadata struct {
		Name            string `json:"name"`
		OwnerReferences []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Containers []struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Name  string `json:"name"`
			Image string `json:"image"`
			State struct {
				Waiting *struct {
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"waiting"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// parsePodsForDeployment returns the parsedPods whose ownerReferences point
// at a ReplicaSet named "<deployment>-<hash>". Pods owned by other
// controllers in the same namespace are skipped.
func parsePodsForDeployment(rawJSON, deployment string) ([]parsedPod, error) {
	var list podListRaw
	if err := json.Unmarshal([]byte(rawJSON), &list); err != nil {
		return nil, err
	}
	prefix := deployment + "-"
	var out []parsedPod
	for _, p := range list.Items {
		if !ownedByDeployment(p, prefix) {
			continue
		}
		pp := parsedPod{name: p.Metadata.Name}

		specImage := make(map[string]string, len(p.Spec.Containers))
		for _, sc := range p.Spec.Containers {
			specImage[sc.Name] = sc.Image
		}

		for _, cs := range p.Status.ContainerStatuses {
			pc := parsedContainer{name: cs.Name, image: cs.Image}
			if pc.image == "" {
				pc.image = specImage[cs.Name]
			}
			if cs.State.Waiting != nil {
				pc.reason = cs.State.Waiting.Reason
				pc.message = cs.State.Waiting.Message
			}
			pp.containers = append(pp.containers, pc)
		}
		// Newly-created pod with no containerStatuses yet: surface the spec
		// images so callers can still associate a future failure signal with
		// the right reference (the watcher won't fire on these because
		// reason is empty, but the parsed shape is consistent).
		if len(pp.containers) == 0 {
			for _, sc := range p.Spec.Containers {
				pp.containers = append(pp.containers, parsedContainer{name: sc.Name, image: sc.Image})
			}
		}
		out = append(out, pp)
	}
	return out, nil
}

// ownedByDeployment returns true if the pod is transitively owned by the
// named Deployment. A Deployment owns one or more ReplicaSets; each
// ReplicaSet's name is "<deployment>-<hash>". We match on that prefix rather
// than walk owner chains across two API calls.
func ownedByDeployment(p podItemRaw, deploymentPrefix string) bool {
	for _, ref := range p.Metadata.OwnerReferences {
		if ref.Kind == "ReplicaSet" && strings.HasPrefix(ref.Name, deploymentPrefix) {
			return true
		}
	}
	return false
}
