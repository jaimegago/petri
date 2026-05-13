package oasis

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockKubeClient is a test double for KubeClient.
type mockKubeClient struct {
	mu                sync.Mutex
	appliedManifests  []string
	createdNamespaces []createdNS
	deletedNamespaces []string
	resources         map[string]string // key: "kind/ns/name" or "kind/ns" for list
	tokenResponses    map[string]string // key: "ns/sa"
	clusterServerURL  string
	clusterCAData     string
	err               error
	// applyYAMLErr, when non-nil, is returned from ApplyYAML instead of
	// the default success path. Lets tests simulate kubectl apply
	// failures (e.g. the "namespace is being terminated" message) without
	// the test having to also break unrelated operations.
	applyYAMLErr error
	// namespacePhases overrides the .status.phase returned by
	// GetNamespacePhase for a given namespace name. Empty string means
	// "namespace does not exist" (404) — matching the chaos.KubeClient
	// contract for that method.
	namespacePhases map[string]string
	// getNamespacePhaseErr, when non-nil, is returned from
	// GetNamespacePhase regardless of the namespacePhases override.
	getNamespacePhaseErr error
	// deleteNamespaceWithTimeoutCalls records every namespace passed to
	// DeleteNamespaceWithTimeout; deleteNamespaceWithTimeoutBlock makes
	// the next call block until the matching release channel is closed.
	deleteNamespaceWithTimeoutCalls []string
	// deleteNamespaceWithTimeoutBlock, when non-nil, is closed by the
	// test to release a blocked DeleteNamespaceWithTimeout call. Used to
	// reproduce the "two concurrent /v1/teardown" race for the registry.
	deleteNamespaceWithTimeoutBlock chan struct{}
	// deleteNamespaceWithTimeoutErr, when non-nil, is returned from
	// DeleteNamespaceWithTimeout after the optional block elapses.
	deleteNamespaceWithTimeoutErr error
	waitRolloutErr                map[string]error // key: "ns/deploy" → per-deployment error
	waitRolloutCalls              []string         // recorded "ns/deploy" calls
	// waitRolloutBlock, when true, makes WaitForRollout block on ctx.Done or
	// the configured timeout instead of returning immediately. Used by
	// fast-fail watcher tests so the watcher has a chance to fire first.
	waitRolloutBlock bool
	// waitRolloutDelay, when non-zero, makes WaitForRollout sleep that long
	// (or until ctx is cancelled, whichever is first) before returning.
	waitRolloutDelay time.Duration
	// waitRolloutIgnoreCancel, when true with waitRolloutDelay > 0, makes
	// WaitForRollout sleep the full delay without responding to ctx
	// cancellation. Simulates `kubectl rollout status` taking seconds to
	// notice its parent context being cancelled — the exact behavior the
	// fast-fail path must not block on after detecting a pull failure.
	waitRolloutIgnoreCancel bool
	// deleteNamespaceDelay sleeps that long inside DeleteNamespace before
	// returning. Used by async-cleanup tests to verify Provision returns
	// without waiting for the slow kube call.
	deleteNamespaceDelay time.Duration
	// deleteNamespaceErr, when non-nil, is returned from DeleteNamespace
	// after deleteNamespaceDelay elapses. Independent of m.err so a test
	// can let other operations succeed but force DeleteNamespace to fail.
	deleteNamespaceErr error
	// waitRolloutInflight tracks the number of WaitForRollout calls
	// currently in flight; waitRolloutMaxInflight records the peak.
	// Used by the parallel-wait concurrency-cap test.
	waitRolloutInflight    int
	waitRolloutMaxInflight int
}

type createdNS struct {
	name   string
	labels map[string]string
}

func newMockKube() *mockKubeClient {
	return &mockKubeClient{
		resources:        make(map[string]string),
		tokenResponses:   make(map[string]string),
		namespacePhases:  make(map[string]string),
		clusterServerURL: "https://127.0.0.1:6443",
		clusterCAData:    "dGVzdC1jYQ==",
	}
}

func (m *mockKubeClient) CreateNamespace(_ context.Context, name string, labels map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.createdNamespaces = append(m.createdNamespaces, createdNS{name: name, labels: labels})
	return nil
}

func (m *mockKubeClient) DeleteNamespace(ctx context.Context, name string) error {
	if delay := m.deleteNamespaceDelay; delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.deleteNamespaceErr != nil {
		return m.deleteNamespaceErr
	}
	m.deletedNamespaces = append(m.deletedNamespaces, name)
	return nil
}

