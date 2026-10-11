package workloadstate

import (
	"fmt"
	"strings"

	"github.com/jaimegago/petri/pkg/fault"
	"github.com/jaimegago/petri/pkg/manifest"
)

// DefaultUtilImage is the small shell image used for states that need an
// exit-1 or busy loop. It is hosted on registry.k8s.io rather than Docker Hub,
// whose blob storage runs on Cloudflare R2 (null-routed by some networks).
const DefaultUtilImage = "registry.k8s.io/e2e-test-images/busybox:1.37.0-2"

// defaultErrorRate is the percentage of 5xx responses used by
// StateElevatedErrorRate when Spec.ErrorRate is unset.
const defaultErrorRate = 50

// appPort is the port the application listens on (its PORT default).
const appPort = 8080

// faultStates maps each fault class's symptom onto the state it is rendered
// under, so a fault declared beside the wrong state is refused rather than
// rendered as either.
var faultStates = map[string]State{
	fault.SymptomCrashLoopBackOff: StateCrashLoopBackOff,
	fault.SymptomOOMKilled:        StateOOMKilled,
}

// HTTPServerImage is the HTTP server elevated_error_rate runs. It is nginx on
// registry.k8s.io, pinned because the rendered configuration depends on this
// build (split_clients, $request_id). Docker Hub is never used: its blob
// storage runs on Cloudflare R2, which some networks null-route.
const HTTPServerImage = "registry.k8s.io/nginx-slim:0.27"

// Render produces the manifest for spec: a Deployment, or a bare Pod when
// spec.Kind is KindPod. It is a pure function: no cluster access,
// deterministic for a given spec (modulo Go map iteration order in
// label/annotation blocks, matching the existing builders). It validates the
// requested state first, so an unrecognized non-empty state — or a state the
// kind cannot materialise — returns an error and no manifest.
func Render(spec Spec) (string, error) {
	kind, state, err := spec.resolve()
	if err != nil {
		return "", err
	}

	replicas := spec.Replicas
	if replicas < 1 {
		replicas = 1
	}
	matchLabels := spec.MatchLabels
	if len(matchLabels) == 0 {
		matchLabels = map[string]string{"app": spec.Name}
	}
	utilImage := spec.UtilImage
	if utilImage == "" {
		utilImage = DefaultUtilImage
	}
	containerName, err := spec.containerName()
	if err != nil {
		return "", err
	}

	// A faulted workload runs the application whatever its state, so its
	// declared environment always has a container to land on.
	if spec.Fault == nil && len(spec.Env) > 0 && !stateRunsCallerImage(state) {
		return "", fmt.Errorf(
			"workload %q: state %q synthesises its own container, so a declared container environment would not be rendered; declared env is supported for %s",
			spec.Name, state, callerImageStatesList(),
		)
	}

	var container string
	if spec.Fault != nil {
		if err := spec.validateFault(state); err != nil {
			return "", err
		}
		container = renderFaulted(spec)
	} else {
		switch state {
		case StateCrashLoopBackOff:
			container = renderCrashLoop(spec, utilImage)
		case StateOOMKilled:
			container = renderOOMKilled(utilImage)
		case StatePending:
			container = renderPending(spec)
		case StateDegraded:
			if replicas < 2 {
				replicas = 2 // Need at least 2 replicas for partial failure.
			}
			container = renderDegraded(spec)
		case StateElevatedErrorRate:
			rate := spec.ErrorRate
			if rate < 1 {
				rate = defaultErrorRate
			}
			container = renderElevatedErrorRate(rate)
		case StateError:
			container = renderError()
		case StateRunning:
			container = renderRunning(spec)
		default:
			// Unreachable: normalizeState only returns recognised states or err.
			return "", fmt.Errorf("workload %q: unhandled state %q", spec.Name, state)
		}
	}

	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)
	var sb strings.Builder
	if kind == KindPod {
		writePodHead(&sb, spec.Name, spec.Namespace, spec.NodeName, containerName, podLabels, annotations)
		sb.WriteString(podContainer(container))
		return sb.String(), nil
	}
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, containerName, replicas, podLabels, annotations, matchLabels)
	sb.WriteString(container)
	return sb.String(), nil
}

// containerName resolves the name of the workload's single container: the
// declared ContainerName, or DefaultContainerName when none is declared. A
// declared name that is not a valid container name is refused rather than
// rendered, since it would otherwise reach the cluster as a malformed manifest
// or be rewritten into something the scenario did not declare.
func (s Spec) containerName() (string, error) {
	if s.ContainerName == "" {
		return DefaultContainerName, nil
	}
	if !isDNS1123Label(s.ContainerName) {
		return "", fmt.Errorf("workload %q: container name %q is not a valid Kubernetes container name (lowercase alphanumerics and '-', starting and ending alphanumeric, at most 63 characters)",
			s.Name, s.ContainerName)
	}
	return s.ContainerName, nil
}

