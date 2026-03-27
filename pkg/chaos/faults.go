package chaos

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// DefaultFaults returns the complete set of built-in fault implementations keyed by
// FaultType. Callers may extend this map with additional implementations before
// passing it to NewRunner; no existing code needs modification to add a new fault type.
func DefaultFaults() map[FaultType]Fault {
	return map[FaultType]Fault{
		FaultKillPod:              &killPodFault{},
		FaultRestartDeployment:    &restartDeploymentFault{},
		FaultCPUPressure:          &cpuPressureFault{},
		FaultMemPressure:          &memPressureFault{},
		FaultCorruptConfigMap:     &corruptConfigMapFault{},
		FaultRevokeServiceAccount: &revokeServiceAccountFault{},
		FaultNetworkLatency:       &networkLatencyFault{},
		FaultScaleToZero:          &scaleToZeroFault{},
	}
}

// ── killPodFault ───────────────────────────────────────────────────────────────

// killPodFault force-deletes a running pod, triggering a restart via its controller.
// The fault is idempotent: if the pod is already terminating or gone, the delete
// call may return a not-found error which is treated as a successful injection.
type killPodFault struct{}

func (f *killPodFault) Type() FaultType { return FaultKillPod }

// Execute resolves a specific pod from target (by pod name or label-derived selector)
// and force-deletes it. Supported params: none.
func (f *killPodFault) Execute(ctx context.Context, kube KubeClient, target TargetResource, _ map[string]string) error {
	pod, err := resolvePodName(ctx, kube, target)
	if err != nil {
		return fmt.Errorf("resolving pod for kill: %w", err)
	}
	return kube.DeletePod(ctx, target.Namespace, pod)
}

// ── restartDeploymentFault ─────────────────────────────────────────────────────

// restartDeploymentFault triggers a rolling restart of a Deployment by updating
// its restart annotation. The Deployment name is read from target.Name.
type restartDeploymentFault struct{}

func (f *restartDeploymentFault) Type() FaultType { return FaultRestartDeployment }

// Execute performs kubectl rollout restart on the target Deployment.
// Supported params: none.
func (f *restartDeploymentFault) Execute(ctx context.Context, kube KubeClient, target TargetResource, _ map[string]string) error {
	return kube.RestartDeployment(ctx, target.Namespace, target.Name)
}

// ── cpuPressureFault ───────────────────────────────────────────────────────────

// cpuPressureFault injects CPU load into a running pod using stress-ng.
// Falls back to a busyloop via dd if stress-ng is unavailable in the container.
// Supported params:
//   - "duration": how long to apply pressure (e.g. "30s"). Default: 30s.
//   - "workers": number of CPU worker threads. Default: 1.
type cpuPressureFault struct{}

func (f *cpuPressureFault) Type() FaultType { return FaultCPUPressure }

func (f *cpuPressureFault) Execute(ctx context.Context, kube KubeClient, target TargetResource, params map[string]string) error {
	dur := paramDuration(params, "duration", 30*time.Second)
	workers := paramInt(params, "workers", 1)

	pod, err := resolvePodName(ctx, kube, target)
	if err != nil {
		return fmt.Errorf("resolving pod for cpu pressure: %w", err)
	}

	secs := int(dur.Seconds())
	// Prefer stress-ng; fall back to dd busyloop if not installed.
	script := fmt.Sprintf(
		"stress-ng --cpu %d --timeout %ds 2>/dev/null || "+
			"(for i in $(seq 1 %d); do dd if=/dev/zero of=/dev/null bs=4k &; done; sleep %d; kill $(jobs -p) 2>/dev/null; true)",
		workers, secs, workers, secs,
	)
	_, err = kube.ExecInPod(ctx, target.Namespace, pod, []string{"sh", "-c", script})
	return err
}

// ── memPressureFault ───────────────────────────────────────────────────────────

// memPressureFault injects memory pressure into a running pod using stress-ng.
// Supported params:
//   - "duration": how long to apply pressure (e.g. "30s"). Default: 30s.
//   - "bytes": amount of memory to consume (e.g. "256M"). Default: "256M".
type memPressureFault struct{}

func (f *memPressureFault) Type() FaultType { return FaultMemPressure }

func (f *memPressureFault) Execute(ctx context.Context, kube KubeClient, target TargetResource, params map[string]string) error {
	dur := paramDuration(params, "duration", 30*time.Second)
	bytes := params["bytes"]
	if bytes == "" {
		bytes = "256M"
	}

	pod, err := resolvePodName(ctx, kube, target)
	if err != nil {
		return fmt.Errorf("resolving pod for memory pressure: %w", err)
	}

	script := fmt.Sprintf("stress-ng --vm 1 --vm-bytes %s --timeout %ds 2>/dev/null || true", bytes, int(dur.Seconds()))
	_, err = kube.ExecInPod(ctx, target.Namespace, pod, []string{"sh", "-c", script})
	return err
}

// ── corruptConfigMapFault ─────────────────────────────────────────────────────

// corruptConfigMapFault overwrites a key in a ConfigMap with an invalid sentinel value.
// The fault is partially idempotent: a second injection overwrites the (already corrupted)
// value with a fresh sentinel, which is safe but visible in the event timeline.
// Supported params:
//   - "key": the ConfigMap data key to overwrite. Default: first key found.
//   - "value": the corrupted replacement value. Default: "PETRI_CORRUPTED_<timestamp>".
type corruptConfigMapFault struct{}

func (f *corruptConfigMapFault) Type() FaultType { return FaultCorruptConfigMap }

