package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/preflight"
	"github.com/jaimegago/petri/pkg/state"
)

// fakeKubeStub satisfies preflight.KubeClient for CLI tests. It always
// returns a healthy substrate.
type fakeKubeStub struct{}

func (fakeKubeStub) ServerVersion(context.Context) (string, error) { return "v1.30.0", nil }
func (fakeKubeStub) CanI(context.Context, string, string) (bool, string, error) {
	return true, "", nil
}
func (fakeKubeStub) CreateNamespace(context.Context, string) error               { return nil }
func (fakeKubeStub) DeleteNamespace(context.Context, string) error               { return nil }
func (fakeKubeStub) CreatePullPod(context.Context, string, string, string) error { return nil }
func (fakeKubeStub) PodPullStatus(context.Context, string, string) (preflight.PodPullStatus, error) {
	return preflight.PodPullStatus{Phase: "Running", Ready: true}, nil
}

// brokenKubeStub returns a network error from ServerVersion to simulate a
// broken substrate.
type brokenKubeStub struct{}

func (brokenKubeStub) ServerVersion(context.Context) (string, error) {
	return "", fmt.Errorf("dial tcp 10.0.0.1:6443: i/o timeout")
}
func (brokenKubeStub) CanI(context.Context, string, string) (bool, string, error) {
	return true, "", nil
}
func (brokenKubeStub) CreateNamespace(context.Context, string) error               { return nil }
func (brokenKubeStub) DeleteNamespace(context.Context, string) error               { return nil }
func (brokenKubeStub) CreatePullPod(context.Context, string, string, string) error { return nil }
func (brokenKubeStub) PodPullStatus(context.Context, string, string) (preflight.PodPullStatus, error) {
	return preflight.PodPullStatus{Phase: "Pending"}, nil
}

// startHealthyRegistry stands up a minimal OCI-shaped registry that
// successfully serves manifests + blobs. Returns a TLS-trusting client and
// a host:port string for image refs.
func startHealthyRegistry(t *testing.T) (*http.Client, string) {
	t.Helper()
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			fmt.Fprintf(w, `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":%q,"size":100}}`, digest)
		case strings.Contains(r.URL.Path, "/blobs/"):
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	return srv.Client(), host
}

func newVerifyTestCLI(t *testing.T, kube preflight.KubeClient, httpClient *http.Client, oasisCfg config.OASISConfig, registryHost string) *CLI {
	t.Helper()
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	c.cfg = &config.Config{OASIS: oasisCfg}
	c.preflightKube = kube
	c.preflightHTTP = httpClient
	// Route the util-image probe at the same fake registry so tests don't
	// depend on the real registry.k8s.io.
	c.preflightUtilImage = registryHost + "/util:1.0"
	c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return c
}

func TestRunVerify_HappyPath(t *testing.T) {
	httpClient, host := startHealthyRegistry(t)
	c := newVerifyTestCLI(t, fakeKubeStub{}, httpClient, config.OASISConfig{
		DefaultImage: host + "/default:1.0",
	}, host)

	var runErr error
	out := runWithCapturedStdout(t, func() {
		runErr = c.runVerify(t.Context(), &verifyOptions{json: true})
	})
	if runErr != nil {
		t.Fatalf("runVerify: %v\nstdout:\n%s", runErr, out)
	}

	var rep preflight.Report
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("JSON unmarshal: %v\nstdout:\n%s", err, out)
	}
	if !rep.Passed {
		t.Errorf("expected passed=true; report: %+v", rep)
	}
	if len(rep.Checks) == 0 {
		t.Errorf("expected non-empty checks slice")
	}
}

func TestRunVerify_KubeconfigMissing_ExitsNonZero(t *testing.T) {
	httpClient, host := startHealthyRegistry(t)
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	c.cfg = &config.Config{OASIS: config.OASISConfig{DefaultImage: host + "/default:1.0"}}
	c.preflightHTTP = httpClient // no preflightKube — forces real kubeconfig loading
	c.preflightUtilImage = host + "/util:1.0"
	c.log = slog.New(slog.NewTextHandler(io.Discard, nil))

	missing := filepath.Join(t.TempDir(), "no-kubeconfig-here")
	var (
		runErr  error
		elapsed time.Duration
	)
	out := runWithCapturedStdout(t, func() {
		start := time.Now()
		runErr = c.runVerify(t.Context(), &verifyOptions{kubeconfigPath: missing})
		elapsed = time.Since(start)
	})

	if runErr == nil {
		t.Fatalf("expected error, got nil; stdout:\n%s", out)
	}
	if elapsed > 2*time.Second {
		t.Errorf("expected fast failure (<2s), got %s", elapsed)
	}
	if !strings.Contains(string(out), "kubeconfig") {
		t.Errorf("expected report to mention kubeconfig, got:\n%s", out)
	}
}

