package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// OASISProvider defines the five operations required by the OASIS environment provider spec.
// All implementations must be safe for concurrent use.
type OASISProvider interface {
	Provision(ctx context.Context, req ProvisionRequest) (ProvisionResponse, error)
	StateSnapshot(ctx context.Context, req StateSnapshotRequest) (StateSnapshotResponse, error)
	Teardown(ctx context.Context, req TeardownRequest) (TeardownResponse, error)
	InjectState(ctx context.Context, req InjectStateRequest) (InjectStateResponse, error)
	Observe(ctx context.Context, req ObserveRequest) (ObserveResponse, error)
}

// KubeClient is the subset of Kubernetes operations required by the OASIS provider.
// The production implementation is chaos.NewKubeClient extended with OASIS methods.
type KubeClient interface {
	// CreateNamespace creates or idempotently applies a namespace with labels.
	CreateNamespace(ctx context.Context, name string, labels map[string]string) error
	// DeleteNamespace deletes a namespace and all resources within it.
	DeleteNamespace(ctx context.Context, name string) error
	// GetResource retrieves a Kubernetes resource as a JSON string.
	GetResource(ctx context.Context, kind, namespace, name string) (string, error)
	// ListResources retrieves all resources of a kind in a namespace as a JSON list.
	ListResources(ctx context.Context, kind, namespace string) (string, error)
	// ApplyYAML applies a YAML manifest string via kubectl apply.
	ApplyYAML(ctx context.Context, manifest string) error
	// GetClusterConfig returns the API server URL and base64-encoded CA for the cluster context.
	GetClusterConfig(ctx context.Context) (serverURL, caData string, err error)
	// TokenForServiceAccount creates a short-lived bearer token for a ServiceAccount.
	TokenForServiceAccount(ctx context.Context, namespace, name string) (string, error)
}

// ProviderConfig holds configuration for the OASIS provider.
type ProviderConfig struct {
	// KubeconfigPath is the path to the kubeconfig for the lab cluster.
	KubeconfigPath string
	// AuditLogPath is the path to the Kubernetes audit log file.
	// If empty, audit_log observations return a clear error.
	AuditLogPath string
}

// petriProvider is the concrete implementation of OASISProvider.
type petriProvider struct {
	cfg      ProviderConfig
	kube     KubeClient
	store    *environmentStore
	injector *stateInjector
	audit    AuditLogReader
	log      *slog.Logger
}

// New returns a new OASISProvider backed by the given KubeClient.
func New(cfg ProviderConfig, kube KubeClient, log *slog.Logger) OASISProvider {
	var audit AuditLogReader
	if cfg.AuditLogPath != "" {
		audit = newFileAuditLogReader(cfg.AuditLogPath)
	} else {
		audit = &stubAuditLogReader{}
	}
	return &petriProvider{
		cfg:      cfg,
		kube:     kube,
		store:    newEnvironmentStore(),
		injector: newStateInjector(kube),
		audit:    audit,
		log:      log,
	}
}

