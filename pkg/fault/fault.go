// Package fault is the catalog of causes under diagnosis: misconfigurations a
// petri-owned application genuinely fails on, so that an agent evaluated
// against the cluster diagnoses a failure rather than reads a caption.
//
// It is distinct from pkg/chaos, which perturbs a healthy system at runtime,
// and from pkg/workloadstate's synthetic states, which render a symptom
// directly. A fault here is defined once — its misconfiguration, the symptom
// it produces, and the guarantee that nothing readable names the mechanism —
// and both entry points resolve into it: provision time (pkg/workloadstate
// renders the application born misconfigured) and runtime (Inject applies the
// misconfiguration to a healthy application). Both verify the declared
// symptom with WaitForSymptom before reporting success.
//
// The package imports neither pkg/oasis nor pkg/workloadstate; they consume
// it.
package fault

import (
	"fmt"
	"sort"
	"strings"
)

// Class identifies a fault class in the catalog.
type Class string

// ClassConfigMissingKey is a configuration key the application requires,
// absent from the ConfigMap the application reads it from. The application
// fails its own startup validation naming the key.
const ClassConfigMissingKey Class = "config.missing-key"

// ClassConfigUnboundedCache is a cache bound set to the value the
// application reads as "no bound". The application's own record cache then
// grows with its directory sync until the container's memory limit is
// reached and the kernel OOM-kills it.
const ClassConfigUnboundedCache Class = "config.unbounded-cache"

// AppImage is the pinned application image. It is a release surface:
// Catalog documents, per class, what this version of the image genuinely
// does, and a declaration outside that coverage is refused rather than
// provisioned against an application that would not fail.
const AppImage = "ghcr.io/jaimegago/svc:0.2.0"

// AppRepository is the image repository AppImage is pinned from. Inject uses
// it to recognise a Deployment that runs the application, whatever version.
const AppRepository = "ghcr.io/jaimegago/svc"

// SymptomCrashLoopBackOff is the symptom a startup failure produces.
const SymptomCrashLoopBackOff = "CrashLoopBackOff"

// SymptomOOMKilled is the symptom memory growth past the container's limit
// produces: the kernel kills the process and the kubelet records OOMKilled.
const SymptomOOMKilled = "OOMKilled"

// Definition is one catalog entry: what a class needs declared, what the
// pinned application does with it, and the symptom it produces.
type Definition struct {
	Class Class
	// Summary is one line for help text and docs.
	Summary string
	// Params are the parameter names a Spec of this class carries, in
	// documented order. Inject reads them from --param; the provider reads
	// them from the scenario's fault block.
	Params []string
	// Symptom is the status the class produces, and what Inject waits for
	// when the caller declares no expect of its own.
	Symptom string
	// Keys documents which configuration keys the pinned application gives
	// this class's semantics, and how: for config.missing-key the keys it
	// requires and under what condition, for config.unbounded-cache the
	// settings whose Value it reads as no bound. A Spec naming a key outside
	// this set is refused: the application would not fail.
	Keys map[string]string
	// Value is, for classes that set a key rather than remove one, the
	// value that is the misconfiguration.
	Value string
	// MemoryLimit is the container memory limit a class whose symptom is
	// memory exhaustion renders on the application, and the limit a runtime
	// injection requires the target to already carry. Empty for classes
	// that need none.
	MemoryLimit string
}

// catalog is the single source of truth. Order is documented order.
var catalog = []Definition{
	{
		Class:   ClassConfigMissingKey,
		Summary: "a required configuration key is absent from the ConfigMap the application reads; the application fails startup naming it",
		Params:  []string{"configMap", "key"},
		Symptom: SymptomCrashLoopBackOff,
		Keys: map[string]string{
			"SMTP_PORT": "required when SMTP_HOST is set",
		},
	},
	{
		Class:   ClassConfigUnboundedCache,
		Summary: "a cache bound is set to the value the application reads as no bound; its record cache grows until the container is OOM-killed",
		Params:  []string{"env"},
		Symptom: SymptomOOMKilled,
		Keys: map[string]string{
			"CACHE_MAX_ENTRIES": "0 means no bound; the record cache keeps every record the directory sync delivers",
		},
		Value:       "0",
		MemoryLimit: "64Mi",
	},
}

