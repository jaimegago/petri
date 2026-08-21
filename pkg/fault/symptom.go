package fault

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PodLister is the narrow cluster surface symptom verification needs: the
// pods of a namespace as a JSON list, as `kubectl get pods -o json` returns
// them. Both the OASIS provider's client and pkg/chaos's satisfy it.
type PodLister interface {
	ListResources(ctx context.Context, kind, namespace string) (string, error)
}

// symptomPollInterval is how often WaitForSymptom re-lists the pods.
const symptomPollInterval = 2 * time.Second

// ErrSymptomUnmet is returned when the declared symptom was not observed on
// every pod of the workload within the budget. It carries what was seen so
// the caller can say precisely how the application disagreed with the
// declaration.
type ErrSymptomUnmet struct {
	Namespace string
	Workload  string
	Expect    Expect
	Timeout   time.Duration
	// Observed is the last per-pod container state reason seen, keyed by
	// pod name, for the pods that had not satisfied the expectation.
	Observed map[string]string
}

func (e *ErrSymptomUnmet) Error() string {
	names := make([]string, 0, len(e.Observed))
	for n := range e.Observed {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%s", n, e.Observed[n]))
	}
	seen := "no pods"
	if len(parts) > 0 {
		seen = strings.Join(parts, ", ")
	}
	return fmt.Sprintf("workload %s/%s did not reach declared symptom %q within %s; observed: %s",
		e.Namespace, e.Workload, e.Expect.Status, e.Timeout, seen)
}

// WaitForSymptom polls the pods selected by selector in namespace until every
// one of them — and at least minPods of them — has exhibited expect, or the
// timeout elapses. It returns *ErrSymptomUnmet on timeout.
//
// Satisfaction is sticky per pod: a crash-looping container alternates
// between a terminated state and a CrashLoopBackOff wait, so a pod that has
// been seen in the symptom counts even when the next poll catches it
// mid-restart.
//
// For CrashLoopBackOff the match is the kubelet's own definition of the
// condition — a container restarted at least once after a failed
// termination — rather than the literal waiting reason alone. Measured on
// kind v1.35.0, the waiting reason is present for under half of the backoff
// period and first appears after several restarts, while the restart count
// and the failed termination are visible from the first restart onwards.
// Any other status matches a container whose current or last state reason
// equals it, case-insensitively.
func WaitForSymptom(ctx context.Context, kube PodLister, namespace string, selector map[string]string, minPods int, expect Expect, timeout time.Duration) error {
	if minPods < 1 {
		minPods = 1
	}
	deadline := time.Now().Add(timeout)
	satisfied := map[string]bool{}
	observed := map[string]string{}

	for {
		raw, err := kube.ListResources(ctx, "pods", namespace)
		if err != nil {
			return fmt.Errorf("listing pods in %s: %w", namespace, err)
		}
		pods, err := parsePods(raw)
		if err != nil {
			return fmt.Errorf("parsing pods in %s: %w", namespace, err)
		}

		matched := 0
		for _, p := range pods {
			if !matchesSelector(p.Metadata.Labels, selector) || p.Metadata.DeletionTimestamp != "" {
				continue
			}
			matched++
			name := p.Metadata.Name
			if satisfied[name] {
				continue
			}
			ok, seen := p.exhibits(expect)
			observed[name] = seen
			if ok {
				satisfied[name] = true
				delete(observed, name)
			}
		}
		if matched >= minPods && matched > 0 && len(satisfied) == matched {
			return nil
		}

		if time.Now().After(deadline) {
			return &ErrSymptomUnmet{Namespace: namespace, Workload: selectorString(selector), Expect: expect, Timeout: timeout, Observed: observed}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(symptomPollInterval):
		}
	}
}

// pod is the slice of a Pod object symptom matching reads.
type pod struct {
	Metadata struct {
		Name              string            `json:"name"`
		Labels            map[string]string `json:"labels"`
		DeletionTimestamp string            `json:"deletionTimestamp"`
	} `json:"metadata"`
	Status struct {
		Phase             string            `json:"phase"`
		ContainerStatuses []containerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type containerStatus struct {
	RestartCount int            `json:"restartCount"`
	State        containerState `json:"state"`
	LastState    containerState `json:"lastState"`
}

type containerState struct {
	Waiting    *stateDetail `json:"waiting"`
	Terminated *stateDetail `json:"terminated"`
}

type stateDetail struct {
	Reason   string `json:"reason"`
	ExitCode int    `json:"exitCode"`
}

func parsePods(raw string) ([]pod, error) {
	var list struct {
		Items []pod `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// exhibits reports whether any container of the pod shows expect, and a
// short description of what was seen for the error path.
func (p pod) exhibits(expect Expect) (bool, string) {
	want := strings.ToLower(strings.TrimSpace(expect.Status))
	if len(p.Status.ContainerStatuses) == 0 {
		return false, strings.ToLower(p.Status.Phase)
	}
	seen := make([]string, 0, len(p.Status.ContainerStatuses))
	for _, c := range p.Status.ContainerStatuses {
		reasons := c.reasons()
		seen = append(seen, fmt.Sprintf("%s(restarts=%d)", strings.Join(reasons, "/"), c.RestartCount))
		if want == strings.ToLower(SymptomCrashLoopBackOff) && c.crashLooping() {
			return true, ""
		}
		for _, r := range reasons {
			if strings.ToLower(r) == want {
				return true, ""
			}
		}
	}
	return false, strings.Join(seen, ",")
}

// crashLooping is the kubelet's condition: the container has been restarted
// after a failed termination, or is explicitly waiting in backoff.
func (c containerStatus) crashLooping() bool {
	if c.State.Waiting != nil && strings.EqualFold(c.State.Waiting.Reason, SymptomCrashLoopBackOff) {
		return true
	}
	if c.RestartCount < 1 {
		return false
	}
	for _, t := range []*stateDetail{c.State.Terminated, c.LastState.Terminated} {
		if t != nil && t.ExitCode != 0 {
			return true
		}
	}
	return false
}

// reasons lists the non-empty state reasons on the container, current first.
func (c containerStatus) reasons() []string {
	var out []string
	for _, d := range []*stateDetail{c.State.Waiting, c.State.Terminated, c.LastState.Terminated} {
		if d != nil && d.Reason != "" {
			out = append(out, d.Reason)
		}
	}
	if len(out) == 0 {
		out = []string{"running"}
	}
	return out
}

func matchesSelector(labels, selector map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func selectorString(selector map[string]string) string {
	keys := make([]string, 0, len(selector))
	for k := range selector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+selector[k])
	}
	return strings.Join(parts, ",")
}
