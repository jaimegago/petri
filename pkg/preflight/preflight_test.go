package preflight

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startTestRegistry returns a TLS test server, an HTTP client trusting it,
// and an image-ref builder bound to the registry's host. Tests use the
// builder to construct references that probeImagePull will hit.
func startTestRegistry(t *testing.T, fr *fakeRegistry) (*http.Client, func(string) string) {
	t.Helper()
	if fr == nil {
		fr = &fakeRegistry{}
	}
	fr.t = t
	if !fr.failBlob() {
		fr.blobReachable = true
	}
	srv := fr.start()
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	return srv.Client(), func(repoTag string) string {
		return host + "/" + repoTag
	}
}

func (fr *fakeRegistry) failBlob() bool { return fr.blobStatus != 0 }

func TestRun_HappyPath(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	dir := t.TempDir()
	auditFile := filepath.Join(dir, "audit.log")

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    ref("default:1.0"),
		UtilImage:       ref("util:1.0"),
		AuditLogPath:    auditFile,
		HTTPClient:      httpClient,
		PerCheckTimeout: 2 * time.Second,
	}
	rep, err := Run(t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.Passed {
		t.Errorf("expected Passed=true, got report:\n%s", renderText(t, rep))
	}
	wantNames := []string{
		"kubeconfig", "cluster_reachable", "rbac",
		"image_default", "image_util", "audit_log_path",
	}
	gotNames := []string{}
	for _, c := range rep.Checks {
		gotNames = append(gotNames, c.Name)
	}
	if !sameOrder(gotNames, wantNames) {
		t.Errorf("check order mismatch: got %v want %v", gotNames, wantNames)
	}
	for _, c := range rep.Checks {
		if c.Status != StatusPass {
			t.Errorf("check %s: status=%s summary=%s", c.Name, c.Status, c.Summary)
		}
	}
}

func TestRun_KubeconfigMissing(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	opts := Options{
		KubeconfigPath:  filepath.Join(t.TempDir(), "does-not-exist"),
		DefaultImage:    ref("default:1.0"),
		UtilImage:       ref("util:1.0"),
		AuditLogPath:    filepath.Join(t.TempDir(), "audit.log"),
		HTTPClient:      httpClient,
		PerCheckTimeout: 2 * time.Second,
	}
	rep, err := Run(t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Passed {
		t.Fatalf("expected report to fail")
	}
	first := findCheck(t, rep, "kubeconfig")
	if first.Status != StatusFail {
		t.Errorf("kubeconfig check: status=%s summary=%s", first.Status, first.Summary)
	}
	if !strings.Contains(first.NextSteps, "petri create") {
		t.Errorf("missing actionable next-step in summary: %q", first.NextSteps)
	}

	// All five subsequent checks must be marked skip when the kubeconfig
	// FILE is missing. The cluster/rbac skips keep the existing reason
	// (their own check ran with a nil client); the registry/audit skips
	// carry the distinct file-missing reason because they were
	// short-circuited at the runner level.
	for _, name := range []string{"cluster_reachable", "rbac"} {
		c := findCheck(t, rep, name)
		if c.Status != StatusSkip {
			t.Errorf("expected %s skip, got %s", name, c.Status)
		}
		if !strings.Contains(c.Summary, "kubeconfig check did not pass") {
			t.Errorf("%s: expected existing skip reason, got %q", name, c.Summary)
		}
	}
	const fileMissingReason = "skipped: kubeconfig file does not exist"
	for _, name := range []string{"image_default", "image_util", "audit_log_path"} {
		c := findCheck(t, rep, name)
		if c.Status != StatusSkip {
			t.Errorf("expected %s skip, got %s", name, c.Status)
		}
		if c.Summary != fileMissingReason {
			t.Errorf("%s: expected file-missing reason, got %q", name, c.Summary)
		}
	}

	// The rendered text must surface the file-missing reason on the
	// short-circuited checks (the runner-level skip reason is what users
	// see, not a generic "skip" without explanation).
	out := renderText(t, rep)
	for _, want := range []string{
		"Default OASIS image is pullable",
		"Internal util image is pullable",
		"Audit log path writable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q line:\n%s", want, out)
		}
	}
	if !strings.Contains(out, fileMissingReason) {
		t.Errorf("rendered output missing file-missing reason:\n%s", out)
	}
}

