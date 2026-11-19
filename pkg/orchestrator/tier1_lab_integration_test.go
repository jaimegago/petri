//go:build integration

package orchestrator_test

// A level-1 OASIS lab, stood up for real and held to the SI profile's Tier 1.
//
// The test creates its own kind cluster through the same orchestrator and
// provider `petri create --oasis` and `petri serve` use, with state and
// kubeconfig under a temp directory, and deletes it when it is done. It
// asserts four things against the live cluster, each read by kubectl and not
// through the code under test:
//
//  1. the lab has at least three nodes, and a scenario that declares three
//     named nodes provisions against them;
//  2. certificate management is a running controller with its CRDs, shown by
//     having it issue a certificate;
//  3. the provider's conformance response carries tier evidence that matches
//     what kubectl sees, and stops saying the tier is met when the cluster
//     stops meeting it;
//  4. the evidence's logging check names, on every Ready node, a pod whose
//     log kubectl reads too, and the evidence holds no check for the figures
//     the profile counts from scenarios.
//
// Run it with:
//
//	go test -tags integration -run TestIntegration_Level1LabMeetsTier1 -timeout 40m ./pkg/orchestrator/
//
// It needs Docker, kind and kubectl, and about ten minutes. Set
// TIER1_LAB_KEEP=1 to leave the cluster up afterwards.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/crypto"
	appsgen "github.com/jaimegago/petri/pkg/generators/apps"
	commitsgen "github.com/jaimegago/petri/pkg/generators/commits"
	gitopsgen "github.com/jaimegago/petri/pkg/generators/gitops"
	obsgen "github.com/jaimegago/petri/pkg/generators/observability"
	platformgen "github.com/jaimegago/petri/pkg/generators/platform"
	"github.com/jaimegago/petri/pkg/oasis"
	"github.com/jaimegago/petri/pkg/orchestrator"
	"github.com/jaimegago/petri/pkg/preflight"
	"github.com/jaimegago/petri/pkg/provisioners/kubectl"
	localprov "github.com/jaimegago/petri/pkg/provisioners/local"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

const siProfile = "oasis-profile-software-infrastructure"

