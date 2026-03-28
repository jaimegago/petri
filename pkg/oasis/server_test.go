package oasis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockOASISProvider is a test double for OASISProvider.
type mockOASISProvider struct {
	provisionResp     ProvisionResponse
	snapshotResp      StateSnapshotResponse
	teardownResp      TeardownResponse
	injectResp        InjectStateResponse
	observeResp       ObserveResponse
	err               error
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

func TestServer_ContentType(t *testing.T) {
	t.Parallel()

	srv := NewServer(&mockOASISProvider{}, noopLogger())
	w := getRequest(t, srv.Handler(), "/healthz")

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}
