package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// KubeClient provides the Kubernetes operations required for fault injection.
// It is defined at the point of use (this package) and may be backed by any
// implementation — the production code uses the kubectl CLI.
type KubeClient interface {
	// ListPods returns the names of pods in namespace matching labelSelector.
	// An empty labelSelector returns all pods in the namespace.
	ListPods(ctx context.Context, namespace, labelSelector string) ([]string, error)
	// DeletePod force-deletes a pod by name, triggering a restart via its controller.
	DeletePod(ctx context.Context, namespace, name string) error
	// RestartDeployment performs a rolling restart of a Deployment.
	RestartDeployment(ctx context.Context, namespace, name string) error
	// ScaleDeployment sets the replica count for a Deployment.
	ScaleDeployment(ctx context.Context, namespace, name string, replicas int32) error
	// GetConfigMap returns the data map of a ConfigMap.
	GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error)
	// UpdateConfigMap replaces the data map of an existing ConfigMap.
	UpdateConfigMap(ctx context.Context, namespace, name string, data map[string]string) error
	// ListServiceAccountSecrets returns the names of token Secrets bound to a ServiceAccount.
	// In Kubernetes 1.24+ with auto-generated tokens this list may be empty.
	ListServiceAccountSecrets(ctx context.Context, namespace, name string) ([]string, error)
	// DeleteSecret removes a Kubernetes Secret.
	DeleteSecret(ctx context.Context, namespace, name string) error
	// ExecInPod runs command in the first container of pod and returns combined output.
	ExecInPod(ctx context.Context, namespace, pod string, command []string) (string, error)
	// CreateNamespace creates or idempotently applies a namespace with optional labels.
	CreateNamespace(ctx context.Context, name string, labels map[string]string) error
	// DeleteNamespace deletes a namespace and all resources within it.
	DeleteNamespace(ctx context.Context, name string) error
	// DeleteNamespaceWithTimeout deletes a namespace with a kubectl-side
	// --timeout flag. When the budget is exhausted kubectl exits non-zero
	// but the in-kube deletion continues asynchronously. The OASIS
	// provider uses this variant on the /v1/teardown path so a busy
	// finaliser does not surface as an opaque 500. See ADR 0014.
	DeleteNamespaceWithTimeout(ctx context.Context, name string, timeout time.Duration) error
	// GetNamespacePhase returns the .status.phase of a namespace
	// ("Active" / "Terminating" / "Unknown"). It returns ("", nil) when
	// the namespace does not exist (404) and ("", err) for any other
	// transport failure.
	GetNamespacePhase(ctx context.Context, name string) (string, error)
	// GetResource retrieves a Kubernetes resource as a JSON string.
	GetResource(ctx context.Context, kind, namespace, name string) (string, error)
	// ListResources retrieves all resources of a given kind in a namespace as a JSON list.
	ListResources(ctx context.Context, kind, namespace string) (string, error)
	// ApplyYAML applies a YAML manifest string via kubectl apply.
	ApplyYAML(ctx context.Context, manifest string) error
	// GetClusterConfig returns the API server URL and base64-encoded CA for the current cluster context.
	GetClusterConfig(ctx context.Context) (serverURL, caData string, err error)
	// TokenForServiceAccount creates a short-lived bearer token for a ServiceAccount.
	TokenForServiceAccount(ctx context.Context, namespace, name string) (string, error)
	// WaitForRollout waits for a Deployment to complete its rollout.
	WaitForRollout(ctx context.Context, namespace, deployment string, timeout time.Duration) error
}

// kubeRunner abstracts kubectl command execution so tests can inject a fake.
type kubeRunner interface {
	run(ctx context.Context, args []string) error
	output(ctx context.Context, args []string) (string, error)
}

// cliKubeClient implements KubeClient by shelling out to the kubectl binary.
type cliKubeClient struct {
	runner kubeRunner
}

// NewKubeClient returns a KubeClient backed by the kubectl CLI.
// kubeconfigPath may be empty to use the in-cluster or default kubeconfig.
func NewKubeClient(kubeconfigPath string) KubeClient {
	return &cliKubeClient{
		runner: &cliKubeRunner{binary: "kubectl", kubeconfig: kubeconfigPath},
	}
}

// newKubeClientWithRunner injects a runner; used in unit tests.
func newKubeClientWithRunner(r kubeRunner) KubeClient {
	return &cliKubeClient{runner: r}
}

// ── cliKubeRunner ──────────────────────────────────────────────────────────────

// cliKubeRunner implements kubeRunner by executing the real kubectl binary.
type cliKubeRunner struct {
	binary     string
	kubeconfig string
}

func (r *cliKubeRunner) baseArgs() []string {
	if r.kubeconfig != "" {
		return []string{"--kubeconfig", r.kubeconfig}
	}
	return nil
}

func (r *cliKubeRunner) run(ctx context.Context, args []string) error {
	_, err := r.output(ctx, args)
	return err
}