func TestRunVerify_JSONOutputIsValid(t *testing.T) {
	httpClient, host := startHealthyRegistry(t)
	c := newVerifyTestCLI(t, fakeKubeStub{}, httpClient, config.OASISConfig{
		DefaultImage: host + "/default:1.0",
		AuditLogPath: filepath.Join(t.TempDir(), "audit.log"),
	}, host)

	var runErr error
	out := runWithCapturedStdout(t, func() {
		runErr = c.runVerify(t.Context(), &verifyOptions{json: true})
	})
	if runErr != nil {
		t.Fatalf("runVerify: %v\nstdout:\n%s", runErr, out)
	}

	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}
	for _, key := range []string{"started_at", "duration", "checks", "passed"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing JSON key %q", key)
		}
	}
}

func TestRunServeVerify_FailureAbortsBeforeListen(t *testing.T) {
	httpClient, host := startHealthyRegistry(t)

	c := newVerifyTestCLI(t, brokenKubeStub{}, httpClient, config.OASISConfig{
		DefaultImage: host + "/default:1.0",
	}, host)

	// Wire a stderr-substitute and a JSON-handler log buffer so we can
	// assert the new failure-path behaviour: the rendered report goes
	// directly to the report writer (no per-line slog wrapping) and a
	// single structured WARN line goes to the logger.
	var reportBuf bytes.Buffer
	c.serveVerifyOut = &reportBuf
	var logBuf safeBuffer
	c.log = slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Pick a free port; the test's success criterion is that runServe
	// returns an error AND nothing is listening on that port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close() // free the port; serve must not bind it

	opts := &serveOptions{
		listen:       addr,
		auditLogPath: "",
		verify:       true,
	}
	err = c.runServeVerify(t.Context(), opts, serveLabInfo{}, "")
	if err == nil {
		t.Fatalf("expected runServeVerify to return error on broken substrate")
	}
	if !strings.Contains(err.Error(), "preflight failed") {
		t.Errorf("expected 'preflight failed' in error: %v", err)
	}

	// Confirm nothing has bound the listen address.
	conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		t.Errorf("expected connection refused, got connection succeeded")
	}

	// The human-readable report must reach the report writer, not the
	// logger — operators need a clean, prefix-free view on stderr.
	report := reportBuf.String()
	if !strings.Contains(report, "Petri preflight report") {
		t.Errorf("expected rendered report header in stderr buffer, got:\n%s", report)
	}
	if !strings.Contains(report, "Result: FAIL") {
		t.Errorf("expected FAIL footer in stderr buffer, got:\n%s", report)
	}

	// Logger must carry exactly one structured WARN line summarising the
	// failure with the expected fields. Per-line WARN spam (the old
	// behaviour) would show up as many entries.
	logs := logBuf.String()
	warnLines := 0
	var summary map[string]any
	for _, line := range strings.Split(strings.TrimRight(logs, "\n"), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if entry["level"] == "WARN" {
			warnLines++
			summary = entry
		}
	}
	if warnLines != 1 {
		t.Errorf("expected exactly one WARN log line, got %d:\n%s", warnLines, logs)
	}
	if summary == nil {
		t.Fatalf("no WARN line found in logs:\n%s", logs)
	}
	if summary["msg"] != "preflight failed" {
		t.Errorf("WARN msg=%q want %q", summary["msg"], "preflight failed")
	}
	for _, field := range []string{"failed_checks", "total_checks", "duration_ms"} {
		if _, ok := summary[field]; !ok {
			t.Errorf("WARN line missing field %q: %v", field, summary)
		}
	}
	// The structured summary must NOT include the rendered report text.
	if msg, _ := summary["msg"].(string); strings.Contains(msg, "[FAIL]") || strings.Contains(msg, "Petri preflight") {
		t.Errorf("WARN msg leaked rendered report text: %v", summary)
	}
}

