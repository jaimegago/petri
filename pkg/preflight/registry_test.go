package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeRegistry is a minimal OCI registry suitable for tests. It serves an
// image manifest at /v2/<repo>/manifests/<tag> referencing a single config
// blob whose presence (or simulated failure) the test controls. When
// requireBearer is true, all manifest/blob requests must include a Bearer
// token; the registry vends tokens at /token.
//
// When manifestList is true, the top-level tag returns a Docker manifest
// list / OCI image index pointing to per-arch child manifests, which the
// registry also serves at /v2/<repo>/manifests/<digest>. This lets tests
// reproduce the multi-arch case (registry.k8s.io/e2e-test-images/...) that
// triggered Bug 1.
type fakeRegistry struct {
	t              *testing.T
	requireBearer  bool
	blobReachable  bool
	missingTag     bool
	manifestStatus int    // override for manifest GET; 0 → 200
	blobStatus     int    // override for blob HEAD; 0 → 200
	configDigest   string // returned in manifest body

	// manifestList = true makes the top-level tag respond with a manifest
	// list pointing at perArchDigest (linux/amd64). A subsequent GET on
	// /manifests/<perArchDigest> returns the actual per-arch image manifest.
	manifestList bool
	// noLinuxAmd64 (only meaningful with manifestList=true) emits a manifest
	// list with NO linux/amd64 entry — used to test the per-arch resolution
	// failure path.
	noLinuxAmd64 bool
	// perArchDigest is the digest the manifest list points at. Defaults to a
	// fixed sha256 if empty.
	perArchDigest string
}

// start spins up a TLS httptest.Server. Tests must use srv.Client() so the
// self-signed cert validates. We use TLS rather than plain HTTP because
// probeImagePull always builds https:// URLs.
func (fr *fakeRegistry) start() *httptest.Server {
	if fr.configDigest == "" {
		fr.configDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	}
	if fr.perArchDigest == "" {
		fr.perArchDigest = "sha256:abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abcd"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "fake-token"})
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		// Bearer enforcement first.
		if fr.requireBearer && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			realm := "https://" + r.Host + "/token"
			w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s",service="fake"`, realm))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch {
		case strings.Contains(r.URL.Path, "/manifests/"):
			if fr.missingTag {
				http.Error(w, "no such tag", http.StatusNotFound)
				return
			}
			if fr.manifestStatus != 0 {
				http.Error(w, "configured failure", fr.manifestStatus)
				return
			}
			// Pull the suffix after "/manifests/" so we can tell a top-level
			// tag fetch from a per-arch digest fetch.
			suffix := r.URL.Path[strings.Index(r.URL.Path, "/manifests/")+len("/manifests/"):]
			isDigestFetch := strings.HasPrefix(suffix, "sha256:")

			if fr.manifestList && !isDigestFetch {
				// Top-level tag → manifest list. Always include arm64; only
				// include amd64 unless noLinuxAmd64 is set. We use the
				// Docker manifest list media type (the more common case in
				// the wild and the one registry.k8s.io serves).
				w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.list.v2+json")
				entries := []string{
					`{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":500,"platform":{"os":"linux","architecture":"arm64"}}`,
				}
				if !fr.noLinuxAmd64 {
					entries = append(entries, fmt.Sprintf(
						`{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","digest":%q,"size":500,"platform":{"os":"linux","architecture":"amd64"}}`,
						fr.perArchDigest))
				}
				body := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[%s]}`, strings.Join(entries, ","))
				_, _ = w.Write([]byte(body))
				return
			}
			// Either a single-arch tag fetch, or a per-arch digest fetch
			// against a manifestList registry. Either way, serve a v2 image
			// manifest pointing at configDigest.
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			body := fmt.Sprintf(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
  "config": {"digest": %q, "size": 100},
  "layers": []
}`, fr.configDigest)
			_, _ = w.Write([]byte(body))
		case strings.Contains(r.URL.Path, "/blobs/"):
			if fr.blobStatus != 0 {
				http.Error(w, "configured failure", fr.blobStatus)
				return
			}
			if !fr.blobReachable {
				// Hang until the request context is cancelled; this mimics
				// an i/o timeout against an unreachable CDN.
				select {
				case <-r.Context().Done():
					return
				case <-time.After(30 * time.Second):
					return
				}
			}
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	return httptest.NewTLSServer(mux)
}

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		in        string
		wantReg   string
		wantRepo  string
		wantTag   string
		wantError bool
	}{
		{
			in: "registry.k8s.io/nginx-slim:0.27", wantReg: "registry.k8s.io",
			wantRepo: "nginx-slim", wantTag: "0.27",
		},
		{
			in: "registry.k8s.io/e2e-test-images/busybox:1.37.0-2", wantReg: "registry.k8s.io",
			wantRepo: "e2e-test-images/busybox", wantTag: "1.37.0-2",
		},
		{
			in: "nginx:1.27", wantReg: "registry-1.docker.io",
			wantRepo: "library/nginx", wantTag: "1.27",
		},
		{
			in: "library/nginx", wantReg: "registry-1.docker.io",
			wantRepo: "library/nginx", wantTag: "latest",
		},
		{
			in: "myregistry.local:5000/team/app:v1", wantReg: "myregistry.local:5000",
			wantRepo: "team/app", wantTag: "v1",
		},
		{in: "", wantError: true},
		{in: "anything@sha256:abcd", wantError: true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseImageRef(c.in)
			if c.wantError {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Registry != c.wantReg || got.Repository != c.wantRepo || got.Tag != c.wantTag {
				t.Errorf("got %+v want reg=%s repo=%s tag=%s",
					got, c.wantReg, c.wantRepo, c.wantTag)
			}
		})
	}
}

