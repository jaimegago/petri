package oasis

import (
	"bytes"
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
	s.mux.HandleFunc("GET /v1/conformance", s.handleConformance)
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

func (s *Server) handleConformance(w http.ResponseWriter, r *http.Request) {
	profile := r.URL.Query().Get("profile")
	if profile == "" {
		writeError(w, http.StatusBadRequest, "missing required query parameter: profile")
		return
	}
	resp, err := s.provider.Conformance(r.Context(), profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleProvision(w http.ResponseWriter, r *http.Request) {
	var req ProvisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.provider.Provision(r.Context(), req)
	if err != nil {
		writeProvisionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeProvisionError maps a Provision-side error to the right HTTP status
// and body shape. ErrImagePullFailure → 502 with structured fields,
// ErrRolloutTimeout → 500 with the historical phrasing, everything else →
// 500 with the generic envelope.
//
// Defined here (not in writeError) because the typed-error mapping is
// provision-specific: other endpoints don't have an "unreachable upstream
// registry" failure mode and shouldn't sprout one.
func writeProvisionError(w http.ResponseWriter, err error) {
	var pull *ErrImagePullFailure
	if errors.As(err, &pull) {
		writeJSON(w, http.StatusBadGateway, imagePullErrorResponse{
			Status:         "error",
			Message:        pull.Error(),
			Image:          pull.Image,
			Namespace:      pull.Namespace,
			Pod:            pull.Pod,
			Reason:         pull.Reason,
			KubeletMessage: pull.Message,
		})
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
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

// maxErrorBodyCapture is the maximum number of bytes captured from error
// response bodies for logging purposes.
const maxErrorBodyCapture = 4096

type statusRecorder struct {
	http.ResponseWriter
	status    int
	errBody   bytes.Buffer
	capturing bool
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.capturing = status >= 400
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.capturing && r.errBody.Len() < maxErrorBodyCapture {
		remaining := maxErrorBodyCapture - r.errBody.Len()
		if len(b) > remaining {
			r.errBody.Write(b[:remaining])
		} else {
			r.errBody.Write(b)
		}
	}
	return r.ResponseWriter.Write(b)
}

func loggingMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		if rec.status >= 400 {
			errMsg := strings.TrimRight(rec.errBody.String(), " \t\r\n")
			// Try to extract the message from writeError's JSON envelope.
			var envelope struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(errMsg), &envelope) == nil && envelope.Message != "" {
				errMsg = envelope.Message
			}
			log.Warn("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"error", errMsg,
			)
		} else {
			log.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}
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
	if errors.Is(err, ErrAuditNotConfigured) {
		return http.StatusServiceUnavailable
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