func TestIntegration_Level1LabMeetsTier1(t *testing.T) {
	for _, bin := range []string{"docker", "kind", "kubectl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found in PATH; skipping", bin)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()

	lab, kubeconfig := createLevel1Lab(ctx, t)
	kube := &liveCluster{t: t, kubeconfig: kubeconfig}
	cluster := lab.Metadata.Clusters[0]

	// ── 1. Nodes ──────────────────────────────────────────────────────────
	nodes := kube.nodes(ctx)
	var ready, workers int
	for _, n := range nodes {
		if n.ready {
			ready++
		}
		if !n.controlPlane {
			workers++
		}
	}
	t.Logf("live cluster: %d nodes, %d Ready, %d workers", len(nodes), ready, workers)
	if len(nodes) < 3 || ready < 3 {
		t.Errorf("lab has %d nodes, %d Ready; Tier 1 requires at least 3", len(nodes), ready)
	}
	if workers < 3 {
		t.Errorf("lab has %d workers; C-OE-002 declares three nodes and binding prefers workers", workers)
	}
	if cluster.NodeCount != len(nodes) {
		t.Errorf("lab state records %d nodes, the cluster has %d", cluster.NodeCount, len(nodes))
	}

	// ── 2. Certificate management ────────────────────────────────────────
	for _, d := range []string{"cert-manager", "cert-manager-cainjector", "cert-manager-webhook"} {
		if got := kube.readyReplicas(ctx, "cert-manager", d); got < 1 {
			t.Errorf("deployment cert-manager/%s has %d ready replicas", d, got)
		}
	}
	controllerImage := kube.jsonpath(ctx, "deployment", "cert-manager", "cert-manager", "{.spec.template.spec.containers[0].image}")
	if want := "quay.io/jetstack/cert-manager-controller:" + platformgen.CertManagerVersion; controllerImage != want {
		t.Errorf("cert-manager controller runs %q, want the vendored release %q", controllerImage, want)
	}
	for _, crd := range []string{"certificates.cert-manager.io", "issuers.cert-manager.io", "clusterissuers.cert-manager.io"} {
		if got := kube.jsonpath(ctx, "crd", "", crd, `{.status.conditions[?(@.type=="Established")].status}`); got != "True" {
			t.Errorf("CRD %s Established = %q, want True", crd, got)
		}
	}
	kube.issueCertificate(ctx)

	// ── 3. Tier evidence, over the wire ──────────────────────────────────
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	provider := oasis.New(oasis.ProviderConfig{
		KubeconfigPath:   kubeconfig,
		AuditLogPath:     cluster.AuditLogPath,
		LabLevel:         lab.Level,
		OASISModeEnabled: cluster.OASISMode,
		LabNodeShape:     cluster.NodeShape.Summary(),
	}, chaos.NewKubeClient(kubeconfig), log)
	srv := httptest.NewServer(oasis.NewServer(provider, log).Handler())
	defer srv.Close()

	resp := getConformance(t, srv.URL)
	t.Logf("conformance: supported=%v tier=%d unmet=%v", resp.Supported, resp.Requirements.ComplexityTierSupported, resp.UnmetRequirements)
	if !resp.Supported {
		t.Errorf("supported = false on a fresh level-1 OASIS lab: %+v", resp.UnmetRequirements)
	}
	ev := resp.TierEvidence
	if ev == nil {
		t.Fatal("conformance response carries no tier_evidence")
	}
	for _, c := range ev.Checks {
		t.Logf("tier evidence: %-25s %-12s %s", c.Requirement, c.Status, c.Observed)
	}
	if ev.Tier != 1 || !ev.Met {
		t.Errorf("tier_evidence: tier = %d, met = %v, want tier 1 met: %s", ev.Tier, ev.Met, ev.UnmetSummary())
	}
	if resp.Requirements.ComplexityTierSupported != 1 {
		t.Errorf("complexity_tier_supported = %d, want the verified tier, 1", resp.Requirements.ComplexityTierSupported)
	}
	// The node count in the evidence is the one kubectl counted above.
	wantNodes := fmt.Sprintf("%d Ready of %d (%d control-plane, %d worker)", ready, len(nodes), len(nodes)-workers, workers)
	if got := evidenceCheck(t, ev, "nodes"); got.Status != preflight.TierStatusMet || got.Observed != wantNodes {
		t.Errorf("nodes evidence = %q (%s), kubectl sees %q", got.Observed, got.Status, wantNodes)
	}
	// The certificate-management evidence names the image kubectl read.
	cm := evidenceCheck(t, ev, "certificate-management")
	if cm.Status != preflight.TierStatusMet || !strings.Contains(cm.Observed, controllerImage) {
		t.Errorf("certificate-management evidence = %q (%s), want met and naming %s", cm.Observed, cm.Status, controllerImage)
	}
	if !strings.Contains(cm.Observed, "CRDs established: certificates.cert-manager.io") {
		t.Errorf("certificate-management evidence does not show the CRDs: %q", cm.Observed)
	}
	for _, requirement := range []string{"observability-metrics", "observability-dashboards", "ingress"} {
		if got := evidenceCheck(t, ev, requirement); got.Status != preflight.TierStatusMet {
			t.Errorf("%s evidence = %q (%s), want met", requirement, got.Observed, got.Status)
		}
	}
	// ── 4. Logging, and what is not the provider's to judge ──────────────
	// Workload logs are readable through the API on every Ready node: the
	// evidence names one pod per node, and kubectl reads that pod's log.
	logging := evidenceCheck(t, ev, "observability-logging")
	wantLogging := fmt.Sprintf("pod logs read through the API on %d of %d Ready nodes (", ready, ready)
	if logging.Status != preflight.TierStatusMet || !strings.HasPrefix(logging.Observed, wantLogging) {
		t.Errorf("observability-logging evidence = %q (%s), want met with prefix %q", logging.Observed, logging.Status, wantLogging)
	}
	read := strings.TrimSuffix(strings.TrimPrefix(logging.Observed, wantLogging), ")")
	probed := map[string]string{}
	for _, entry := range strings.Split(read, ", ") {
		if node, pod, ok := strings.Cut(entry, ": kube-system/"); ok {
			probed[node] = pod
		}
	}
	for _, n := range nodes {
		if !n.ready {
			continue
		}
		pod, ok := probed[n.name]
		if !ok {
			t.Errorf("observability-logging evidence names no pod on Ready node %s: %q", n.name, logging.Observed)
			continue
		}
		if got := kube.jsonpath(ctx, "pod", "kube-system", pod, "{.spec.nodeName}"); got != n.name {
			t.Errorf("evidence reads %s as a pod on %s; kubectl sees it on %s", pod, n.name, got)
		}
		if out := kube.run(ctx, "logs", pod, "-n", "kube-system", "--tail=1"); out == "" {
			t.Errorf("kubectl reads no log line from kube-system/%s on %s, which the evidence says it read", pod, n.name)
		}
	}
	// The profile counts namespaces, security zones and microservices from
	// what scenarios declare, and the evaluator judges them. The evidence
	// holds no check for them and no status but met and unmet; what the lab
	// holds is an observation.
	for _, c := range ev.Checks {
		switch c.Requirement {
		case "namespaces", "security-zones", "microservices":
			t.Errorf("tier evidence carries a %q check: %+v", c.Requirement, c)
		}
		if c.Status != preflight.TierStatusMet && c.Status != preflight.TierStatusUnmet {
			t.Errorf("check %q has status %q, want met or unmet", c.Requirement, c.Status)
		}
	}
	subjects := map[string]string{}
	for _, o := range ev.Observations {
		t.Logf("tier observation: %-22s %s", o.Subject, o.Observed)
		subjects[o.Subject] = o.Observed
	}
	for _, subject := range []string{"namespaces", "security-zones", "deployments"} {
		if subjects[subject] == "" {
			t.Errorf("tier evidence has no %q observation", subject)
		}
	}
	if got := kube.run(ctx, "get", "namespaces", "-o", "name"); !strings.HasPrefix(subjects["namespaces"], fmt.Sprintf("%d: ", len(strings.Fields(got)))) {
		t.Errorf("namespaces observation = %q, kubectl sees %d namespaces", subjects["namespaces"], len(strings.Fields(got)))
	}
	// Nothing the level lists is missing from the lab.
	if len(lab.Metadata.NotInstalled) != 0 {
		t.Errorf("lab records components listed and not installed: %v", lab.Metadata.NotInstalled)
	}

	// ── 1, continued. Declared nodes bind to real ones ───────────────────
	// C-OE-002's node block: three nodes, each verified against a real
	// worker's allocatable. A single-node lab refused this.
	if shape := cluster.NodeShape; shape == nil || !shape.Shaped {
		t.Logf("lab is unshaped (%s); provisioning the three nodes without allocatable facts", cluster.NodeShape.Summary())
		provisionThreeNodes(ctx, t, provider, false)
	} else {
		provisionThreeNodes(ctx, t, provider, true)
	}

	// ── 3, continued. The evidence follows the cluster ───────────────────
	// Take the certificate controller away. The lab's level has not
	// changed; what it has, has.
	kube.run(ctx, "scale", "deployment/cert-manager", "-n", "cert-manager", "--replicas=0")
	kube.run(ctx, "wait", "--for=delete", "pod", "-l", "app.kubernetes.io/component=controller", "-n", "cert-manager", "--timeout=120s")

	// Nothing the tier rests on has been restarted since the lab came up:
	// evidence that is met between two crashes is not evidence. The ingress
	// controller used to be OOM-killed on every reload.
	for _, pods := range []struct{ namespace, selector string }{
		{"ingress-nginx", "app=ingress-nginx"},
		{"monitoring", "app=prometheus"},
		{"monitoring", "app=grafana"},
		{"cert-manager", "app.kubernetes.io/instance=cert-manager"},
	} {
		restarts := kube.run(ctx, "get", "pods", "-n", pods.namespace, "-l", pods.selector,
			"-o", "jsonpath={range .items[*]}{.metadata.name}={.status.containerStatuses[*].restartCount} {end}")
		if restarts == "" {
			t.Errorf("no pods match %s in %s", pods.selector, pods.namespace)
		}
		for _, pod := range strings.Fields(restarts) {
			if !strings.HasSuffix(pod, "=0") {
				t.Errorf("pod restarted since the lab was created: %s/%s", pods.namespace, pod)
			}
		}
	}

	broken := getConformance(t, srv.URL)
	if broken.Supported {
		t.Error("supported = true with the certificate controller scaled to zero")
	}
	if broken.TierEvidence == nil || broken.TierEvidence.Met {
		t.Fatalf("tier_evidence = %+v, want unmet once the controller is gone", broken.TierEvidence)
	}
	if unmet := broken.TierEvidence.Unmet(); len(unmet) != 1 {
		t.Errorf("unmet checks = %+v, want certificate-management alone: nothing else was touched", unmet)
	}
	if got := evidenceCheck(t, broken.TierEvidence, "certificate-management"); got.Status != preflight.TierStatusUnmet ||
		!strings.Contains(got.Observed, "deployment cert-manager/cert-manager 0/0 ready") {
		t.Errorf("certificate-management evidence = %q (%s), want unmet at 0/0 ready", got.Observed, got.Status)
	}
	var reason string
	for _, u := range broken.UnmetRequirements {
		if u.Requirement == "complexity_tier_supported" {
			reason = u.Reason
		}
	}
	t.Logf("unmet reason: %s", reason)
	if !strings.Contains(reason, "does not meet tier 1") || !strings.Contains(reason, "certificate-management") {
		t.Errorf("unmet_requirements does not say which tier requirement failed: %q", reason)
	}
}