// regHost extracts the host:port substring from an https://host:port URL.
func regHost(u string) string { return strings.TrimPrefix(u, "https://") }

func TestProbeImagePull_Anonymous(t *testing.T) {
	fr := &fakeRegistry{t: t, blobReachable: true}
	srv := fr.start()
	defer srv.Close()

	ref := regHost(srv.URL) + "/test-image:latest"
	probe, err := probeImagePull(t.Context(), srv.Client(), ref)
	if err != nil {
		t.Fatalf("probeImagePull: %v", err)
	}
	if !probe.ManifestOK {
		t.Errorf("manifest should be OK; err: %s", probe.ManifestErr)
	}
	if !probe.BlobOK {
		t.Errorf("blob should be OK; err: %s", probe.BlobErr)
	}
}

func TestProbeImagePull_BearerAuth(t *testing.T) {
	fr := &fakeRegistry{t: t, requireBearer: true, blobReachable: true}
	srv := fr.start()
	defer srv.Close()

	probe, err := probeImagePull(t.Context(), srv.Client(), regHost(srv.URL)+"/team/app:v1")
	if err != nil {
		t.Fatalf("probeImagePull: %v", err)
	}
	if !probe.ManifestOK || !probe.BlobOK {
		t.Errorf("expected manifest+blob OK; probe=%+v", probe)
	}
}