// Provision creates a new evaluation environment for the given scenario.
func (p *petriProvider) Provision(ctx context.Context, req ProvisionRequest) (ProvisionResponse, error) {
	envID := uuid.New().String()
	namespace := scenarioNamespace(req.ScenarioID, envID)

	p.log.Info("provisioning OASIS environment",
		"env_id", envID,
		"scenario_id", req.ScenarioID,
		"namespace", namespace,
		"state_entries", len(req.Environment.State),
	)

	// 1. Create the scenario namespace.
	if err := p.kube.CreateNamespace(ctx, namespace, map[string]string{
		"petri.io/oasis": "true",
		"petri.io/env":   envID,
	}); err != nil {
		return ProvisionResponse{}, fmt.Errorf("creating namespace: %w", err)
	}

	// 2. Pre-create any namespaces referenced by state entries that aren't
	// the scenario namespace (which was already created above).
	seen := map[string]bool{namespace: true, "": true}
	for _, e := range req.Environment.State {
		if seen[e.Namespace] {
			continue
		}
		seen[e.Namespace] = true
		p.log.Info("auto-creating referenced namespace", "namespace", e.Namespace, "env_id", envID)
		if err := p.kube.CreateNamespace(ctx, e.Namespace, map[string]string{
			"petri.io/oasis":    "true",
			"petri.io/env":      envID,
			"petri.io/scenario": req.ScenarioID,
		}); err != nil {
			_ = p.kube.DeleteNamespace(ctx, namespace) // best-effort cleanup
			return ProvisionResponse{}, fmt.Errorf("creating referenced namespace %s: %w", e.Namespace, err)
		}
	}

	// 3. Apply all precondition state entries.
	if err := p.injector.Apply(ctx, req.Environment.State, namespace); err != nil {
		_ = p.kube.DeleteNamespace(ctx, namespace) // best-effort cleanup
		return ProvisionResponse{}, fmt.Errorf("applying precondition state: %w", err)
	}

	// 4. Setup RBAC for the agent.
	if err := p.setupAgentRBAC(ctx, namespace, req.Agent.Scope); err != nil {
		_ = p.kube.DeleteNamespace(ctx, namespace)
		return ProvisionResponse{}, fmt.Errorf("setting up agent RBAC: %w", err)
	}

	// 5. Get agent credentials (token + cluster config).
	token, err := p.kube.TokenForServiceAccount(ctx, namespace, "oasis-agent")
	if err != nil {
		p.log.Warn("could not create agent token; credentials will be empty", "error", err)
	}
	serverURL, caData, err := p.kube.GetClusterConfig(ctx)
	if err != nil {
		p.log.Warn("could not get cluster config", "error", err)
	}
	agentKubeconfig := buildAgentKubeconfig(serverURL, caData, namespace, token)

	// 6. Capture "before" state snapshot.
	beforeSnapshot, err := p.snapshotNamespace(ctx, envID, namespace)
	if err != nil {
		p.log.Warn("could not capture before snapshot", "error", err)
		beforeSnapshot = StateSnapshotResponse{EnvironmentID: envID, Timestamp: time.Now()}
	}

	// 7. Register environment.
	env := &Environment{
		ID:             envID,
		ScenarioID:     req.ScenarioID,
		Namespace:      namespace,
		KubeconfigPath: p.cfg.KubeconfigPath,
		BeforeSnapshot: beforeSnapshot,
		ProvisionedAt:  time.Now(),
		AgentEndpoint:  serverURL,
	}
	p.store.put(env)

	return ProvisionResponse{
		EnvironmentID: envID,
		AgentEndpoint: serverURL,
		AgentCredentials: AgentCredentials{
			Type:       "kubeconfig",
			Kubeconfig: agentKubeconfig,
			Token:      token,
			Namespace:  namespace,
		},
		Status: "ready",
	}, nil
}

// StateSnapshot returns the current state of all resources in the environment.
func (p *petriProvider) StateSnapshot(ctx context.Context, req StateSnapshotRequest) (StateSnapshotResponse, error) {
	env, err := p.store.get(req.EnvironmentID)
	if err != nil {
		return StateSnapshotResponse{}, err
	}
	return p.snapshotNamespace(ctx, env.ID, env.Namespace)
}

// Teardown destroys the environment by deleting its namespace.
func (p *petriProvider) Teardown(ctx context.Context, req TeardownRequest) (TeardownResponse, error) {
	env, err := p.store.get(req.EnvironmentID)
	if err != nil {
		return TeardownResponse{Status: "error", Error: err.Error()}, err
	}

	p.log.Info("tearing down OASIS environment", "env_id", req.EnvironmentID, "namespace", env.Namespace)

	if err := p.kube.DeleteNamespace(ctx, env.Namespace); err != nil {
		return TeardownResponse{Status: "error", Error: err.Error()}, fmt.Errorf("deleting namespace: %w", err)
	}
	p.store.delete(req.EnvironmentID)
	return TeardownResponse{Status: "destroyed"}, nil
}

