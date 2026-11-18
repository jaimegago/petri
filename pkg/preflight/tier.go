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
// environment (profile.md §8): one cluster of at least three nodes, metrics
// and dashboards, ingress, certificate management. A gitops controller is not
// among them; the maintainer moved it to Tier 2 on 2026-11-18.
//
// Three of §8's Tier 1 bullets are reported and not judged — the namespace
// ceiling, the microservice count and the security zones — because the
// profile does not yet say what counts toward them: whether platform and
// per-scenario namespaces are namespaces in its sense, and whether a zone is
// a property of the lab or of a scenario. VerifyTier states what the cluster
// has and marks the check not_assessed. It does not decide.
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
	// TierStatusNotAssessed means the observation is reported and no
	// verdict is drawn from it. It never makes the evidence unmet.
	TierStatusNotAssessed TierStatus = "not_assessed"
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
	// Status is met, unmet or not_assessed.
	Status TierStatus `json:"status"`
}

// TierEvidence is the result of verifying a cluster against a tier.
type TierEvidence struct {
	// Tier is the complexity tier the checks belong to.
	Tier int `json:"tier"`
	// Met is true when no check is unmet.
	Met bool `json:"met"`
	// ObservedAt is when the cluster was read.
	ObservedAt time.Time `json:"observed_at"`
	// Checks is one entry per requirement, in the profile's order.
	Checks []TierCheck `json:"checks"`
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
// point of use. Both methods return the object, or the list, as the JSON
// kubectl prints. A missing object is an error or an empty string; either
// reads as "not found".
type TierReader interface {
	GetResource(ctx context.Context, kind, namespace, name string) (string, error)
	ListResources(ctx context.Context, kind, namespace string) (string, error)
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

// tier1Components are the components of the SI profile's Tier 1 environment,
// in the profile's order: basic observability, then basic platform.
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

	ev.Checks = append(ev.Checks, checkNodes(ctx, r))
	for _, c := range tier1Components {
		ev.Checks = append(ev.Checks, checkComponent(ctx, r, c))
	}
	namespaceChecks, namespaces := observeNamespaces(ctx, r)
	ev.Checks = append(ev.Checks, namespaceChecks...)
	ev.Checks = append(ev.Checks, observeWorkloads(ctx, r, namespaces))

	ev.Met = len(ev.Unmet()) == 0
	return ev
}

// checkNodes counts the cluster's Ready nodes against the Tier 1 floor.
func checkNodes(ctx context.Context, r TierReader) TierCheck {
	c := TierCheck{
		Requirement: "nodes",
		Required:    fmt.Sprintf("a single cluster of at least %d Ready nodes", tier1MinReadyNodes),
		Status:      TierStatusUnmet,
	}
	raw, err := r.ListResources(ctx, "nodes", "")
	if err != nil {
		c.Observed = "nodes could not be listed: " + firstLine(err.Error())
		return c
	}
	var list struct {
		Items []struct {
			Metadata struct {
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
		return c
	}
	var ready, controlPlanes int
	for _, n := range list.Items {
		if _, cp := n.Metadata.Labels["node-role.kubernetes.io/control-plane"]; cp {
			controlPlanes++
		}
		for _, cond := range n.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				ready++
			}
		}
	}
	total := len(list.Items)
	c.Observed = fmt.Sprintf("%d Ready of %d (%d control-plane, %d worker)", ready, total, controlPlanes, total-controlPlanes)
	if ready >= tier1MinReadyNodes {
		c.Status = TierStatusMet
	}
	return c
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

// observeNamespaces reports the cluster's namespaces and how many security
// zones they carry, and returns the namespace names. Neither check is
// judged; see the file comment.
func observeNamespaces(ctx context.Context, r TierReader) ([]TierCheck, []string) {
	namespaces := TierCheck{Requirement: "namespaces", Required: "at most 3 namespaces", Status: TierStatusNotAssessed}
	zones := TierCheck{Requirement: "security-zones", Required: "at least 2 security zones", Status: TierStatusNotAssessed}

	raw, err := r.ListResources(ctx, "namespaces", "")
	if err != nil {
		msg := "namespaces could not be listed: " + firstLine(err.Error())
		namespaces.Observed, zones.Observed = msg, msg
		return []TierCheck{namespaces, zones}, nil
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
		return []TierCheck{namespaces, zones}, nil
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
	return []TierCheck{namespaces, zones}, names
}

// observeWorkloads reports the Deployments in each of the given namespaces.
// It is not judged: which of them are the profile's microservices depends on
// which namespaces count.
func observeWorkloads(ctx context.Context, r TierReader, namespaces []string) TierCheck {
	c := TierCheck{
		Requirement: "microservices",
		Required:    "1 to 5 microservices, or a single monolithic application",
		Status:      TierStatusNotAssessed,
	}
	total := 0
	var parts []string
	for _, ns := range namespaces {
		raw, err := r.ListResources(ctx, "deployments", ns)
		if err != nil {
			c.Observed = fmt.Sprintf("deployments in %s could not be listed: %s", ns, firstLine(err.Error()))
			return c
		}
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			c.Observed = fmt.Sprintf("the deployment list of %s could not be parsed: %v", ns, err)
			return c
		}
		if n := len(list.Items); n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%s %d", ns, n))
		}
	}
	c.Observed = fmt.Sprintf("%d Deployments", total)
	if len(parts) > 0 {
		c.Observed += " by namespace: " + strings.Join(parts, ", ")
	}
	return c
}

// firstLine trims an error to its first line, so a check's observation stays
// one line however much a client printed.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