// isDNS1123Label reports whether v is an RFC 1123 label, the form Kubernetes
// requires of a container name.
func isDNS1123Label(v string) bool {
	if len(v) == 0 || len(v) > 63 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if alnum {
			continue
		}
		if c == '-' && i != 0 && i != len(v)-1 {
			continue
		}
		return false
	}
	return true
}

// The state builders below write the body of the workload's single container
// — every field after its name — at a Deployment's indentation, eight spaces. A Pod's
// container sits four spaces shallower; podContainer shifts the block there.
const (
	deploymentContainerIndent = "        "
	podContainerIndent        = "    "
)

// podContainer re-indents a container body written at a Deployment's
// indentation to a Pod's. Every line of the body carries at least the
// Deployment's indentation, so the shift preserves the block's structure.
func podContainer(body string) string {
	lines := strings.SplitAfter(body, "\n")
	for i, l := range lines {
		if rest, ok := strings.CutPrefix(l, deploymentContainerIndent); ok {
			lines[i] = podContainerIndent + rest
		}
	}
	return strings.Join(lines, "")
}

// writePodHead writes a bare Pod's apiVersion/kind/metadata/spec preamble up
// to (but not including) the body of its single container, containerName.
func writePodHead(sb *strings.Builder, name, namespace, nodeName, containerName string, labels, annotations map[string]string) {
	sb.WriteString("apiVersion: v1\nkind: Pod\nmetadata:\n")
	fmt.Fprintf(sb, "  name: %s\n  namespace: %s\n", name, namespace)
	manifest.WriteAnnotationsYAML(sb, annotations)
	sb.WriteString("  labels:\n")
	sb.WriteString(manifest.LabelsToYAML(labels, 4))
	sb.WriteString("\nspec:\n")
	if nodeName != "" {
		fmt.Fprintf(sb, "  nodeName: %s\n", nodeName)
	}
	fmt.Fprintf(sb, "  containers:\n  - name: %s\n", containerName)
}

// writeDeploymentHead writes the apiVersion/kind/metadata/spec preamble shared
// by every state builder up to (but not including) the body of its single
// container, containerName.
func writeDeploymentHead(sb *strings.Builder, name, namespace, containerName string, replicas int, podLabels, annotations, matchLabels map[string]string) {
	sb.WriteString("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n")
	fmt.Fprintf(sb, "  name: %s\n  namespace: %s\n", name, namespace)
	manifest.WriteAnnotationsYAML(sb, annotations)
	sb.WriteString("  labels:\n")
	sb.WriteString(manifest.LabelsToYAML(podLabels, 4))
	fmt.Fprintf(sb, "\nspec:\n  replicas: %d\n  selector:\n    matchLabels:\n", replicas)
	sb.WriteString(manifest.LabelsToYAML(matchLabels, 6))
	sb.WriteString("\n  template:\n    metadata:\n      labels:\n")
	sb.WriteString(manifest.LabelsToYAML(podLabels, 8))
	fmt.Fprintf(sb, "\n    spec:\n      containers:\n      - name: %s\n", containerName)
}

// renderRunning materialises a healthy workload: its container on the given
// image exposing containerPort 80.
func renderRunning(spec Spec) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "        image: %s\n        ports:\n        - containerPort: 80\n", spec.Image)
	writeEnvYAML(&sb, spec.Env, false)
	return sb.String()
}

// renderCrashLoop materialises a workload that lands in CrashLoopBackOff
// without a declared cause. With declared Env the container runs spec.Image
// and each ConfigMap reference is required, so an absent key stops the
// container with the key named by the kubelet; otherwise the container is a
// symptom-only exit on the util image, for scenarios in which the crash is
// scenery rather than the thing under diagnosis. Neither path names its
// mechanism: a cause under diagnosis is declared as a Fault and goes through
// the application (see renderFaulted).
func renderCrashLoop(spec Spec, utilImage string) string {
	var sb strings.Builder
	if len(spec.Env) > 0 {
		fmt.Fprintf(&sb, "        image: %s\n", spec.Image)
		writeEnvYAML(&sb, spec.Env, false)
		return sb.String()
	}
	fmt.Fprintf(&sb, "        image: %s\n", utilImage)
	sb.WriteString("        command: [\"sh\", \"-c\", \"exit 1\"]\n")
	return sb.String()
}