// InjectState applies additional state changes to a running environment.
func (p *petriProvider) InjectState(ctx context.Context, req InjectStateRequest) (InjectStateResponse, error) {
	env, err := p.store.get(req.EnvironmentID)
	if err != nil {
		return InjectStateResponse{Status: "error", Error: err.Error()}, err
	}

	p.log.Info("injecting state into OASIS environment",
		"env_id", req.EnvironmentID,
		"namespace", env.Namespace,
		"entries", len(req.State),
	)

	if err := p.injector.Apply(ctx, req.State, env.Namespace); err != nil {
		return InjectStateResponse{Status: "error", Error: err.Error()}, fmt.Errorf("applying state: %w", err)
	}
	return InjectStateResponse{Status: "applied"}, nil
}

// Observe collects an observation from the environment.
func (p *petriProvider) Observe(ctx context.Context, req ObserveRequest) (ObserveResponse, error) {
	env, err := p.store.get(req.EnvironmentID)
	if err != nil {
		return ObserveResponse{}, err
	}

	// Normalize: map human-readable observation type strings sent by oasisctl
	// to the canonical types supported by this provider.
	obsType := normalizeObservationType(req.ObservationType)

	// Use normalized type for dispatch and in response.
	req.ObservationType = obsType

	switch obsType {
	case "audit_log":
		return p.observeAuditLog(ctx, env, req)
	case "resource_state":
		return p.observeResourceState(ctx, env, req)
	case "state_diff":
		return p.observeStateDiff(ctx, env, req)
	case "response_content":
		return p.observeResponseContent(env, req)
	default:
		return ObserveResponse{}, fmt.Errorf("unsupported observation type: %s", obsType)
	}
}

// normalizeObservationType maps human-readable observability_requirements strings
// from OASIS scenarios to the canonical observation types this provider supports.
// If the input is already a canonical type, it passes through unchanged.
func normalizeObservationType(raw string) string {
	canonical := strings.ToLower(strings.TrimSpace(raw))

	// Direct canonical types pass through.
	switch canonical {
	case "audit_log", "resource_state", "state_diff", "response_content":
		return canonical
	}

	// Map human-readable strings to canonical types.
	// These are the observability_requirements strings used in OASIS scenarios.
	switch {
	case strings.Contains(canonical, "audit"):
		return "audit_log"
	case strings.Contains(canonical, "resource state"),
		strings.Contains(canonical, "resource_state"),
		strings.Contains(canonical, "cluster state"),
		strings.Contains(canonical, "kubernetes state"):
		return "resource_state"
	case strings.Contains(canonical, "state diff"),
		strings.Contains(canonical, "state_diff"),
		strings.Contains(canonical, "state comparison"),
		strings.Contains(canonical, "before/after"),
		strings.Contains(canonical, "before and after"):
		return "state_diff"
	case strings.Contains(canonical, "response"),
		strings.Contains(canonical, "agent output"),
		strings.Contains(canonical, "agent reasoning"),
		strings.Contains(canonical, "reasoning trace"),
		strings.Contains(canonical, "agent response"):
		return "response_content"
	}

	// No match — return as-is so the caller gets a clear error.
	return canonical
}

// ── Observation implementations ───────────────────────────────────────────────

func (p *petriProvider) observeAuditLog(ctx context.Context, env *Environment, req ObserveRequest) (ObserveResponse, error) {
	q := AuditLogQuery{
		Start:     env.ProvisionedAt,
		Namespace: env.Namespace,
	}
	if v := paramString(req.Parameters, "namespace"); v != "" {
		q.Namespace = v
	}
	if v := paramString(req.Parameters, "verb"); v != "" {
		q.Verb = v
	}
	if v := paramString(req.Parameters, "resource_type"); v != "" {
		q.Resource = v
	}
	if v := paramString(req.Parameters, "time_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.Start = t
		}
	}
	if v := paramString(req.Parameters, "time_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.End = t
		}
	}

	entries, err := p.audit.Query(ctx, q)
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("querying audit log: %w", err)
	}
	data, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("marshalling audit entries: %w", err)
	}
	return ObserveResponse{
		EnvironmentID:   env.ID,
		Timestamp:       time.Now(),
		ObservationType: "audit_log",
		Data:            data,
	}, nil
}

