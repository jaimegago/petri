package preflight

import (
	"context"
	"errors"
	"sync"
	"time"
)

// fakeKube is an in-memory KubeClient for unit tests. Each method's
// behaviour can be configured by setting the matching field; the default
// zero value returns a "happy path" answer.
type fakeKube struct {
	mu sync.Mutex

	version    string
	versionErr error
	// versionDelay artificially stretches ServerVersion so duration-tracking
	// tests can assert non-zero per-check timings against a real wall clock.
	versionDelay time.Duration

	canI map[string]bool // verb+":"+resource → allowed
	// canIReason allows tests to seed a specific reason text.
	canIReason map[string]string
	canIErr    error

	// Pod state machine for deep tests. Status returned on Nth call is
	// statuses[i], or the last entry if i >= len.
	statuses []PodPullStatus
	statusi  int
	statusEr error

	createNSErr error
	deleteNSErr error
	createPodEr error

	createdNS    []string
	createdPod   []string
	deletedNS    []string
	createdImage string
}

func (f *fakeKube) ServerVersion(_ context.Context) (string, error) {
	if f.versionDelay > 0 {
		time.Sleep(f.versionDelay)
	}
	if f.versionErr != nil {
		return "", f.versionErr
	}
	if f.version == "" {
		return "v1.30.0", nil
	}
	return f.version, nil
}

func (f *fakeKube) CanI(_ context.Context, verb, resource string) (bool, string, error) {
	if f.canIErr != nil {
		return false, "", f.canIErr
	}
	key := verb + ":" + resource
	if v, ok := f.canI[key]; ok {
		reason := ""
		if f.canIReason != nil {
			reason = f.canIReason[key]
		}
		return v, reason, nil
	}
	// Default: allow everything.
	return true, "", nil
}

func (f *fakeKube) CreateNamespace(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createNSErr != nil {
		return f.createNSErr
	}
	f.createdNS = append(f.createdNS, name)
	return nil
}

func (f *fakeKube) DeleteNamespace(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteNSErr != nil {
		return f.deleteNSErr
	}
	f.deletedNS = append(f.deletedNS, name)
	return nil
}

func (f *fakeKube) CreatePullPod(_ context.Context, namespace, name, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createPodEr != nil {
		return f.createPodEr
	}
	f.createdPod = append(f.createdPod, namespace+"/"+name)
	f.createdImage = image
	return nil
}

func (f *fakeKube) PodPullStatus(_ context.Context, _, _ string) (PodPullStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusEr != nil {
		return PodPullStatus{}, f.statusEr
	}
	if len(f.statuses) == 0 {
		return PodPullStatus{Phase: "Pending"}, nil
	}
	idx := f.statusi
	if idx >= len(f.statuses) {
		idx = len(f.statuses) - 1
	}
	s := f.statuses[idx]
	f.statusi++
	return s, nil
}

// errReachable is a sentinel for tests that want a "network unreachable"
// flavored error from the fake.
var errReachable = errors.New("dial tcp 10.0.0.1:6443: i/o timeout")
