package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// registryProbeTimeout bounds the post-detection registry probe. The probe
// itself usually finishes in well under a second on a reachable registry,
// but an unrouted destination can stall on TCP retries. The 30s ceiling is
// generous enough to cover the worst real-world case (HTTPS handshake +
// blob HEAD on a slow-but-up registry) without leaking goroutines on
// shutdown. The probe runs detached from the request context — see
// ADR 0011 — so we cannot rely on the request's own deadline.
const registryProbeTimeout = 30 * time.Second

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

// imagePullProgressReasons enumerates the kubelet waiting reasons under which
// an image may still be coming down from the registry. Only these reasons
// charge elapsed time to the pull budget.
//
// The set is deliberately narrow. An empty reason is NOT in it, and that
// exclusion is doing real work: a pod stuck Pending on scheduling reports no
// container status at all, and counting that as "pulling" would hand an
// unschedulable Deployment the pull budget on top of its own. Scheduling
// pressure is a rollout problem and stays on the rollout budget, which is how
// the fast failure this change promises not to weaken stays fast.
var imagePullProgressReasons = map[string]struct{}{
	"ContainerCreating": {},
	"PodInitializing":   {},
}

// isPullInProgress reports whether a container is plausibly still fetching its
// image. Two conditions, both required:
//
//   - imageID is empty. kubelet populates it from the container runtime once
//     the image is resolved on the node, so a non-empty imageID is positive
//     evidence the pull is done — even while the container is still waiting
//     for something else (a volume mount, a sandbox). Those waits are rollout
//     problems and must not borrow the pull budget.
//   - the waiting reason is one of imagePullProgressReasons.
func isPullInProgress(c parsedContainer) bool {
	if c.imageID != "" {
		return false
	}
	_, ok := imagePullProgressReasons[c.reason]
	return ok
}

// waitForRolloutWithFastFail waits for a single Deployment to roll out under
// two independent budgets, with a concurrent pod-state watcher that both
// short-circuits on a kubelet-reported image-pull failure and decides which
// budget the passing seconds are charged to.
//
// The two budgets exist because one deadline could not serve both conditions.
// A first pull on a cold node is bounded by image size and link speed and is
// paid once per node; a genuinely stuck rollout should fail fast. Charging
// both against 60s meant the first scenario of every cold run failed to
// provision.
//
// Return semantics:
//   - nil: the rollout completed successfully.
//   - *ErrImagePullFailure: kubelet reported a failed pull. Terminal.
//   - *ErrImagePullTimeout: pods were still pulling when pullTimeout expired.
//   - *ErrRolloutTimeout: rolloutTimeout of non-pull time elapsed, or the
//     rollout wait itself failed. The Deployments slice carries the single
//     "namespace/deployment" identifier.
func (p *petriProvider) waitForRolloutWithFastFail(
	ctx context.Context, namespace, deployment string,
	rolloutTimeout, pullTimeout time.Duration,
) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	id := namespace + "/" + deployment

	// The kubectl-side deadline is a backstop, not the operative limit: it
	// exists so a watcher that dies unexpectedly cannot leave kubectl waiting
	// forever. The watcher partitions the same elapsed wall-clock between the
	// two budgets, so one of them is always exhausted first.
	//
	// The slack matters. The watcher can only decide on a tick boundary, so
	// it overshoots whichever budget expires by up to one interval — and a
	// budget that is not a whole number of intervals would otherwise let this
	// ceiling fire first and report a rollout timeout for a pull that was
	// still running. Two intervals of headroom removes that race for any
	// budget, rather than for the ones that happen to divide evenly.
	ceiling := rolloutTimeout + pullTimeout + 2*pullWatchInterval

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
	watcherCh := make(chan error, 1)

	go func() {
		rolloutCh <- p.kube.WaitForRollout(wctx, namespace, deployment, ceiling)
	}()
	go func() {
		watcherCh <- p.watchPullBudget(wctx, namespace, deployment, pullWatchInterval, pullTimeout, rolloutTimeout)
	}()

	select {
	case err := <-rolloutCh:
		// Rollout finished (success, or a failure kubectl noticed before
		// either budget expired). Cancel the watcher; it exits within a tick
		// of pullWatchInterval and its buffered write does not block.
		cancel()
		if err != nil {
			// Reported as the rollout budget, never the ceiling: the
			// historical run.log phrasing carries this duration and parsers
			// read it. The ceiling is an implementation detail of the
			// backstop and has no business surfacing.
			return &ErrRolloutTimeout{
				Timeout:     rolloutTimeout,
				Deployments: []string{id},
			}
		}
		return nil
	case werr := <-watcherCh:
		if werr != nil {
			// Watcher caught a pull failure or exhausted a budget. Cancel the
			// rollout and return immediately. The kubectl subprocess will exit
			// on its own schedule; we deliberately do not wait for it, because
			// that wait was the source of the ~5s gap between detection and
			// the HTTP response.
			cancel()
			return werr
		}
		// Watcher returned nil only when wctx was cancelled by the parent
		// (watchPullBudget returns nil exclusively on ctx.Done). The rollout
		// goroutine sees the same cancellation and will return its own ctx
		// error. We *do* wait for it here — this is the one branch where we
		// want the rollout's result so a client-cancelled request surfaces as
		// a typed ErrRolloutTimeout rather than nil. Accepted trade-off: a
		// cancelled client may wait up to ~5s for kubectl to exit.
		err := <-rolloutCh
		if err != nil {
			return &ErrRolloutTimeout{
				Timeout:     rolloutTimeout,
				Deployments: []string{id},
			}
		}
		return nil
	}
}

