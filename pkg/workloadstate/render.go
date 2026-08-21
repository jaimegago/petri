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
}

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

	if spec.Fault != nil {
		if err := spec.validateFault(state); err != nil {
			return "", err
		}
		return renderFaulted(spec, replicas, matchLabels), nil
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
	writeEnvYAML(&sb, spec.Env, false)
	return sb.String()
}

// renderCrashLoop materialises a Deployment that lands in CrashLoopBackOff
// without a declared cause. With declared Env the container runs spec.Image
// and each ConfigMap reference is required, so an absent key stops the
// container with the key named by the kubelet; otherwise the container is a
// symptom-only exit on the util image, for scenarios in which the crash is
// scenery rather than the thing under diagnosis. Neither path names its
// mechanism: a cause under diagnosis is declared as a Fault and goes through
// the application (see renderFaulted).
func renderCrashLoop(spec Spec, replicas int, matchLabels map[string]string, utilImage string) string {
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
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
// application image under the workload's own name, its port, and the
// declared environment. ConfigMap references are optional so that an absent
// key reaches the application as an unset variable; the application's own
// validation is then what fails, in its own log, naming the key.
func renderFaulted(spec Spec, replicas int, matchLabels map[string]string) string {
	podLabels := manifest.MergeLabels(matchLabels, spec.Labels)
	annotations := manifest.MergeAnnotations(spec.Annotations, spec.ManagedBy)
	image := spec.AppImage
	if image == "" {
		image = fault.AppImage
	}

	var sb strings.Builder
	writeDeploymentHead(&sb, spec.Name, spec.Namespace, replicas, podLabels, annotations, matchLabels)
	fmt.Fprintf(&sb, "        image: %s\n        ports:\n        - containerPort: %d\n", image, appPort)
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
// application sees the variable unset and validates it itself. A literal
// value is written as `value:`.
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
	writeEnvYAML(&sb, spec.Env, false)
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
	writeEnvYAML(&sb, spec.Env, false)
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
