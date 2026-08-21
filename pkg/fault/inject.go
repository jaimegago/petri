package fault

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InjectClient is the cluster surface the runtime trigger needs. pkg/chaos's
// KubeClient satisfies it.
type InjectClient interface {
	PodLister
	GetResource(ctx context.Context, kind, namespace, name string) (string, error)
	GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error)
	DeleteConfigMapKey(ctx context.Context, namespace, name, key string) error
	RestartDeployment(ctx context.Context, namespace, name string) error
}

// Target is the Deployment a runtime injection applies to.
type Target struct {
	Namespace string
	Name      string
}

// Inject applies spec to a healthy running application at runtime and waits
// for expect. It is the second trigger into the same catalog as provision
// time, and leaves the trail a real bad change leaves: the ConfigMap edited,
// a rollout restarted, a previous ReplicaSet that was fine.
//
// It refuses a target that does not run the application image, because only
// the application genuinely fails on the misconfiguration — anything else
// would turn the key's absence into a kubelet message or into nothing.
func Inject(ctx context.Context, kube InjectClient, target Target, spec Spec, expect Expect, timeout time.Duration) error {
	raw, err := kube.GetResource(ctx, "deployment", target.Namespace, target.Name)
	if err != nil {
		return fmt.Errorf("reading target deployment: %w", err)
	}
	dep, err := parseDeployment(raw)
	if err != nil {
		return fmt.Errorf("parsing target deployment %s/%s: %w", target.Namespace, target.Name, err)
	}
	if !dep.runsApplication() {
		return fmt.Errorf("deployment %s/%s does not run the application image (%s); fault %s is materialised by the application and cannot be injected into %s",
			target.Namespace, target.Name, AppRepository, spec.Class, dep.images())
	}

	selector := dep.selector()
	// Pods of the current generation are snapshotted before the restart so
	// the symptom wait can exclude them: after a rollout restart the old
	// pods stay up until the new ones are ready — which they never will
	// be — and only the new generation carries the misconfiguration.
	previous, err := podNames(ctx, kube, target.Namespace, selector)
	if err != nil {
		return fmt.Errorf("listing pods of %s/%s before injection: %w", target.Namespace, target.Name, err)
	}

	switch spec.Class {
	case ClassConfigMissingKey:
		if !dep.referencesConfigMap(spec.ConfigMap) {
			return fmt.Errorf("deployment %s/%s does not read ConfigMap %q; removing %s from it would change nothing",
				target.Namespace, target.Name, spec.ConfigMap, spec.Key)
		}
		data, err := kube.GetConfigMap(ctx, target.Namespace, spec.ConfigMap)
		if err != nil {
			return fmt.Errorf("reading ConfigMap %s/%s: %w", target.Namespace, spec.ConfigMap, err)
		}
		if _, present := data[spec.Key]; !present {
			return fmt.Errorf("ConfigMap %s/%s has no key %q to remove; the application is not healthy with respect to this fault",
				target.Namespace, spec.ConfigMap, spec.Key)
		}
		if err := kube.DeleteConfigMapKey(ctx, target.Namespace, spec.ConfigMap, spec.Key); err != nil {
			return err
		}
		if err := kube.RestartDeployment(ctx, target.Namespace, target.Name); err != nil {
			return err
		}
	default:
		return fmt.Errorf("fault class %q has no runtime trigger", spec.Class)
	}

	if expect.Status == "" {
		expect.Status = spec.Definition().Symptom
	}
	// A rolling update surges one new pod and holds the rest until it is
	// ready, so the new generation is a single crash-looping pod beside the
	// healthy old ones — the stuck rollout a real bad change produces. The
	// wait therefore asks for at least one new pod, not the replica count.
	lister := &excludingLister{kube: kube, exclude: previous}
	return WaitForSymptom(ctx, lister, target.Namespace, selector, 1, expect, timeout)
}

// podNames lists the names of the pods matching selector in namespace.
func podNames(ctx context.Context, kube PodLister, namespace string, selector map[string]string) (map[string]bool, error) {
	raw, err := kube.ListResources(ctx, "pods", namespace)
	if err != nil {
		return nil, err
	}
	pods, err := parsePods(raw)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, p := range pods {
		if matchesSelector(p.Metadata.Labels, selector) {
			names[p.Metadata.Name] = true
		}
	}
	return names, nil
}

// excludingLister hides a fixed set of pods from the symptom wait.
type excludingLister struct {
	kube    PodLister
	exclude map[string]bool
}

func (l *excludingLister) ListResources(ctx context.Context, kind, namespace string) (string, error) {
	raw, err := l.kube.ListResources(ctx, kind, namespace)
	if err != nil {
		return "", err
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return "", err
	}
	kept := make([]json.RawMessage, 0, len(list.Items))
	for _, item := range list.Items {
		var m struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(item, &m); err != nil {
			return "", err
		}
		if l.exclude[m.Metadata.Name] {
			continue
		}
		kept = append(kept, item)
	}
	out, err := json.Marshal(map[string]any{"items": kept})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// deployment is the slice of a Deployment object Inject reads.
type deployment struct {
	Spec struct {
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template struct {
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
					Env   []struct {
						ValueFrom *struct {
							ConfigMapKeyRef *struct {
								Name string `json:"name"`
							} `json:"configMapKeyRef"`
						} `json:"valueFrom"`
					} `json:"env"`
					EnvFrom []struct {
						ConfigMapRef *struct {
							Name string `json:"name"`
						} `json:"configMapRef"`
					} `json:"envFrom"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func parseDeployment(raw string) (deployment, error) {
	var d deployment
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return deployment{}, err
	}
	return d, nil
}

func (d deployment) runsApplication() bool {
	for _, c := range d.Spec.Template.Spec.Containers {
		if strings.HasPrefix(c.Image, AppRepository+":") || strings.HasPrefix(c.Image, AppRepository+"@") {
			return true
		}
	}
	return false
}

func (d deployment) images() string {
	names := make([]string, 0, len(d.Spec.Template.Spec.Containers))
	for _, c := range d.Spec.Template.Spec.Containers {
		names = append(names, c.Image)
	}
	return strings.Join(names, ", ")
}

func (d deployment) referencesConfigMap(name string) bool {
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil && e.ValueFrom.ConfigMapKeyRef.Name == name {
				return true
			}
		}
		for _, e := range c.EnvFrom {
			if e.ConfigMapRef != nil && e.ConfigMapRef.Name == name {
				return true
			}
		}
	}
	return false
}

func (d deployment) selector() map[string]string {
	return d.Spec.Selector.MatchLabels
}
