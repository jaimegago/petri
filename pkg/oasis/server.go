package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Server is the OASIS HTTP environment provider server.
// It exposes the five OASIS API endpoints plus a health check.
type Server struct {
	provider OASISProvider
	log      *slog.Logger
	mux      *http.ServeMux
}

// NewServer creates a new OASIS HTTP provider server.
func NewServer(provider OASISProvider, log *slog.Logger) *Server {
	s := &Server{
		provider: provider,
		log:      log,
		mux:      http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("POST /v1/provision", s.handleProvision)
	s.mux.HandleFunc("POST /v1/state-snapshot", s.handleStateSnapshot)
	s.mux.HandleFunc("POST /v1/teardown", s.handleTeardown)
	s.mux.HandleFunc("POST /v1/inject-state", s.handleInjectState)
	s.mux.HandleFunc("POST /v1/observe", s.handleObserve)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
}

// Handler returns the http.Handler for the server, with request logging middleware.
func (s *Server) Handler() http.Handler {
	return loggingMiddleware(s.log, s.mux)
}

// ListenAndServe starts the HTTP server and blocks until ctx is cancelled or an error occurs.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	s.log.Info("OASIS provider server listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (s *Server) handleProvision(w http.ResponseWriter, r *http.Request) {
	var req ProvisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.provider.Provision(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStateSnapshot(w http.ResponseWriter, r *http.Request) {
	var req StateSnapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.provider.StateSnapshot(r.Context(), req)
	if err != nil {
		writeError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleTeardown(w http.ResponseWriter, r *http.Request) {
	var req TeardownRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.provider.Teardown(r.Context(), req)
	if err != nil {
		writeError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleInjectState(w http.ResponseWriter, r *http.Request) {
	var req InjectStateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.provider.InjectState(r.Context(), req)
	if err != nil {
		writeError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleObserve(w http.ResponseWriter, r *http.Request) {
	var req ObserveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.provider.Observe(r.Context(), req)
	if err != nil {
		writeError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Middleware ────────────────────────────────────────────────────────────────

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func loggingMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("decoding request body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Status: "error", Message: msg})
}

// httpStatusForErr maps common error patterns to HTTP status codes.
func httpStatusForErr(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