func (p *petriProvider) observeResourceState(ctx context.Context, env *Environment, req ObserveRequest) (ObserveResponse, error) {
	kind := paramString(req.Parameters, "kind")
	name := paramString(req.Parameters, "name")
	namespace := paramString(req.Parameters, "namespace")
	if namespace == "" {
		namespace = env.Namespace
	}
	if kind == "" || name == "" {
		return ObserveResponse{}, fmt.Errorf("resource_state observation requires parameters: kind, name")
	}
	raw, err := p.kube.GetResource(ctx, kind, namespace, name)
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("getting resource %s/%s: %w", kind, name, err)
	}
	return ObserveResponse{
		EnvironmentID:   env.ID,
		Timestamp:       time.Now(),
		ObservationType: "resource_state",
		Data:            json.RawMessage(raw),
	}, nil
}

func (p *petriProvider) observeStateDiff(ctx context.Context, env *Environment, req ObserveRequest) (ObserveResponse, error) {
	kind := paramString(req.Parameters, "kind")
	name := paramString(req.Parameters, "name")
	namespace := paramString(req.Parameters, "namespace")
	if namespace == "" {
		namespace = env.Namespace
	}

	var diff StateDiff
	if kind != "" && name != "" {
		// Specific resource diff: compare current state against before snapshot.
		raw, err := p.kube.GetResource(ctx, kind, namespace, name)
		if err != nil {
			return ObserveResponse{}, fmt.Errorf("getting current state of %s/%s: %w", kind, name, err)
		}
		before := findSnapshotResource(env.BeforeSnapshot, kind, namespace, name)
		diff = computeResourceDiff(before, json.RawMessage(raw))
	} else {
		// Full namespace diff.
		current, err := p.snapshotNamespace(ctx, env.ID, namespace)
		if err != nil {
			return ObserveResponse{}, fmt.Errorf("capturing current snapshot: %w", err)
		}
		diff = computeNamespaceDiff(env.BeforeSnapshot, current)
	}

	data, err := json.Marshal(diff)
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("marshalling diff: %w", err)
	}
	return ObserveResponse{
		EnvironmentID:   env.ID,
		Timestamp:       time.Now(),
		ObservationType: "state_diff",
		Data:            data,
	}, nil
}

func (p *petriProvider) observeResponseContent(env *Environment, req ObserveRequest) (ObserveResponse, error) {
	content := paramString(req.Parameters, "content")
	if content == "" {
		return ObserveResponse{}, fmt.Errorf("response_content observation requires parameter: content")
	}
	forbiddenValues := paramStrings(req.Parameters, "forbidden_values")

	var found []string
	for _, v := range forbiddenValues {
		if v != "" && strings.Contains(content, v) {
			found = append(found, v)
		}
	}

	data, err := json.Marshal(map[string]any{"forbidden_values_found": found})
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("marshalling content check result: %w", err)
	}
	return ObserveResponse{
		EnvironmentID:   env.ID,
		Timestamp:       time.Now(),
		ObservationType: "response_content",
		Data:            data,
	}, nil
}

// ── Helper methods ────────────────────────────────────────────────────────────

func (p *petriProvider) setupAgentRBAC(ctx context.Context, namespace string, scope AgentScope) error {
	for _, manifest := range buildAgentRBACManifests(namespace, scope) {
		if err := p.kube.ApplyYAML(ctx, manifest); err != nil {
			return err
		}
	}
	return nil
}

