package workloadstate

import (
	"fmt"
	"strings"

	"github.com/jaimegago/petri/pkg/manifest"
)

// DefaultUtilImage is the small shell image used for states that need an
// exit-1 or busy loop. It is hosted on registry.k8s.io rather than Docker Hub,
// whose blob storage runs on Cloudflare R2 (null-routed by some networks).
const DefaultUtilImage = "registry.k8s.io/e2e-test-images/busybox:1.37.0-2"

// defaultErrorRate is the percentage of 5xx responses used by
// StateElevatedErrorRate when Spec.ErrorRate is unset.
const defaultErrorRate = 50

// missingKeyPlaceholder is the synthetic key name the ConfigMapRef variant of
// crashloopbackoff references. It is deliberately unmistakable: a scenario
// that reaches this path has not declared the container environment, so no
// real key name is available and the cause the agent reads names nothing the
// scenario scores on. Spec.Env is the conformant route.
const missingKeyPlaceholder = "__petri_missing_key__"

// Render produces the Deployment manifest for spec. It is a pure function: no
// cluster access, deterministic for a given spec (modulo Go map iteration
// order in label/annotation blocks, matching the existing builders). It
// validates the requested state first, so an unrecognized non-empty state
// returns an error and no manifest.
func Render(spec Spec) (string, error) {
	state, err := spec.normalizeState()
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

	if len(spec.Env) > 0 && !stateRunsCallerImage(state) {
		return "", fmt.Errorf(
			"workload %q: state %q synthesises its own container, so a declared container environment would not be rendered; declared env is supported for %s",
			spec.Name, state, callerImageStatesList(),
		)
	}

	switch state {
	case StateCrashLoopBackOff:
		return renderCrashLoop(spec, replicas, matchLabels, utilImage), nil
	case StateOOMKilled:
		return renderOOMKilled(spec, replicas, matchLabels, utilImage), nil
	case StatePending:
		return renderPending(spec, replicas, matchLabels), nil
	case StateDegraded:
		return renderDegraded(spec, replicas, matchLabels), nil
	case StateElevatedErrorRate:
		rate := spec.ErrorRate
		if rate < 1 {
			rate = defaultErrorRate
		}
		return renderElevatedErrorRate(spec, replicas, rate, matchLabels), nil
	case StateError:
		return renderError(spec, replicas, matchLabels), nil
	case StateRunning:
		return renderRunning(spec, replicas, matchLabels), nil
	default:
		// Unreachable: normalizeState only returns recognised states or err.
		return "", fmt.Errorf("workload %q: unhandled state %q", spec.Name, state)
	}
}

// writeDeploymentHead writes the apiVersion/kind/metadata/spec preamble shared
// by every state builder up to (but not including) the container list.
func writeDeploymentHead(sb *strings.Builder, name, namespace string, replicas int, podLabels, annotations, matchLabels map[string]string) {
	sb.WriteString("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n")
	fmt.Fprintf(sb, "  name: %s\n  namespace: %s\n", name, namespace)
	manifest.WriteAnnotationsYAML(sb, annotations)
	sb.WriteString("  labels:\n")
	sb.WriteString(manifest.LabelsToYAML(podLabels, 4))
	fmt.Fprintf(sb, "\nspec:\n  replicas: %d\n  selector:\n    matchLabels:\n", replicas)
	sb.WriteString(manifest.LabelsToYAML(matchLabels, 6))
	sb.WriteString("\n  template:\n    metadata:\n      labels:\n")
	sb.WriteString(manifest.LabelsToYAML(podLabels, 8))
	sb.WriteString("\n    spec:\n      containers:\n      - name: app\n")
}

// renderRunning materialises a healthy Deployment: container `app` on the given
// image exposing containerPort 80.
func renderRunning(spec Spec, replicas int, matchLabels map[string]string) string {
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
	fmt.Fprintf(&sb, "        image: %s\n        ports:\n        - containerPort: 80\n", spec.Image)
	writeEnvYAML(&sb, spec.Env)
	return sb.String()
}

