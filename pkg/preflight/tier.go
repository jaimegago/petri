package preflight

// Complexity-tier verification.
//
// OASIS Provider Conformance §3.6 requires a provider to verify that the
// environment meets the claimed complexity tier before an evaluation begins,
// and to include tier evidence in the report. VerifyTier is that
// verification: it reads the cluster and says what it found. Nothing in it
// takes a tier from the operator — the level a lab was created at is what
// somebody asked for, and this is what is there.
//
// The requirements are the Software Infrastructure profile's Tier 1
// environment (profile.md §8): one cluster of at least three nodes, metrics,
// logging and dashboards, ingress, certificate management. A gitops
// controller is not among them; the maintainer moved it to Tier 2 on
// 2026-11-18.
//
// Three of §8's Tier 1 bullets are not checks here: the namespace ceiling,
// the microservice count and the security zones. §8 counts all three from
// what scenario preconditions declare and gives them to the evaluation
// implementation to judge, from the scenarios it runs. They are not
// properties of the lab, so the provider has no verdict on them. VerifyTier
// still says what the cluster holds, as observations beside the checks.
//
// petri verifies Tier 1 and no higher tier. A lab created at level 2 or 3 is
// therefore evidenced at Tier 1.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// VerifiableTier is the highest complexity tier VerifyTier can verify, and so
// the highest a petri provider declares.
const VerifiableTier = 1

// tier1MinReadyNodes is the SI profile's Tier 1 node floor (profile.md §8).
const tier1MinReadyNodes = 3

// TierStatus is the outcome of one tier check.
type TierStatus string

// TierStatus values. They are wire strings: an evaluator reads them from the
// conformance response.
const (
	// TierStatusMet means the cluster shows what the tier requires.
	TierStatusMet TierStatus = "met"
	// TierStatusUnmet means the cluster does not show it, or could not be
	// read: what was not observed is not claimed.
	TierStatusUnmet TierStatus = "unmet"
)

// TierCheck is one requirement of a tier, beside what the cluster shows.
type TierCheck struct {
	// Requirement is a short stable identifier: "nodes",
	// "certificate-management".
	Requirement string `json:"requirement"`
	// Required is what the tier asks, in words.
	Required string `json:"required"`
	// Observed is what the cluster shows, read at ObservedAt.
	Observed string `json:"observed"`
	// Status is met or unmet.
	Status TierStatus `json:"status"`
}

// TierObservation is something the cluster holds that the provider reports
// and does not judge. It carries no requirement and no status.
type TierObservation struct {
	// Subject is a short stable identifier: "namespaces", "deployments".
	Subject string `json:"subject"`
	// Observed is what the cluster shows, read at ObservedAt.
	Observed string `json:"observed"`
}

// TierEvidence is the result of verifying a cluster against a tier.
type TierEvidence struct {
	// Tier is the complexity tier the checks belong to.
	Tier int `json:"tier"`
	// Met is true when no check is unmet.
	Met bool `json:"met"`
	// ObservedAt is when the cluster was read.
	ObservedAt time.Time `json:"observed_at"`
	// Checks is one entry per requirement the environment itself meets or
	// fails, in the profile's order.
	Checks []TierCheck `json:"checks"`
	// Observations is what the lab holds of the things the profile counts
	// from scenarios: namespaces, zone labels, Deployments. None is judged
	// and none bears on Met.
	Observations []TierObservation `json:"observations,omitempty"`
}

// Unmet returns the checks the cluster fails.
func (e TierEvidence) Unmet() []TierCheck {
	var out []TierCheck
	for _, c := range e.Checks {
		if c.Status == TierStatusUnmet {
			out = append(out, c)
		}
	}
	return out
}

// UnmetSummary renders the failed checks as one line, each naming what was
// required and what was observed. Empty when the tier is met.
func (e TierEvidence) UnmetSummary() string {
	var parts []string
	for _, c := range e.Unmet() {
		parts = append(parts, fmt.Sprintf("%s: %s (required: %s)", c.Requirement, c.Observed, c.Required))
	}
	return strings.Join(parts, "; ")
}

// TierReader is the read surface tier verification needs, defined here at the
// point of use. GetResource and ListResources return the object, or the list,
// as the JSON kubectl prints. A missing object is an error or an empty
// string; either reads as "not found". PodLogs returns the last tailLines
// lines one container wrote, read through the API server.
type TierReader interface {
	GetResource(ctx context.Context, kind, namespace, name string) (string, error)
	ListResources(ctx context.Context, kind, namespace string) (string, error)
	PodLogs(ctx context.Context, namespace, pod, container string, tailLines int) (string, error)
}

