package oasis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jaimegago/petri/pkg/manifest"
	"github.com/jaimegago/petri/pkg/workloadstate"
)

// stateInjector translates OASIS StateEntry objects into Kubernetes operations.
type stateInjector struct {
	kube KubeClient
	// defaultImage is the OCI image used for Deployment and Pod state
	// entries that omit spec.image. It must be a fully-qualified, pinned
	// image hosted on a registry not backed by Cloudflare R2 (Docker Hub
	// blob storage relies on R2, which is null-routed by some networks).
	// See pkg/config.DefaultOASISImage.
	defaultImage string
	// utilImage is the OCI image used internally to construct intentionally
	// unhealthy or behavioural pod states (CrashLoopBackOff, OOMKilled,
	// log emission). Like defaultImage, it must not pull from Docker Hub.
	utilImage string
}

// defaultUtilImage is the busybox-equivalent hosted on registry.k8s.io.
// It is used for builders that need a small shell + sleep loop without
// making the choice configurable to scenarios.
const defaultUtilImage = "registry.k8s.io/e2e-test-images/busybox:1.37.0-2"

func newStateInjector(kube KubeClient, defaultImage string) *stateInjector {
	if defaultImage == "" {
		defaultImage = defaultOASISImage
	}
	return &stateInjector{
		kube:         kube,
		defaultImage: defaultImage,
		utilImage:    defaultUtilImage,
	}
}

// Apply translates and applies each state entry to the cluster.
func (si *stateInjector) Apply(ctx context.Context, entries []StateEntry, defaultNamespace string) error {
	for _, e := range entries {
		if err := si.applyEntry(ctx, e, defaultNamespace); err != nil {
			return fmt.Errorf("applying %s %s: %w", e.Kind, e.Name, err)
		}
	}
	return nil
}

func (si *stateInjector) applyEntry(ctx context.Context, e StateEntry, defaultNamespace string) error {
	ns := e.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	switch strings.ToLower(e.Kind) {
	case "namespace":
		return si.applyNamespace(ctx, e)
	case "deployment":
		return si.applyDeployment(ctx, e, ns)
	case "configmap":
		return si.applyConfigMap(ctx, e, ns)
	case "secret":
		return si.applySecret(ctx, e, ns)
	case "service":
		return si.applyService(ctx, e, ns)
	case "serviceaccount":
		return si.applyServiceAccount(ctx, e, ns)
	case "role":
		return si.applyRole(ctx, e, ns)
	case "rolebinding":
		return si.applyRoleBinding(ctx, e, ns)
	case "hpa", "horizontalpodautoscaler":
		return si.applyHPA(ctx, e, ns)
	case "pvc", "persistentvolumeclaim":
		return si.applyPVC(ctx, e, ns)
	case "pod":
		return si.applyPod(ctx, e, ns)
	case "dashboard":
		return si.applyDashboard(ctx, e, ns)
	case "gitops-application":
		return si.applyGitOpsApplication(ctx, e, ns)
	case "metrics":
		return si.applyMetrics(ctx, e, ns)
	case "traces":
		return si.applyTraces(ctx, e, ns)
	case "alert":
		return si.applyAlert(ctx, e, ns)
	case "events":
		return si.applyEvents(ctx, e, ns)
	case "runbook":
		return si.applyRunbook(ctx, e, ns)
	case "ingress":
		return si.applyIngress(ctx, e, ns)
	case "networkpolicy":
		return si.applyNetworkPolicy(ctx, e, ns)
	case "logs":
		return si.applyLogs(ctx, e, ns)
	default:
		return fmt.Errorf("unsupported state entry kind %q", e.Kind)
	}
}

func (si *stateInjector) applyNamespace(ctx context.Context, e StateEntry) error {
	labels := make(map[string]string, len(e.Labels)+1)
	for k, v := range e.Labels {
		labels[k] = v
	}
	if e.Zone != "" {
		labels["petri.oasis/zone"] = e.Zone
	}
	return si.kube.CreateNamespace(ctx, e.Name, labels)
}

// applyDeployment builds a workloadstate.Spec from the OASIS state entry and
// delegates to the workload-state capability, which renders and applies the
// born-into-state Deployment. The capability is fail-loud: a non-empty,
// unrecognized status surfaces here as a provision error rather than a
// silently healthy deployment. See ADR 0015.
func (si *stateInjector) applyDeployment(ctx context.Context, e StateEntry, namespace string) error {
	replicas := 1
	if v, ok := e.Spec["replicas"]; ok {
		switch r := v.(type) {
		case int:
			replicas = r
		case float64:
			replicas = int(r)
		}
	}

	image := si.defaultImage
	if v, ok := e.Spec["image"]; ok {
		if s, ok := v.(string); ok && s != "" {
			image = s
		}
	}

	// Raw status string; workloadstate normalizes case/aliases and treats an
	// omitted or empty value as running.
	status := ""
	if v, ok := e.Spec["status"]; ok {
		if s, ok := v.(string); ok {
			status = s
		}
	}

	managedBy := ""
	if v, ok := e.Spec["managed_by"]; ok {
		if s, ok := v.(string); ok {
			managedBy = s
		}
	}

	// Custom matchLabels for selector (defaults to {"app": name}).
	// If scenario labels contain keys that overlap (e.g. "app"), the scenario
	// value takes precedence so the selector and pod template always agree.
	matchLabels := map[string]string{"app": e.Name}
	if v, ok := e.Spec["matchLabels"].(map[string]any); ok {
		matchLabels = make(map[string]string, len(v))
		for mk, mv := range v {
			if s, ok := mv.(string); ok {
				matchLabels[mk] = s
			}
		}
	}
	for k, v := range e.Labels {
		matchLabels[k] = v
	}

	configMapRef := ""
	if v, ok := e.Spec["configMapRef"]; ok {
		if s, ok := v.(string); ok {
			configMapRef = s
		}
	}

	env, err := parseContainerEnv(e.Spec["containers"])
	if err != nil {
		return fmt.Errorf("deployment %q: %w", e.Name, err)
	}

	errorRate := 0
	if v, ok := e.Spec["error_rate"]; ok {
		switch r := v.(type) {
		case int:
			errorRate = r
		case float64:
			errorRate = int(r)
		}
	}

	return workloadstate.Provision(ctx, si.kube, workloadstate.Spec{
		Name:         e.Name,
		Namespace:    namespace,
		Replicas:     replicas,
		Image:        image,
		Labels:       e.Labels,
		Annotations:  e.Annotations,
		ManagedBy:    managedBy,
		MatchLabels:  matchLabels,
		State:        status,
		Env:          env,
		ConfigMapRef: configMapRef,
		ErrorRate:    errorRate,
		UtilImage:    si.utilImage,
	})
}

