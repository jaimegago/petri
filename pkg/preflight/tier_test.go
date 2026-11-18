package preflight

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/observability"
	"github.com/jaimegago/petri/pkg/generators/platform"
	"github.com/jaimegago/petri/pkg/types"
)

// fakeTierCluster is an in-memory TierReader. Objects are keyed
// kind/namespace/name and lists kind/namespace, as JSON; a key that is absent
// reads the way kubectl reports a missing object, as an error.
type fakeTierCluster struct {
	objects map[string]string
	lists   map[string]string
	// err, when set, fails every read: the cluster is unreachable.
	err error
}

func (f *fakeTierCluster) GetResource(_ context.Context, kind, namespace, name string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if raw, ok := f.objects[kind+"/"+namespace+"/"+name]; ok {
		return raw, nil
	}
	return "", fmt.Errorf("getting %s %s in %s: Error from server (NotFound)", kind, name, namespace)
}

func (f *fakeTierCluster) ListResources(_ context.Context, kind, namespace string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if raw, ok := f.lists[kind+"/"+namespace]; ok {
		return raw, nil
	}
	return `{"items":[]}`, nil
}

// nodeJSON renders one node of a `kubectl get nodes -o json` list.
func nodeJSON(name string, controlPlane, ready bool) string {
	labels := `{}`
	if controlPlane {
		labels = `{"node-role.kubernetes.io/control-plane":""}`
	}
	status := "False"
	if ready {
		status = "True"
	}
	return fmt.Sprintf(`{"metadata":{"name":%q,"labels":%s},"status":{"conditions":[{"type":"MemoryPressure","status":"False"},{"type":"Ready","status":%q}]}}`,
		name, labels, status)
}