// createLevel1Lab stands up acme's level-1 OASIS lab on a fresh kind cluster
// and registers its deletion. It returns the lab as the orchestrator left it
// and the path of its kubeconfig.
func createLevel1Lab(ctx context.Context, t *testing.T) (*types.Lab, string) {
	t.Helper()

	// The cluster name appears in node names. It avoids the provider's own
	// name, which one test forbids in anything an agent can read, and the
	// "e2e-" prefix, which the joe-oasis-e2e harness owns.
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generating cluster name: %v", err)
	}
	name := "tier1-" + hex.EncodeToString(suffix)

	// A short path of its own, not t.TempDir(): the audit directory is
	// bind-mounted into the node, and Docker Desktop shares /tmp.
	dir, err := os.MkdirTemp("/tmp", "tier1-lab-")
	if err != nil {
		t.Fatalf("creating work dir: %v", err)
	}
	keep := os.Getenv("TIER1_LAB_KEEP") != ""
	if !keep {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}

	mgr, err := state.NewSQLiteManager(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating key: %v", err)
	}
	cipher, err := crypto.NewAESCipherFromKey(key)
	if err != nil {
		t.Fatalf("building cipher: %v", err)
	}
	local := localprov.New(localprov.Config{KubeconfigDir: filepath.Join(dir, "kubeconfigs")})
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	orch := orchestrator.New(orchestrator.Config{WorkDir: filepath.Join(dir, "labs")}, orchestrator.Deps{
		State:     mgr,
		Cipher:    cipher,
		Log:       log,
		LocalProv: local,
		KubectlFactory: func(kubeconfigPath string) orchestrator.KubectlClient {
			return kubectl.New(kubectl.Config{KubeconfigPath: kubeconfigPath})
		},
		GitOpsGen:        gitopsgen.New(),
		AppsGen:          appsgen.New(),
		ObservabilityGen: obsgen.New(),
		PlatformGen:      platformgen.New(),
		CommitsGen:       commitsgen.New(),
	})

	companies, err := config.LoadCompanies("../../configs/companies.yaml")
	if err != nil {
		t.Fatalf("loading companies: %v", err)
	}
	var company *types.Company
	for i := range companies {
		if companies[i].Name == "acme" {
			company = &companies[i]
		}
	}
	if company == nil {
		t.Fatal("configs/companies.yaml has no acme")
	}
	spec, err := company.GetLevel(1)
	if err != nil {
		t.Fatalf("acme level 1: %v", err)
	}

	now := time.Now().UTC()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          name,
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusCreating,
		CreatedAt:     now,
		TTLHours:      2,
		ExpiresAt:     now.Add(2 * time.Hour),
	}
	if err := mgr.CreateLab(ctx, lab); err != nil {
		t.Fatalf("recording lab: %v", err)
	}

	// Registered before Create: a create that fails half-way rolls back,
	// and one that is interrupted may not.
	t.Cleanup(func() {
		if keep {
			t.Logf("TIER1_LAB_KEEP set; leaving kind cluster %q and %s", name, dir)
			return
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer ccancel()
		if err := local.Delete(cctx, name); err != nil {
			t.Logf("deleting kind cluster %q: %v", name, err)
		}
	})

	t.Logf("creating level-1 OASIS lab %q", name)
	if err := orch.Create(ctx, orchestrator.CreateOptions{
		Lab:       lab,
		Company:   company,
		Spec:      spec,
		OASISMode: true,
	}); err != nil {
		t.Fatalf("creating lab: %v", err)
	}
	if lab.Status != types.LabStatusActive || len(lab.Metadata.Clusters) != 1 {
		t.Fatalf("lab status = %s with %d clusters, want ACTIVE with one", lab.Status, len(lab.Metadata.Clusters))
	}
	return lab, lab.Metadata.Clusters[0].KubeconfigPath
}

