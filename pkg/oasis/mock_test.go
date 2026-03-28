package oasis

import (
	"context"
	"io"
	"log/slog"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockKubeClient is a test double for KubeClient.
type mockKubeClient struct {
	appliedManifests  []string
	createdNamespaces []createdNS
	deletedNamespaces []string
	resources         map[string]string // key: "kind/ns/name" or "kind/ns" for list
	tokenResponses    map[string]string // key: "ns/sa"
	clusterServerURL  string
	clusterCAData     string
	err               error
}

type createdNS struct {
	name   string
	labels map[string]string
}

func newMockKube() *mockKubeClient {
	return &mockKubeClient{
		resources:        make(map[string]string),
		tokenResponses:   make(map[string]string),
		clusterServerURL: "https://127.0.0.1:6443",
		clusterCAData:    "dGVzdC1jYQ==",
	}
}

func (m *mockKubeClient) CreateNamespace(_ context.Context, name string, labels map[string]string) error {
	if m.err != nil {
		return m.err
	}
	m.createdNamespaces = append(m.createdNamespaces, createdNS{name: name, labels: labels})
	return nil
}

func (m *mockKubeClient) DeleteNamespace(_ context.Context, name string) error {
	if m.err != nil {
		return m.err
	}
	m.deletedNamespaces = append(m.deletedNamespaces, name)
	return nil
}

func (m *mockKubeClient) GetResource(_ context.Context, kind, namespace, name string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.resources[kind+"/"+namespace+"/"+name], nil
}

func (m *mockKubeClient) ListResources(_ context.Context, kind, namespace string) (string, error) {
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
	if m.err != nil {
		return m.err
	}
	m.appliedManifests = append(m.appliedManifests, manifest)
	return nil
}

func (m *mockKubeClient) GetClusterConfig(_ context.Context) (string, string, error) {
	if m.err != nil {
		return "", "", m.err
	}
	return m.clusterServerURL, m.clusterCAData, nil
}

func (m *mockKubeClient) TokenForServiceAccount(_ context.Context, namespace, name string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.tokenResponses[namespace+"/"+name], nil
}