// Catalog returns the fault classes the pinned application covers, keyed by
// class. Callers enumerate it rather than hardcoding members or a count.
func Catalog() map[Class]Definition {
	out := make(map[Class]Definition, len(catalog))
	for _, d := range catalog {
		out[d.Class] = d
	}
	return out
}

// Classes returns the catalog's class identifiers, sorted.
func Classes() []string {
	names := make([]string, 0, len(catalog))
	for _, d := range catalog {
		names = append(names, string(d.Class))
	}
	sort.Strings(names)
	return names
}

// Spec is a declared fault, resolved against the catalog.
type Spec struct {
	Class Class
	// ConfigMap applies to config.missing-key: the ConfigMap the key is
	// absent from.
	ConfigMap string
	// Key is the configuration key the fault concerns: the key absent from
	// ConfigMap for config.missing-key, the environment variable set to the
	// class's Value for config.unbounded-cache.
	Key string
}

// Expect is a declared symptom: the status the workload must exhibit before
// the environment is handed over, and optionally the restart count each pod
// must have reached. Status is matched against the pod's container state
// reasons (see WaitForSymptom).
type Expect struct {
	Status string
	// Restarts is the minimum restart count every pod must reach. Zero
	// asks for none beyond what Status implies.
	Restarts int
}

// Parse resolves a class name and its parameters into a Spec, validating
// against the catalog. It is the one construction path for both triggers,
// so a scenario block and a --param list are held to the same rules.
func Parse(class string, params map[string]string) (Spec, error) {
	def, ok := Catalog()[Class(strings.TrimSpace(class))]
	if !ok {
		return Spec{}, fmt.Errorf("unknown fault class %q; accepted classes: %s", class, strings.Join(Classes(), ", "))
	}
	for k := range params {
		if !contains(def.Params, k) {
			return Spec{}, fmt.Errorf("fault %s: unknown parameter %q; accepted: %s", def.Class, k, strings.Join(def.Params, ", "))
		}
	}
	for _, p := range def.Params {
		if strings.TrimSpace(params[p]) == "" {
			return Spec{}, fmt.Errorf("fault %s: parameter %q is required", def.Class, p)
		}
	}
	spec := Spec{Class: def.Class}
	switch def.Class {
	case ClassConfigMissingKey:
		spec.ConfigMap = params["configMap"]
		spec.Key = params["key"]
		if _, covered := def.Keys[spec.Key]; !covered {
			return Spec{}, fmt.Errorf("fault %s: key %q is not one %s validates (%s); the application would start",
				def.Class, spec.Key, AppImage, describeKeys(def.Keys))
		}
	case ClassConfigUnboundedCache:
		spec.Key = params["env"]
		if _, covered := def.Keys[spec.Key]; !covered {
			return Spec{}, fmt.Errorf("fault %s: setting %q is not one %s reads as a cache bound (%s); the application would stay bounded",
				def.Class, spec.Key, AppImage, describeKeys(def.Keys))
		}
	}
	return spec, nil
}

// Definition returns the catalog entry for the spec's class.
func (s Spec) Definition() Definition {
	return Catalog()[s.Class]
}

// String renders the spec for logs and plans.
func (s Spec) String() string {
	switch s.Class {
	case ClassConfigMissingKey:
		return fmt.Sprintf("%s configMap=%s key=%s", s.Class, s.ConfigMap, s.Key)
	case ClassConfigUnboundedCache:
		return fmt.Sprintf("%s env=%s", s.Class, s.Key)
	default:
		return string(s.Class)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func describeKeys(keys map[string]string) string {
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, fmt.Sprintf("%s, %s", k, keys[k]))
	}
	return strings.Join(parts, "; ")
}