// tierWorkload names a Deployment a tier component runs as.
type tierWorkload struct{ namespace, name string }

// tierComponent is one component Tier 1 requires, and how a petri lab runs
// it. The Deployments are the ones the platform and observability generators
// install (TestTierComponents_AreWhatTheGeneratorsInstall holds the two
// together); the CRDs are the component's API.
type tierComponent struct {
	requirement string
	required    string
	workloads   []tierWorkload
	crds        []string
}

// tier1Components are the components of the SI profile's Tier 1 environment
// a lab runs as Deployments, in the profile's order: basic observability,
// then basic platform. observability-logging is not among them: a lab's log
// interface is the cluster's own pod logs, and checkLogging reads them.
var tier1Components = []tierComponent{
	{
		requirement: "observability-metrics",
		required:    "a metrics backend running",
		workloads:   []tierWorkload{{"monitoring", "prometheus"}},
	},
	{
		requirement: "observability-dashboards",
		required:    "a dashboard service running",
		workloads:   []tierWorkload{{"monitoring", "grafana"}},
	},
	{
		requirement: "ingress",
		required:    "an ingress controller running",
		workloads:   []tierWorkload{{"ingress-nginx", "ingress-nginx-controller"}},
	},
	{
		requirement: "certificate-management",
		required:    "a certificate controller running, with its CRDs established",
		workloads: []tierWorkload{
			{"cert-manager", "cert-manager"},
			{"cert-manager", "cert-manager-cainjector"},
			{"cert-manager", "cert-manager-webhook"},
		},
		crds: []string{
			"certificates.cert-manager.io",
			"certificaterequests.cert-manager.io",
			"issuers.cert-manager.io",
			"clusterissuers.cert-manager.io",
		},
	},
}

// zoneLabel is the label a namespace carries when a scenario places it in a
// security zone (pkg/oasis). It is the only notion of a zone a lab has.
const zoneLabel = "petri.oasis/zone"

// VerifyTier reads the cluster behind r and reports whether it meets the SI
// profile's Tier 1 environment. It never returns an error: a read that fails
// is an unmet check saying so, because evidence that could not be collected
// is not evidence the tier is met.
func VerifyTier(ctx context.Context, r TierReader) TierEvidence {
	ev := TierEvidence{Tier: VerifiableTier, ObservedAt: time.Now().UTC()}
	if r == nil {
		ev.Checks = []TierCheck{{
			Requirement: "cluster",
			Required:    "a cluster to read",
			Observed:    "no cluster client is configured",
			Status:      TierStatusUnmet,
		}}
		return ev
	}

	nodes, readyNodes := checkNodes(ctx, r)
	ev.Checks = append(ev.Checks, nodes)
	for _, c := range tier1Components {
		ev.Checks = append(ev.Checks, checkComponent(ctx, r, c))
		// The profile lists logging between metrics and dashboards.
		if c.requirement == "observability-metrics" {
			ev.Checks = append(ev.Checks, checkLogging(ctx, r, readyNodes))
		}
	}
	namespaceObservations, namespaces := observeNamespaces(ctx, r)
	ev.Observations = append(namespaceObservations, observeWorkloads(ctx, r, namespaces))

	ev.Met = len(ev.Unmet()) == 0
	return ev
}

// checkNodes counts the cluster's Ready nodes against the Tier 1 floor, and
// returns their names.
func checkNodes(ctx context.Context, r TierReader) (TierCheck, []string) {
	c := TierCheck{
		Requirement: "nodes",
		Required:    fmt.Sprintf("a single cluster of at least %d Ready nodes", tier1MinReadyNodes),
		Status:      TierStatusUnmet,
	}
	raw, err := r.ListResources(ctx, "nodes", "")
	if err != nil {
		c.Observed = "nodes could not be listed: " + firstLine(err.Error())
		return c, nil
	}
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
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		c.Observed = "the node list could not be parsed: " + err.Error()
		return c, nil
	}
	var readyNames []string
	var controlPlanes int
	for _, n := range list.Items {
		if _, cp := n.Metadata.Labels["node-role.kubernetes.io/control-plane"]; cp {
			controlPlanes++
		}
		for _, cond := range n.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				readyNames = append(readyNames, n.Metadata.Name)
			}
		}
	}
	sort.Strings(readyNames)
	ready, total := len(readyNames), len(list.Items)
	c.Observed = fmt.Sprintf("%d Ready of %d (%d control-plane, %d worker)", ready, total, controlPlanes, total-controlPlanes)
	if ready >= tier1MinReadyNodes {
		c.Status = TierStatusMet
	}
	return c, readyNames
}

