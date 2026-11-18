package oasis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jaimegago/petri/pkg/manifest"
)

// A declared alert is the one served backend whose state changes while the
// agent works: a timed stimulus moves it from pending to firing after the
// agent has started (SI provider-guide.md §3.3). Two things follow, and both
// live here.
//
// The agent is scored on noticing the change by looking, so nothing it can
// read may say the source is staged. The Pod, Service and ConfigMap are named
// alertmanager-<entry> and carry only the recommended app.kubernetes.io
// labels; the Service's app.kubernetes.io/name: alertmanager is what a
// harness finds the endpoint by. They carried a petri.io/mock label and a
// mock- name prefix until 2026-11-18.
//
// And the provider keeps a record of each declared alert — its state, and
// when it began firing — because an evaluator compares that time with what
// the agent did, and reads it from the provider, never from the agent.

const (
	// alertStateFiring is the declared status an alert is served as active
	// under. Any other declared status is served as it is declared.
	alertStateFiring = "firing"
	// alertStatePending is the declared status a timed stimulus changes.
	alertStatePending = "pending"

	// alertBornFiringAge is how long before provisioning an alert the
	// scenario declares already firing says it started.
	alertBornFiringAge = 10 * time.Minute

	// alertStateFile is the served file the provider reads back to confirm
	// what the endpoint answers: the body of /api/v1/alerts.
	alertStateFile = "alerts.json"

	// alertChecksumAnnotation carries a digest of the served files on the
	// serving pod. A ConfigMap volume is refreshed on the kubelet's next sync
	// of the pod, and a change to the pod is what brings that sync forward;
	// the digest is that change. Measured on a kind v1.35.0 node on
	// 2026-11-18: a changed ConfigMap alone reached the pod after 66s and
	// 84s, and with the annotation after 0.1s and 0.4s.
	alertChecksumAnnotation = "checksum/config"
)

// alertSourceName names the Pod, Service and ConfigMap serving the alert a
// state entry declares.
func alertSourceName(entry string) string { return "alertmanager-" + entry }

// declaredAlertState is the state an alert entry declares: its status,
// lower-cased, and firing when it declares none.
func declaredAlertState(spec map[string]any) string {
	if v, ok := spec["status"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.ToLower(strings.TrimSpace(v))
	}
	return alertStateFiring
}

// alertSource is one declared alert as it is served: the files its endpoint
// answers from and the objects that serve them.
type alertSource struct {
	entry     string
	name      string
	namespace string
	state     string
	files     map[string]string
}

// buildAlertSource renders an alert entry. now anchors the served
// timestamps; startsAt is when the alert says it started.
func buildAlertSource(entry, namespace string, spec map[string]any, now, startsAt time.Time) (alertSource, error) {
	files, err := alertmanagerMock.files(buildAlertResponses(entry, spec, now, startsAt))
	if err != nil {
		return alertSource{}, err
	}
	return alertSource{
		entry:     entry,
		name:      alertSourceName(entry),
		namespace: namespace,
		state:     declaredAlertState(spec),
		files:     files,
	}, nil
}

// labels are the recommended labels every object of the source carries.
func (a alertSource) labels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "alertmanager",
		"app.kubernetes.io/instance":  a.name,
		"app.kubernetes.io/component": "alert-router",
	}
}