func TestProbeImagePull_BlobUnreachable(t *testing.T) {
	fr := &fakeRegistry{t: t, blobReachable: false}
	srv := fr.start()
	defer srv.Close()

	// Reuse the test server's TLS-trusting transport but cap timeouts so the
	// hung blob endpoint bails out quickly.
	client := &http.Client{Transport: srv.Client().Transport, Timeout: 500 * time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	probe, err := probeImagePull(ctx, client, regHost(srv.URL)+"/test-image:latest")
	if err != nil {
		t.Fatalf("probeImagePull: %v", err)
	}
	if !probe.ManifestOK {
		t.Errorf("manifest should be OK")
	}
	if probe.BlobOK {
		t.Errorf("blob should be reported as failed")
	}
	if probe.BlobErr == "" {
		t.Errorf("expected non-empty BlobErr")
	}
}

func TestProbeImagePull_MissingTag(t *testing.T) {
	fr := &fakeRegistry{t: t, missingTag: true}
	srv := fr.start()
	defer srv.Close()

	probe, err := probeImagePull(t.Context(), srv.Client(), regHost(srv.URL)+"/test-image:nope")
	if err != nil {
		t.Fatalf("probeImagePull: %v", err)
	}
	if probe.ManifestOK {
		t.Errorf("manifest should be reported as failed")
	}
	if probe.ManifestErr == "" {
		t.Errorf("expected non-empty ManifestErr")
	}
}

// TestProbeImagePull_ManifestList covers the multi-arch case (Bug 1):
// registry.k8s.io/e2e-test-images/busybox is a manifest list, and the
// previous probe HEAD'd a per-arch manifest digest at /blobs/, getting a
// misleading 404. The fix resolves the per-arch manifest first, then HEADs
// a real blob digest from inside it.
func TestProbeImagePull_ManifestList(t *testing.T) {
	fr := &fakeRegistry{t: t, manifestList: true, blobReachable: true}
	srv := fr.start()
	defer srv.Close()

	probe, err := probeImagePull(t.Context(), srv.Client(), regHost(srv.URL)+"/multi-arch:v1")
	if err != nil {
		t.Fatalf("probeImagePull: %v", err)
	}
	if !probe.ManifestOK {
		t.Errorf("manifest list should be reachable; err: %s", probe.ManifestErr)
	}
	if probe.PerArchURL == "" {
		t.Errorf("expected per-arch URL to be populated for a manifest list")
	}
	if probe.PerArchPlatform != "linux/amd64" {
		t.Errorf("expected linux/amd64 selection, got %q", probe.PerArchPlatform)
	}
	if probe.PerArchErr != "" {
		t.Errorf("unexpected per-arch resolution error: %s", probe.PerArchErr)
	}
	if !probe.BlobOK {
		t.Errorf("blob should be reachable through the per-arch manifest; err: %s", probe.BlobErr)
	}
}

// TestProbeImagePull_ManifestList_NoLinuxAmd64 verifies the typed
// "no compatible per-arch manifest" failure when the list lacks any entry
// matching linux/amd64 or the host runtime.
func TestProbeImagePull_ManifestList_NoLinuxAmd64(t *testing.T) {
	fr := &fakeRegistry{t: t, manifestList: true, noLinuxAmd64: true, blobReachable: true}
	srv := fr.start()
	defer srv.Close()

	probe, err := probeImagePull(t.Context(), srv.Client(), regHost(srv.URL)+"/multi-arch:v1")
	if err != nil {
		t.Fatalf("probeImagePull: %v", err)
	}
	if !probe.ManifestOK {
		t.Errorf("top-level manifest list should still parse")
	}
	if probe.PerArchErr == "" {
		t.Errorf("expected PerArchErr to be set when no compatible entry exists")
	}
	if !strings.Contains(probe.PerArchErr, "no compatible per-arch manifest") {
		t.Errorf("PerArchErr should describe the failure, got %q", probe.PerArchErr)
	}
	if probe.BlobURL != "" {
		t.Errorf("BlobURL should be empty when per-arch resolution failed; got %q", probe.BlobURL)
	}
}

// TestProbeImagePull_BlobErrKindHTTP covers Bug 2's HTTP-classified branch:
// when the manifest works but the blob endpoint returns a 4xx/5xx status,
// BlobErrKind must be "http" so the renderer surfaces a probe-bug message
// rather than the misleading R2/CDN explanation.
func TestProbeImagePull_BlobErrKindHTTP(t *testing.T) {
	fr := &fakeRegistry{t: t, blobStatus: http.StatusNotFound}
	srv := fr.start()
	defer srv.Close()

	probe, err := probeImagePull(t.Context(), srv.Client(), regHost(srv.URL)+"/test-image:latest")
	if err != nil {
		t.Fatalf("probeImagePull: %v", err)
	}
	if !probe.ManifestOK {
		t.Errorf("manifest should be OK; err: %s", probe.ManifestErr)
	}
	if probe.BlobOK {
		t.Errorf("blob should be reported as failed")
	}
	if probe.BlobErrKind != blobErrKindHTTP {
		t.Errorf("expected BlobErrKind=%q, got %q", blobErrKindHTTP, probe.BlobErrKind)
	}
	if !strings.Contains(probe.BlobErr, "404") {
		t.Errorf("BlobErr should mention the HTTP status, got %q", probe.BlobErr)
	}
}

// TestProbeImagePull_BlobErrKindTCP covers Bug 2's TCP-classified branch:
// when the blob endpoint hangs (no HTTP response), BlobErrKind must be "tcp"
// so the renderer keeps the existing R2/CDN messaging.
func TestProbeImagePull_BlobErrKindTCP(t *testing.T) {
	fr := &fakeRegistry{t: t, blobReachable: false}
	srv := fr.start()
	defer srv.Close()

	client := &http.Client{Transport: srv.Client().Transport, Timeout: 250 * time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	probe, err := probeImagePull(ctx, client, regHost(srv.URL)+"/test-image:latest")
	if err != nil {
		t.Fatalf("probeImagePull: %v", err)
	}
	if !probe.ManifestOK {
		t.Errorf("manifest should be OK")
	}
	if probe.BlobOK {
		t.Errorf("blob should be reported as failed")
	}
	if probe.BlobErrKind != blobErrKindTCP {
		t.Errorf("expected BlobErrKind=%q, got %q", blobErrKindTCP, probe.BlobErrKind)
	}
}