// parseContainerEnv reads the container environment a scenario declares on a
// deployment state entry's `containers` list, returning the first container's
// `env` as workloadstate EnvVars.
//
// Only the first container is read. Rendering is single-container (see
// writeDeploymentHead), so a second declared container has nowhere to go, and
// reading it would silently merge two environments into one.
//
// A malformed declaration is an error rather than a skipped entry. A declared
// environment variable that quietly materialises as nothing is the defect this
// parser exists to remove, and dropping one on a type assertion would
// reintroduce it one layer down.
func parseContainerEnv(raw any) ([]workloadstate.EnvVar, error) {
	if raw == nil {
		return nil, nil
	}
	containers, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a list", "containers")
	}
	if len(containers) == 0 {
		return nil, nil
	}
	first, ok := containers[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("containers[0] must be a mapping")
	}
	rawEnv, ok := first["env"]
	if !ok || rawEnv == nil {
		return nil, nil
	}
	entries, ok := rawEnv.([]any)
	if !ok {
		return nil, fmt.Errorf("containers[0].env must be a list")
	}

	out := make([]workloadstate.EnvVar, 0, len(entries))
	for i, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("containers[0].env[%d] must be a mapping", i)
		}
		name, ok := entry["name"].(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("containers[0].env[%d] must declare a non-empty %q", i, "name")
		}
		ev := workloadstate.EnvVar{Name: name}

		if vf, present := entry["valueFrom"]; present && vf != nil {
			ref, err := parseConfigMapKeyRef(vf)
			if err != nil {
				return nil, fmt.Errorf("containers[0].env[%d] (%s): %w", i, name, err)
			}
			ev.ConfigMapKeyRef = ref
			out = append(out, ev)
			continue
		}

		value, ok := entry["value"]
		if !ok {
			return nil, fmt.Errorf("containers[0].env[%d] (%s) must declare %q or %q", i, name, "value", "valueFrom")
		}
		ev.Value = fmt.Sprintf("%v", value)
		out = append(out, ev)
	}
	return out, nil
}

// parseConfigMapKeyRef reads an env entry's `valueFrom`. Only
// `configMapKeyRef` is supported; `secretKeyRef` and the other Kubernetes
// sources are not declared by the SI profile and are rejected rather than
// ignored.
func parseConfigMapKeyRef(raw any) (*workloadstate.ConfigMapKeySelector, error) {
	valueFrom, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a mapping", "valueFrom")
	}
	rawRef, ok := valueFrom["configMapKeyRef"]
	if !ok {
		return nil, fmt.Errorf("%q supports only %q", "valueFrom", "configMapKeyRef")
	}
	ref, ok := rawRef.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a mapping", "configMapKeyRef")
	}
	name, ok := ref["name"].(string)
	if !ok || name == "" {
		return nil, fmt.Errorf("%q must declare a non-empty %q", "configMapKeyRef", "name")
	}
	key, ok := ref["key"].(string)
	if !ok || key == "" {
		return nil, fmt.Errorf("%q must declare a non-empty %q", "configMapKeyRef", "key")
	}
	return &workloadstate.ConfigMapKeySelector{Name: name, Key: key}, nil
}

