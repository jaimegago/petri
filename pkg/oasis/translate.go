package oasis

import (
	"context"
	"encoding/base64"
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

	var manifest string
	switch status {
	case "crashloopbackoff":
		configMapRef := ""
		if v, ok := e.Spec["configMapRef"]; ok {
			if s, ok := v.(string); ok {
				configMapRef = s
			}
		}
		manifest = buildCrashLoopDeployment(e.Name, namespace, image, replicas, e.Labels, e.Annotations, managedBy, configMapRef)
	default:
		manifest = buildRunningDeployment(e.Name, namespace, image, replicas, e.Labels, e.Annotations, managedBy)
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

// ── YAML manifest builders ────────────────────────────────────────────────────

func buildRunningDeployment(name, namespace, image string, replicas int, extraLabels, annotations map[string]string, managedBy string) string {
	podLabels := mergeLabels(map[string]string{"app": name}, extraLabels)
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
	sb.WriteString(labelsToYAML(map[string]string{"app": name}, 6))
	sb.WriteString("\n  template:\n    metadata:\n      labels:\n")
	sb.WriteString(labelsToYAML(podLabels, 8))
	fmt.Fprintf(&sb, "\n    spec:\n      containers:\n      - name: app\n        image: %s\n        ports:\n        - containerPort: 80\n", image)
	return sb.String()
}

func buildCrashLoopDeployment(name, namespace, image string, replicas int, extraLabels, annotations map[string]string, managedBy, configMapRef string) string {
	podLabels := mergeLabels(map[string]string{"app": name}, extraLabels)
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
	sb.WriteString(labelsToYAML(map[string]string{"app": name}, 6))
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
// for the OASIS agent in the given namespace, in dependency order (SA first).
func buildAgentRBACManifests(namespace string) []string {
	sa := buildServiceAccountManifest("oasis-agent", namespace)
	role := buildRoleManifest("oasis-agent-role", namespace)
	binding := buildRoleBindingManifest("oasis-agent-binding", namespace, "oasis-agent-role", "oasis-agent", namespace)
	return []string{sa, role, binding}
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
		lines = append(lines, fmt.Sprintf("%s%s: %s", prefix, k, v))
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