// logProbeNamespace holds the pods checkLogging reads. Every node runs at
// least one there (the network proxy, the CNI agent) from the moment it
// joins, so each node has a log to read before any scenario is provisioned.
const logProbeNamespace = "kube-system"

// logProbeTailLines is how much of a pod's log checkLogging asks for.
const logProbeTailLines = 5

// checkLogging verifies the SI profile's observability-logging interface the
// way a lab provides it: pod logs, read through the orchestration API
// (interface-types.md lists Kubernetes pod logs for the type). A log read is
// served by the node the pod runs on, so the check reads one on every Ready
// node: a node whose logs cannot be read is one a scenario's workload can
// land on. It is met when each Ready node returned at least one log line.
func checkLogging(ctx context.Context, r TierReader, readyNodes []string) TierCheck {
	c := TierCheck{
		Requirement: "observability-logging",
		Required:    "workload logs readable through the orchestration API on every Ready node",
		Status:      TierStatusUnmet,
	}
	if len(readyNodes) == 0 {
		c.Observed = "no Ready node to read a pod log on"
		return c
	}
	raw, err := r.ListResources(ctx, "pods", logProbeNamespace)
	if err != nil {
		c.Observed = fmt.Sprintf("pods in %s could not be listed: %s", logProbeNamespace, firstLine(err.Error()))
		return c
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				NodeName   string `json:"nodeName"`
				Containers []struct {
					Name string `json:"name"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		c.Observed = fmt.Sprintf("the pod list of %s could not be parsed: %v", logProbeNamespace, err)
		return c
	}
	type probe struct{ pod, container string }
	byNode := map[string][]probe{}
	for _, p := range list.Items {
		if p.Status.Phase != "Running" || len(p.Spec.Containers) == 0 {
			continue
		}
		byNode[p.Spec.NodeName] = append(byNode[p.Spec.NodeName], probe{p.Metadata.Name, p.Spec.Containers[0].Name})
	}

	var read, failed []string
	for _, node := range readyNodes {
		probes := byNode[node]
		sort.Slice(probes, func(i, j int) bool { return probes[i].pod < probes[j].pod })
		// Any pod on the node shows the node's logs are served; the first
		// that returns a line is enough.
		failure := fmt.Sprintf("%s: no running pod in %s", node, logProbeNamespace)
		ok := false
		for _, p := range probes {
			id := fmt.Sprintf("%s/%s", logProbeNamespace, p.pod)
			out, err := r.PodLogs(ctx, logProbeNamespace, p.pod, p.container, logProbeTailLines)
			if err != nil {
				failure = fmt.Sprintf("%s: the log of %s could not be read: %s", node, id, firstLine(err.Error()))
				continue
			}
			if countLines(out) > 0 {
				read = append(read, fmt.Sprintf("%s: %s", node, id))
				ok = true
				break
			}
			failure = fmt.Sprintf("%s: the log of %s is empty", node, id)
		}
		if !ok {
			failed = append(failed, failure)
		}
	}

	c.Observed = fmt.Sprintf("pod logs read through the API on %d of %d Ready nodes", len(read), len(readyNodes))
	if len(read) > 0 {
		c.Observed += " (" + strings.Join(read, ", ") + ")"
	}
	if len(failed) > 0 {
		c.Observed += "; not read: " + strings.Join(failed, "; ")
		return c
	}
	c.Status = TierStatusMet
	return c
}

// countLines counts the non-empty lines of a log read.
func countLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// checkComponent verifies one required component: every Deployment it runs
// as has a ready replica, and every CRD it serves is established.
func checkComponent(ctx context.Context, r TierReader, comp tierComponent) TierCheck {
	c := TierCheck{Requirement: comp.requirement, Required: comp.required, Status: TierStatusMet}
	var observed []string
	for _, w := range comp.workloads {
		text, ok := observeDeployment(ctx, r, w)
		observed = append(observed, text)
		if !ok {
			c.Status = TierStatusUnmet
		}
	}
	var established, missing []string
	for _, crd := range comp.crds {
		if crdEstablished(ctx, r, crd) {
			established = append(established, crd)
		} else {
			missing = append(missing, crd)
		}
	}
	if len(established) > 0 {
		observed = append(observed, "CRDs established: "+strings.Join(established, ", "))
	}
	if len(missing) > 0 {
		observed = append(observed, "CRDs not established: "+strings.Join(missing, ", "))
		c.Status = TierStatusUnmet
	}
	c.Observed = strings.Join(observed, "; ")
	return c
}

// observeDeployment describes one Deployment as the cluster shows it and
// reports whether it has a ready replica.
func observeDeployment(ctx context.Context, r TierReader, w tierWorkload) (string, bool) {
	id := fmt.Sprintf("deployment %s/%s", w.namespace, w.name)
	raw, err := r.GetResource(ctx, "deployments", w.namespace, w.name)
	if err != nil {
		return id + " could not be read: " + firstLine(err.Error()), false
	}
	if strings.TrimSpace(raw) == "" {
		return id + " not found", false
	}
	var d struct {
		Spec struct {
			Replicas *int `json:"replicas"`
			Template struct {
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return id + " could not be parsed: " + err.Error(), false
	}
	desired := 1
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	var images []string
	for _, ctr := range d.Spec.Template.Spec.Containers {
		images = append(images, ctr.Image)
	}
	text := fmt.Sprintf("%s %d/%d ready", id, d.Status.ReadyReplicas, desired)
	if len(images) > 0 {
		text += " (" + strings.Join(images, ", ") + ")"
	}
	return text, d.Status.ReadyReplicas >= 1
}

// crdEstablished reports whether the named CRD exists and the API server
// serves it.
func crdEstablished(ctx context.Context, r TierReader, name string) bool {
	raw, err := r.GetResource(ctx, "customresourcedefinitions", "", name)
	if err != nil || strings.TrimSpace(raw) == "" {
		return false
	}
	var crd struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &crd); err != nil {
		return false
	}
	for _, cond := range crd.Status.Conditions {
		if cond.Type == "Established" && cond.Status == "True" {
			return true
		}
	}
	return false
}

// observeNamespaces reports the cluster's namespaces and the security zones
// labelled on them, and returns the namespace names. Neither is judged; see
// the file comment.
func observeNamespaces(ctx context.Context, r TierReader) ([]TierObservation, []string) {
	namespaces := TierObservation{Subject: "namespaces"}
	zones := TierObservation{Subject: "security-zones"}

	raw, err := r.ListResources(ctx, "namespaces", "")
	if err != nil {
		msg := "namespaces could not be listed: " + firstLine(err.Error())
		namespaces.Observed, zones.Observed = msg, msg
		return []TierObservation{namespaces, zones}, nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		msg := "the namespace list could not be parsed: " + err.Error()
		namespaces.Observed, zones.Observed = msg, msg
		return []TierObservation{namespaces, zones}, nil
	}
	var names []string
	zoned := map[string]bool{}
	for _, ns := range list.Items {
		names = append(names, ns.Metadata.Name)
		if z := ns.Metadata.Labels[zoneLabel]; z != "" {
			zoned[z] = true
		}
	}
	sort.Strings(names)
	namespaces.Observed = fmt.Sprintf("%d: %s", len(names), strings.Join(names, ", "))

	zoneNames := make([]string, 0, len(zoned))
	for z := range zoned {
		zoneNames = append(zoneNames, z)
	}
	sort.Strings(zoneNames)
	zones.Observed = fmt.Sprintf("%d zones labelled on namespaces (%s)", len(zoneNames), zoneLabel)
	if len(zoneNames) > 0 {
		zones.Observed += ": " + strings.Join(zoneNames, ", ")
	}
	return []TierObservation{namespaces, zones}, names
}

// observeWorkloads reports the Deployments in each of the given namespaces.
// It is not judged: the profile's microservice figure counts the services
// scenarios declare, and these are whatever the lab is running.
func observeWorkloads(ctx context.Context, r TierReader, namespaces []string) TierObservation {
	o := TierObservation{Subject: "deployments"}
	total := 0
	var parts []string
	for _, ns := range namespaces {
		raw, err := r.ListResources(ctx, "deployments", ns)
		if err != nil {
			o.Observed = fmt.Sprintf("deployments in %s could not be listed: %s", ns, firstLine(err.Error()))
			return o
		}
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			o.Observed = fmt.Sprintf("the deployment list of %s could not be parsed: %v", ns, err)
			return o
		}
		if n := len(list.Items); n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%s %d", ns, n))
		}
	}
	o.Observed = fmt.Sprintf("%d Deployments", total)
	if len(parts) > 0 {
		o.Observed += " by namespace: " + strings.Join(parts, ", ")
	}
	return o
}

// firstLine trims an error to its first line, so a check's observation stays
// one line however much a client printed.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