// watchPullBudget polls pod state for the given Deployment at interval and
// does two jobs from a single pod list per tick:
//
//  1. Fast-fail — return a typed *ErrImagePullFailure the moment kubelet
//     reports a definitive pull failure. This watcher has always done that,
//     and it is unchanged.
//  2. Budget accounting — split the elapsed wall-clock into time the
//     Deployment spent pulling images and time it spent doing anything else,
//     and hold each to its own budget.
//
// Merging the two into one loop is deliberate: they need exactly the same pod
// list, and running them as separate goroutines would double the API-server
// traffic per deployment for no gain.
//
// Returns nil only when ctx is cancelled.
//
// The watcher scopes its listing to pods whose ownerReferences point at a
// ReplicaSet named "<deployment>-<hash>" — the standard Deployment-managed
// ReplicaSet naming convention. This isolation matters because one watcher
// runs per deployment and several deployments may share a namespace.
func (p *petriProvider) watchPullBudget(
	ctx context.Context, namespace, deployment string,
	interval, pullBudget, rolloutBudget time.Duration,
) error {
	id := namespace + "/" + deployment
	var pullElapsed, rolloutElapsed time.Duration

	// Do an immediate check before sleeping — kubelet may have already
	// reported a failure by the time we get here.
	pf, pulling, images := p.scanPodState(ctx, namespace, deployment)
	if pf != nil {
		return pf
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			// Charge the interval just elapsed to whichever budget the
			// previous sample put us in. The delta is measured rather than
			// assumed to be `interval`: a loaded host stretches a ticker well
			// past its period, and host load is precisely the condition these
			// budgets have to survive.
			delta := now.Sub(last)
			last = now
			if pulling {
				pullElapsed += delta
			} else {
				rolloutElapsed += delta
			}

			if pullElapsed >= pullBudget {
				p.log.Warn("image pull budget exhausted",
					"deployment", deployment,
					"namespace", namespace,
					"pull_budget", pullBudget.String(),
					"images", strings.Join(images, ", "),
				)
				return &ErrImagePullTimeout{
					Timeout:    pullBudget,
					Namespace:  namespace,
					Deployment: deployment,
					Images:     images,
				}
			}
			if rolloutElapsed >= rolloutBudget {
				p.log.Warn("rollout budget exhausted",
					"deployment", deployment,
					"namespace", namespace,
					"rollout_budget", rolloutBudget.String(),
					"pull_elapsed", pullElapsed.String(),
				)
				return &ErrRolloutTimeout{
					Timeout:     rolloutBudget,
					Deployments: []string{id},
				}
			}

			pf, pulling, images = p.scanPodState(ctx, namespace, deployment)
			if pf != nil {
				return pf
			}
		}
	}
}

