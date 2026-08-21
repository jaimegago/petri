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

// AppImage is the pinned application image. It is a release surface:
// Catalog documents, per class, what this version of the image genuinely
// does, and a declaration outside that coverage is refused rather than
// provisioned against an application that would not fail.
const AppImage = "ghcr.io/jaimegago/svc:0.1.0"

// AppRepository is the image repository AppImage is pinned from. Inject uses
// it to recognise a Deployment that runs the application, whatever version.
const AppRepository = "ghcr.io/jaimegago/svc"

// SymptomCrashLoopBackOff is the symptom a startup failure produces.
const SymptomCrashLoopBackOff = "CrashLoopBackOff"

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
	// Keys documents, for config.missing-key, which keys the pinned
	// application validates and under what condition. A Spec naming a key
	// outside this set is refused: the application would start.
	Keys map[string]string
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
	// ConfigMap and Key apply to config.missing-key.
	ConfigMap string
	Key       string
}

// Expect is a declared symptom: the status the workload must exhibit before
// the environment is handed over. Status is matched against the pod's
// container state reasons (see WaitForSymptom).
type Expect struct {
	Status string
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