func (si *stateInjector) applyConfigMap(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildConfigMapManifest(e.Name, namespace, e.Data, e.Labels, e.Annotations)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applySecret(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildSecretManifest(e.Name, namespace, e.Data, e.Labels)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyService(ctx context.Context, e StateEntry, namespace string) error {
	port := 80
	if v, ok := e.Spec["port"]; ok {
		switch p := v.(type) {
		case int:
			port = p
		case float64:
			port = int(p)
		}
	}
	targetPort := port
	if v, ok := e.Spec["targetPort"]; ok {
		switch tp := v.(type) {
		case int:
			targetPort = tp
		case float64:
			targetPort = int(tp)
		}
	}
	manifest := buildServiceManifest(e.Name, namespace, port, targetPort, e.Labels)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyServiceAccount(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildServiceAccountManifest(e.Name, namespace)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyRole(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildRoleManifest(e.Name, namespace)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyRoleBinding(ctx context.Context, e StateEntry, namespace string) error {
	roleName, _ := e.Spec["roleName"].(string)
	saName, _ := e.Spec["serviceAccountName"].(string)
	saNS := namespace
	if v, ok := e.Spec["serviceAccountNamespace"]; ok {
		if s, ok := v.(string); ok && s != "" {
			saNS = s
		}
	}
	if roleName == "" || saName == "" {
		return fmt.Errorf("rolebinding %s: spec.roleName and spec.serviceAccountName are required", e.Name)
	}
	manifest := buildRoleBindingManifest(e.Name, namespace, roleName, saName, saNS)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyHPA(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildHPAManifest(e.Name, namespace, e.Spec, e.Labels)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyPVC(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildPVCManifest(e.Name, namespace, e.Spec, e.Labels)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyPod(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildPodManifest(e.Name, namespace, e.Spec, e.Labels, si.defaultImage)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyDashboard(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildDashboardConfigMap(e.Name, namespace, e.Spec, e.Data, e.Labels)
	return si.kube.ApplyYAML(ctx, manifest)
}

func (si *stateInjector) applyGitOpsApplication(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildGitOpsApplicationConfigMap(e.Name, namespace, e.Spec, e.Labels)
	return si.kube.ApplyYAML(ctx, manifest)
}

// ── YAML manifest builders ────────────────────────────────────────────────────
//
// Deployment manifests for born-into-state workloads (running and the
// unhealthy states) are produced by pkg/workloadstate; applyDeployment builds
// a workloadstate.Spec and delegates. The builders below cover the remaining
// OASIS state-entry kinds (ConfigMap, Secret, Service, RBAC, Pod, observability
// mocks, etc.).

func buildConfigMapManifest(name, namespace string, data, labels, annotations map[string]string) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(annotations) > 0 {
		sb.WriteString("  annotations:\n")
		sb.WriteString(manifest.LabelsToYAML(annotations, 4))
		sb.WriteString("\n")
	}
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(manifest.LabelsToYAML(labels, 4))
		sb.WriteString("\n")
	}
	if len(data) > 0 {
		sb.WriteString("data:\n")
		for k, v := range data {
			fmt.Fprintf(&sb, "  %s: %q\n", k, v)
		}
	}
	return sb.String()
}

func buildSecretManifest(name, namespace string, data, labels map[string]string) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: Secret\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(manifest.LabelsToYAML(labels, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("type: Opaque\n")
	if len(data) > 0 {
		sb.WriteString("data:\n")
		for k, v := range data {
			encoded := base64.StdEncoding.EncodeToString([]byte(v))
			fmt.Fprintf(&sb, "  %s: %s\n", k, encoded)
		}
	}
	return sb.String()
}

func buildServiceManifest(name, namespace string, port, targetPort int, labels map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    app: %s
  ports:
  - port: %d
    targetPort: %d
`, name, namespace, name, port, targetPort)
}

func buildServiceAccountManifest(name, namespace string) string {
	return fmt.Sprintf("apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: %s\n  namespace: %s\n", name, namespace)
}

func buildRoleManifest(name, namespace string) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %s
  namespace: %s
rules:
- apiGroups: ["", "apps", "extensions", "batch", "rbac.authorization.k8s.io"]
  resources: ["*"]
  verbs: ["*"]
`, name, namespace)
}

func buildRoleBindingManifest(name, namespace, roleName, saName, saNS string) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %s
  namespace: %s
subjects:
- kind: ServiceAccount
  name: %s
  namespace: %s
roleRef:
  kind: Role
  name: %s
  apiGroup: rbac.authorization.k8s.io
`, name, namespace, saName, saNS, roleName)
}

// buildAgentRBACManifests returns the ServiceAccount, Role, and RoleBinding manifests
// for the OASIS agent. The ServiceAccount is always created in the scenario namespace.
// If scope.Namespaces is non-empty, Role and RoleBinding are created in each scoped
// namespace instead of the scenario namespace. If scope.Zones is non-empty, zone
// annotations are added to the ServiceAccount.
func buildAgentRBACManifests(namespace string, scope AgentScope) []string {
	sa := buildServiceAccountManifest("oasis-agent", namespace)
	if len(scope.Zones) > 0 {
		sa = buildServiceAccountWithAnnotations("oasis-agent", namespace, map[string]string{
			"petri.oasis/zones": strings.Join(scope.Zones, ","),
		})
	}

	manifests := []string{sa}

	if len(scope.Namespaces) > 0 {
		// Create Role + RoleBinding in each scoped namespace.
		for _, ns := range scope.Namespaces {
			role := buildRoleManifest("oasis-agent-role", ns)
			binding := buildRoleBindingManifest("oasis-agent-binding", ns, "oasis-agent-role", "oasis-agent", namespace)
			manifests = append(manifests, role, binding)
		}
	} else {
		// Default: RBAC scoped to the scenario namespace.
		role := buildRoleManifest("oasis-agent-role", namespace)
		binding := buildRoleBindingManifest("oasis-agent-binding", namespace, "oasis-agent-role", "oasis-agent", namespace)
		manifests = append(manifests, role, binding)
	}
	return manifests
}

// buildServiceAccountWithAnnotations creates a ServiceAccount manifest with annotations.
func buildServiceAccountWithAnnotations(name, namespace string, annotations map[string]string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: %s\n  namespace: %s\n", name, namespace)
	if len(annotations) > 0 {
		sb.WriteString("  annotations:\n")
		sb.WriteString(manifest.LabelsToYAML(annotations, 4))
		sb.WriteString("\n")
	}
	return sb.String()
}

// buildAgentKubeconfig produces a minimal kubeconfig YAML for the OASIS agent.
func buildAgentKubeconfig(serverURL, caData, namespace, token string) string {
	if serverURL == "" || token == "" {
		return ""
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    certificate-authority-data: %s
  name: oasis-cluster
contexts:
- context:
    cluster: oasis-cluster
    user: oasis-agent
    namespace: %s
  name: oasis-context
current-context: oasis-context
users:
- name: oasis-agent
  user:
    token: %s
`, serverURL, caData, namespace, token)
}

// YAML label/annotation serialisation helpers live in pkg/manifest; the
// builders below call manifest.LabelsToYAML / manifest.MergeLabels.

// ── HPA manifest builder ─────────────────────────────────────────────────────

func buildHPAManifest(name, namespace string, spec map[string]any, labels map[string]string) string {
	targetRef := name // default: HPA targets a deployment with the same name
	if v, ok := spec["targetRef"].(string); ok && v != "" {
		targetRef = v
	}
	minReplicas := 1
	if v, ok := spec["minReplicas"]; ok {
		switch r := v.(type) {
		case int:
			minReplicas = r
		case float64:
			minReplicas = int(r)
		}
	}
	maxReplicas := 10
	if v, ok := spec["maxReplicas"]; ok {
		switch r := v.(type) {
		case int:
			maxReplicas = r
		case float64:
			maxReplicas = int(r)
		}
	}
	cpuTarget := 80
	if v, ok := spec["targetCPUUtilizationPercentage"]; ok {
		switch r := v.(type) {
		case int:
			cpuTarget = r
		case float64:
			cpuTarget = int(r)
		}
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: autoscaling/v2\nkind: HorizontalPodAutoscaler\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(manifest.LabelsToYAML(labels, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("spec:\n")
	sb.WriteString("  scaleTargetRef:\n")
	sb.WriteString("    apiVersion: apps/v1\n")
	sb.WriteString("    kind: Deployment\n")
	fmt.Fprintf(&sb, "    name: %s\n", targetRef)
	fmt.Fprintf(&sb, "  minReplicas: %d\n", minReplicas)
	fmt.Fprintf(&sb, "  maxReplicas: %d\n", maxReplicas)
	sb.WriteString("  metrics:\n")
	sb.WriteString("  - type: Resource\n")
	sb.WriteString("    resource:\n")
	sb.WriteString("      name: cpu\n")
	sb.WriteString("      target:\n")
	sb.WriteString("        type: Utilization\n")
	fmt.Fprintf(&sb, "        averageUtilization: %d\n", cpuTarget)
	return sb.String()
}

// ── PVC manifest builder ─────────────────────────────────────────────────────

func buildPVCManifest(name, namespace string, spec map[string]any, labels map[string]string) string {
	storage := "1Gi"
	if v, ok := spec["storage"].(string); ok && v != "" {
		storage = v
	}
	accessMode := "ReadWriteOnce"
	if v, ok := spec["accessMode"].(string); ok && v != "" {
		accessMode = v
	}
	storageClass := ""
	if v, ok := spec["storageClassName"].(string); ok {
		storageClass = v
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(manifest.LabelsToYAML(labels, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("spec:\n")
	sb.WriteString("  accessModes:\n")
	fmt.Fprintf(&sb, "  - %s\n", accessMode)
	if storageClass != "" {
		fmt.Fprintf(&sb, "  storageClassName: %s\n", storageClass)
	}
	sb.WriteString("  resources:\n")
	sb.WriteString("    requests:\n")
	fmt.Fprintf(&sb, "      storage: %s\n", storage)
	return sb.String()
}

// ── Pod manifest builder ─────────────────────────────────────────────────────

func buildPodManifest(name, namespace string, spec map[string]any, labels map[string]string, defaultImage string) string {
	image := defaultImage
	if image == "" {
		image = defaultOASISImage
	}
	if v, ok := spec["image"].(string); ok && v != "" {
		image = v
	}

	podLabels := manifest.MergeLabels(map[string]string{"app": name}, labels)

	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: Pod\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	sb.WriteString("  labels:\n")
	sb.WriteString(manifest.LabelsToYAML(podLabels, 4))
	sb.WriteString("\nspec:\n  containers:\n  - name: app\n")
	fmt.Fprintf(&sb, "    image: %s\n", image)

	// Support env vars from secrets (secretKeyRef).
	if envList, ok := spec["env"].([]any); ok && len(envList) > 0 {
		sb.WriteString("    env:\n")
		for _, envItem := range envList {
			envMap, ok := envItem.(map[string]any)
			if !ok {
				continue
			}
			envName, _ := envMap["name"].(string)
			if envName == "" {
				continue
			}
			// Direct value.
			if val, ok := envMap["value"].(string); ok {
				fmt.Fprintf(&sb, "    - name: %s\n      value: %q\n", envName, val)
				continue
			}
			// secretKeyRef.
			if ref, ok := envMap["secretKeyRef"].(map[string]any); ok {
				secretName, _ := ref["name"].(string)
				secretKey, _ := ref["key"].(string)
				if secretName != "" && secretKey != "" {
					fmt.Fprintf(&sb, "    - name: %s\n      valueFrom:\n        secretKeyRef:\n          name: %s\n          key: %s\n", envName, secretName, secretKey)
					continue
				}
			}
			// configMapKeyRef.
			if ref, ok := envMap["configMapKeyRef"].(map[string]any); ok {
				cmName, _ := ref["name"].(string)
				cmKey, _ := ref["key"].(string)
				if cmName != "" && cmKey != "" {
					fmt.Fprintf(&sb, "    - name: %s\n      valueFrom:\n        configMapKeyRef:\n          name: %s\n          key: %s\n", envName, cmName, cmKey)
					continue
				}
			}
		}
	}

	return sb.String()
}

// ── Dashboard manifest builder ───────────────────────────────────────────────
// Dashboards are stored as ConfigMaps with a well-known label so the agent can
// discover them. The data field holds the dashboard JSON (panels, title, etc.).

func buildDashboardConfigMap(name, namespace string, spec map[string]any, data, labels map[string]string) string {
	allLabels := manifest.MergeLabels(map[string]string{
		"petri.io/dashboard": "true",
		"grafana_dashboard":  "1",
	}, labels)

	// Build dashboard JSON from spec fields if no explicit data provided.
	dashData := make(map[string]string, len(data))
	for k, v := range data {
		dashData[k] = v
	}
	if len(dashData) == 0 {
		dashJSON := buildDashboardJSON(name, spec)
		dashData["dashboard.json"] = dashJSON
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: dashboard-%s\n  namespace: %s\n", name, namespace)
	sb.WriteString("  labels:\n")
	sb.WriteString(manifest.LabelsToYAML(allLabels, 4))
	sb.WriteString("\ndata:\n")
	for k, v := range dashData {
		fmt.Fprintf(&sb, "  %s: %q\n", k, v)
	}
	return sb.String()
}

func buildDashboardJSON(name string, spec map[string]any) string {
	title := name
	if v, ok := spec["title"].(string); ok && v != "" {
		title = v
	}
	uid := name
	if v, ok := spec["uid"].(string); ok && v != "" {
		uid = v
	}

	// Build panels array.
	var panelsJSON string
	if panels, ok := spec["panels"].([]any); ok {
		panelBytes, err := json.Marshal(panels)
		if err == nil {
			panelsJSON = string(panelBytes)
		}
	}
	if panelsJSON == "" {
		panelsJSON = "[]"
	}

	return fmt.Sprintf(`{"uid":%q,"title":%q,"panels":%s}`, uid, title, panelsJSON)
}

// ── GitOps application manifest builder ──────────────────────────────────────
// GitOps applications are stored as ConfigMaps with discovery labels. The data
// fields capture sync_status, source_repo, and other application metadata that
// the agent can query.

// ── Mock observability server builders ──────────────────────────────────────

// applyMetrics deploys a mock Prometheus API server that returns canned metric
// values specified in the state entry. The agent queries this mock at
// /api/v1/query and /api/v1/query_range.
func (si *stateInjector) applyMetrics(ctx context.Context, e StateEntry, namespace string) error {
	responses := buildMetricsResponses(e.Name, e.Spec)
	configMap := buildMockServerConfigMap("mock-prometheus-"+e.Name, namespace, "responses.json", responses, map[string]string{
		"petri.io/mock":    "prometheus",
		"petri.io/metrics": "true",
	})
	pod := buildMockServerPod("mock-prometheus-"+e.Name, namespace, "mock-prometheus-"+e.Name, 9090, mockPrometheusScript(), map[string]string{
		"app": "mock-prometheus-" + e.Name,
	})
	svc := buildServiceManifest("mock-prometheus-"+e.Name, namespace, 9090, 9090, map[string]string{
		"petri.io/mock": "prometheus",
	})

	for _, m := range []string{configMap, pod, svc} {
		if err := si.kube.ApplyYAML(ctx, m); err != nil {
			return fmt.Errorf("applying metrics mock for %s: %w", e.Name, err)
		}
	}
	return nil
}

// applyTraces deploys a mock Jaeger API server that returns canned trace data.
// The agent queries /api/traces/{traceID} and /api/services.
func (si *stateInjector) applyTraces(ctx context.Context, e StateEntry, namespace string) error {
	responses := buildTracesResponses(e.Name, e.Spec)
	configMap := buildMockServerConfigMap("mock-jaeger-"+e.Name, namespace, "responses.json", responses, map[string]string{
		"petri.io/mock":   "jaeger",
		"petri.io/traces": "true",
	})
	pod := buildMockServerPod("mock-jaeger-"+e.Name, namespace, "mock-jaeger-"+e.Name, 16686, mockJaegerScript(), map[string]string{
		"app": "mock-jaeger-" + e.Name,
	})
	svc := buildServiceManifest("mock-jaeger-"+e.Name, namespace, 16686, 16686, map[string]string{
		"petri.io/mock": "jaeger",
	})

	for _, m := range []string{configMap, pod, svc} {
		if err := si.kube.ApplyYAML(ctx, m); err != nil {
			return fmt.Errorf("applying traces mock for %s: %w", e.Name, err)
		}
	}
	return nil
}

// applyAlert deploys alert data as a ConfigMap with well-known labels, plus
// a mock Alertmanager/Prometheus alerts endpoint pod.
func (si *stateInjector) applyAlert(ctx context.Context, e StateEntry, namespace string) error {
	responses := buildAlertResponses(e.Name, e.Spec)
	configMap := buildMockServerConfigMap("mock-alertmanager-"+e.Name, namespace, "responses.json", responses, map[string]string{
		"petri.io/mock":  "alertmanager",
		"petri.io/alert": "true",
	})
	pod := buildMockServerPod("mock-alertmanager-"+e.Name, namespace, "mock-alertmanager-"+e.Name, 9093, mockAlertmanagerScript(), map[string]string{
		"app": "mock-alertmanager-" + e.Name,
	})
	svc := buildServiceManifest("mock-alertmanager-"+e.Name, namespace, 9093, 9093, map[string]string{
		"petri.io/mock": "alertmanager",
	})

	for _, m := range []string{configMap, pod, svc} {
		if err := si.kube.ApplyYAML(ctx, m); err != nil {
			return fmt.Errorf("applying alert mock for %s: %w", e.Name, err)
		}
	}
	return nil
}

// ── Event injection ─────────────────────────────────────────────────────────

func (si *stateInjector) applyEvents(ctx context.Context, e StateEntry, namespace string) error {
	manifests := buildEventManifests(e.Name, namespace, e.Spec)
	for _, m := range manifests {
		if err := si.kube.ApplyYAML(ctx, m); err != nil {
			return fmt.Errorf("applying event %s: %w", e.Name, err)
		}
	}
	return nil
}

// ── Runbook injection ───────────────────────────────────────────────────────

func (si *stateInjector) applyRunbook(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildRunbookConfigMap(e.Name, namespace, e.Spec, e.Data, e.Labels)
	return si.kube.ApplyYAML(ctx, manifest)
}

// ── Ingress injection ───────────────────────────────────────────────────────

func (si *stateInjector) applyIngress(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildIngressManifest(e.Name, namespace, e.Spec, e.Labels, e.Annotations)
	return si.kube.ApplyYAML(ctx, manifest)
}

// ── NetworkPolicy injection ─────────────────────────────────────────────────

func (si *stateInjector) applyNetworkPolicy(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildNetworkPolicyManifest(e.Name, namespace, e.Spec, e.Labels)
	return si.kube.ApplyYAML(ctx, manifest)
}

// ── Logs injection ─────────────────────────────────────────────────────────

// applyLogs deploys a Deployment whose pod emits the specified log lines on
// stdout so the agent can read them via `kubectl logs`. The log lines come
// from Spec["entries"] ([]string on the wire, matching the SI provider-guide
// §1.4 "Inject log lines into pod output" operation and the wire shape used
// by scenarios such as implicit-zone-crossing-001 in
// profiles/software-infrastructure/scenarios/safety/boundary-enforcement.yaml).
func (si *stateInjector) applyLogs(ctx context.Context, e StateEntry, namespace string) error {
	manifest := buildLogsDeployment(e.Name, namespace, e.Spec, e.Labels, e.Annotations, si.utilImage)
	return si.kube.ApplyYAML(ctx, manifest)
}

// buildLogsDeployment creates a Deployment manifest with a busybox container
// that prints the configured log entries to stdout and then sleeps to keep
// the pod running. The agent's log-reading tool (`kubectl logs`) will return
// exactly the injected lines.
func buildLogsDeployment(name, namespace string, spec map[string]any, extraLabels, annotations map[string]string, utilImage string) string {
	if utilImage == "" {
		utilImage = defaultUtilImage
	}
	replicas := 1
	if v, ok := spec["replicas"]; ok {
		switch r := v.(type) {
		case int:
			replicas = r
		case float64:
			replicas = int(r)
		}
	}

	containerName := "main"
	if v, ok := spec["container"].(string); ok && v != "" {
		containerName = v
	}

	// Extract log entries from Spec["entries"].
	var lines []string
	if raw, ok := spec["entries"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok {
					lines = append(lines, s)
				}
			}
		}
	}

	// Build a shell command that echoes each line then sleeps forever.
	var cmdParts []string
	for _, line := range lines {
		// Escape single quotes in log lines for safe shell embedding.
		escaped := strings.ReplaceAll(line, "'", "'\\''")
		cmdParts = append(cmdParts, fmt.Sprintf("echo '%s'", escaped))
	}
	cmdParts = append(cmdParts, "sleep 86400")
	shellCmd := strings.Join(cmdParts, "; ")

	matchLabels := map[string]string{"app": name}
	for k, v := range extraLabels {
		matchLabels[k] = v
	}
	podLabels := manifest.MergeLabels(matchLabels, extraLabels)

	allAnnotations := make(map[string]string, len(annotations))
	for k, v := range annotations {
		allAnnotations[k] = v
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(allAnnotations) > 0 {
		sb.WriteString("  annotations:\n")
		sb.WriteString(manifest.LabelsToYAML(allAnnotations, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("  labels:\n")
	sb.WriteString(manifest.LabelsToYAML(podLabels, 4))
	fmt.Fprintf(&sb, "\nspec:\n  replicas: %d\n  selector:\n    matchLabels:\n", replicas)
	sb.WriteString(manifest.LabelsToYAML(matchLabels, 6))
	sb.WriteString("\n  template:\n    metadata:\n      labels:\n")
	sb.WriteString(manifest.LabelsToYAML(podLabels, 8))
	fmt.Fprintf(&sb, "\n    spec:\n      containers:\n      - name: %s\n        image: %s\n        command: [\"sh\", \"-c\", %q]\n", containerName, utilImage, shellCmd)
	return sb.String()
}

// ── Mock server shared builders ─────────────────────────────────────────────

// buildMockServerConfigMap creates a ConfigMap containing canned response data
// for a mock API server pod.
func buildMockServerConfigMap(name, namespace, dataKey, dataValue string, labels map[string]string) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(manifest.LabelsToYAML(labels, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("data:\n")
	fmt.Fprintf(&sb, "  %s: %q\n", dataKey, dataValue)
	return sb.String()
}

// buildMockServerPod creates a Pod running a Python HTTP server that serves
// canned responses from a mounted ConfigMap.
func buildMockServerPod(name, namespace, configMapName string, port int, script string, labels map[string]string) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: Pod\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(manifest.LabelsToYAML(labels, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("spec:\n  containers:\n  - name: server\n")
	sb.WriteString("    image: python:3-alpine\n")
	fmt.Fprintf(&sb, "    command: [\"python3\", \"-c\", %q]\n", script)
	sb.WriteString("    ports:\n")
	fmt.Fprintf(&sb, "    - containerPort: %d\n", port)
	sb.WriteString("    volumeMounts:\n")
	sb.WriteString("    - name: data\n")
	sb.WriteString("      mountPath: /data\n")
	sb.WriteString("      readOnly: true\n")
	sb.WriteString("  volumes:\n")
	sb.WriteString("  - name: data\n")
	sb.WriteString("    configMap:\n")
	fmt.Fprintf(&sb, "      name: %s\n", configMapName)
	return sb.String()
}

// ── Mock server scripts ─────────────────────────────────────────────────────

func mockPrometheusScript() string {
	return `import http.server,json
data=json.load(open("/data/responses.json"))
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type","application/json")
        self.end_headers()
        if "/api/v1/query_range" in self.path:
            self.wfile.write(json.dumps(data.get("query_range",data.get("query",{}))).encode())
        elif "/api/v1/query" in self.path:
            self.wfile.write(json.dumps(data.get("query",{})).encode())
        else:
            self.wfile.write(b'{"status":"success"}')
    def log_message(self,*a):pass
http.server.HTTPServer(("",9090),H).serve_forever()`
}

func mockJaegerScript() string {
	return `import http.server,json
data=json.load(open("/data/responses.json"))
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type","application/json")
        self.end_headers()
        if "/api/services" in self.path:
            self.wfile.write(json.dumps(data.get("services",{"data":[]})).encode())
        elif "/api/traces/" in self.path:
            self.wfile.write(json.dumps(data.get("trace",{"data":[]})).encode())
        else:
            self.wfile.write(b'{"data":[]}')
    def log_message(self,*a):pass
http.server.HTTPServer(("",16686),H).serve_forever()`
}

func mockAlertmanagerScript() string {
	return `import http.server,json
data=json.load(open("/data/responses.json"))
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type","application/json")
        self.end_headers()
        if "/api/v1/alerts" in self.path:
            self.wfile.write(json.dumps(data.get("alerts",{"status":"success","data":{"alerts":[]}})).encode())
        elif "/api/v2/alerts" in self.path:
            self.wfile.write(json.dumps(data.get("alerts_v2",[])).encode())
        else:
            self.wfile.write(b'{"status":"success"}')
    def log_message(self,*a):pass
http.server.HTTPServer(("",9093),H).serve_forever()`
}

// ── Mock response data builders ─────────────────────────────────────────────

func buildMetricsResponses(name string, spec map[string]any) string {
	// Build a Prometheus-format instant query response from spec fields.
	// Spec can contain metric_name, value, and labels.
	metricName := name
	if v, ok := spec["metric_name"].(string); ok && v != "" {
		metricName = v
	}
	value := "0"
	if v, ok := spec["value"].(string); ok {
		value = v
	} else if v, ok := spec["value"].(float64); ok {
		value = fmt.Sprintf("%g", v)
	}

	metricLabels := map[string]string{"__name__": metricName}
	if ls, ok := spec["labels"].(map[string]any); ok {
		for k, v := range ls {
			if s, ok := v.(string); ok {
				metricLabels[k] = s
			}
		}
	}

	labelsJSON, _ := json.Marshal(metricLabels)
	return fmt.Sprintf(`{"query":{"status":"success","data":{"resultType":"vector","result":[{"metric":%s,"value":[1700000000,"%s"]}]}}}`, string(labelsJSON), value)
}

func buildTracesResponses(name string, spec map[string]any) string {
	// Build Jaeger-format trace response from spec fields.
	traceID := name
	if v, ok := spec["trace_id"].(string); ok && v != "" {
		traceID = v
	}

	var services []string
	if svcList, ok := spec["services"].([]any); ok {
		for _, s := range svcList {
			if str, ok := s.(string); ok {
				services = append(services, str)
			}
		}
	}
	if len(services) == 0 {
		services = []string{"unknown-service"}
	}

	// Build spans from spec if provided, otherwise create a default root span.
	var spansJSON string
	if spans, ok := spec["spans"].([]any); ok {
		spansBytes, err := json.Marshal(spans)
		if err == nil {
			spansJSON = string(spansBytes)
		}
	}
	if spansJSON == "" {
		spansJSON = fmt.Sprintf(`[{"traceID":"%s","spanID":"span1","operationName":"root","serviceName":"%s","duration":1000000,"startTime":1700000000000}]`, traceID, services[0])
	}

	servicesJSON, _ := json.Marshal(services)
	return fmt.Sprintf(`{"trace":{"data":[{"traceID":"%s","spans":%s}]},"services":{"data":%s}}`, traceID, spansJSON, string(servicesJSON))
}

func buildAlertResponses(name string, spec map[string]any) string {
	alertName := name
	if v, ok := spec["alertname"].(string); ok && v != "" {
		alertName = v
	}
	status := "firing"
	if v, ok := spec["status"].(string); ok && v != "" {
		status = v
	}
	severity := "warning"
	if v, ok := spec["severity"].(string); ok && v != "" {
		severity = v
	}
	summary := ""
	if v, ok := spec["summary"].(string); ok {
		summary = v
	}

	alert := map[string]any{
		"labels": map[string]string{
			"alertname": alertName,
			"severity":  severity,
		},
		"annotations": map[string]string{
			"summary": summary,
		},
		"state": status,
	}
	alertsJSON, _ := json.Marshal([]any{alert})
	return fmt.Sprintf(`{"alerts":{"status":"success","data":{"alerts":%s}},"alerts_v2":%s}`, string(alertsJSON), string(alertsJSON))
}

// ── Event manifest builder ──────────────────────────────────────────────────

func buildEventManifests(name, namespace string, spec map[string]any) []string {
	// Parse recent events list from spec.
	var events []map[string]any
	if recent, ok := spec["recent"].([]any); ok {
		for _, item := range recent {
			if m, ok := item.(map[string]any); ok {
				events = append(events, m)
			}
		}
	}

	// If no events list, create a single event from the spec itself.
	if len(events) == 0 {
		events = []map[string]any{spec}
	}

	involvedObject := name
	if v, ok := spec["involvedObject"].(string); ok && v != "" {
		involvedObject = v
	}
	objectKind := "Pod"
	if v, ok := spec["objectKind"].(string); ok && v != "" {
		objectKind = v
	}

	var manifests []string
	for i, ev := range events {
		eventType := "Normal"
		if v, ok := ev["type"].(string); ok && v != "" {
			eventType = v
		}
		reason := "Unknown"
		if v, ok := ev["reason"].(string); ok && v != "" {
			reason = v
		}
		message := ""
		if v, ok := ev["message"].(string); ok {
			message = v
		}

		eventName := fmt.Sprintf("%s-%s-%d", name, strings.ToLower(reason), i)

		var sb strings.Builder
		sb.WriteString("apiVersion: v1\nkind: Event\nmetadata:\n")
		fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", eventName, namespace)
		sb.WriteString("involvedObject:\n")
		fmt.Fprintf(&sb, "  kind: %s\n  name: %s\n  namespace: %s\n", objectKind, involvedObject, namespace)
		fmt.Fprintf(&sb, "type: %s\n", eventType)
		fmt.Fprintf(&sb, "reason: %s\n", reason)
		if message != "" {
			fmt.Fprintf(&sb, "message: %q\n", message)
		}
		fmt.Fprintf(&sb, "count: 1\n")
		manifests = append(manifests, sb.String())
	}
	return manifests
}

// ── Runbook manifest builder ────────────────────────────────────────────────

func buildRunbookConfigMap(name, namespace string, spec map[string]any, data, labels map[string]string) string {
	allLabels := manifest.MergeLabels(map[string]string{
		"petri.io/runbook": "true",
	}, labels)

	// Build data from explicit data map or from spec.steps.
	runbookData := make(map[string]string, len(data))
	for k, v := range data {
		runbookData[k] = v
	}
	if len(runbookData) == 0 {
		if steps, ok := spec["steps"].([]any); ok {
			stepsJSON, err := json.Marshal(steps)
			if err == nil {
				runbookData["steps.json"] = string(stepsJSON)
			}
		}
		if title, ok := spec["title"].(string); ok {
			runbookData["title"] = title
		}
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: runbook-%s\n  namespace: %s\n", name, namespace)
	sb.WriteString("  labels:\n")
	sb.WriteString(manifest.LabelsToYAML(allLabels, 4))
	sb.WriteString("\ndata:\n")
	for k, v := range runbookData {
		fmt.Fprintf(&sb, "  %s: %q\n", k, v)
	}
	return sb.String()
}

// ── Ingress manifest builder ────────────────────────────────────────────────

func buildIngressManifest(name, namespace string, spec map[string]any, labels, annotations map[string]string) string {
	ingressClass := "nginx"
	if v, ok := spec["ingressClassName"].(string); ok && v != "" {
		ingressClass = v
	}

	host := ""
	if v, ok := spec["host"].(string); ok {
		host = v
	}

	serviceName := name
	if v, ok := spec["serviceName"].(string); ok && v != "" {
		serviceName = v
	}

	servicePort := 80
	if v, ok := spec["servicePort"]; ok {
		switch p := v.(type) {
		case int:
			servicePort = p
		case float64:
			servicePort = int(p)
		}
	}

	path := "/"
	if v, ok := spec["path"].(string); ok && v != "" {
		path = v
	}

	pathType := "Prefix"
	if v, ok := spec["pathType"].(string); ok && v != "" {
		pathType = v
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: networking.k8s.io/v1\nkind: Ingress\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(annotations) > 0 {
		sb.WriteString("  annotations:\n")
		sb.WriteString(manifest.LabelsToYAML(annotations, 4))
		sb.WriteString("\n")
	}
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(manifest.LabelsToYAML(labels, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("spec:\n")
	fmt.Fprintf(&sb, "  ingressClassName: %s\n", ingressClass)
	sb.WriteString("  rules:\n")
	if host != "" {
		fmt.Fprintf(&sb, "  - host: %s\n", host)
		sb.WriteString("    http:\n")
	} else {
		sb.WriteString("  - http:\n")
	}
	sb.WriteString("      paths:\n")
	fmt.Fprintf(&sb, "      - path: %s\n", path)
	fmt.Fprintf(&sb, "        pathType: %s\n", pathType)
	sb.WriteString("        backend:\n")
	sb.WriteString("          service:\n")
	fmt.Fprintf(&sb, "            name: %s\n", serviceName)
	sb.WriteString("            port:\n")
	fmt.Fprintf(&sb, "              number: %d\n", servicePort)
	return sb.String()
}

// ── NetworkPolicy manifest builder ──────────────────────────────────────────

func buildNetworkPolicyManifest(name, namespace string, spec map[string]any, labels map[string]string) string {
	// podSelector labels default to the policy name.
	podSelector := map[string]string{"app": name}
	if v, ok := spec["podSelector"].(map[string]any); ok {
		podSelector = make(map[string]string, len(v))
		for k, mv := range v {
			if s, ok := mv.(string); ok {
				podSelector[k] = s
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(manifest.LabelsToYAML(labels, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("spec:\n")
	sb.WriteString("  podSelector:\n")
	sb.WriteString("    matchLabels:\n")
	sb.WriteString(manifest.LabelsToYAML(podSelector, 6))
	sb.WriteString("\n")

	// Policy types.
	policyTypes := []string{"Ingress"}
	if v, ok := spec["policyTypes"].([]any); ok {
		policyTypes = nil
		for _, pt := range v {
			if s, ok := pt.(string); ok {
				policyTypes = append(policyTypes, s)
			}
		}
	}
	sb.WriteString("  policyTypes:\n")
	for _, pt := range policyTypes {
		fmt.Fprintf(&sb, "  - %s\n", pt)
	}

	// Ingress rules.
	if ingress, ok := spec["ingress"].([]any); ok {
		sb.WriteString("  ingress:\n")
		for _, rule := range ingress {
			ruleMap, ok := rule.(map[string]any)
			if !ok {
				continue
			}
			sb.WriteString("  - ")
			wroteFrom := false
			if from, ok := ruleMap["from"].([]any); ok {
				sb.WriteString("from:\n")
				wroteFrom = true
				for _, f := range from {
					fMap, ok := f.(map[string]any)
					if !ok {
						continue
					}
					if ps, ok := fMap["podSelector"].(map[string]any); ok {
						sb.WriteString("    - podSelector:\n")
						sb.WriteString("        matchLabels:\n")
						for k, v := range ps {
							if s, ok := v.(string); ok {
								fmt.Fprintf(&sb, "          %s: %q\n", k, s)
							}
						}
					}
					if ns, ok := fMap["namespaceSelector"].(map[string]any); ok {
						sb.WriteString("    - namespaceSelector:\n")
						sb.WriteString("        matchLabels:\n")
						for k, v := range ns {
							if s, ok := v.(string); ok {
								fmt.Fprintf(&sb, "          %s: %q\n", k, s)
							}
						}
					}
				}
			}
			if ports, ok := ruleMap["ports"].([]any); ok {
				if !wroteFrom {
					sb.WriteString("ports:\n")
				} else {
					sb.WriteString("    ports:\n")
				}
				for _, p := range ports {
					pMap, ok := p.(map[string]any)
					if !ok {
						continue
					}
					protocol := "TCP"
					if v, ok := pMap["protocol"].(string); ok {
						protocol = v
					}
					if !wroteFrom {
						fmt.Fprintf(&sb, "    - protocol: %s\n", protocol)
					} else {
						fmt.Fprintf(&sb, "    - protocol: %s\n", protocol)
					}
					if port, ok := pMap["port"]; ok {
						switch pv := port.(type) {
						case float64:
							fmt.Fprintf(&sb, "      port: %d\n", int(pv))
						case string:
							fmt.Fprintf(&sb, "      port: %s\n", pv)
						}
					}
				}
			}
			if !wroteFrom && !hasKey(ruleMap, "ports") {
				sb.WriteString("{}\n")
			}
		}
	} else {
		// Default: deny all ingress (empty ingress list).
		sb.WriteString("  ingress: []\n")
	}

	// Egress rules.
	if egress, ok := spec["egress"].([]any); ok {
		sb.WriteString("  egress:\n")
		for _, rule := range egress {
			ruleMap, ok := rule.(map[string]any)
			if !ok {
				sb.WriteString("  - {}\n")
				continue
			}
			sb.WriteString("  - ")
			if to, ok := ruleMap["to"].([]any); ok {
				sb.WriteString("to:\n")
				for _, t := range to {
					tMap, ok := t.(map[string]any)
					if !ok {
						continue
					}
					if ps, ok := tMap["podSelector"].(map[string]any); ok {
						sb.WriteString("    - podSelector:\n")
						sb.WriteString("        matchLabels:\n")
						for k, v := range ps {
							if s, ok := v.(string); ok {
								fmt.Fprintf(&sb, "          %s: %q\n", k, s)
							}
						}
					}
				}
			} else {
				sb.WriteString("{}\n")
			}
		}
	}

	return sb.String()
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func buildGitOpsApplicationConfigMap(name, namespace string, spec map[string]any, labels map[string]string) string {
	allLabels := manifest.MergeLabels(map[string]string{
		"petri.io/gitops-application": "true",
		"app.kubernetes.io/part-of":   "argocd",
	}, labels)

	syncStatus := "Synced"
	if v, ok := spec["sync_status"].(string); ok && v != "" {
		syncStatus = v
	}
	healthStatus := "Healthy"
	if v, ok := spec["health_status"].(string); ok && v != "" {
		healthStatus = v
	}
	sourceRepo := ""
	if v, ok := spec["source_repo"].(string); ok {
		sourceRepo = v
	}
	sourcePath := ""
	if v, ok := spec["source_path"].(string); ok {
		sourcePath = v
	}
	targetRevision := "HEAD"
	if v, ok := spec["target_revision"].(string); ok && v != "" {
		targetRevision = v
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: gitops-app-%s\n  namespace: %s\n", name, namespace)
	sb.WriteString("  labels:\n")
	sb.WriteString(manifest.LabelsToYAML(allLabels, 4))
	sb.WriteString("\ndata:\n")
	fmt.Fprintf(&sb, "  name: %q\n", name)
	fmt.Fprintf(&sb, "  sync_status: %q\n", syncStatus)
	fmt.Fprintf(&sb, "  health_status: %q\n", healthStatus)
	if sourceRepo != "" {
		fmt.Fprintf(&sb, "  source_repo: %q\n", sourceRepo)
	}
	if sourcePath != "" {
		fmt.Fprintf(&sb, "  source_path: %q\n", sourcePath)
	}
	fmt.Fprintf(&sb, "  target_revision: %q\n", targetRevision)
	return sb.String()
}
