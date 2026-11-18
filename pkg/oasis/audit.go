package oasis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		entry, ok := parseAuditLine(scanner.Bytes())
		if !ok {
			continue
		}
		if !q.Start.IsZero() && entry.Timestamp.Before(q.Start) {
			continue
		}
		if !q.End.IsZero() && entry.Timestamp.After(q.End) {
			continue
		}
		if q.Namespace != "" && entry.Namespace != q.Namespace {
			continue
		}
		if q.Verb != "" && entry.Verb != q.Verb {
			continue
		}
		if q.Resource != "" && entry.Resource != q.Resource {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading audit log: %w", err)
	}
	return entries, nil
}

// parseAuditLine parses one line of the audit log. It reports false for a
// malformed line and for any stage but ResponseComplete, the one stage every
// request reaches exactly once.
func parseAuditLine(line []byte) (AuditEntry, bool) {
	var ev kubeAuditEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return AuditEntry{}, false
	}
	if ev.Stage != "ResponseComplete" {
		return AuditEntry{}, false
	}
	// Copy line: a caller's buffer is reused.
	rawCopy := make([]byte, len(line))
	copy(rawCopy, line)
	return AuditEntry{
		AuditID:     ev.AuditID,
		Stage:       ev.Stage,
		Verb:        ev.Verb,
		Namespace:   ev.ObjectRef.Namespace,
		Resource:    ev.ObjectRef.Resource,
		Subresource: ev.ObjectRef.Subresource,
		Name:        ev.ObjectRef.Name,
		User:        ev.User.Username,
		Timestamp:   ev.RequestReceivedTimestamp,
		RequestBody: string(ev.RequestObject),
		Raw:         rawCopy,
	}, true
}

// auditFollower is an AuditLogReader whose log can be followed as it grows.
// A timed stimulus is triggered by an entry appearing in the audit log (SI
// provider-guide.md §3.3), so the provider has to see entries as they are
// written, and Query scans the whole file on every call.
type auditFollower interface {
	// Follow returns a cursor positioned at the log's current end.
	Follow() (auditCursor, error)
}

// auditCursor reads the entries appended to an audit log since the previous
// call.
type auditCursor interface {
	Next() ([]AuditEntry, error)
}

// Follow returns a cursor at the current end of the audit log file. It fails
// when the file cannot be read, which is what a caller that depends on
// following it needs to know before it starts.
func (r *fileAuditLogReader) Follow() (auditCursor, error) {
	info, err := os.Stat(r.path)
	if err != nil {
		return nil, fmt.Errorf("following audit log %s: %w", r.path, err)
	}
	return &fileAuditCursor{path: r.path, offset: info.Size()}, nil
}

// fileAuditCursor follows an audit log file by byte offset.
type fileAuditCursor struct {
	path   string
	offset int64
}

// Next returns the complete lines appended since the last call. A line still
// being written is left for the next call.
//
// The API server rotates the log by renaming it and starting a new file. A
// file shorter than the offset is that new file, and it is read from its
// start; what the old file gained between the last call and the rotation is
// not read.
func (c *fileAuditCursor) Next() ([]AuditEntry, error) {
	f, err := os.Open(c.path)
	if err != nil {
		return nil, fmt.Errorf("opening audit log %s: %w", c.path, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading audit log %s: %w", c.path, err)
	}
	if info.Size() < c.offset {
		c.offset = 0
	}
	if info.Size() == c.offset {
		return nil, nil
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("reading audit log %s: %w", c.path, err)
	}
	buf, err := io.ReadAll(io.LimitReader(f, info.Size()-c.offset))
	if err != nil {
		return nil, fmt.Errorf("reading audit log %s: %w", c.path, err)
	}
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return nil, nil
	}
	c.offset += int64(end + 1)
	var entries []AuditEntry
	for _, line := range bytes.Split(buf[:end], []byte{'\n'}) {
		if entry, ok := parseAuditLine(line); ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