// deletedNamespacesSnapshot returns a copy of the deletedNamespaces slice
// under the mock's lock. Callers in tests use it to read the slice from a
// goroutine other than the one that wrote to it (async cleanup tests).
func (m *mockKubeClient) deletedNamespacesSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.deletedNamespaces))
	copy(out, m.deletedNamespaces)
	return out
}

// maxConcurrentWaitRollout returns the peak WaitForRollout concurrency
// observed during the test. Used to assert the parallel-wait concurrency
// cap is honored.
func (m *mockKubeClient) maxConcurrentWaitRollout() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.waitRolloutMaxInflight
}

func (m *mockKubeClient) GetResource(_ context.Context, kind, namespace, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	return m.resources[kind+"/"+namespace+"/"+name], nil
}

func (m *mockKubeClient) ListResources(_ context.Context, kind, namespace string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	key := kind + "/" + namespace
	if v, ok := m.resources[key]; ok {
		return v, nil
	}
	return `{"items":[]}`, nil
}

func (m *mockKubeClient) ApplyYAML(_ context.Context, manifest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.applyYAMLErr != nil {
		return m.applyYAMLErr
	}
	m.appliedManifests = append(m.appliedManifests, manifest)
	return nil
}

// DeleteNamespaceWithTimeout records the call and returns the configured
// outcome. When deleteNamespaceWithTimeoutBlock is non-nil, the call
// blocks until the channel is closed (or ctx fires); this lets tests
// reproduce "two concurrent /v1/teardown calls" without real time.
func (m *mockKubeClient) DeleteNamespaceWithTimeout(ctx context.Context, name string, _ time.Duration) error {
	m.mu.Lock()
	m.deleteNamespaceWithTimeoutCalls = append(m.deleteNamespaceWithTimeoutCalls, name)
	block := m.deleteNamespaceWithTimeoutBlock
	resultErr := m.deleteNamespaceWithTimeoutErr
	mErr := m.err
	m.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if mErr != nil {
		return mErr
	}
	if resultErr != nil {
		return resultErr
	}
	m.mu.Lock()
	m.deletedNamespaces = append(m.deletedNamespaces, name)
	m.mu.Unlock()
	return nil
}

// GetNamespacePhase returns the override for a specific namespace (or
// empty string when not set, matching the "does not exist" contract).
func (m *mockKubeClient) GetNamespacePhase(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getNamespacePhaseErr != nil {
		return "", m.getNamespacePhaseErr
	}
	if m.err != nil {
		return "", m.err
	}
	return m.namespacePhases[name], nil
}

// deleteNamespaceWithTimeoutCallsSnapshot is a goroutine-safe getter for
// the recorded DeleteNamespaceWithTimeout calls.
func (m *mockKubeClient) deleteNamespaceWithTimeoutCallsSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.deleteNamespaceWithTimeoutCalls))
	copy(out, m.deleteNamespaceWithTimeoutCalls)
	return out
}

func (m *mockKubeClient) GetClusterConfig(_ context.Context) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", "", m.err
	}
	return m.clusterServerURL, m.clusterCAData, nil
}

func (m *mockKubeClient) TokenForServiceAccount(_ context.Context, namespace, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	return m.tokenResponses[namespace+"/"+name], nil
}

func (m *mockKubeClient) WaitForRollout(ctx context.Context, namespace, deployment string, timeout time.Duration) error {
	key := namespace + "/" + deployment
	m.mu.Lock()
	m.waitRolloutCalls = append(m.waitRolloutCalls, key)
	m.waitRolloutInflight++
	if m.waitRolloutInflight > m.waitRolloutMaxInflight {
		m.waitRolloutMaxInflight = m.waitRolloutInflight
	}
	rolloutErr := m.waitRolloutErr
	delay := m.waitRolloutDelay
	ignoreCancel := m.waitRolloutIgnoreCancel
	block := m.waitRolloutBlock
	mErr := m.err
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.waitRolloutInflight--
		m.mu.Unlock()
	}()
	resolveErr := func() error {
		if rolloutErr != nil {
			if err, ok := rolloutErr[key]; ok {
				return err
			}
		}
		return mErr
	}
	switch {
	case delay > 0:
		if ignoreCancel {
			time.Sleep(delay)
			return resolveErr()
		}
		select {
		case <-time.After(delay):
			return resolveErr()
		case <-ctx.Done():
			return ctx.Err()
		}
	case block:
		select {
		case <-time.After(timeout):
			return resolveErr()
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		return resolveErr()
	}
}
