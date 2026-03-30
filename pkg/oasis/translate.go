package oasis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// stateInjector translates OASIS StateEntry objects into Kubernetes operations.
type stateInjector struct {
	kube KubeClient
}

func newStateInjector(kube KubeClient) *stateInjector {
	return &stateInjector{kube: kube}
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

	image := "nginx:latest"
	if v, ok := e.Spec["image"]; ok {
		if s, ok := v.(string); ok && s != "" {
			image = s
		}
	}

	status := "running"
	if v, ok := e.Spec["status"]; ok {
		if s, ok := v.(string); ok {
			status = strings.ToLower(s)
		}
	}

	managedBy := ""
	if v, ok := e.Spec["managed_by"]; ok {
		if s, ok := v.(string); ok {
			managedBy = s
		}
	}

	// Custom matchLabels for selector (defaults to {"app": name}).
	matchLabels := map[string]string{"app": e.Name}
	if v, ok := e.Spec["matchLabels"].(map[string]any); ok {
		matchLabels = make(map[string]string, len(v))
		for mk, mv := range v {
			if s, ok := mv.(string); ok {
				matchLabels[mk] = s
			}
		}
	}

	var manifest string
	switch status {
	case "crashloopbackoff":
		configMapRef := ""
		if v, ok := e.Spec["configMapRef"]; ok {
			if s, ok := v.(string); ok {
				configMapRef = s
			}
		}
		manifest = buildCrashLoopDeployment(e.Name, namespace, image, replicas, e.Labels, e.Annotations, managedBy, configMapRef, matchLabels)
	default:
		manifest = buildRunningDeployment(e.Name, namespace, image, replicas, e.Labels, e.Annotations, managedBy, matchLabels)
	}
	return si.kube.ApplyYAML(ctx, manifest)
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
	manifest := buildPodManifest(e.Name, namespace, e.Spec, e.Labels)
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

func buildRunningDeployment(name, namespace, image string, replicas int, extraLabels, annotations map[string]string, managedBy string, matchLabels map[string]string) string {
	podLabels := mergeLabels(matchLabels, extraLabels)
	allAnnotations := make(map[string]string, len(annotations))
	for k, v := range annotations {
		allAnnotations[k] = v
	}
	if managedBy != "" {
		allAnnotations["app.kubernetes.io/managed-by"] = managedBy
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(allAnnotations) > 0 {
		sb.WriteString("  annotations:\n")
		sb.WriteString(labelsToYAML(allAnnotations, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("  labels:\n")
	sb.WriteString(labelsToYAML(podLabels, 4))
	fmt.Fprintf(&sb, "\nspec:\n  replicas: %d\n  selector:\n    matchLabels:\n", replicas)
	sb.WriteString(labelsToYAML(matchLabels, 6))
	sb.WriteString("\n  template:\n    metadata:\n      labels:\n")
	sb.WriteString(labelsToYAML(podLabels, 8))
	fmt.Fprintf(&sb, "\n    spec:\n      containers:\n      - name: app\n        image: %s\n        ports:\n        - containerPort: 80\n", image)
	return sb.String()
}

func buildCrashLoopDeployment(name, namespace, image string, replicas int, extraLabels, annotations map[string]string, managedBy, configMapRef string, matchLabels map[string]string) string {
	podLabels := mergeLabels(matchLabels, extraLabels)
	allAnnotations := make(map[string]string, len(annotations))
	for k, v := range annotations {
		allAnnotations[k] = v
	}
	if managedBy != "" {
		allAnnotations["app.kubernetes.io/managed-by"] = managedBy
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(allAnnotations) > 0 {
		sb.WriteString("  annotations:\n")
		sb.WriteString(labelsToYAML(allAnnotations, 4))
		sb.WriteString("\n")
	}
	sb.WriteString("  labels:\n")
	sb.WriteString(labelsToYAML(podLabels, 4))
	fmt.Fprintf(&sb, "\nspec:\n  replicas: %d\n  selector:\n    matchLabels:\n", replicas)
	sb.WriteString(labelsToYAML(matchLabels, 6))
	sb.WriteString("\n  template:\n    metadata:\n      labels:\n")
	sb.WriteString(labelsToYAML(podLabels, 8))
	sb.WriteString("\n    spec:\n      containers:\n      - name: app\n")

	if configMapRef != "" {
		// Reference a missing key from an existing ConfigMap to trigger CrashLoopBackOff.
		fmt.Fprintf(&sb, "        image: %s\n", image)
		fmt.Fprintf(&sb, "        env:\n        - name: MISSING_CONFIG_VALUE\n          valueFrom:\n            configMapKeyRef:\n              name: %s\n              key: __petri_missing_key__\n              optional: false\n", configMapRef)
	} else {
		// Simple exit-1 container to trigger CrashLoopBackOff.
		sb.WriteString("        image: busybox:latest\n")
		sb.WriteString("        command: [\"sh\", \"-c\", \"echo CrashLoopBackOff simulation; exit 1\"]\n")
	}
	return sb.String()
}

func buildConfigMapManifest(name, namespace string, data, labels, annotations map[string]string) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	if len(annotations) > 0 {
		sb.WriteString("  annotations:\n")
		sb.WriteString(labelsToYAML(annotations, 4))
		sb.WriteString("\n")
	}
	if len(labels) > 0 {
		sb.WriteString("  labels:\n")
		sb.WriteString(labelsToYAML(labels, 4))
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
		sb.WriteString(labelsToYAML(labels, 4))
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
		sb.WriteString(labelsToYAML(annotations, 4))
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

// ── YAML helper functions ─────────────────────────────────────────────────────

func labelsToYAML(labels map[string]string, indent int) string {
	if len(labels) == 0 {
		return ""
	}
	prefix := strings.Repeat(" ", indent)
	var lines []string
	for k, v := range labels {
		lines = append(lines, fmt.Sprintf("%s%s: %q", prefix, k, v))
	}
	return strings.Join(lines, "\n")
}

func mergeLabels(base, extra map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

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
		sb.WriteString(labelsToYAML(labels, 4))
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
		sb.WriteString(labelsToYAML(labels, 4))
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

func buildPodManifest(name, namespace string, spec map[string]any, labels map[string]string) string {
	image := "nginx:latest"
	if v, ok := spec["image"].(string); ok && v != "" {
		image = v
	}

	podLabels := mergeLabels(map[string]string{"app": name}, labels)

	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: Pod\nmetadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n  namespace: %s\n", name, namespace)
	sb.WriteString("  labels:\n")
	sb.WriteString(labelsToYAML(podLabels, 4))
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
	allLabels := mergeLabels(map[string]string{
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
	sb.WriteString(labelsToYAML(allLabels, 4))
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

func buildGitOpsApplicationConfigMap(name, namespace string, spec map[string]any, labels map[string]string) string {
	allLabels := mergeLabels(map[string]string{
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
	sb.WriteString(labelsToYAML(allLabels, 4))
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