func TestRunServeVerify_HappyPathLogsPassedAndContinues(t *testing.T) {
	httpClient, host := startHealthyRegistry(t)

	var logBuf safeBuffer
	c := newVerifyTestCLI(t, fakeKubeStub{}, httpClient, config.OASISConfig{
		DefaultImage: host + "/default:1.0",
	}, host)
	c.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	opts := &serveOptions{listen: "127.0.0.1:0", verify: true}
	if err := c.runServeVerify(t.Context(), opts, serveLabInfo{}, ""); err != nil {
		t.Fatalf("runServeVerify: %v", err)
	}

	if !strings.Contains(logBuf.String(), "verify checks passed") {
		t.Errorf(`expected "verify checks passed" in logs, got:\n%s`, logBuf.String())
	}
}

// TestServe_KubeconfigOverrideHonoredAndLogsLabIgnored asserts that --kubeconfig
// on `petri serve` overrides --lab (per parity with `petri verify`), and that
// supplying both flags logs a single INFO line noting the lab was ignored.
func TestServe_KubeconfigOverrideHonoredAndLogsLabIgnored(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	var logBuf safeBuffer
	c.log = slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	const explicit = "/tmp/some-kubeconfig"
	info, err := c.resolveServeLabInfo(t.Context(), explicit, "ignored-lab")
	if err != nil {
		t.Fatalf("resolveServeLabInfo: %v", err)
	}
	if info.kubeconfigPath != explicit {
		t.Errorf("kubeconfigPath=%q want %q", info.kubeconfigPath, explicit)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "--kubeconfig provided; --lab ignored") {
		t.Errorf("expected ignored-lab INFO log, got:\n%s", logs)
	}
	// Lab metadata fields must not be populated when --kubeconfig wins —
	// we never queried the state DB for the lab.
	if info.auditLogPath != "" || info.labLevel != 0 || info.oasisMode {
		t.Errorf("lab metadata leaked into resolved info: %+v", info)
	}
}

// TestServe_NoKubeconfigNoLab matches the existing default-kubeconfig path:
// when neither flag is supplied, resolveServeLabInfo emits a WARN and
// returns an empty serveLabInfo so callers fall through to the default
// kubeconfig loading rules.
func TestServe_NoKubeconfigNoLab(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	var logBuf safeBuffer
	c.log = slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	info, err := c.resolveServeLabInfo(t.Context(), "", "")
	if err != nil {
		t.Fatalf("resolveServeLabInfo: %v", err)
	}
	if info.kubeconfigPath != "" {
		t.Errorf("expected empty kubeconfigPath, got %q", info.kubeconfigPath)
	}
	if !strings.Contains(logBuf.String(), "no --lab or --kubeconfig flag provided") {
		t.Errorf("expected default-kubeconfig WARN, got:\n%s", logBuf.String())
	}
}

// TestServe_VerifyWithKubeconfigFlagPreflight asserts the end-to-end:
// runServeVerify succeeds when --kubeconfig points at an arbitrary cluster
// with no lab registered, mirroring the `petri verify --kubeconfig` path.
func TestServe_VerifyWithKubeconfigFlagPreflight(t *testing.T) {
	httpClient, host := startHealthyRegistry(t)

	var logBuf safeBuffer
	c := newVerifyTestCLI(t, fakeKubeStub{}, httpClient, config.OASISConfig{
		DefaultImage: host + "/default:1.0",
	}, host)
	c.log = slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	opts := &serveOptions{
		listen:         "127.0.0.1:0",
		kubeconfigPath: "/some/path",
		verify:         true,
	}
	// Build serveLabInfo via the same helper a real `runServe` would use,
	// to exercise the precedence rules end-to-end.
	info, err := c.resolveServeLabInfo(t.Context(), opts.kubeconfigPath, opts.lab)
	if err != nil {
		t.Fatalf("resolveServeLabInfo: %v", err)
	}
	if info.kubeconfigPath != opts.kubeconfigPath {
		t.Errorf("kubeconfigPath=%q want %q", info.kubeconfigPath, opts.kubeconfigPath)
	}
	if err := c.runServeVerify(t.Context(), opts, info, ""); err != nil {
		t.Fatalf("runServeVerify with --kubeconfig: %v", err)
	}
	if !strings.Contains(logBuf.String(), "verify checks passed") {
		t.Errorf("expected pass log, got:\n%s", logBuf.String())
	}
}

// runWithCapturedStdout swaps os.Stdout for a pipe, runs fn, then closes
// the pipe and returns everything fn wrote. Callers MUST do their assertions
// against the returned bytes — os.Stdout is restored before the function
// returns.
func runWithCapturedStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()

	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}

// safeBuffer is a goroutine-safe bytes.Buffer; the slog handler may write
// from a goroutine in some cases.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