// conformanceWire is the conformance response as a runner decodes it.
type conformanceWire struct {
	Supported    bool `json:"supported"`
	Requirements struct {
		ComplexityTierSupported int `json:"complexity_tier_supported"`
	} `json:"requirements"`
	UnmetRequirements []oasis.UnmetRequirement `json:"unmet_requirements"`
	TierEvidence      *preflight.TierEvidence  `json:"tier_evidence"`
}

func getConformance(t *testing.T, baseURL string) conformanceWire {
	t.Helper()
	httpResp, err := http.Get(baseURL + "/v1/conformance?profile=" + siProfile)
	if err != nil {
		t.Fatalf("GET /v1/conformance: %v", err)
	}
	defer httpResp.Body.Close() //nolint:errcheck
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/conformance: status %d", httpResp.StatusCode)
	}
	var resp conformanceWire
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding conformance response: %v", err)
	}
	return resp
}

func evidenceCheck(t *testing.T, ev *preflight.TierEvidence, requirement string) preflight.TierCheck {
	t.Helper()
	for _, c := range ev.Checks {
		if c.Requirement == requirement {
			return c
		}
	}
	t.Fatalf("tier evidence has no %q check", requirement)
	return preflight.TierCheck{}
}

// provisionThreeNodes provisions an environment declaring three named nodes,
// as C-OE-002 does, and tears it down. With allocatable set, each declared
// node is verified against the real worker it is bound to.
func provisionThreeNodes(ctx context.Context, t *testing.T, provider oasis.OASISProvider, allocatable bool) {
	t.Helper()
	var entries []oasis.StateEntry
	for _, n := range []string{"worker-1", "worker-2", "worker-3"} {
		spec := map[string]any{"current_utilization": "60%"}
		if allocatable {
			spec["allocatable_cpu"] = localprov.OASISNodeAllocatableCPU
			spec["allocatable_memory"] = localprov.OASISNodeAllocatableMemory
		}
		entries = append(entries, oasis.StateEntry{Kind: "node", Name: n, Spec: spec})
	}
	resp, err := provider.Provision(ctx, oasis.ProvisionRequest{
		ScenarioID:  "tier1-three-nodes",
		Environment: oasis.EnvSpec{Type: "kubernetes-cluster", State: entries},
		Tier:        1,
	})
	if err != nil {
		t.Errorf("provisioning three declared nodes against the lab: %v", err)
		return
	}
	if _, err := provider.Teardown(ctx, oasis.TeardownRequest{EnvironmentID: resp.EnvironmentID}); err != nil {
		t.Logf("tearing down %s: %v", resp.EnvironmentID, err)
	}
}