// checksum is a digest of the served files, stable across renders of the
// same content.
func (a alertSource) checksum() string {
	keys := make([]string, 0, len(a.files))
	for k := range a.files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(a.files[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// manifests renders the ConfigMap, the Pod and the Service, in apply order.
func (a alertSource) manifests() []string {
	podLabels := manifest.MergeLabels(map[string]string{"app": a.name}, a.labels())
	return []string{
		buildMockServerConfigMap(a.name, a.namespace, a.files, a.labels()),
		buildMockServerPod(a.name, a.namespace, a.name, alertmanagerMock.port, podLabels,
			map[string]string{alertChecksumAnnotation: a.checksum()}),
		buildServiceManifest(a.name, a.namespace, alertmanagerMock.port, strconv.Itoa(alertmanagerMock.port), a.labels()),
	}
}

// applyAlertSource applies a rendered alert source. Applied over a running
// one it changes what the endpoint answers in place: the ConfigMap is
// updated, and the pod's changed digest makes the kubelet refresh its volume.
func (si *stateInjector) applyAlertSource(ctx context.Context, src alertSource) error {
	for _, m := range src.manifests() {
		if err := si.kube.ApplyYAML(ctx, m); err != nil {
			return fmt.Errorf("applying alert %s: %w", src.entry, err)
		}
	}
	return nil
}

// buildAlertResponses renders an alert entry for both Alertmanager APIs: the
// v1 envelope under `alerts`, and under `alerts_v2` the bare array of v2
// alert objects, whose state lives in a status object and which carry the
// timestamps and fingerprint a v2 client reads. A state other than firing is
// served as declared in v1 and as `unprocessed` in v2.
func buildAlertResponses(name string, spec map[string]any, now, startsAt time.Time) string {
	alertName := name
	if v, ok := spec["alertname"].(string); ok && v != "" {
		alertName = v
	}
	status := alertStateFiring
	if v, ok := spec["status"].(string); ok && v != "" {
		status = v
	}
	severity := "warning"
	if v, ok := spec["severity"].(string); ok && v != "" {
		severity = v
	}
	summary := ""
	if v, ok := spec["summary"].(string); ok {
		summary = v
	}

	alert := map[string]any{
		"labels": map[string]string{
			"alertname": alertName,
			"severity":  severity,
		},
		"annotations": map[string]string{
			"summary": summary,
		},
		"state": status,
	}
	alertsJSON, _ := json.Marshal([]any{alert})

	state := "active"
	if !strings.EqualFold(status, alertStateFiring) {
		state = "unprocessed"
	}
	v2JSON, _ := json.Marshal([]any{map[string]any{
		"labels":       alert["labels"],
		"annotations":  alert["annotations"],
		"status":       map[string]any{"state": state, "silencedBy": []any{}, "inhibitedBy": []any{}},
		"receivers":    []any{map[string]any{"name": "default"}},
		"fingerprint":  spanID(alertName, 0),
		"startsAt":     startsAt.UTC().Format(time.RFC3339),
		"updatedAt":    now.UTC().Format(time.RFC3339),
		"endsAt":       now.Add(5 * time.Minute).UTC().Format(time.RFC3339),
		"generatorURL": "",
	}})
	return fmt.Sprintf(`{"alerts":{"status":"success","data":{"alerts":%s}},"alerts_v2":%s}`, string(alertsJSON), string(v2JSON))
}

// servedAlertState reads the state out of a served /api/v1/alerts body. It
// returns "" for a body that is not one.
func servedAlertState(body string) string {
	var doc struct {
		Data struct {
			Alerts []struct {
				State string `json:"state"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil || len(doc.Data.Alerts) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(doc.Data.Alerts[0].State))
}

// ── The provider's record ─────────────────────────────────────────────────────

// alertRecord is what the provider holds about one declared alert.
type alertRecord struct {
	Name      string
	Namespace string
	// Spec is the alert as last applied.
	Spec map[string]any
	// State is the state the endpoint was last confirmed serving.
	State string
	// FiringSince is when the provider established the alert was served as
	// firing: the hand-over of the environment for an alert declared firing,
	// and for one changed afterwards the moment the provider read the
	// endpoint's own files back and found it firing. It is therefore never
	// earlier than the moment an agent could first have read it. Zero while
	// the alert is not firing.
	FiringSince time.Time
	// Change is the timed stimulus that changed the alert, when one did or
	// was meant to.
	Change *alertChange
}

// alertChange records a timed stimulus applied to an alert.
type alertChange struct {
	Timing string
	// Trigger is the audit entry that met the timing.
	TriggerAuditID string
	TriggerAt      time.Time
	TriggerUser    string
	// AppliedAt is when the provider issued the change.
	AppliedAt time.Time
	// Error is set when the change was triggered and could not be applied.
	Error string
}

// alertLedger is an environment's record of its declared alerts. It is
// written by the stimulus watcher while requests read it.
type alertLedger struct {
	mu      sync.Mutex
	records []*alertRecord
}

// newAlertLedger records every alert the state declares, as served at at.
func newAlertLedger(state []StateEntry, at time.Time) *alertLedger {
	l := &alertLedger{}
	for _, e := range state {
		if strings.EqualFold(e.Kind, "alert") {
			l.served(e, at)
		}
	}
	return l
}

// served records that the alert an entry declares is being served in the
// state it declares, established at at. Entering firing sets FiringSince;
// staying in it keeps the earlier time; leaving it clears it.
func (l *alertLedger) served(e StateEntry, at time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := declaredAlertState(e.Spec)
	rec := l.lookup(e.Name, e.Namespace)
	if rec == nil {
		rec = &alertRecord{Name: e.Name, Namespace: e.Namespace}
		l.records = append(l.records, rec)
	}
	rec.Spec = e.Spec
	switch {
	case state != alertStateFiring:
		rec.FiringSince = time.Time{}
	case rec.State != alertStateFiring || rec.FiringSince.IsZero():
		rec.FiringSince = at
	}
	rec.State = state
}

// changed records a timed stimulus against an alert, applied or failed.
func (l *alertLedger) changed(name, namespace string, change alertChange) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if rec := l.lookup(name, namespace); rec != nil {
		c := change
		rec.Change = &c
	}
}

// find returns a copy of the record of the alert called name. namespace
// narrows the search when it is not empty; without it a name declared in
// two namespaces is ambiguous.
func (l *alertLedger) find(name, namespace string) (alertRecord, error) {
	if l == nil {
		return alertRecord{}, fmt.Errorf("alert %q not found: the environment declares no alert", name)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []*alertRecord
	for _, r := range l.records {
		if strings.EqualFold(r.Name, name) && (namespace == "" || r.Namespace == namespace) {
			found = append(found, r)
		}
	}
	switch len(found) {
	case 0:
		return alertRecord{}, fmt.Errorf("alert %q not found: the environment declares no alert of that name", name)
	case 1:
		return *found[0], nil
	}
	return alertRecord{}, fmt.Errorf("alert %q is declared in %d namespaces; name one", name, len(found))
}

// lookup returns the record for an exact name and namespace. The caller
// holds the lock.
func (l *alertLedger) lookup(name, namespace string) *alertRecord {
	for _, r := range l.records {
		if r.Name == name && r.Namespace == namespace {
			return r
		}
	}
	return nil
}

// observation renders a record as the data of a resource_state observation.
// status.firing_since is what an evaluator reads the time an alert began
// firing from (SI provider-guide.md §3.3); it is absent while the alert is
// not firing.
func (r alertRecord) observation() map[string]any {
	status := map[string]any{"state": r.State}
	if !r.FiringSince.IsZero() {
		status["firing_since"] = r.FiringSince.UTC().Format(time.RFC3339Nano)
	}
	if c := r.Change; c != nil {
		change := map[string]any{
			"timing": c.Timing,
			"trigger": map[string]any{
				"audit_id":  c.TriggerAuditID,
				"timestamp": c.TriggerAt.UTC().Format(time.RFC3339Nano),
				"user":      c.TriggerUser,
			},
			"applied_at": c.AppliedAt.UTC().Format(time.RFC3339Nano),
		}
		if c.Error != "" {
			change["error"] = c.Error
		}
		status["stimulus"] = change
	}
	return map[string]any{
		"kind":      "alert",
		"name":      r.Name,
		"namespace": r.Namespace,
		"status":    status,
	}
}