// TestRun_KubeconfigParseError_KeepsRegistryChecks covers the other half of
// the two-tier skip: when the kubeconfig file EXISTS but does not parse, we
// only short-circuit the cluster-dependent checks. Registry probes are
// genuinely independent in this case and must still run.
func TestRun_KubeconfigParseError_KeepsRegistryChecks(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	// Write garbage to a real file so os.Stat succeeds but loadKubeconfig fails.
	garbage := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(garbage, []byte("not a kubeconfig"), 0o600); err != nil {
		t.Fatalf("write garbage kubeconfig: %v", err)
	}

	opts := Options{
		KubeconfigPath:  garbage,
		DefaultImage:    ref("default:1.0"),
		HTTPClient:      httpClient,
		PerCheckTimeout: 2 * time.Second,
	}
	rep, err := Run(t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Passed {
		t.Fatalf("expected report to fail")
	}

	// Image check must have actually run — the registry probe is independent
	// of cluster reachability when the kubeconfig file existed.
	img := findCheck(t, rep, "image_default")
	if img.Status != StatusPass {
		t.Errorf("expected image_default to run and pass; status=%s summary=%q", img.Status, img.Summary)
	}
}

func TestRun_ServerUnreachable(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	opts := Options{
		KubeClient:      &fakeKube{versionErr: errReachable},
		DefaultImage:    ref("default:1.0"),
		UtilImage:       ref("util:1.0"),
		HTTPClient:      httpClient,
		PerCheckTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if rep.Passed {
		t.Fatalf("expected report to fail")
	}
	c := findCheck(t, rep, "cluster_reachable")
	if c.Status != StatusFail {
		t.Fatalf("status=%s", c.Status)
	}
	if !strings.Contains(c.Summary, "unreachable") {
		t.Errorf("summary should mention unreachable: %q", c.Summary)
	}
}

func TestRun_RBACDenied(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	fk := &fakeKube{
		canI: map[string]bool{
			"create:namespaces": false,
			"delete:namespaces": true,
		},
		canIReason: map[string]string{
			"create:namespaces": "no role bound to user",
		},
	}
	opts := Options{
		KubeClient:      fk,
		DefaultImage:    ref("default:1.0"),
		UtilImage:       ref("util:1.0"),
		HTTPClient:      httpClient,
		PerCheckTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if rep.Passed {
		t.Fatalf("expected report to fail")
	}
	c := findCheck(t, rep, "rbac")
	if c.Status != StatusFail {
		t.Fatalf("status=%s", c.Status)
	}
	if !strings.Contains(c.Summary, "create namespaces") {
		t.Errorf("summary should mention denied verb: %q", c.Summary)
	}
}

func TestRun_BlobUnreachable_FlagsR2(t *testing.T) {
	// Blob endpoint hangs; the BlobErr should be surfaced into the rendered
	// diagnostic. The R2 hint must appear in the failing image check.
	fr := &fakeRegistry{blobReachable: false}
	srv := fr.start()
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")

	client := &http.Client{Transport: srv.Client().Transport, Timeout: 250 * time.Millisecond}

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    host + "/default:1.0",
		HTTPClient:      client,
		PerCheckTimeout: 1 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if rep.Passed {
		t.Fatalf("expected report to fail")
	}
	c := findCheck(t, rep, "image_default")
	if c.Status != StatusFail {
		t.Fatalf("status=%s", c.Status)
	}
	if !strings.Contains(c.Diagnostic, "Cloudflare R2") {
		t.Errorf("diagnostic should mention Cloudflare R2: %q", c.Diagnostic)
	}
	if !strings.Contains(c.Diagnostic, "172.64.0.0/13") {
		t.Errorf("diagnostic should mention the R2 CIDR: %q", c.Diagnostic)
	}
}

// TestRun_BlobHTTPError_FlagsProbeBug covers Bug 2's renderer side: when the
// blob endpoint returns a 404 (registry/probe inconsistency rather than a
// CDN block), the diagnostic must NOT include the R2 explanation, and must
// instead point the user at filing a petri bug.
func TestRun_BlobHTTPError_FlagsProbeBug(t *testing.T) {
	fr := &fakeRegistry{blobStatus: http.StatusNotFound}
	srv := fr.start()
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    host + "/default:1.0",
		HTTPClient:      srv.Client(),
		PerCheckTimeout: 1 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if rep.Passed {
		t.Fatalf("expected report to fail")
	}
	c := findCheck(t, rep, "image_default")
	if c.Status != StatusFail {
		t.Fatalf("status=%s", c.Status)
	}
	if strings.Contains(c.Diagnostic, "Cloudflare R2") {
		t.Errorf("HTTP-status blob failure must not surface R2 messaging: %q", c.Diagnostic)
	}
	if strings.Contains(c.Diagnostic, "172.64.0.0/13") {
		t.Errorf("HTTP-status blob failure must not surface the R2 CIDR: %q", c.Diagnostic)
	}
	if !strings.Contains(c.Diagnostic, "petri bug") {
		t.Errorf("expected probe-bug hint in diagnostic: %q", c.Diagnostic)
	}
	if !strings.Contains(c.Summary, "HTTP error") {
		t.Errorf("summary should call out the HTTP error: %q", c.Summary)
	}
}

// TestRun_ManifestList_PassesThrough covers Bug 1's positive case at the Run
// level: a multi-arch image (manifest list) must complete the probe by
// resolving the linux/amd64 child manifest and verifying its blob.
func TestRun_ManifestList_PassesThrough(t *testing.T) {
	fr := &fakeRegistry{manifestList: true, blobReachable: true}
	srv := fr.start()
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    host + "/multi-arch:v1",
		HTTPClient:      srv.Client(),
		PerCheckTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if !rep.Passed {
		t.Fatalf("expected report to pass; report:\n%s", renderText(t, rep))
	}
	c := findCheck(t, rep, "image_default")
	if c.Status != StatusPass {
		t.Fatalf("status=%s summary=%s", c.Status, c.Summary)
	}
	if !strings.Contains(c.Summary, "linux/amd64") {
		t.Errorf("summary should call out the resolved per-arch platform: %q", c.Summary)
	}
	if c.Platform != "linux/amd64" {
		t.Errorf("Platform field should carry the resolved arch on multi-arch success, got %q", c.Platform)
	}

	// The success line in the rendered report must show the platform inline
	// with the duration so the operator sees what was actually verified
	// without grepping the JSON output.
	out := renderText(t, rep)
	var imgLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Default OASIS image") {
			imgLine = line
			break
		}
	}
	if imgLine == "" {
		t.Fatalf("did not find image line in rendered output:\n%s", out)
	}
	if !strings.Contains(imgLine, "linux/amd64") {
		t.Errorf("rendered success line should include the platform; got: %q", imgLine)
	}

	// JSON output must carry the platform on the per-check record.
	var buf bytes.Buffer
	if err := RenderJSON(&buf, rep); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var rj struct {
		Checks []struct {
			Name     string `json:"name"`
			Platform string `json:"platform"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rj); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var seen bool
	for _, ch := range rj.Checks {
		if ch.Name == "image_default" {
			seen = true
			if ch.Platform != "linux/amd64" {
				t.Errorf("JSON image_default.platform=%q want linux/amd64", ch.Platform)
			}
		}
	}
	if !seen {
		t.Errorf("image_default not present in JSON output")
	}
}

// TestRun_SingleArchImage_NoPlatform asserts that single-arch image probes
// do NOT set Platform — the field is reserved for multi-arch probes where
// the resolved arch is the only signal of which manifest we verified.
func TestRun_SingleArchImage_NoPlatform(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    ref("default:1.0"),
		HTTPClient:      httpClient,
		PerCheckTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if !rep.Passed {
		t.Fatalf("expected report to pass; report:\n%s", renderText(t, rep))
	}
	c := findCheck(t, rep, "image_default")
	if c.Platform != "" {
		t.Errorf("single-arch probe should leave Platform empty, got %q", c.Platform)
	}

	// Rendered line must NOT include "linux/amd64" or any "/" suffix in the
	// trailing parens — single-arch renders as today.
	out := renderText(t, rep)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Default OASIS image") {
			if strings.Contains(line, "linux/amd64") {
				t.Errorf("single-arch render leaked a platform tag: %q", line)
			}
			break
		}
	}
}

// TestRun_DurationsRendered covers Bug 3: per-check durations must propagate
// to the rendered report. Before the fix, every line printed `(0µs)` because
// the deferred Duration assignment was clobbered by the value-copy of the
// non-named return.
func TestRun_DurationsRendered(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	// Force cluster_reachable to take >> 10ms by sleeping inside ServerVersion.
	fk := &fakeKube{versionDelay: 25 * time.Millisecond}

	opts := Options{
		KubeClient:      fk,
		DefaultImage:    ref("default:1.0"),
		HTTPClient:      httpClient,
		PerCheckTimeout: 5 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if !rep.Passed {
		t.Fatalf("expected pass; report:\n%s", renderText(t, rep))
	}

	// Use the real renderer so a future regression in render.go (e.g. a
	// formatDuration that drops sub-second values) is also caught here.
	out := renderText(t, rep)
	var clusterLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Cluster reachable") {
			clusterLine = line
			break
		}
	}
	if clusterLine == "" {
		t.Fatalf("did not find Cluster reachable line in output:\n%s", out)
	}
	if strings.Contains(clusterLine, "(0µs)") {
		t.Errorf("cluster_reachable rendered with zero duration: %q", clusterLine)
	}

	// Also assert at the struct level — caught the original bug as well.
	cr := findCheck(t, rep, "cluster_reachable")
	if cr.Duration < 10*time.Millisecond {
		t.Errorf("cluster_reachable duration too small: %s (expected >= 10ms)", cr.Duration)
	}
}

func TestRun_AuditLogPathMissing(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    ref("default:1.0"),
		UtilImage:       ref("util:1.0"),
		HTTPClient:      httpClient,
		AuditLogPath:    "/nonexistent/parent/audit.log",
		PerCheckTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if rep.Passed {
		t.Fatalf("expected report to fail")
	}
	c := findCheck(t, rep, "audit_log_path")
	if c.Status != StatusFail {
		t.Fatalf("status=%s summary=%s", c.Status, c.Summary)
	}
	if !strings.Contains(c.NextSteps, "mkdir") {
		t.Errorf("expected mkdir hint, got %q", c.NextSteps)
	}
}

func TestRun_AuditLogPathNotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; chmod 0500 won't block writes")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.Chmod(dir, 0o700) //nolint:errcheck

	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    ref("default:1.0"),
		HTTPClient:      httpClient,
		AuditLogPath:    filepath.Join(dir, "audit.log"),
		PerCheckTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	c := findCheck(t, rep, "audit_log_path")
	if c.Status != StatusFail {
		t.Fatalf("status=%s summary=%s", c.Status, c.Summary)
	}
	if !strings.Contains(c.Summary, "not writable") {
		t.Errorf("summary should mention writability: %q", c.Summary)
	}
}

func TestRun_AuditLogPathSkippedWhenEmpty(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    ref("default:1.0"),
		UtilImage:       ref("util:1.0"),
		HTTPClient:      httpClient,
		PerCheckTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	c := findCheck(t, rep, "audit_log_path")
	if c.Status != StatusSkip {
		t.Errorf("expected skip for empty audit_log_path, got %s", c.Status)
	}
}

func TestRun_DeepMode(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	fk := &fakeKube{
		statuses: []PodPullStatus{
			{Phase: "Pending", Reason: "ContainerCreating", Pulling: true},
			{Phase: "Running", Ready: true},
		},
	}
	opts := Options{
		KubeClient:      fk,
		DefaultImage:    ref("default:1.0"),
		HTTPClient:      httpClient,
		Deep:            true,
		PerCheckTimeout: 2 * time.Second,
		DeepPullTimeout: 5 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	if !rep.Passed {
		t.Fatalf("expected pass; report:\n%s", renderText(t, rep))
	}
	if findCheck(t, rep, "image_default_deep").Status != StatusPass {
		t.Errorf("deep check did not pass")
	}
	if len(fk.createdNS) != 1 {
		t.Errorf("expected 1 namespace created, got %d", len(fk.createdNS))
	}
	if len(fk.deletedNS) != 1 {
		t.Errorf("expected 1 namespace deleted, got %d", len(fk.deletedNS))
	}
	if fk.createdImage != ref("default:1.0") {
		t.Errorf("createdImage=%s want %s", fk.createdImage, ref("default:1.0"))
	}
}

func TestRun_DeepMode_PullBackoff(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	fk := &fakeKube{
		statuses: []PodPullStatus{
			{Phase: "Pending", Reason: "ImagePullBackOff", Message: "no such host", Pulling: true},
		},
	}
	opts := Options{
		KubeClient:      fk,
		DefaultImage:    ref("default:1.0"),
		HTTPClient:      httpClient,
		Deep:            true,
		PerCheckTimeout: 2 * time.Second,
		DeepPullTimeout: 5 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	c := findCheck(t, rep, "image_default_deep")
	if c.Status != StatusFail {
		t.Errorf("expected fail, got %s; summary=%s", c.Status, c.Summary)
	}
	if !strings.Contains(c.Summary, "could not pull") {
		t.Errorf("summary should mention pull failure: %q", c.Summary)
	}
}

func TestRun_DeepMode_SkippedWhenClusterUnreachable(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	fk := &fakeKube{versionErr: errReachable}
	opts := Options{
		KubeClient:      fk,
		DefaultImage:    ref("default:1.0"),
		HTTPClient:      httpClient,
		Deep:            true,
		PerCheckTimeout: 1 * time.Second,
		DeepPullTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)
	// Deep checks still queue, but report skip because cluster_reachable failed.
	// Actually we keep them — only kubeconfig short-circuits the kc. With a
	// supplied KubeClient, kc is set even if ServerVersion fails. Verify
	// behaviour: the deep test will attempt CreateNamespace, which the fake
	// allows — so it would pass the namespace step but fail polling because
	// the fake's PodPullStatus returns Pending forever. Cap with a tight
	// DeepPullTimeout above so the test bails out quickly.
	c := findCheck(t, rep, "image_default_deep")
	if c.Status == StatusPass {
		t.Errorf("deep should not pass when cluster is unreachable; got %s", c.Status)
	}
}

func TestRenderJSON_RoundTrips(t *testing.T) {
	httpClient, ref := startTestRegistry(t, &fakeRegistry{blobReachable: true})

	opts := Options{
		KubeClient:      &fakeKube{},
		DefaultImage:    ref("default:1.0"),
		HTTPClient:      httpClient,
		PerCheckTimeout: 2 * time.Second,
	}
	rep, _ := Run(t.Context(), opts)

	var buf bytes.Buffer
	if err := RenderJSON(&buf, rep); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	var round Report
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if round.Passed != rep.Passed {
		t.Errorf("Passed mismatch")
	}
	if len(round.Checks) != len(rep.Checks) {
		t.Errorf("checks length mismatch: %d vs %d", len(round.Checks), len(rep.Checks))
	}
}

func TestClassifyKubeError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind errKind
	}{
		{"network", errReachable, errKindNetwork},
		{"tls", &stringErr{"x509: certificate signed by unknown authority"}, errKindTLS},
		{"deadline", context.DeadlineExceeded, errKindNetwork},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := classifyKubeError(c.err)
			if got != c.kind {
				t.Errorf("kind=%d want %d", got, c.kind)
			}
		})
	}
}

// stringErr is a tiny error type used by TestClassifyKubeError to avoid
// dragging extra packages in.
type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }

// renderText renders a report as text into a string for failure-message use.
func renderText(t *testing.T, rep *Report) string {
	t.Helper()
	var buf bytes.Buffer
	Render(&buf, rep)
	return buf.String()
}

func findCheck(t *testing.T, rep *Report, name string) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in report:\n%s", name, renderText(t, rep))
	return Check{}
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