func (r *cliKubeRunner) output(ctx context.Context, args []string) (string, error) {
	full := append(r.baseArgs(), args...) //nolint:gocritic // intentional new slice
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.binary, full...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%s %v: %w: %s", r.binary, full, err, msg)
		}
		return "", fmt.Errorf("%s %v: %w", r.binary, full, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ── KubeClient method implementations ──────────────────────────────────────────

func (c *cliKubeClient) ListPods(ctx context.Context, namespace, labelSelector string) ([]string, error) {
	args := []string{"get", "pods", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}"}
	if labelSelector != "" {
		args = append(args, "-l", labelSelector)
	}
	out, err := c.runner.output(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("listing pods in %s: %w", namespace, err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Fields(out), nil
}

func (c *cliKubeClient) DeletePod(ctx context.Context, namespace, name string) error {
	args := []string{"delete", "pod", name, "-n", namespace, "--grace-period=0", "--force"}
	if err := c.runner.run(ctx, args); err != nil {
		return fmt.Errorf("deleting pod %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (c *cliKubeClient) RestartDeployment(ctx context.Context, namespace, name string) error {
	args := []string{"rollout", "restart", "deployment/" + name, "-n", namespace}
	if err := c.runner.run(ctx, args); err != nil {
		return fmt.Errorf("restarting deployment %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (c *cliKubeClient) ScaleDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	args := []string{
		"scale", "deployment/" + name, "-n", namespace,
		fmt.Sprintf("--replicas=%d", replicas),
	}
	if err := c.runner.run(ctx, args); err != nil {
		return fmt.Errorf("scaling deployment %s/%s to %d: %w", namespace, name, replicas, err)
	}
	return nil
}

type configMapJSON struct {
	Data map[string]string `json:"data"`
}

func (c *cliKubeClient) GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error) {
	out, err := c.runner.output(ctx, []string{"get", "configmap", name, "-n", namespace, "-o", "json"})
	if err != nil {
		return nil, fmt.Errorf("getting configmap %s/%s: %w", namespace, name, err)
	}
	var cm configMapJSON
	if err := json.Unmarshal([]byte(out), &cm); err != nil {
		return nil, fmt.Errorf("parsing configmap %s/%s: %w", namespace, name, err)
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	return cm.Data, nil
}

func (c *cliKubeClient) UpdateConfigMap(ctx context.Context, namespace, name string, data map[string]string) error {
	patch, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return fmt.Errorf("marshalling configmap patch: %w", err)
	}
	args := []string{"patch", "configmap", name, "-n", namespace, "--type=merge", fmt.Sprintf("-p=%s", patch)}
	if err := c.runner.run(ctx, args); err != nil {
		return fmt.Errorf("patching configmap %s/%s: %w", namespace, name, err)
	}
	return nil
}

type serviceAccountJSON struct {
	Secrets []struct {
		Name string `json:"name"`
	} `json:"secrets"`
}

func (c *cliKubeClient) ListServiceAccountSecrets(ctx context.Context, namespace, name string) ([]string, error) {
	out, err := c.runner.output(ctx, []string{"get", "serviceaccount", name, "-n", namespace, "-o", "json"})
	if err != nil {
		return nil, fmt.Errorf("getting serviceaccount %s/%s: %w", namespace, name, err)
	}
	var sa serviceAccountJSON
	if err := json.Unmarshal([]byte(out), &sa); err != nil {
		return nil, fmt.Errorf("parsing serviceaccount %s/%s: %w", namespace, name, err)
	}
	names := make([]string, 0, len(sa.Secrets))
	for _, s := range sa.Secrets {
		names = append(names, s.Name)
	}
	return names, nil
}

func (c *cliKubeClient) DeleteSecret(ctx context.Context, namespace, name string) error {
	if err := c.runner.run(ctx, []string{"delete", "secret", name, "-n", namespace}); err != nil {
		return fmt.Errorf("deleting secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (c *cliKubeClient) ExecInPod(ctx context.Context, namespace, pod string, command []string) (string, error) {
	args := append([]string{"exec", pod, "-n", namespace, "--"}, command...)
	out, err := c.runner.output(ctx, args)
	if err != nil {
		return "", fmt.Errorf("exec in pod %s/%s: %w", namespace, pod, err)
	}
	return out, nil
}

func (c *cliKubeClient) CreateNamespace(ctx context.Context, name string, labels map[string]string) error {
	manifest := buildNamespaceManifest(name, labels)
	return c.ApplyYAML(ctx, manifest)
}

func buildNamespaceManifest(name string, labels map[string]string) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ")
	sb.WriteString(name)
	if len(labels) > 0 {
		sb.WriteString("\n  labels:")
		for k, v := range labels {
			sb.WriteString("\n    ")
			sb.WriteString(k)
			sb.WriteString(": ")
			fmt.Fprintf(&sb, "%q", v)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

func (c *cliKubeClient) DeleteNamespace(ctx context.Context, name string) error {
	if err := c.runner.run(ctx, []string{"delete", "namespace", name, "--ignore-not-found"}); err != nil {
		return fmt.Errorf("deleting namespace %s: %w", name, err)
	}
	return nil
}

// DeleteNamespaceWithTimeout delegates to kubectl's own --timeout so the
// process exits cleanly when the budget runs out instead of being SIGKILLed
// by a Go context deadline. The in-kube deletion request still lands —
// kubectl only gives up its wait — so callers can confirm by checking
// GetNamespacePhase afterwards.
func (c *cliKubeClient) DeleteNamespaceWithTimeout(ctx context.Context, name string, timeout time.Duration) error {
	args := []string{
		"delete", "namespace", name,
		"--ignore-not-found",
		"--timeout", formatKubectlDuration(timeout),
	}
	if err := c.runner.run(ctx, args); err != nil {
		return fmt.Errorf("deleting namespace %s: %w", name, err)
	}
	return nil
}

// GetNamespacePhase reads .status.phase via jsonpath. `--ignore-not-found`
// makes kubectl return empty stdout (and exit 0) when the namespace does
// not exist, which we surface as ("", nil).
func (c *cliKubeClient) GetNamespacePhase(ctx context.Context, name string) (string, error) {
	out, err := c.runner.output(ctx, []string{
		"get", "namespace", name,
		"--ignore-not-found",
		"-o", "jsonpath={.status.phase}",
	})
	if err != nil {
		return "", fmt.Errorf("getting namespace %s phase: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

// formatKubectlDuration emits a kubectl-friendly duration string ("30s",
// "5m"). kubectl rejects compound forms like "1m30s" on the --timeout
// flag, so we collapse to whole minutes when possible and otherwise emit
// seconds.
func formatKubectlDuration(d time.Duration) string {
	total := int(d.Seconds())
	if total < 1 {
		total = 1
	}
	if total >= 60 && total%60 == 0 {
		return fmt.Sprintf("%dm", total/60)
	}
	return fmt.Sprintf("%ds", total)
}

func (c *cliKubeClient) GetResource(ctx context.Context, kind, namespace, name string) (string, error) {
	args := []string{"get", kind, name, "-o", "json"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := c.runner.output(ctx, args)
	if err != nil {
		return "", fmt.Errorf("getting %s %s in %s: %w", kind, name, namespace, err)
	}
	return out, nil
}

func (c *cliKubeClient) ListResources(ctx context.Context, kind, namespace string) (string, error) {
	args := []string{"get", kind, "-o", "json"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := c.runner.output(ctx, args)
	if err != nil {
		return "", fmt.Errorf("listing %s in %s: %w", kind, namespace, err)
	}
	return out, nil
}

func (c *cliKubeClient) ApplyYAML(ctx context.Context, manifest string) error {
	f, err := os.CreateTemp("", "petri-manifest-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temp manifest file: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(manifest); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing manifest: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing manifest: %w", err)
	}
	if err := c.runner.run(ctx, []string{"apply", "-f", f.Name()}); err != nil {
		return fmt.Errorf("applying manifest: %w", err)
	}
	return nil
}

func (c *cliKubeClient) GetClusterConfig(ctx context.Context) (string, string, error) {
	server, err := c.runner.output(ctx, []string{
		"config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}",
	})
	if err != nil {
		return "", "", fmt.Errorf("getting cluster server URL: %w", err)
	}
	ca, err := c.runner.output(ctx, []string{
		"config", "view", "--minify", "--raw", "-o", "jsonpath={.clusters[0].cluster.certificate-authority-data}",
	})
	if err != nil {
		return "", "", fmt.Errorf("getting cluster CA: %w", err)
	}
	return strings.TrimSpace(server), strings.TrimSpace(ca), nil
}

func (c *cliKubeClient) TokenForServiceAccount(ctx context.Context, namespace, name string) (string, error) {
	out, err := c.runner.output(ctx, []string{"create", "token", name, "-n", namespace})
	if err != nil {
		return "", fmt.Errorf("creating token for serviceaccount %s/%s: %w", namespace, name, err)
	}
	return strings.TrimSpace(out), nil
}

func (c *cliKubeClient) WaitForRollout(ctx context.Context, namespace, deployment string, timeout time.Duration) error {
	totalSec := int(timeout.Seconds())
	timeoutStr := fmt.Sprintf("%ds", totalSec)
	if totalSec >= 60 && totalSec%60 == 0 {
		timeoutStr = fmt.Sprintf("%dm", totalSec/60)
	}
	if err := c.runner.run(ctx, []string{
		"rollout", "status", "deployment/" + deployment,
		"-n", namespace,
		"--timeout", timeoutStr,
	}); err != nil {
		return fmt.Errorf("rollout status %s/%s: %w", namespace, deployment, err)
	}
	return nil
}
