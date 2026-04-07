package oasis

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// AuditLogQuery specifies the filter parameters for an audit log query.
type AuditLogQuery struct {
	Start     time.Time
	End       time.Time
	Namespace string
	Verb      string
	Resource  string
}

// AuditEntry represents a parsed Kubernetes API server audit event.
type AuditEntry struct {
	AuditID   string          `json:"auditID"`
	Stage     string          `json:"stage"`
	Verb      string          `json:"verb"`
	Namespace string          `json:"namespace,omitempty"`
	Resource  string          `json:"resource,omitempty"`
	User      string          `json:"user,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Raw       json.RawMessage `json:"raw"`
}

// AuditLogReader queries Kubernetes audit logs for an evaluation window.
type AuditLogReader interface {
	Query(ctx context.Context, q AuditLogQuery) ([]AuditEntry, error)
}

// stubAuditLogReader is used when no audit log path is configured.
type stubAuditLogReader struct{}

// Query returns an empty result set when audit logging is not configured.
// This allows the observe handler to return a valid 200 response with an
// empty entries array rather than a 500 error.
func (s *stubAuditLogReader) Query(_ context.Context, _ AuditLogQuery) ([]AuditEntry, error) {
	return []AuditEntry{}, nil
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
		Namespace string `json:"namespace"`
		Resource  string `json:"resource"`
	} `json:"objectRef"`
	RequestReceivedTimestamp time.Time `json:"requestReceivedTimestamp"`
}

// Query reads the audit log file and returns entries matching the query filters.
func (r *fileAuditLogReader) Query(_ context.Context, q AuditLogQuery) ([]AuditEntry, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("opening audit log %s: %w", r.path, err)
	}
	defer f.Close()

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
			AuditID:   ev.AuditID,
			Stage:     ev.Stage,
			Verb:      ev.Verb,
			Namespace: ev.ObjectRef.Namespace,
			Resource:  ev.ObjectRef.Resource,
			User:      ev.User.Username,
			Timestamp: ts,
			Raw:       rawCopy,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading audit log: %w", err)
	}
	return entries, nil
}