func (f *corruptConfigMapFault) Execute(ctx context.Context, kube KubeClient, target TargetResource, params map[string]string) error {
	data, err := kube.GetConfigMap(ctx, target.Namespace, target.Name)
	if err != nil {
		return fmt.Errorf("reading configmap for corruption: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("configmap %s/%s has no data keys to corrupt", target.Namespace, target.Name)
	}

	key := params["key"]
	if key == "" {
		for k := range data {
			key = k
			break
		}
	}

	value := params["value"]
	if value == "" {
		value = "PETRI_CORRUPTED_" + time.Now().UTC().Format("20060102150405")
	}

	data[key] = value
	return kube.UpdateConfigMap(ctx, target.Namespace, target.Name, data)
}

// ── revokeServiceAccountFault ─────────────────────────────────────────────────

// revokeServiceAccountFault deletes the legacy token Secrets associated with a
// ServiceAccount, forcing pods to re-authenticate on their next restart.
// In Kubernetes 1.24+ with projected service account tokens this is typically a
// no-op (no legacy secrets exist), which is treated as a successful injection.
// Supported params: none.
type revokeServiceAccountFault struct{}

func (f *revokeServiceAccountFault) Type() FaultType { return FaultRevokeServiceAccount }

func (f *revokeServiceAccountFault) Execute(ctx context.Context, kube KubeClient, target TargetResource, _ map[string]string) error {
	secrets, err := kube.ListServiceAccountSecrets(ctx, target.Namespace, target.Name)
	if err != nil {
		return fmt.Errorf("listing service account secrets for %s/%s: %w", target.Namespace, target.Name, err)
	}
	// No legacy token secrets — idempotent success on modern clusters.
	if len(secrets) == 0 {
		return nil
	}
	var errs []string
	for _, secret := range secrets {
		if err := kube.DeleteSecret(ctx, target.Namespace, secret); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("deleting service account secrets: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ── networkLatencyFault ───────────────────────────────────────────────────────

// networkLatencyFault adds artificial network latency to a pod's eth0 interface using
// the Linux tc/netem traffic control subsystem. The target pod must have CAP_NET_ADMIN
// or run in privileged mode; otherwise the fault fails with a permission error.
// Supported params:
//   - "latency_ms": base latency to add in milliseconds. Default: 100.
//   - "jitter_ms": random jitter added to each packet in milliseconds. Default: 10.
type networkLatencyFault struct{}

func (f *networkLatencyFault) Type() FaultType { return FaultNetworkLatency }

func (f *networkLatencyFault) Execute(ctx context.Context, kube KubeClient, target TargetResource, params map[string]string) error {
	latencyMs := paramInt(params, "latency_ms", 100)
	jitterMs := paramInt(params, "jitter_ms", 10)

	pod, err := resolvePodName(ctx, kube, target)
	if err != nil {
		return fmt.Errorf("resolving pod for network latency: %w", err)
	}

	// Clear any existing qdisc first (idempotency).
	clearScript := "tc qdisc del dev eth0 root 2>/dev/null || true"
	if _, err := kube.ExecInPod(ctx, target.Namespace, pod, []string{"sh", "-c", clearScript}); err != nil {
		return fmt.Errorf("clearing existing qdisc on %s/%s: %w", target.Namespace, pod, err)
	}

	addScript := fmt.Sprintf("tc qdisc add dev eth0 root netem delay %dms %dms", latencyMs, jitterMs)
	if _, err := kube.ExecInPod(ctx, target.Namespace, pod, []string{"sh", "-c", addScript}); err != nil {
		return fmt.Errorf("adding %dms network latency to %s/%s: %w", latencyMs, target.Namespace, pod, err)
	}
	return nil
}

// ── scaleToZeroFault ──────────────────────────────────────────────────────────

// scaleToZeroFault scales a Deployment to zero replicas, rendering the workload
// unavailable. The fault is idempotent: scaling an already-zero Deployment succeeds.
// Supported params: none.
type scaleToZeroFault struct{}

func (f *scaleToZeroFault) Type() FaultType { return FaultScaleToZero }

func (f *scaleToZeroFault) Execute(ctx context.Context, kube KubeClient, target TargetResource, _ map[string]string) error {
	return kube.ScaleDeployment(ctx, target.Namespace, target.Name, 0)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// resolvePodName resolves a TargetResource to a concrete pod name.
//   - If target.Kind is "Pod" and target.Name is a bare name (no "="), it is returned directly.
//   - Otherwise target.Name is treated as a label selector (or converted to "app=<name>"),
//     pods are listed, and a random one is chosen.
func resolvePodName(ctx context.Context, kube KubeClient, target TargetResource) (string, error) {
	if target.Kind == "Pod" && !strings.Contains(target.Name, "=") {
		return target.Name, nil
	}

	selector := target.Name
	if !strings.Contains(selector, "=") {
		selector = "app=" + selector
	}

	pods, err := kube.ListPods(ctx, target.Namespace, selector)
	if err != nil {
		return "", fmt.Errorf("listing pods with selector %q in %s: %w", selector, target.Namespace, err)
	}
	if len(pods) == 0 {
		return "", fmt.Errorf("no pods found in %s with selector %q", target.Namespace, selector)
	}
	return pods[rand.Intn(len(pods))], nil //nolint:gosec // non-cryptographic selection
}

// paramDuration extracts a time.Duration from params[key], returning def on
// missing or unparseable values.
func paramDuration(params map[string]string, key string, def time.Duration) time.Duration {
	s, ok := params[key]
	if !ok || s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// paramInt extracts an integer from params[key], returning def on missing or
// unparseable values.
func paramInt(params map[string]string, key string, def int) int {
	s, ok := params[key]
	if !ok || s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
