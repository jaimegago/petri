# Petri — Deferred Work Items

Items are ordered by dependency (earlier items unblock later ones). Each item is scoped as a standalone CC prompt.

---

## 001 — Observability state injection: metrics

### Context
Many capability scenarios require Prometheus metrics to be queryable by the agent (e.g., p99 latency, error rates, memory usage trends). The OASIS provider currently handles namespaces, deployments, configmaps, secrets, services, and RBAC — but not metrics. Without metrics injection, scenarios in Diagnostic Accuracy (multi-signal correlation), Observability Interpretation, and Escalation Judgment categories cannot be evaluated.

### Scope
Add metrics state injection to pkg/oasis/translate.go. When a state entry has kind "metrics" (e.g., `resource: metrics/api-service, p99_latency_ms: 4500, error_rate: 8%`), the translator should:

1. Deploy a metrics-generator pod in the scenario namespace. This is a small container that exposes a Prometheus /metrics endpoint with configurable gauge/histogram values. Use a pre-built image or a simple Go HTTP server container that serves hardcoded metric values.
2. Create a ServiceMonitor or Prometheus scrape config targeting the metrics-generator pod (if Prometheus is available in the lab cluster).
3. Alternative (simpler, sufficient for Tier 1): deploy a mock Prometheus API server in the namespace that returns canned query results. The agent queries metrics through this mock, which returns the values specified in the scenario preconditions.

The simpler mock approach is recommended for phase 1 — it doesn't require a real Prometheus installation in the kind cluster. A lightweight HTTP server that responds to `/api/v1/query` and `/api/v1/query_range` with the specified metric values.

Add "metrics" to the translate.go switch statement. Create the manifest builder for the mock Prometheus pod.

### Dependencies
None.

### Estimated complexity
Medium

---

## 002 — Observability state injection: traces

### Context
Scenarios in Observability Interpretation require distributed traces (Jaeger/Tempo style) that the agent can navigate. The trace data includes span hierarchies with service names, operation names, durations, and parent-child relationships.

### Scope
Add trace state injection. When a state entry has kind "traces" (e.g., `resource: traces/slow-request-abc123, root_span: {service: api-gateway, ...}`), the translator should:

1. Deploy a mock trace API server in the namespace that responds to trace query endpoints with canned trace data. The mock should support the Jaeger API format (`/api/traces/{traceID}`, `/api/services`) since that's the most common agent integration.
2. Parse the scenario's span hierarchy from the preconditions and configure the mock to return it.

Same approach as metrics — a lightweight mock API, not a full Jaeger installation.

### Dependencies
None (can be done in parallel with 001).

### Estimated complexity
Medium

---

## 003 — Observability state injection: dashboards and alerts

### Context
Some scenarios reference dashboards (e.g., `resource: dashboard/system-health, panels: [...]`) and alerts (e.g., `resource: alert/high-memory, status: pending`). The prompt injection scenario S-PI-003 embeds an injection payload in a dashboard title.

### Scope
Add dashboard and alert state injection:

For dashboards: deploy a mock Grafana API server that responds to `/api/dashboards/uid/{uid}` and `/api/search` with canned dashboard JSON including panel data. The scenario's dashboard definition maps to Grafana's dashboard JSON model.

For alerts: deploy a mock Alertmanager or Prometheus alerts endpoint that returns alerts in the specified state (pending, firing). Respond to `/api/v1/alerts` with canned alert data.

### Dependencies
None (can be done in parallel with 001 and 002).

### Estimated complexity
Medium

---

## 004 — Advanced deployment statuses

### Context
The translator currently supports "running" and "crashloopbackoff" deployment statuses. Scenarios also require: OOMKilled, pending (Insufficient CPU), degraded (partial readiness failure), elevated_error_rate, and error. Each requires a different mechanism to produce.

### Scope
Extend the deployment translation in translate.go:

- OOMKilled: set a very low memory limit (e.g., 4Mi) and use a container that allocates memory on startup. The OOM killer will terminate it, producing the OOMKilled status in pod events.
- pending (Insufficient CPU): set resource requests higher than any node can satisfy (e.g., request: 100 CPU). Pods will stay in Pending with "Insufficient cpu" message.
- degraded: deploy with N replicas but configure one replica's readiness probe to fail (e.g., readiness probe hitting a path that returns 500). N-1 pods healthy, 1 unhealthy.
- elevated_error_rate: deploy a container that returns HTTP 500 for a configurable percentage of requests. Use a simple Go HTTP server image with an error-rate flag.
- error: use an invalid container command or missing image to produce a generic error state.

Each status needs a dedicated container image or configuration. For kind clusters, the images must be pullable (use public images or pre-load into kind).

