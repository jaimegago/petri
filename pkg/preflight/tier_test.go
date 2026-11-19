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
// reads the way kubectl reports a missing object, as an error. Logs are keyed
// namespace/pod.
type fakeTierCluster struct {
	objects map[string]string
	lists   map[string]string
	logs    map[string]string
	// logReads records every PodLogs call, as namespace/pod/container.
	logReads []string
	// err, when set, fails every read: the cluster is unreachable.
	err error
}

func (f *fakeTierCluster) PodLogs(_ context.Context, namespace, pod, container string, _ int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.logReads = append(f.logReads, namespace+"/"+pod+"/"+container)
	if out, ok := f.logs[namespace+"/"+pod]; ok {
		return out, nil
	}
	return "", fmt.Errorf("reading logs of %s/%s: Error from server (NotFound)", namespace, pod)
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

// podJSON renders one pod of a `kubectl get pods -o json` list.
func podJSON(name, node, phase string, containers ...string) string {
	var cs []string
	for _, c := range containers {
		cs = append(cs, fmt.Sprintf(`{"name":%q}`, c))
	}
	return fmt.Sprintf(`{"metadata":{"name":%q},"spec":{"nodeName":%q,"containers":[%s]},"status":{"phase":%q}}`,
		name, node, strings.Join(cs, ","), phase)
}

const establishedCRD = `{"status":{"conditions":[{"type":"NamesAccepted","status":"True"},{"type":"Established","status":"True"}]}}`

// tier1Nodes are the nodes of tier1Cluster.
var tier1Nodes = []string{"lab-control-plane", "lab-worker", "lab-worker2", "lab-worker3"}

// tier1Cluster is a cluster that meets Tier 1 the way a petri lab does: one
// control-plane and three workers, a network proxy on each whose log can be
// read, and every component running.
func tier1Cluster() *fakeTierCluster {
	f := &fakeTierCluster{objects: map[string]string{}, lists: map[string]string{}, logs: map[string]string{}}
	f.lists["nodes/"] = listJSON(
		nodeJSON("lab-control-plane", true, true),
		nodeJSON("lab-worker", false, true),
		nodeJSON("lab-worker2", false, true),
		nodeJSON("lab-worker3", false, true),
	)
	var proxies []string
	for _, n := range tier1Nodes {
		proxies = append(proxies, podJSON("kube-proxy-"+n, n, "Running", "kube-proxy"))
		f.logs["kube-system/kube-proxy-"+n] = "I1119 10:00:00 server.go:1 Starting\nI1119 10:00:01 proxier.go:1 Synced\n"
	}
	f.lists["pods/kube-system"] = listJSON(proxies...)
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
	// The logging evidence names the pod read on each node.
	logging := checkNamed(t, ev, "observability-logging")
	if logging.Status != TierStatusMet || !strings.HasPrefix(logging.Observed, "pod logs read through the API on 4 of 4 Ready nodes (") {
		t.Errorf("observability-logging check = %+v", logging)
	}
	for _, n := range tier1Nodes {
		if want := n + ": kube-system/kube-proxy-" + n; !strings.Contains(logging.Observed, want) {
			t.Errorf("observability-logging observed %q, missing %q", logging.Observed, want)
		}
	}
	// Checks follow the profile's Tier 1 order.
	var order []string
	for _, c := range ev.Checks {
		order = append(order, c.Requirement)
	}
	want := "nodes observability-metrics observability-logging observability-dashboards ingress certificate-management"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("checks = %q, want %q", got, want)
	}
}