// validateFault checks that a declared fault can be materialised under the
// requested state: the state must be the one the fault's class produces, and
// the declared environment must actually read the ConfigMap the fault names,
// or the application would start.
func (s Spec) validateFault(state State) error {
	def := s.Fault.Definition()
	want, ok := faultStates[def.Symptom]
	if !ok {
		return fmt.Errorf("workload %q: fault %s produces %q, which no state renders", s.Name, def.Class, def.Symptom)
	}
	if state != want {
		return fmt.Errorf("workload %q: fault %s produces %s and must be declared with state %q, not %q",
			s.Name, def.Class, def.Symptom, want, state)
	}
	if s.Fault.Class == fault.ClassConfigUnboundedCache {
		for _, e := range s.Env {
			if e.Name != s.Fault.Key {
				continue
			}
			if e.ConfigMapKeyRef != nil {
				return fmt.Errorf("workload %q: fault %s needs %s declared as a literal value %q; a ConfigMap reference is not supported for this class",
					s.Name, def.Class, s.Fault.Key, def.Value)
			}
			if e.Value != def.Value {
				return fmt.Errorf("workload %q: fault %s declares %s unbounded, but the declared env sets it to %q; the application would stay bounded",
					s.Name, def.Class, s.Fault.Key, e.Value)
			}
			return nil
		}
		return fmt.Errorf("workload %q: fault %s names %s but no declared container env sets it to %q; the application would use its bounded default",
			s.Name, def.Class, s.Fault.Key, def.Value)
	}
	if s.Fault.Class == fault.ClassConfigMissingKey {
		reads := false
		for _, e := range s.Env {
			if e.ConfigMapKeyRef != nil && e.ConfigMapKeyRef.Name == s.Fault.ConfigMap {
				reads = true
				break
			}
		}
		if !reads {
			return fmt.Errorf("workload %q: fault %s names ConfigMap %q but no declared container env reads it; the application would start",
				s.Name, def.Class, s.Fault.ConfigMap)
		}
	}
	return nil
}

// renderFaulted materialises a Deployment running the application with the
// declared environment, misconfigured as the fault declares. The manifest is
// what an operator would have written for a healthy instance: the
// application image under the workload's own name, its port, the declared
// environment, and — for a class whose symptom is memory exhaustion — the
// memory limit the class names. ConfigMap references are optional so that an
// absent key reaches the application as an unset variable; the application's
// own validation is then what fails, in its own log, naming the key.
func renderFaulted(spec Spec) string {
	image := spec.AppImage
	if image == "" {
		image = fault.AppImage
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "        image: %s\n        ports:\n        - containerPort: %d\n", image, appPort)
	if limit := spec.Fault.Definition().MemoryLimit; limit != "" {
		fmt.Fprintf(&sb, "        resources:\n          limits:\n            memory: %s\n", limit)
	}
	writeEnvYAML(&sb, spec.Env, true)
	return sb.String()
}

// callerImageStates are the states whose container runs Spec.Image, and so the
// states a declared container environment can be rendered onto. The remaining
// three synthesise their own image or command to produce the named failure
// (oomkilled allocates memory, error references a bad image,
// elevated_error_rate runs an HTTP server), and an injected required
// ConfigMap reference would stop the container before it could fail the way
// the scenario asked for.
var callerImageStates = []State{StateRunning, StateCrashLoopBackOff, StatePending, StateDegraded}

// stateRunsCallerImage reports whether state renders Spec.Image.
func stateRunsCallerImage(state State) bool {
	for _, s := range callerImageStates {
		if s == state {
			return true
		}
	}
	return false
}

