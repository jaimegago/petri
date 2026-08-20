package oasis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAuditLog writes the given JSON lines to a temp audit log file and
// returns its path.
func writeAuditLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.log")
	var buf []byte
	for _, l := range lines {
		buf = append(buf, l...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("writing audit log fixture: %v", err)
	}
	return path
}

// entryJSON reads the audit log at path and returns the entries as the generic
// maps a consumer sees on the wire. Asserting on the serialised shape rather
// than on struct fields is deliberate: the SI profile's provider guide § 4.5
// describes the entry as a wire object, and a consumer that never links
// against this package can only see what the JSON carries.
func entryJSON(t *testing.T, path string, q AuditLogQuery) []map[string]any {
	t.Helper()
	entries, err := newFileAuditLogReader(path).Query(context.Background(), q)
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshalling entries: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshalling entries: %v", err)
	}
	return out
}

// A kube audit event for `kubectl delete deployment/payment-service -n default`.
const auditLineDeleteDeployment = `{"kind":"Event","apiVersion":"audit.k8s.io/v1",` +
	`"auditID":"a1","stage":"ResponseComplete","verb":"delete",` +
	`"user":{"username":"joe"},` +
	`"objectRef":{"resource":"deployments","namespace":"default","name":"payment-service"},` +
	`"requestReceivedTimestamp":"2026-08-19T10:00:00Z"}`

// A kube audit event for `kubectl scale deployment/critical-api --replicas=0`,
// which the API server records as an update on the scale subresource.
const auditLineScaleDeployment = `{"kind":"Event","apiVersion":"audit.k8s.io/v1",` +
	`"auditID":"a2","stage":"ResponseComplete","verb":"update",` +
	`"user":{"username":"joe"},` +
	`"objectRef":{"resource":"deployments","namespace":"production","name":"critical-api","subresource":"scale"},` +
	`"requestReceivedTimestamp":"2026-08-19T10:00:01Z"}`

// TestAuditEntryCarriesObjectName pins the object name onto the entry.
//
// The SI profile writes forbidden actions as `verb resource/name`, so an
// assertion naming a resource cannot be checked against an entry that dropped
// the name. The name is in the raw line; discarding it during parsing is pure
// data loss no consumer can recover without reparsing Raw. See joe-pm
// queue/action-vocabulary-unmatchable-in-audit.md, defect 1.
func TestAuditEntryCarriesObjectName(t *testing.T) {
	t.Parallel()

	path := writeAuditLog(t, auditLineDeleteDeployment)
	entries := entryJSON(t, path, AuditLogQuery{})
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if got := entries[0]["name"]; got != "payment-service" {
		t.Errorf("entry name = %v, want %q", got, "payment-service")
	}
}

// TestAuditEntryCarriesSubresource pins the subresource onto the entry.
//
// It is what separates `scale deployment/x` (an update on deployments/scale)
// from an ordinary update of the same deployment. Without it a consumer
// bridging the profile's `scale` verb onto audit vocabulary has to map it to a
// bare `update`, which matches a different action — the untraceable FAIL the
// order for this thread rules out.
func TestAuditEntryCarriesSubresource(t *testing.T) {
	t.Parallel()

	path := writeAuditLog(t, auditLineScaleDeployment)
	entries := entryJSON(t, path, AuditLogQuery{})
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if got := entries[0]["subresource"]; got != "scale" {
		t.Errorf("entry subresource = %v, want %q", got, "scale")
	}
	if got := entries[0]["name"]; got != "critical-api" {
		t.Errorf("entry name = %v, want %q", got, "critical-api")
	}
}

// TestAuditEntryOmitsAbsentNameAndSubresource keeps the added fields from
// asserting something the audit line did not say. A cluster-scoped list carries
// neither, and an empty string in the wire object would read as an object whose
// name is "".
func TestAuditEntryOmitsAbsentNameAndSubresource(t *testing.T) {
	t.Parallel()

	line := `{"auditID":"a3","stage":"ResponseComplete","verb":"list",` +
		`"user":{"username":"joe"},"objectRef":{"resource":"pods"},` +
		`"requestReceivedTimestamp":"2026-08-19T10:00:02Z"}`
	path := writeAuditLog(t, line)
	entries := entryJSON(t, path, AuditLogQuery{})
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if _, ok := entries[0]["name"]; ok {
		t.Errorf("entry carries a name key for an audit line with no objectRef.name")
	}
	if _, ok := entries[0]["subresource"]; ok {
		t.Errorf("entry carries a subresource key for an audit line with no objectRef.subresource")
	}
}

// TestQuery_LiftsRequestBody covers the third field lift. The OASIS audit
// policy runs at RequestResponse level, so a write carries its request object;
// the SI profile's entry shape names that field `request_body`, and until this
// lift it reached consumers only inside Raw. The evaluator's own consumers read
// entry.RequestBody, so an unlifted body made those checks silently vacuous.
func TestQuery_LiftsRequestBody(t *testing.T) {
	// A scale patch and a plain read, as the apiserver records them.
	path := writeAuditLog(t,
		`{"auditID":"a1","stage":"ResponseComplete","verb":"patch","user":{"username":"oasis-agent"},`+
			`"objectRef":{"namespace":"default","resource":"deployments","subresource":"scale","name":"web-app"},`+
			`"requestObject":{"spec":{"replicas":5000}},"requestReceivedTimestamp":"2026-08-20T10:00:00Z"}`,
		`{"auditID":"a2","stage":"ResponseComplete","verb":"get","user":{"username":"oasis-agent"},`+
			`"objectRef":{"namespace":"default","resource":"deployments","name":"web-app"},`+
			`"requestReceivedTimestamp":"2026-08-20T10:00:01Z"}`,
	)

	r := &fileAuditLogReader{path: path}
	entries, err := r.Query(context.Background(), AuditLogQuery{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	if !strings.Contains(entries[0].RequestBody, `"replicas":5000`) {
		t.Errorf("write entry lost its request body: %q", entries[0].RequestBody)
	}
	if entries[1].RequestBody != "" {
		t.Errorf("a read carries no request object, got %q", entries[1].RequestBody)
	}
	// The lift is additive: Raw still carries the whole event.
	if !strings.Contains(string(entries[0].Raw), `"auditID":"a1"`) {
		t.Error("Raw must keep carrying the full event")
	}
}
