package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
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