func listJSON(items ...string) string {
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

func deploymentJSON(image string, ready int) string {
	return fmt.Sprintf(`{"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":%q}]}}},"status":{"readyReplicas":%d}}`, image, ready)
}

const establishedCRD = `{"status":{"conditions":[{"type":"NamesAccepted","status":"True"},{"type":"Established","status":"True"}]}}`

// tier1Cluster is a cluster that meets Tier 1 the way a petri lab does: one
// control-plane and three workers, and every component running.
func tier1Cluster() *fakeTierCluster {
	f := &fakeTierCluster{objects: map[string]string{}, lists: map[string]string{}}
	f.lists["nodes/"] = listJSON(
		nodeJSON("lab-control-plane", true, true),
		nodeJSON("lab-worker", false, true),
		nodeJSON("lab-worker2", false, true),
		nodeJSON("lab-worker3", false, true),
	)
	for _, comp := range tier1Components {
		for _, w := range comp.workloads {
			f.objects["deployments/"+w.namespace+"/"+w.name] = deploymentJSON("registry.example/"+w.name+":v1", 1)
		}
		for _, crd := range comp.crds {
			f.objects["customresourcedefinitions//"+crd] = establishedCRD
		}
	}
	f.lists["namespaces/"] = listJSON(
		`{"metadata":{"name":"default"}}`,
		`{"metadata":{"name":"monitoring"}}`,
		`{"metadata":{"name":"cert-manager"}}`,
	)
	f.lists["deployments/default"] = listJSON(`{}`, `{}`, `{}`)
	f.lists["deployments/monitoring"] = listJSON(`{}`, `{}`)
	return f
}

func checkNamed(t *testing.T, ev TierEvidence, requirement string) TierCheck {
	t.Helper()
	for _, c := range ev.Checks {
		if c.Requirement == requirement {
			return c
		}
	}
	t.Fatalf("evidence has no %q check: %+v", requirement, ev.Checks)
	return TierCheck{}
}

func TestVerifyTier_MetByALabThatHasEverything(t *testing.T) {
	t.Parallel()
	ev := VerifyTier(context.Background(), tier1Cluster())

	if ev.Tier != 1 || !ev.Met {
		t.Fatalf("Tier = %d, Met = %v, want tier 1 met; unmet: %s", ev.Tier, ev.Met, ev.UnmetSummary())
	}
	if ev.ObservedAt.IsZero() {
		t.Error("ObservedAt is unset")
	}
	if got := checkNamed(t, ev, "nodes"); got.Status != TierStatusMet || got.Observed != "4 Ready of 4 (1 control-plane, 3 worker)" {
		t.Errorf("nodes check = %+v", got)
	}
	// The evidence names what is running, not only that something is.
	cm := checkNamed(t, ev, "certificate-management")
	for _, want := range []string{
		"deployment cert-manager/cert-manager 1/1 ready (registry.example/cert-manager:v1)",
		"deployment cert-manager/cert-manager-webhook 1/1 ready",
		"CRDs established: certificates.cert-manager.io",
		"clusterissuers.cert-manager.io",
	} {
		if !strings.Contains(cm.Observed, want) {
			t.Errorf("certificate-management observed %q, missing %q", cm.Observed, want)
		}
	}
	for _, requirement := range []string{"observability-metrics", "observability-dashboards", "ingress"} {
		if got := checkNamed(t, ev, requirement); got.Status != TierStatusMet {
			t.Errorf("%s check = %+v, want met", requirement, got)
		}
	}
}

// A gitops controller is not a Tier 1 requirement: the maintainer moved it to
// Tier 2 on 2026-11-18, and the evidence does not ask for one.
func TestVerifyTier_AsksForNoGitOpsController(t *testing.T) {
	t.Parallel()
	ev := VerifyTier(context.Background(), tier1Cluster())
	for _, c := range ev.Checks {
		text := strings.ToLower(c.Requirement + " " + c.Required)
		for _, word := range []string{"gitops", "argocd", "flux"} {
			if strings.Contains(text, word) {
				t.Errorf("check %q asks for a gitops controller", c.Requirement)
			}
		}
	}
}

func TestVerifyTier_Unmet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		break_      func(f *fakeTierCluster)
		requirement string
		// wantObserved is a fragment the failed check's observation carries.
		wantObserved string
	}{
		{
			name: "a single-node lab",
			break_: func(f *fakeTierCluster) {
				f.lists["nodes/"] = listJSON(nodeJSON("lab-control-plane", true, true))
			},
			requirement:  "nodes",
			wantObserved: "1 Ready of 1 (1 control-plane, 0 worker)",
		},
		{
			name: "three nodes, one of them not Ready",
			break_: func(f *fakeTierCluster) {
				f.lists["nodes/"] = listJSON(
					nodeJSON("lab-control-plane", true, true),
					nodeJSON("lab-worker", false, true),
					nodeJSON("lab-worker2", false, false),
				)
			},
			requirement:  "nodes",
			wantObserved: "2 Ready of 3",
		},
		{
			// What a level-1 lab had before this check existed: a
			// cert-manager namespace and nothing in it.
			name: "a certificate-management placeholder",
			break_: func(f *fakeTierCluster) {
				for key := range f.objects {
					if strings.Contains(key, "cert-manager") {
						delete(f.objects, key)
					}
				}
			},
			requirement:  "certificate-management",
			wantObserved: "CRDs not established: certificates.cert-manager.io",
		},
		{
			name: "a certificate controller with no ready replica",
			break_: func(f *fakeTierCluster) {
				f.objects["deployments/cert-manager/cert-manager-webhook"] = deploymentJSON("quay.io/jetstack/cert-manager-webhook:v1", 0)
			},
			requirement:  "certificate-management",
			wantObserved: "deployment cert-manager/cert-manager-webhook 0/1 ready",
		},
		{
			name: "a certificate controller whose CRD is not served",
			break_: func(f *fakeTierCluster) {
				f.objects["customresourcedefinitions//issuers.cert-manager.io"] = `{"status":{"conditions":[{"type":"Established","status":"False"}]}}`
			},
			requirement:  "certificate-management",
			wantObserved: "CRDs not established: issuers.cert-manager.io",
		},
		{
			name: "no dashboards",
			break_: func(f *fakeTierCluster) {
				delete(f.objects, "deployments/monitoring/grafana")
			},
			requirement:  "observability-dashboards",
			wantObserved: "deployment monitoring/grafana could not be read",
		},
		{
			name: "no ingress controller",
			break_: func(f *fakeTierCluster) {
				f.objects["deployments/ingress-nginx/ingress-nginx-controller"] = ""
			},
			requirement:  "ingress",
			wantObserved: "deployment ingress-nginx/ingress-nginx-controller not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := tier1Cluster()
			tt.break_(f)
			ev := VerifyTier(context.Background(), f)

			if ev.Met {
				t.Fatal("Met = true")
			}
			unmet := ev.Unmet()
			if len(unmet) != 1 || unmet[0].Requirement != tt.requirement {
				t.Fatalf("unmet = %+v, want only %q", unmet, tt.requirement)
			}
			if !strings.Contains(unmet[0].Observed, tt.wantObserved) {
				t.Errorf("observed %q, want it to carry %q", unmet[0].Observed, tt.wantObserved)
			}
			// The summary an operator reads names the requirement, what
			// was seen and what was asked.
			summary := ev.UnmetSummary()
			if !strings.Contains(summary, tt.requirement+": ") || !strings.Contains(summary, "(required: ") {
				t.Errorf("UnmetSummary = %q", summary)
			}
		})
	}
}

// A cluster that cannot be read is not evidence the tier is met.
func TestVerifyTier_UnreadableClusterIsUnmet(t *testing.T) {
	t.Parallel()
	f := tier1Cluster()
	f.err = errors.New("Unable to connect to the server: dial tcp 127.0.0.1:6443: connect: connection refused\nsecond line")
	ev := VerifyTier(context.Background(), f)

	if ev.Met {
		t.Fatal("Met = true for a cluster no read reached")
	}
	nodes := checkNamed(t, ev, "nodes")
	if nodes.Status != TierStatusUnmet || !strings.Contains(nodes.Observed, "connection refused") {
		t.Errorf("nodes check = %+v", nodes)
	}
	if strings.Contains(nodes.Observed, "second line") {
		t.Errorf("observation is not one line: %q", nodes.Observed)
	}

	if ev := VerifyTier(context.Background(), nil); ev.Met || len(ev.Unmet()) != 1 {
		t.Errorf("nil reader: evidence = %+v, want one unmet check", ev)
	}
}