// callerImageStatesList renders callerImageStates for an error message.
func callerImageStatesList() string {
	names := make([]string, len(callerImageStates))
	for i, s := range callerImageStates {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

// writeEnvYAML writes the container `env:` block for the declared environment.
// A ConfigMapKeyRef is written with `optional: false` so an absent key stops
// the container from starting and the kubelet reports the key by name — or,
// when optional is true (the application path), with `optional: true` so the
// application sees the variable unset and validates it itself. A
// SecretKeyRef is always a required reference. A literal value is written as
// `value:`.
func writeEnvYAML(sb *strings.Builder, env []EnvVar, optional bool) {
	if len(env) == 0 {
		return
	}
	sb.WriteString("        env:\n")
	for _, e := range env {
		fmt.Fprintf(sb, "        - name: %s\n", e.Name)
		if e.ConfigMapKeyRef != nil {
			fmt.Fprintf(sb, "          valueFrom:\n            configMapKeyRef:\n              name: %s\n              key: %s\n              optional: %t\n",
				e.ConfigMapKeyRef.Name, e.ConfigMapKeyRef.Key, optional)
			continue
		}
		if e.SecretKeyRef != nil {
			fmt.Fprintf(sb, "          valueFrom:\n            secretKeyRef:\n              name: %s\n              key: %s\n",
				e.SecretKeyRef.Name, e.SecretKeyRef.Key)
			continue
		}
		fmt.Fprintf(sb, "          value: %q\n", e.Value)
	}
}

// OOMKilledMemoryLimit is the container memory limit the symptom-only
// oomkilled state renders. Measured on kind: under 4Mi the runtime's own
// container init is OOM-killed before the process starts (StartError, not
// OOMKilled), and a fixed-buffer allocator never reaches the limit at all.
// 16Mi starts the container every time and leaves the kill to the kernel.
const OOMKilledMemoryLimit = "16Mi"

// renderOOMKilled materialises a workload whose container allocates memory
// without bound under OOMKilledMemoryLimit, so the kernel OOM-kills it and the
// kubelet records OOMKilled. `tail` on /dev/zero buffers a line that never
// ends.
func renderOOMKilled(utilImage string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "        image: %s\n", utilImage)
	sb.WriteString("        command: [\"sh\", \"-c\", \"tail /dev/zero\"]\n")
	sb.WriteString("        resources:\n")
	sb.WriteString("          limits:\n")
	fmt.Fprintf(&sb, "            memory: %s\n", OOMKilledMemoryLimit)
	return sb.String()
}

// renderPending materialises a workload with an unschedulable CPU request so
// pods stay Pending with "Insufficient cpu".
func renderPending(spec Spec) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "        image: %s\n", spec.Image)
	sb.WriteString("        resources:\n")
	sb.WriteString("          requests:\n")
	sb.WriteString("            cpu: \"100\"\n")
	writeEnvYAML(&sb, spec.Env, false)
	return sb.String()
}

// renderDegraded materialises the container of a Deployment (Render forces
// min 2 replicas) with a failing readiness probe so a subset of pods never
// become Ready.
func renderDegraded(spec Spec) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "        image: %s\n        ports:\n        - containerPort: 80\n", spec.Image)
	sb.WriteString("        readinessProbe:\n")
	sb.WriteString("          httpGet:\n")
	sb.WriteString("            path: /healthz\n")
	sb.WriteString("            port: 80\n")
	sb.WriteString("          initialDelaySeconds: 1\n")
	sb.WriteString("          periodSeconds: 2\n")
	sb.WriteString("          failureThreshold: 1\n")
	writeEnvYAML(&sb, spec.Env, false)
	return sb.String()
}

// errorRateServerPort is the port elevated_error_rate's server listens on.
const errorRateServerPort = 8080

// renderElevatedErrorRate materialises a workload whose HTTP server
// (HTTPServerImage) answers HTTP 500 to errorRate percent of requests and 200
// to the rest. nginx's split_clients assigns each request by a hash of its
// random request id, so the share holds per request rather than per client.
// The configuration is written at startup by the image's own shell; a rate of
// 100 or more fails every request.
func renderElevatedErrorRate(errorRate int) string {
	split := fmt.Sprintf("%d%% 1;\n        * \"\";", errorRate)
	if errorRate >= 100 {
		split = "* 1;"
	}
	conf := fmt.Sprintf(`worker_processes 1;
daemon off;
pid /tmp/nginx.pid;
error_log stderr;
events {}
http {
    access_log off;
    split_clients "${request_id}" $failed {
        %s
    }
    server {
        listen %d;
        location / {
            if ($failed) {
                return 500;
            }
            return 200;
        }
    }
}
`, split, errorRateServerPort)
	script := "cat > /tmp/nginx.conf <<'EOF'\n" + conf + "EOF\nexec nginx -c /tmp/nginx.conf\n"

	var sb strings.Builder
	fmt.Fprintf(&sb, "        image: %s\n", HTTPServerImage)
	fmt.Fprintf(&sb, "        command: [\"sh\", \"-c\", %q]\n", script)
	fmt.Fprintf(&sb, "        ports:\n        - containerPort: %d\n", errorRateServerPort)
	return sb.String()
}

// renderError materialises a workload referencing a non-existent image,
// producing ErrImagePull / ImagePullBackOff.
func renderError() string {
	var sb strings.Builder
	sb.WriteString("        image: invalid-registry.example.com/does-not-exist:v0.0.0\n")
	return sb.String()
}
