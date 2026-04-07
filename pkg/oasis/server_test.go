package oasis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockOASISProvider is a test double for OASISProvider.
type mockOASISProvider struct {
	provisionResp ProvisionResponse
	snapshotResp  StateSnapshotResponse
	teardownResp  TeardownResponse
	injectResp    InjectStateResponse
	observeResp   ObserveResponse
	err           error
}

func (m *mockOASISProvider) Provision(_ context.Context, _ ProvisionRequest) (ProvisionResponse, error) {
	return m.provisionResp, m.err
}

func (m *mockOASISProvider) StateSnapshot(_ context.Context, _ StateSnapshotRequest) (StateSnapshotResponse, error) {
	return m.snapshotResp, m.err
}

func (m *mockOASISProvider) Teardown(_ context.Context, _ TeardownRequest) (TeardownResponse, error) {
	return m.teardownResp, m.err
}

func (m *mockOASISProvider) InjectState(_ context.Context, _ InjectStateRequest) (InjectStateResponse, error) {
	return m.injectResp, m.err
}

func (m *mockOASISProvider) Observe(_ context.Context, _ ObserveRequest) (ObserveResponse, error) {
	return m.observeResp, m.err
}

// ── Test helpers ──────────────────────────────────────────────────────────────

func postJSON(t *testing.T, handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshalling request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func getRequest(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// ── Handler tests ─────────────────────────────────────────────────────────────

func TestServer_Healthz(t *testing.T) {
	t.Parallel()

	srv := NewServer(&mockOASISProvider{}, noopLogger())
	w := getRequest(t, srv.Handler(), "/healthz")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
}

func TestServer_Provision(t *testing.T) {
	t.Parallel()

	t.Run("success returns 200 with environment_id", func(t *testing.T) {
		t.Parallel()
		mock := &mockOASISProvider{
			provisionResp: ProvisionResponse{
				EnvironmentID: "env-123",
				Status:        "ready",
			},
		}
		srv := NewServer(mock, noopLogger())
		w := postJSON(t, srv.Handler(), "/v1/provision", ProvisionRequest{
			ScenarioID: "sc1",
		})

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
		var resp ProvisionResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if resp.EnvironmentID != "env-123" {
			t.Errorf("EnvironmentID = %q, want %q", resp.EnvironmentID, "env-123")
		}
	})

	t.Run("provider error returns 500", func(t *testing.T) {
		t.Parallel()
		mock := &mockOASISProvider{err: errors.New("cluster unavailable")}
		srv := NewServer(mock, noopLogger())
		w := postJSON(t, srv.Handler(), "/v1/provision", ProvisionRequest{})

		if w.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
		var errResp errorResponse
		if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
			t.Fatalf("decoding error response: %v", err)
		}
		if errResp.Status != "error" {
			t.Errorf("error status = %q, want %q", errResp.Status, "error")
		}
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		t.Parallel()
		srv := NewServer(&mockOASISProvider{}, noopLogger())
		req := httptest.NewRequest(http.MethodPost, "/v1/provision", bytes.NewBufferString("not-json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})
}

func TestServer_StateSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("success returns snapshot", func(t *testing.T) {
		t.Parallel()
		mock := &mockOASISProvider{
			snapshotResp: StateSnapshotResponse{
				EnvironmentID: "env-456",
				Timestamp:     time.Now(),
			},
		}
		srv := NewServer(mock, noopLogger())
		w := postJSON(t, srv.Handler(), "/v1/state-snapshot", StateSnapshotRequest{
			EnvironmentID: "env-456",
		})

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("not found returns 404", func(t *testing.T) {
		t.Parallel()
		mock := &mockOASISProvider{err: errors.New("environment not found")}
		srv := NewServer(mock, noopLogger())
		w := postJSON(t, srv.Handler(), "/v1/state-snapshot", StateSnapshotRequest{EnvironmentID: "missing"})

		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
		}
	})
}

func TestServer_Teardown(t *testing.T) {
	t.Parallel()

	t.Run("success returns 200", func(t *testing.T) {
		t.Parallel()
		mock := &mockOASISProvider{teardownResp: TeardownResponse{Status: "destroyed"}}
		srv := NewServer(mock, noopLogger())
		w := postJSON(t, srv.Handler(), "/v1/teardown", TeardownRequest{EnvironmentID: "env-789"})

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
		var resp TeardownResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if resp.Status != "destroyed" {
			t.Errorf("status = %q, want %q", resp.Status, "destroyed")
		}
	})
}

func TestServer_InjectState(t *testing.T) {
	t.Parallel()

	t.Run("success returns applied status", func(t *testing.T) {
		t.Parallel()
		mock := &mockOASISProvider{injectResp: InjectStateResponse{Status: "applied"}}
		srv := NewServer(mock, noopLogger())
		w := postJSON(t, srv.Handler(), "/v1/inject-state", InjectStateRequest{
			EnvironmentID: "env-101",
			State:         []StateEntry{{Kind: "ConfigMap", Name: "cm"}},
		})

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
	})
}

func TestServer_Observe(t *testing.T) {
	t.Parallel()

	t.Run("success returns observation data", func(t *testing.T) {
		t.Parallel()
		mock := &mockOASISProvider{
			observeResp: ObserveResponse{
				EnvironmentID:   "env-202",
				Timestamp:       time.Now(),
				ObservationType: "resource_state",
				Data:            json.RawMessage(`{"kind":"Deployment"}`),
			},
		}
		srv := NewServer(mock, noopLogger())
		w := postJSON(t, srv.Handler(), "/v1/observe", ObserveRequest{
			EnvironmentID:   "env-202",
			ObservationType: "resource_state",
		})

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
		var resp ObserveResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if resp.ObservationType != "resource_state" {
			t.Errorf("ObservationType = %q, want %q", resp.ObservationType, "resource_state")
		}
	})
}

// TestServer_ObserveRegression exercises /v1/observe through the full HTTP
// handler stack with a real provider and a freshly provisioned environment.
// Both resource_state and audit_log must return 200 with non-empty body.
// This catches regressions where observe returns 500 for valid env_ids.
func TestServer_ObserveRegression(t *testing.T) {
	t.Parallel()

	// Wire up a real provider with a mock kube client.
	mock := newMockKubeForServer()
	provider := New(ProviderConfig{}, mock, noopLogger())
	srv := NewServer(provider, noopLogger())
	handler := srv.Handler()

	// Provision an environment through the HTTP layer.
	provReq := map[string]any{
		"scenario_id": "regression-observe",
		"environment": map[string]any{
			"type": "kubernetes-cluster",
			"state": []map[string]any{
				{
					"kind": "configmap",
					"name": "test-config",
					"data": map[string]string{"KEY": "value"},
				},
			},
		},
	}
	w := postJSON(t, handler, "/v1/provision", provReq)
	if w.Code != http.StatusOK {
		t.Fatalf("provision status = %d, body = %s", w.Code, w.Body.String())
	}
	var provResp ProvisionResponse
	if err := json.NewDecoder(w.Body).Decode(&provResp); err != nil {
		t.Fatalf("decoding provision response: %v", err)
	}
	envID := provResp.EnvironmentID
	if envID == "" {
		t.Fatal("provision returned empty environment_id")
	}

	t.Run("resource_state with params returns 200", func(t *testing.T) {
		t.Parallel()
		obsReq := map[string]any{
			"environment_id":   envID,
			"observation_type": "resource_state",
			"parameters": map[string]any{
				"kind": "configmaps",
				"name": "test-config",
			},
		}
		w := postJSON(t, handler, "/v1/observe", obsReq)
		// The mock returns empty string for unknown resources (not an error),
		// so the handler should still return 200.
		if w.Code != http.StatusOK {
			t.Fatalf("observe resource_state status = %d, body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("resource_state without params returns 200", func(t *testing.T) {
		t.Parallel()
		obsReq := map[string]any{
			"environment_id":   envID,
			"observation_type": "resource_state",
		}
		w := postJSON(t, handler, "/v1/observe", obsReq)
		if w.Code != http.StatusOK {
			t.Fatalf("observe resource_state (no params) status = %d, body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("audit_log returns 200", func(t *testing.T) {
		t.Parallel()
		obsReq := map[string]any{
			"environment_id":   envID,
			"observation_type": "audit_log",
		}
		w := postJSON(t, handler, "/v1/observe", obsReq)
		if w.Code != http.StatusOK {
			t.Fatalf("observe audit_log status = %d, body = %s", w.Code, w.Body.String())
		}
		var resp ObserveResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decoding observe response: %v", err)
		}
		if resp.ObservationType != "audit_log" {
			t.Errorf("ObservationType = %q, want %q", resp.ObservationType, "audit_log")
		}
		if len(resp.Data) == 0 {
			t.Error("observe audit_log returned empty data")
		}
	})
}

// newMockKubeForServer returns a mockKubeClient usable by server-level tests.
// It is defined here (in the server_test.go file) because server_test.go is in
// the same package and can access unexported types from mock_test.go.
func newMockKubeForServer() *mockKubeClient {
	return newMockKube()
}

func TestLoggingMiddleware_ErrorResponse(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	handler := loggingMiddleware(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusBadRequest, "missing required field: scenario_id")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/provision", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Verify the response was still written correctly to the client.
	if w.Code != http.StatusBadRequest {
		t.Errorf("response status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	logged := logBuf.String()
	for _, want := range []string{
		`method=POST`,
		`path=/v1/provision`,
		`status=400`,
		`error="missing required field: scenario_id"`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log output missing %q\ngot: %s", want, logged)
		}
	}
}

func TestLoggingMiddleware_SuccessNoBody(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	handler := loggingMiddleware(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("response status = %d, want %d", w.Code, http.StatusOK)
	}

	logged := logBuf.String()
	if strings.Contains(logged, "error=") {
		t.Errorf("success response should not contain error field\ngot: %s", logged)
	}
	for _, want := range []string{`method=GET`, `path=/healthz`, `status=200`} {
		if !strings.Contains(logged, want) {
			t.Errorf("log output missing %q\ngot: %s", want, logged)
		}
	}
}

func TestServer_ContentType(t *testing.T) {
	t.Parallel()

	srv := NewServer(&mockOASISProvider{}, noopLogger())
	w := getRequest(t, srv.Handler(), "/healthz")

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}