// The logging check reads a log on every Ready node, through the container
// the pod names, and stops at the first pod on a node that returns a line.
func TestVerifyTier_LoggingReadsOnePodPerNode(t *testing.T) {
	t.Parallel()
	f := tier1Cluster()
	// lab-worker runs three more pods: one not Running, one whose log is
	// empty and sorts first, one that would be read only if the others
	// failed.
	f.lists["pods/kube-system"] = listJSON(
		podJSON("kube-proxy-lab-control-plane", "lab-control-plane", "Running", "kube-proxy"),
		podJSON("a-pending", "lab-worker", "Pending", "main"),
		podJSON("b-silent", "lab-worker", "Running", "main"),
		podJSON("kube-proxy-lab-worker", "lab-worker", "Running", "kube-proxy"),
		podJSON("z-unread", "lab-worker", "Running", "main"),
		podJSON("kube-proxy-lab-worker2", "lab-worker2", "Running", "kube-proxy"),
		podJSON("kube-proxy-lab-worker3", "lab-worker3", "Running", "kube-proxy", "sidecar"),
	)
	f.logs["kube-system/b-silent"] = "\n"
	f.logs["kube-system/z-unread"] = "a line\n"
	ev := VerifyTier(context.Background(), f)

	logging := checkNamed(t, ev, "observability-logging")
	if logging.Status != TierStatusMet || !strings.Contains(logging.Observed, "lab-worker: kube-system/kube-proxy-lab-worker,") {
		t.Errorf("observability-logging check = %+v", logging)
	}
	wantReads := []string{
		"kube-system/kube-proxy-lab-control-plane/kube-proxy",
		"kube-system/b-silent/main",
		"kube-system/kube-proxy-lab-worker/kube-proxy",
		"kube-system/kube-proxy-lab-worker2/kube-proxy",
		"kube-system/kube-proxy-lab-worker3/kube-proxy",
	}
	if strings.Join(f.logReads, " ") != strings.Join(wantReads, " ") {
		t.Errorf("log reads = %v, want %v", f.logReads, wantReads)
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
		{
			// The API server could not reach the node's kubelet.
			name: "a node whose pod logs cannot be read",
			break_: func(f *fakeTierCluster) {
				delete(f.logs, "kube-system/kube-proxy-lab-worker2")
			},
			requirement:  "observability-logging",
			wantObserved: "on 3 of 4 Ready nodes (lab-control-plane: kube-system/kube-proxy-lab-control-plane, lab-worker: kube-system/kube-proxy-lab-worker, lab-worker3: kube-system/kube-proxy-lab-worker3); not read: lab-worker2: the log of kube-system/kube-proxy-lab-worker2 could not be read: reading logs of kube-system/kube-proxy-lab-worker2: Error from server (NotFound)",
		},
		{
			name: "a node whose only pod has written nothing",
			break_: func(f *fakeTierCluster) {
				f.logs["kube-system/kube-proxy-lab-worker3"] = ""
			},
			requirement:  "observability-logging",
			wantObserved: "not read: lab-worker3: the log of kube-system/kube-proxy-lab-worker3 is empty",
		},
		{
			name: "a node with no running pod to read",
			break_: func(f *fakeTierCluster) {
				f.lists["pods/kube-system"] = listJSON(
					podJSON("kube-proxy-lab-control-plane", "lab-control-plane", "Running", "kube-proxy"),
					podJSON("kube-proxy-lab-worker", "lab-worker", "Running", "kube-proxy"),
					podJSON("kube-proxy-lab-worker2", "lab-worker2", "Running", "kube-proxy"),
					podJSON("kube-proxy-lab-worker3", "lab-worker3", "Pending", "kube-proxy"),
				)
			},
			requirement:  "observability-logging",
			wantObserved: "not read: lab-worker3: no running pod in kube-system",
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

// Namespaces, security zones and microservices are not the provider's to
// judge: the profile counts them from what scenarios declare and the
// evaluator judges them (profile.md §8). What the lab holds is reported as
// observations, which carry no requirement and no status, and no check
// stands for any of the three.
func TestVerifyTier_ScenarioCountedFiguresAreObservationsNotChecks(t *testing.T) {
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
		t.Fatalf("Met = false: an observation made the tier unmet: %s", ev.UnmetSummary())
	}
	for _, c := range ev.Checks {
		switch c.Requirement {
		case "namespaces", "security-zones", "microservices":
			t.Errorf("evidence carries a %q check: %+v", c.Requirement, c)
		}
		if c.Status != TierStatusMet && c.Status != TierStatusUnmet {
			t.Errorf("check %q has status %q, want met or unmet", c.Requirement, c.Status)
		}
	}
	want := []TierObservation{
		{Subject: "namespaces", Observed: "7: cert-manager, default, ingress-nginx, kube-system, monitoring, scenario-a, scenario-b"},
		{Subject: "security-zones", Observed: "2 zones labelled on namespaces (petri.oasis/zone): dev, prod"},
		{Subject: "deployments", Observed: "5 Deployments by namespace: default 3, monitoring 2"},
	}
	if len(ev.Observations) != len(want) {
		t.Fatalf("observations = %+v, want %+v", ev.Observations, want)
	}
	for i, o := range ev.Observations {
		if o != want[i] {
			t.Errorf("observation %d = %+v, want %+v", i, o, want[i])
		}
	}
}

// A namespace list that cannot be read costs the observations and nothing
// else: they bear on no verdict.
func TestVerifyTier_UnreadableObservationsLeaveTheTierMet(t *testing.T) {
	t.Parallel()
	f := tier1Cluster()
	f.lists["namespaces/"] = `not json`
	ev := VerifyTier(context.Background(), f)

	if !ev.Met {
		t.Fatalf("Met = false: %s", ev.UnmetSummary())
	}
	if len(ev.Observations) != 3 || !strings.Contains(ev.Observations[0].Observed, "could not be parsed") {
		t.Errorf("observations = %+v", ev.Observations)
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