// renderCrashLoop materialises a Deployment that lands in CrashLoopBackOff.
// Three variants, in precedence order: declared Env is rendered verbatim (the
// container uses spec.Image); otherwise a ConfigMapRef references a synthetic
// missing key in that ConfigMap; otherwise it runs an exit-1 loop on the util
// image.
//
// Declared Env wins because the agent under evaluation reads the cluster: the
// key names it finds there must be the ones the scenario declares and scores
// on, not a placeholder this package invented.
func renderCrashLoop(spec Spec, replicas int, matchLabels map[string]string, utilImage string) string {
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
	switch {
	case len(spec.Env) > 0:
		fmt.Fprintf(&sb, "        image: %s\n", spec.Image)
		writeEnvYAML(&sb, spec.Env)
	case spec.ConfigMapRef != "":
		// Reference a missing key from an existing ConfigMap to trigger CrashLoopBackOff.
		fmt.Fprintf(&sb, "        image: %s\n", spec.Image)
		fmt.Fprintf(&sb, "        env:\n        - name: MISSING_CONFIG_VALUE\n          valueFrom:\n            configMapKeyRef:\n              name: %s\n              key: %s\n              optional: false\n", spec.ConfigMapRef, missingKeyPlaceholder)
	default:
		// Simple exit-1 container to trigger CrashLoopBackOff. Sourced from
		// registry.k8s.io rather than Docker Hub to avoid R2.
		fmt.Fprintf(&sb, "        image: %s\n", utilImage)
		sb.WriteString("        command: [\"sh\", \"-c\", \"echo CrashLoopBackOff simulation; exit 1\"]\n")
	}
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
// the container from starting and the kubelet reports the key by name; a
// literal value is written as `value:`.
func writeEnvYAML(sb *strings.Builder, env []EnvVar) {
	if len(env) == 0 {
		return
	}
	sb.WriteString("        env:\n")
	for _, e := range env {
		fmt.Fprintf(sb, "        - name: %s\n", e.Name)
		if e.ConfigMapKeyRef != nil {
			fmt.Fprintf(sb, "          valueFrom:\n            configMapKeyRef:\n              name: %s\n              key: %s\n              optional: false\n",
				e.ConfigMapKeyRef.Name, e.ConfigMapKeyRef.Key)
			continue
		}
		fmt.Fprintf(sb, "          value: %q\n", e.Value)
	}
}

// renderOOMKilled materialises a Deployment whose container allocates memory
// under a 4Mi limit so the OOM killer terminates it.
func renderOOMKilled(spec Spec, replicas int, matchLabels map[string]string, utilImage string) string {
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
	fmt.Fprintf(&sb, "        image: %s\n", utilImage)
	sb.WriteString("        command: [\"sh\", \"-c\", \"dd if=/dev/zero of=/dev/null bs=1M\"]\n")
	sb.WriteString("        resources:\n")
	sb.WriteString("          limits:\n")
	sb.WriteString("            memory: 4Mi\n")
	return sb.String()
}

// renderPending materialises a Deployment with an unschedulable CPU request so
// pods stay Pending with "Insufficient cpu".
func renderPending(spec Spec, replicas int, matchLabels map[string]string) string {
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
	fmt.Fprintf(&sb, "        image: %s\n", spec.Image)
	sb.WriteString("        resources:\n")
	sb.WriteString("          requests:\n")
	sb.WriteString("            cpu: \"100\"\n")
	writeEnvYAML(&sb, spec.Env)
	return sb.String()
}

// renderDegraded materialises a Deployment (min 2 replicas) with a failing
// readiness probe so a subset of pods never become Ready.
func renderDegraded(spec Spec, replicas int, matchLabels map[string]string) string {
	if replicas < 2 {
		replicas = 2 // Need at least 2 replicas for partial failure.
	}
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
	fmt.Fprintf(&sb, "        image: %s\n        ports:\n        - containerPort: 80\n", spec.Image)
	sb.WriteString("        readinessProbe:\n")
	sb.WriteString("          httpGet:\n")
	sb.WriteString("            path: /healthz\n")
	sb.WriteString("            port: 80\n")
	sb.WriteString("          initialDelaySeconds: 1\n")
	sb.WriteString("          periodSeconds: 2\n")
	sb.WriteString("          failureThreshold: 1\n")
	writeEnvYAML(&sb, spec.Env)
	return sb.String()
}

// renderElevatedErrorRate materialises a Deployment running a Python HTTP
// server that returns HTTP 500 for errorRate percent of requests.
func renderElevatedErrorRate(spec Spec, replicas, errorRate int, matchLabels map[string]string) string {
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)

	script := fmt.Sprintf(`import http.server,random
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if random.randint(1,100)<=%d:
            self.send_response(500)
        else:
            self.send_response(200)
        self.end_headers()
    def log_message(self,*a):pass
http.server.HTTPServer(("",8080),H).serve_forever()`, errorRate)

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
	sb.WriteString("        image: python:3-alpine\n")
	fmt.Fprintf(&sb, "        command: [\"python3\", \"-c\", %q]\n", script)
	sb.WriteString("        ports:\n        - containerPort: 8080\n")
	return sb.String()
}

// renderError materialises a Deployment referencing a non-existent image,
// producing ErrImagePull / ImagePullBackOff.
func renderError(spec Spec, replicas int, matchLabels map[string]string) string {
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
	sb.WriteString("        image: invalid-registry.example.com/does-not-exist:v0.0.0\n")
	return sb.String()
}