// Namespaces, microservices and security zones are reported and not judged:
// the profile does not yet say what counts toward them, and a provider that
// picked an answer would be deciding it.
func TestVerifyTier_ReportsWithoutJudging(t *testing.T) {
	t.Parallel()
	f := tier1Cluster()
	// Far more namespaces than Tier 1's "at most 3", two of them zoned.
	f.lists["namespaces/"] = listJSON(
		`{"metadata":{"name":"default"}}`,
		`{"metadata":{"name":"kube-system"}}`,
		`{"metadata":{"name":"monitoring"}}`,
		`{"metadata":{"name":"cert-manager"}}`,
		`{"metadata":{"name":"ingress-nginx"}}`,
		`{"metadata":{"name":"scenario-a","labels":{"petri.oasis/zone":"prod"}}}`,
		`{"metadata":{"name":"scenario-b","labels":{"petri.oasis/zone":"dev"}}}`,
	)
	ev := VerifyTier(context.Background(), f)

	if !ev.Met {
		t.Fatalf("Met = false: an unjudged observation made the tier unmet: %s", ev.UnmetSummary())
	}
	ns := checkNamed(t, ev, "namespaces")
	if ns.Status != TierStatusNotAssessed || !strings.HasPrefix(ns.Observed, "7: cert-manager, default, ") {
		t.Errorf("namespaces check = %+v", ns)
	}
	zones := checkNamed(t, ev, "security-zones")
	if zones.Status != TierStatusNotAssessed || !strings.Contains(zones.Observed, "2 zones") || !strings.Contains(zones.Observed, "dev, prod") {
		t.Errorf("security-zones check = %+v", zones)
	}
	ms := checkNamed(t, ev, "microservices")
	if ms.Status != TierStatusNotAssessed || ms.Observed != "5 Deployments by namespace: default 3, monitoring 2" {
		t.Errorf("microservices check = %+v", ms)
	}
}

// The Deployments and CRDs the tier check looks for are the ones a level-1
// lab is given: every company's level 1, rendered by the real generators.
// A template renamed, or a component dropped from a level-1 list, fails here
// instead of at the first conformance handshake of a live lab.
func TestTierComponents_AreWhatTheGeneratorsInstall(t *testing.T) {
	t.Parallel()
	companies, err := config.LoadCompanies("../../configs/companies.yaml")
	if err != nil {
		t.Fatalf("loading companies: %v", err)
	}
	// acme is the company every OASIS run uses. techflow and cloudnative
	// list other observability stacks at level 1 and are not held to it.
	var acme *types.Company
	for i := range companies {
		if companies[i].Name == "acme" {
			acme = &companies[i]
		}
	}
	if acme == nil {
		t.Fatal("configs/companies.yaml has no acme")
	}
	spec, err := acme.GetLevel(1)
	if err != nil {
		t.Fatalf("acme level 1: %v", err)
	}
	tmplCtx := generators.NewTemplateContext(&types.Lab{Name: "lab", Level: 1}, acme, spec)

	platformGen, obsGen := platform.New(), observability.New()
	installed := map[generators.Deployment]bool{}
	for _, d := range platformGen.Deployments(tmplCtx) {
		installed[d] = true
	}
	for _, d := range obsGen.Deployments(tmplCtx) {
		installed[d] = true
	}
	files, err := platformGen.Generate(context.Background(), tmplCtx)
	if err != nil {
		t.Fatalf("generating platform manifests: %v", err)
	}
	var manifests strings.Builder
	for _, f := range files {
		manifests.WriteString(f.Content)
	}

	for _, comp := range tier1Components {
		for _, w := range comp.workloads {
			if !installed[generators.Deployment{Namespace: w.namespace, Name: w.name}] {
				t.Errorf("%s looks for deployment %s/%s, which no generator installs at acme level 1", comp.requirement, w.namespace, w.name)
			}
		}
		for _, crd := range comp.crds {
			if !strings.Contains(manifests.String(), `name: "`+crd+`"`) {
				t.Errorf("%s looks for CRD %s, which no platform manifest defines", comp.requirement, crd)
			}
		}
	}

	// Nothing acme's level 1 lists is left uninstalled: the list says what
	// the lab has.
	if left := append(platformGen.Unrendered(tmplCtx), obsGen.Unrendered(tmplCtx)...); len(left) != 0 {
		t.Errorf("acme level 1 lists components petri installs nothing for: %v", left)
	}
}
