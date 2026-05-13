package oasis

import "sync"

// teardownRegistry tracks namespaces with an in-flight deletion so that
// two simultaneous paths (the async post-failure cleanup goroutine and a
// foreground /v1/teardown call) cannot spawn duplicate kubectl
// invocations for the same target. Part 5 of ADR 0014.
//
// The registry is intentionally minimal: a single map under a mutex, no
// per-namespace metadata. The goal is "is anyone already deleting this
// namespace?" — full deletion state lives in kube, not in petri.
type teardownRegistry struct {
	mu  sync.Mutex
	set map[string]struct{}
}

func newTeardownRegistry() *teardownRegistry {
	return &teardownRegistry{set: make(map[string]struct{})}
}

// tryAcquire reserves the registry slot for ns if no teardown is in
// flight. It returns true when the caller now owns the slot and must
// release it via Release when the teardown finishes (success or terminal
// failure). It returns false when another teardown is already in flight;
// the caller must surface ErrTeardownInProgress instead of starting a
// duplicate kubectl delete.
func (r *teardownRegistry) tryAcquire(ns string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.set[ns]; ok {
		return false
	}
	r.set[ns] = struct{}{}
	return true
}

// Release frees the registry slot for ns. Safe to call even if ns was
// never acquired — the map delete is a no-op in that case.
func (r *teardownRegistry) Release(ns string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.set, ns)
}

// inFlight reports whether a teardown is currently in flight for ns. Used
// by tests; not part of the registry's normal API.
func (r *teardownRegistry) inFlight(ns string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.set[ns]
	return ok
}