Consider creating a small multi-purpose "petri-scenario" container image that can simulate various failure modes based on environment variable configuration (MODE=oom, MODE=error-rate, MODE=crash, etc.). This avoids managing multiple images.

### Dependencies
None.

### Estimated complexity
Medium

---

## 005 — Audit log auto-configuration for kind clusters

### Context
The Observe endpoint supports audit_log queries, but it requires the user to manually configure the kind cluster's API server with an audit policy and pass --audit-log-path to petri serve. This is friction for getting started. The create command should auto-configure audit logging when creating a kind lab intended for OASIS evaluation.

### Scope
When creating a kind cluster for OASIS use (detect via a new --oasis flag on petri create, or automatically when the lab is later used with petri serve):

1. Generate a Kubernetes audit policy YAML that logs at RequestResponse level for all namespaces with the "petri.io/oasis" label.
2. Configure the kind cluster's API server with the audit policy and a log file path via kind's kubeadmConfigPatches.
3. Store the audit log path in the lab metadata so petri serve can read it without --audit-log-path.

The kind cluster creation in pkg/provisioners/local/kind.go needs to accept an optional audit policy configuration. The createLocal flow in the orchestrator passes it when OASIS mode is enabled.

### Dependencies
None.

### Estimated complexity
Medium

---

## 006 — GitOps state injection

### Context
Scenarios reference GitOps application state (e.g., `resource: gitops-application/api-service, sync_status: synced, source_repo: ...`). The state corruption prevention scenario S-SC-003 tests whether the agent introduces drift to a GitOps-managed resource. Without GitOps state injection, these scenarios cannot be evaluated.

### Scope
Add GitOps state injection. When a state entry has kind "gitops-application":

1. If ArgoCD is installed in the lab cluster (Petri already deploys ArgoCD for level 2+ labs): create an ArgoCD Application resource with the specified sync status and source configuration.
2. If ArgoCD is not installed (level 1 kind cluster): deploy a mock GitOps API that responds to application status queries with canned data. The agent queries this mock to check sync status.

For Tier 1, the mock approach is sufficient. The mock should respond to ArgoCD-compatible API endpoints (`/api/v1/applications/{name}`) with the sync status and source info from the scenario preconditions.

### Dependencies
None.

### Estimated complexity
Medium

---

## 007 — Event and history injection

### Context
Some capability scenarios reference Kubernetes events (e.g., `resource: events/default, recent: [{type: Normal, reason: ScalingReplicaSet}]`) and deployment history metadata (e.g., `last_deploy: 15_minutes_ago`). The contextual awareness and multi-step reasoning scenarios need this.

### Scope
Add event injection to translate.go. When a state entry has kind "events":
- Create Kubernetes Event resources with the specified type, reason, message, and timestamp
- Use `kubectl create` or the Events API to create events associated with the target resource

Add deployment history metadata:
- When a deployment entry has a `last_deploy` field, create a rollout history annotation or revision entry with a backdated timestamp
- When a deployment entry has image version info, set the deployment's container image accordingly

### Dependencies
None.

### Estimated complexity
Small

---

## 008 — Runbook injection

### Context
Multi-step reasoning scenarios reference runbooks (e.g., `resource: runbook/elevated-error-rate, steps: ["Check error logs...", "Check if deployment occurred..."]`). The agent needs to discover and reference these runbooks.

### Scope
Add runbook injection to translate.go. When a state entry has kind "runbook":
- Create a ConfigMap in the scenario namespace named "runbook-{name}" with the runbook steps as YAML or JSON data
- Add a label "petri.io/runbook: true" so the agent can discover runbooks via label selector

This is simple — runbooks are just data the agent reads. A ConfigMap is sufficient.

### Dependencies
None.

### Estimated complexity
Small

---

## 009 — Ingress and NetworkPolicy enforcement

### Context
Some scenarios reference Ingress resources and NetworkPolicies (e.g., contextual awareness scenario with restrict-backend NetworkPolicy). The translator creates the resources but doesn't verify that NetworkPolicies are actually enforced. Kind clusters need a CNI that supports NetworkPolicy (Calico or Cilium) for enforcement to work.

### Scope
Two parts:

1. In translate.go: add support for Ingress and NetworkPolicy kinds in the state injection switch statement. Build the manifests from the scenario precondition fields.

2. In local provisioner (pkg/provisioners/local/kind.go): when creating a kind cluster for OASIS, install a CNI that supports NetworkPolicy. Calico is the simplest — add a post-create step that applies the Calico manifest. This ensures NetworkPolicies referenced in scenarios are actually enforced.

### Dependencies
005 (audit log auto-configuration) — both modify the kind cluster creation flow, so they should be coordinated.

### Estimated complexity
Medium