// liveCluster reads the lab through kubectl, independently of the provider.
type liveCluster struct {
	t          *testing.T
	kubeconfig string
}

func (c *liveCluster) output(ctx context.Context, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", c.kubeconfig}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (c *liveCluster) run(ctx context.Context, args ...string) string {
	c.t.Helper()
	out, err := c.output(ctx, "", args...)
	if err != nil {
		c.t.Fatal(err)
	}
	return out
}

func (c *liveCluster) jsonpath(ctx context.Context, kind, namespace, name, path string) string {
	c.t.Helper()
	args := []string{"get", kind, name, "-o", "jsonpath=" + path}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return c.run(ctx, args...)
}

func (c *liveCluster) readyReplicas(ctx context.Context, namespace, deployment string) int {
	c.t.Helper()
	var n int
	_, _ = fmt.Sscanf(c.jsonpath(ctx, "deployment", namespace, deployment, "{.status.readyReplicas}"), "%d", &n)
	return n
}

type liveNode struct {
	name         string
	controlPlane bool
	ready        bool
}

func (c *liveCluster) nodes(ctx context.Context) []liveNode {
	c.t.Helper()
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(c.run(ctx, "get", "nodes", "-o", "json")), &list); err != nil {
		c.t.Fatalf("parsing node list: %v", err)
	}
	var out []liveNode
	for _, n := range list.Items {
		node := liveNode{name: n.Metadata.Name}
		_, node.controlPlane = n.Metadata.Labels["node-role.kubernetes.io/control-plane"]
		for _, cond := range n.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				node.ready = true
			}
		}
		out = append(out, node)
	}
	return out
}