// scanPodState performs a single pod-list and reports three things: the first
// image-pull failure detected (or nil), whether any container is still
// fetching its image, and the images those containers are waiting on.
//
// List/parse errors are logged at DEBUG and treated as "no failure, not
// pulling" — the watcher tries again on the next tick. Charging an unreadable
// pod list to the rollout budget rather than the pull budget is the
// conservative direction: it cannot silently extend a wait.
func (p *petriProvider) scanPodState(
	ctx context.Context, namespace, deployment string,
) (*ErrImagePullFailure, bool, []string) {
	raw, err := p.kube.ListResources(ctx, "pods", namespace)
	if err != nil {
		// Don't propagate: the rollout wait is authoritative; a transient
		// list error should not trigger a fast-fail.
		p.log.Debug("pod-event watcher: list pods failed",
			"namespace", namespace, "deployment", deployment, "error", err)
		return nil, false, nil
	}
	pods, err := parsePodsForDeployment(raw, deployment)
	if err != nil {
		p.log.Debug("pod-event watcher: parse failed",
			"namespace", namespace, "deployment", deployment, "error", err)
		return nil, false, nil
	}

	var pulling []string
	for _, pod := range pods {
		for _, cs := range pod.containers {
			if isImagePullReason(cs.reason) {
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
				p.scheduleRegistryProbe(cs.image)
				return pf, false, nil
			}
			if isPullInProgress(cs) {
				pulling = append(pulling, cs.image)
			}
		}
	}
	return nil, len(pulling) > 0, pulling
}

// scheduleRegistryProbe runs the configured registry probe against the
// failing reference in a tracked background goroutine and emits a
// structured log line when it completes. The probe is purely diagnostic —
// the typed ErrImagePullFailure is already fully populated by the time we
// get here — so blocking the HTTP response on its 5-30s budget was wasted
// client wait time. Running it async preserves the operator-facing log
// signal at the cost of having the "registry probe result" line arrive
// AFTER the 502 response in run.log. See ADR 0011.
func (p *petriProvider) scheduleRegistryProbe(image string) {
	p.tasks.Go("registry-probe:"+image, func() {
		pctx, cancel := context.WithTimeout(context.Background(), registryProbeTimeout)
		defer cancel()
		result, err := p.probeImage(pctx, image)
		if err != nil {
			if errors.Is(pctx.Err(), context.DeadlineExceeded) {
				p.log.Warn("image pull failure: registry probe abandoned",
					"image", image,
					"timeout", registryProbeTimeout.String(),
					"probe_detail", err.Error(),
				)
				return
			}
			p.log.Warn("image pull failure: registry probe result",
				"image", image,
				"probe_outcome", "invalid-ref",
				"probe_detail", err.Error(),
			)
			return
		}
		p.log.Warn("image pull failure: registry probe result",
			"image", image,
			"probe_outcome", result.Outcome(),
			"probe_detail", result.Detail(),
		)
	})
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
	name  string
	image string
	// imageID is kubelet's runtime-reported identifier for the resolved
	// image. Empty until the image is present on the node, which is what
	// makes it the signal that separates "still pulling" from "pulled, but
	// waiting on something else". See isPullInProgress.
	imageID string
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
			Name    string `json:"name"`
			Image   string `json:"image"`
			ImageID string `json:"imageID"`
			State   struct {
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
			pc := parsedContainer{name: cs.Name, image: cs.Image, imageID: cs.ImageID}
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
