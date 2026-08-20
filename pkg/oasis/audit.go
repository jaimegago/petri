package oasis

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrAuditNotConfigured is returned by the stub audit reader when no audit log
// path is configured. Callers should check for this sentinel to distinguish
// "audit logging is not set up" from transient read errors.
var ErrAuditNotConfigured = errors.New("audit logging is not configured: no audit log path provided")

// AuditLogQuery specifies the filter parameters for an audit log query.
type AuditLogQuery struct {
	Start     time.Time
	End       time.Time
	Namespace string
	Verb      string
	Resource  string
}

// AuditEntry represents a parsed Kubernetes API server audit event.
//
// Name and Subresource are additive against the SI profile's provider guide
// § 4.5 entry shape, which lists timestamp, verb, resource, namespace, user and
// request_body and names no object identity at all. They are lifted because
// both are already in the raw line and no consumer can recover them without
// reparsing Raw:
//
//   - Name is what an SI forbidden action of the form `verb resource/name`
//     names. An entry without it cannot answer whether the object the action
//     named is the object that was acted on.
//   - Subresource is what separates an update on `deployments/scale` from an
//     ordinary update of the same deployment — that is, the profile's `scale`
//     verb from its `update` verb.
//   - RequestBody is the third instance of the same lift, and the guide does
//     name it. Six SI safety action strings say what a write must not contain
//     rather than what it acts on — `spec.replicas`, `metadata.labels`,
//     `metadata.annotations`, `image`, and two `replicas=` qualifiers — and
//     none can be answered from object identity. The cluster already records
//     the body: the OASIS audit policy runs at `RequestResponse` level for
//     every group the profile touches (see provisioners/local, oasisAuditPolicy).
//     It was reaching consumers only inside Raw, where the SI profile's
//     `request_body` field name does not appear, so the consumer that reads
//     that field saw an empty string on every entry of every run.
type AuditEntry struct {
	AuditID     string          `json:"auditID"`
	Stage       string          `json:"stage"`
	Verb        string          `json:"verb"`
	Namespace   string          `json:"namespace,omitempty"`
	Resource    string          `json:"resource,omitempty"`
	Subresource string          `json:"subresource,omitempty"`
	Name        string          `json:"name,omitempty"`
	User        string          `json:"user,omitempty"`
	Timestamp   time.Time       `json:"timestamp"`
	RequestBody string          `json:"request_body,omitempty"`
	Raw         json.RawMessage `json:"raw"`
}

// AuditLogReader queries Kubernetes audit logs for an evaluation window.
type AuditLogReader interface {
	Query(ctx context.Context, q AuditLogQuery) ([]AuditEntry, error)
}

// stubAuditLogReader is used when no audit log path is configured.
type stubAuditLogReader struct{}

// Query returns ErrAuditNotConfigured when audit logging is not configured.
// The conformance endpoint and observeAuditLog check for this sentinel to
// honestly report that audit evidence is unavailable rather than silently
// returning empty entries (the Goodhart failure mode the v0.4 spec rejects).
func (s *stubAuditLogReader) Query(_ context.Context, _ AuditLogQuery) ([]AuditEntry, error) {
	return nil, ErrAuditNotConfigured
}

// fileAuditLogReader reads Kubernetes audit log JSON-lines from a file.
// Each line must be a JSON audit.k8s.io/v1 Event object.
type fileAuditLogReader struct {
	path string
}

// newFileAuditLogReader returns an AuditLogReader backed by the given log file path.
func newFileAuditLogReader(path string) AuditLogReader {
	return &fileAuditLogReader{path: path}
}

// kubeAuditEvent is the minimal Kubernetes audit event structure used for filtering.
type kubeAuditEvent struct {
	AuditID string `json:"auditID"`
	Stage   string `json:"stage"`
	Verb    string `json:"verb"`
	User    struct {
		Username string `json:"username"`
	} `json:"user"`
	ObjectRef struct {
		Namespace   string `json:"namespace"`
		Resource    string `json:"resource"`
		Subresource string `json:"subresource"`
		Name        string `json:"name"`
	} `json:"objectRef"`
	// RequestObject is present only at RequestResponse level and only on
	// writes; a read, or any event the policy logs at Metadata level, carries
	// none. It is left as raw JSON and re-encoded rather than parsed, because
	// the consumer is a substring and field-path check over whatever the agent
	// actually sent, and parsing here would impose a schema on a body that may
	// be a strategic-merge patch, a JSON patch, or a whole object.
	RequestObject            json.RawMessage `json:"requestObject,omitempty"`
	RequestReceivedTimestamp time.Time       `json:"requestReceivedTimestamp"`
}

// Query reads the audit log file and returns entries matching the query filters.
func (r *fileAuditLogReader) Query(_ context.Context, q AuditLogQuery) ([]AuditEntry, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("opening audit log %s: %w", r.path, err)
	}
	defer func() { _ = f.Close() }()

	var entries []AuditEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MiB per line
	for scanner.Scan() {
		line := scanner.Bytes()
		var ev kubeAuditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}
		if ev.Stage != "ResponseComplete" {
			continue
		}
		ts := ev.RequestReceivedTimestamp
		if !q.Start.IsZero() && ts.Before(q.Start) {
			continue
		}
		if !q.End.IsZero() && ts.After(q.End) {
			continue
		}
		if q.Namespace != "" && ev.ObjectRef.Namespace != q.Namespace {
			continue
		}
		if q.Verb != "" && ev.Verb != q.Verb {
			continue
		}
		if q.Resource != "" && ev.ObjectRef.Resource != q.Resource {
			continue
		}
		// Copy line to avoid scanner buffer reuse.
		rawCopy := make([]byte, len(line))
		copy(rawCopy, line)
		entries = append(entries, AuditEntry{
			AuditID:     ev.AuditID,
			Stage:       ev.Stage,
			Verb:        ev.Verb,
			Namespace:   ev.ObjectRef.Namespace,
			Resource:    ev.ObjectRef.Resource,
			Subresource: ev.ObjectRef.Subresource,
			Name:        ev.ObjectRef.Name,
			User:        ev.User.Username,
			Timestamp:   ts,
			RequestBody: string(ev.RequestObject),
			Raw:         rawCopy,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading audit log: %w", err)
	}
	return entries, nil
}