// issueCertificate has the lab's certificate controller issue a certificate
// from a self-signed issuer and checks the Secret it writes. A namespace and
// a note pass no part of this; a controller, its webhook and its CRDs pass
// all of it.
func (c *liveCluster) issueCertificate(ctx context.Context) {
	c.t.Helper()
	const ns = "tier1-cert-check"
	manifest := `apiVersion: v1
kind: Namespace
metadata:
  name: ` + ns + `
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: selfsigned
  namespace: ` + ns + `
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: tier1-check
  namespace: ` + ns + `
spec:
  secretName: tier1-check-tls
  commonName: tier1-check.example.internal
  dnsNames:
  - tier1-check.example.internal
  issuerRef:
    name: selfsigned
    kind: Issuer
`
	// The webhook answers a moment after its Deployment reports ready.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, err := c.output(ctx, manifest, "apply", "-f", "-")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("applying an Issuer and a Certificate: %v", err)
		}
		time.Sleep(5 * time.Second)
	}
	defer func() {
		_, _ = c.output(context.Background(), "", "delete", "namespace", ns, "--wait=false")
	}()

	if _, err := c.output(ctx, "", "wait", "--for=condition=Ready", "certificate/tier1-check", "-n", ns, "--timeout=180s"); err != nil {
		c.t.Errorf("the lab's certificate controller did not issue a certificate: %v", err)
		return
	}
	crt := c.jsonpath(ctx, "secret", ns, "tier1-check-tls", `{.data.tls\.crt}`)
	if crt == "" {
		c.t.Error("certificate is Ready and its Secret holds no tls.crt")
		return
	}
	c.t.Logf("certificate issued by the lab's controller: secret %s/tier1-check-tls, tls.crt %d bytes (base64)", ns, len(crt))
}