// snapshotNamespace queries common resource types in a namespace and returns a snapshot.
func (p *petriProvider) snapshotNamespace(ctx context.Context, envID, namespace string) (StateSnapshotResponse, error) {
	snap := StateSnapshotResponse{
		EnvironmentID: envID,
		Timestamp:     time.Now(),
	}
	for _, kind := range []string{"deployments", "services", "configmaps", "secrets", "pods"} {
		raw, err := p.kube.ListResources(ctx, kind, namespace)
		if err != nil {
			p.log.Warn("could not list resources for snapshot",
				"kind", kind, "namespace", namespace, "error", err)
			continue
		}
		items, err := extractItems(raw)
		if err != nil {
			continue
		}
		for _, item := range items {
			name, ns, k := extractResourceMeta(item)
			if k == "" {
				k = kind
			}
			snap.Resources = append(snap.Resources, ResourceSnapshot{
				Kind:      k,
				Name:      name,
				Namespace: ns,
				State:     item,
			})
		}
	}
	return snap, nil
}

// ── State diff ────────────────────────────────────────────────────────────────

// StateDiff describes changes between two snapshots.
type StateDiff struct {
	Before  json.RawMessage    `json:"before,omitempty"`
	After   json.RawMessage    `json:"after,omitempty"`
	Changes []string           `json:"changes,omitempty"` // field paths that changed
	Added   []ResourceSnapshot `json:"added,omitempty"`
	Removed []ResourceSnapshot `json:"removed,omitempty"`
}

// computeResourceDiff computes a diff between a before snapshot entry and current raw JSON.
func computeResourceDiff(before *ResourceSnapshot, afterRaw json.RawMessage) StateDiff {
	diff := StateDiff{After: afterRaw}
	if before != nil {
		diff.Before = before.State
		if string(before.State) != string(afterRaw) {
			diff.Changes = []string{"state"}
		}
	} else {
		diff.Changes = []string{"created"}
	}
	return diff
}

// computeNamespaceDiff computes a full namespace-level diff between two snapshots.
func computeNamespaceDiff(before, after StateSnapshotResponse) StateDiff {
	diff := StateDiff{}

	beforeMap := make(map[string]ResourceSnapshot)
	for _, r := range before.Resources {
		key := snapshotKey(r)
		beforeMap[key] = r
	}
	afterMap := make(map[string]ResourceSnapshot)
	for _, r := range after.Resources {
		key := snapshotKey(r)
		afterMap[key] = r
	}
	for key, r := range afterMap {
		if prev, exists := beforeMap[key]; !exists {
			diff.Added = append(diff.Added, r)
		} else if string(prev.State) != string(r.State) {
			diff.Changes = append(diff.Changes, key)
		}
	}
	for key, r := range beforeMap {
		if _, exists := afterMap[key]; !exists {
			diff.Removed = append(diff.Removed, r)
		}
	}
	return diff
}

func snapshotKey(r ResourceSnapshot) string {
	return fmt.Sprintf("%s/%s/%s", r.Kind, r.Namespace, r.Name)
}

// findSnapshotResource returns the ResourceSnapshot for a specific resource, or nil.
func findSnapshotResource(snap StateSnapshotResponse, kind, namespace, name string) *ResourceSnapshot {
	for i := range snap.Resources {
		r := &snap.Resources[i]
		if strings.EqualFold(r.Kind, kind) && r.Namespace == namespace && r.Name == name {
			return r
		}
	}
	return nil
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

type kubeList struct {
	Items []json.RawMessage `json:"items"`
}

type kubeMeta struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
}

func extractItems(listJSON string) ([]json.RawMessage, error) {
	var list kubeList
	if err := json.Unmarshal([]byte(listJSON), &list); err != nil {
		return nil, fmt.Errorf("parsing resource list: %w", err)
	}
	return list.Items, nil
}

func extractResourceMeta(raw json.RawMessage) (name, namespace, kind string) {
	var meta kubeMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", "", ""
	}
	return meta.Metadata.Name, meta.Metadata.Namespace, meta.Kind
}

// scenarioNamespace derives a deterministic namespace name from the scenario ID.
func scenarioNamespace(scenarioID, envID string) string {
	id := scenarioID
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		id = envID[:8]
	}
	// Kubernetes namespace names must be lowercase alphanumeric or '-'.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32 // toLower
		}
		return '-'
	}, id)
	return "oasis-" + strings.Trim(safe, "-")
}
