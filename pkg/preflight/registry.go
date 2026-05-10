package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
)

// Blob-failure classification. probeImagePull sets BlobErrKind so the renderer
// can distinguish a real CDN/network problem (R2-style, the original
// motivating case) from an HTTP error that almost always indicates the probe
// was looking at the wrong digest.
const (
	blobErrKindTCP  = "tcp"
	blobErrKindHTTP = "http"
)

// imageRef is a parsed OCI image reference broken into its registry-API parts.
type imageRef struct {
	// Registry is the host (e.g. "registry.k8s.io", "registry-1.docker.io").
	Registry string
	// Repository is the path-ish part (e.g. "nginx-slim", "library/nginx").
	Repository string
	// Tag is the version (e.g. "0.27"); "" if no tag.
	Tag string
}

// parseImageRef breaks an OCI reference like "registry.k8s.io/nginx-slim:0.27"
// or "library/nginx:1.27" into its parts. Bare repositories like "nginx"
// expand to "library/nginx" on Docker Hub.
//
// Implementation is deliberately minimal — we don't support digests (`@sha256`)
// or Docker Hub's implicit `:latest` (we set Tag="latest" if no tag was given).
func parseImageRef(ref string) (imageRef, error) {
	if ref == "" {
		return imageRef{}, errors.New("empty image reference")
	}
	if strings.Contains(ref, "@") {
		return imageRef{}, fmt.Errorf("digest references not supported: %s", ref)
	}

	// Split off tag.
	tag := "latest"
	if i := strings.LastIndex(ref, ":"); i > 0 {
		// Make sure the colon is in the tag position, not in a port (e.g.
		// "myregistry:5000/foo"). The tag never contains "/".
		if !strings.Contains(ref[i:], "/") {
			tag = ref[i+1:]
			ref = ref[:i]
		}
	}

	// Split registry from repo. The first segment is treated as the registry
	// only if it contains "." or ":" or is "localhost"; otherwise it's
	// part of the repo and the registry is Docker Hub.
	registry := "registry-1.docker.io"
	repository := ref
	if i := strings.Index(ref, "/"); i > 0 {
		first := ref[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			registry = first
			repository = ref[i+1:]
		}
	}

	// Docker Hub auto-prefixes single-segment repos with library/.
	if registry == "registry-1.docker.io" && !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}

	if repository == "" {
		return imageRef{}, fmt.Errorf("missing repository in %q", ref)
	}
	return imageRef{Registry: registry, Repository: repository, Tag: tag}, nil
}

// imagePullProbe is the structured outcome of a registry-side image check.
// It carries enough detail for the renderer to produce the canonical R2
// error message when the manifest works but the blob HEAD times out, and to
// distinguish that case from an HTTP-level blob failure (probe bug).
type imagePullProbe struct {
	// ManifestURL is the /v2/<repo>/manifests/<tag> URL we hit.
	ManifestURL string
	// ManifestOK is true if the top-level manifest GET succeeded.
	ManifestOK bool
	// ManifestErr captures the failure if ManifestOK is false.
	ManifestErr string

	// PerArchURL is the /v2/<repo>/manifests/<digest> URL of the per-
	// architecture manifest selected from a manifest list / image index.
	// Empty when the top-level manifest was already a single-arch image
	// manifest.
	PerArchURL string
	// PerArchPlatform is "os/arch" of the selected per-arch entry, for
	// diagnostics.
	PerArchPlatform string
	// PerArchErr captures why per-arch resolution failed (no compatible
	// entry, or the GET on the resolved manifest URL failed).
	PerArchErr string

	// BlobURL is the /v2/<repo>/blobs/<digest> URL we hit, if any.
	BlobURL string
	// BlobOK is true if the blob HEAD succeeded.
	BlobOK bool
	// BlobErr captures the failure if BlobOK is false.
	BlobErr string
	// BlobErrKind classifies a blob failure: "tcp" for connection/timeout
	// (the CDN/R2 case) or "http" for a 4xx/5xx response (probe-bug case).
	BlobErrKind string
}

// probeImagePull performs the manifest-then-blob probe for ref. Errors come
// back via the imagePullProbe fields, not as Go errors — a returned non-nil
// error means we couldn't even attempt the probe (e.g. parse failure).
//
// If the top-level manifest is a manifest list (Docker) or image index
// (OCI), the probe resolves the per-arch child manifest first, then extracts
// a blob digest from that. HEADing a per-arch manifest digest at /blobs/
// (the original bug) returns 404 because per-arch digests address manifests
// not blobs.
func probeImagePull(ctx context.Context, client *http.Client, ref string) (imagePullProbe, error) {
	parsed, err := parseImageRef(ref)
	if err != nil {
		return imagePullProbe{}, err
	}
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s",
		parsed.Registry, parsed.Repository, parsed.Tag)

	probe := imagePullProbe{ManifestURL: manifestURL}

	body, contentType, err := getManifest(ctx, client, parsed, manifestURL)
	if err != nil {
		probe.ManifestErr = err.Error()
		return probe, nil
	}
	probe.ManifestOK = true

	if isManifestList(contentType) {
		digest, platform, err := selectPerArchDigest(body)
		if err != nil {
			probe.PerArchErr = err.Error()
			return probe, nil
		}
		probe.PerArchPlatform = platform
		probe.PerArchURL = fmt.Sprintf("https://%s/v2/%s/manifests/%s",
			parsed.Registry, parsed.Repository, digest)
		body, contentType, err = getManifest(ctx, client, parsed, probe.PerArchURL)
		if err != nil {
			probe.PerArchErr = err.Error()
			return probe, nil
		}
	}

	blobDigest := firstBlobDigest(body, contentType)
	if blobDigest == "" {
		// Manifest path was healthy but we couldn't pick a blob to verify
		// (unknown manifest shape). Still a useful signal — soft pass.
		return probe, nil
	}

	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s",
		parsed.Registry, parsed.Repository, blobDigest)
	probe.BlobURL = blobURL
	kind, err := headBlob(ctx, client, parsed, blobURL)
	if err != nil {
		probe.BlobErrKind = kind
		probe.BlobErr = err.Error()
		return probe, nil
	}
	probe.BlobOK = true
	return probe, nil
}

// isManifestList returns true if contentType identifies a Docker manifest
// list or OCI image index.
func isManifestList(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "manifest.list") || strings.Contains(ct, "image.index")
}

// selectPerArchDigest picks a child manifest digest from a manifest list /
// image index body. It prefers linux/amd64, then falls back to the host's
// runtime.GOOS/runtime.GOARCH. Returns an error describing the search if
// no compatible entry exists.
func selectPerArchDigest(body []byte) (digest, platform string, err error) {
	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if jerr := json.Unmarshal(body, &idx); jerr != nil {
		return "", "", fmt.Errorf("decoding manifest list: %w", jerr)
	}
	if len(idx.Manifests) == 0 {
		return "", "", errors.New("manifest list has no entries")
	}
	for _, m := range idx.Manifests {
		if m.Digest == "" {
			continue
		}
		if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
			return m.Digest, "linux/amd64", nil
		}
	}
	for _, m := range idx.Manifests {
		if m.Digest == "" {
			continue
		}
		if m.Platform.OS == runtime.GOOS && m.Platform.Architecture == runtime.GOARCH {
			return m.Digest, fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH), nil
		}
	}
	return "", "", fmt.Errorf("no compatible per-arch manifest (looked for linux/amd64, fallback %s/%s)",
		runtime.GOOS, runtime.GOARCH)
}

// getManifest fetches the manifest body and Content-Type. We do GET (not
// HEAD) because some registries don't support HEAD reliably for the
// manifest, and we need the body anyway to discover a referenced blob
// digest.
func getManifest(ctx context.Context, client *http.Client, ref imageRef, u string) (body []byte, contentType string, err error) {
	resp, err := doWithBearer(ctx, client, ref, http.MethodGet, u)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		snip, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("manifest GET %s: status %d: %s", u, resp.StatusCode, strings.TrimSpace(string(snip)))
	}

	body, err = io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("reading manifest body: %w", err)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// doWithBearer executes an authenticated registry request. It tries
// anonymous first; on 401 with a Bearer challenge it fetches a token and
// retries once.
func doWithBearer(ctx context.Context, client *http.Client, ref imageRef, method, u string) (*http.Response, error) {
	req, err := newRegistryRequest(ctx, method, u)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	challenge := resp.Header.Get("Www-Authenticate")
	resp.Body.Close()
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return nil, fmt.Errorf("%s %s: 401 with non-bearer challenge: %q", method, u, challenge)
	}
	token, err := fetchBearerToken(ctx, client, ref, challenge)
	if err != nil {
		return nil, fmt.Errorf("fetching auth token: %w", err)
	}
	req2, err := newRegistryRequest(ctx, method, u)
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Authorization", "Bearer "+token)
	return client.Do(req2)
}

// headBlob does a HEAD on a blob URL. Some registries (Docker Hub) issue a
// redirect to a CDN — we let the http client follow it. The whole point of
// the blob HEAD is to catch the case where the manifest endpoint is healthy
// but the CDN that serves blobs is not (the R2 case).
//
// Returns a kind alongside the error so callers can distinguish the two
// failure shapes:
//   - blobErrKindTCP: client.Do returned an error before the registry could
//     respond (DNS/dial/timeout). This is the genuine CDN-block scenario.
//   - blobErrKindHTTP: the registry responded with a 4xx/5xx. This usually
//     means the digest we HEAD'd was not a blob (e.g. we passed a per-arch
//     manifest digest into /blobs/) — a probe bug, not a network problem.
func headBlob(ctx context.Context, client *http.Client, ref imageRef, u string) (string, error) {
	resp, err := doWithBearer(ctx, client, ref, http.MethodHead, u)
	if err != nil {
		return blobErrKindTCP, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return blobErrKindHTTP, fmt.Errorf("blob HEAD %s: status %d", u, resp.StatusCode)
	}
	return "", nil
}

// newRegistryRequest builds a request with the OCI-compatible Accept header
// so the registry returns the right manifest media type.
func newRegistryRequest(ctx context.Context, method, u string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))
	return req, nil
}

// fetchBearerToken parses a Www-Authenticate Bearer challenge and exchanges
// it for an anonymous bearer token. Works against Docker Hub and gcr.io.
func fetchBearerToken(ctx context.Context, client *http.Client, ref imageRef, challenge string) (string, error) {
	params := parseChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("Www-Authenticate missing realm: %q", challenge)
	}

	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	scope := params["scope"]
	if scope == "" {
		scope = fmt.Sprintf("repository:%s:pull", ref.Repository)
	}
	q.Set("scope", scope)

	tokenURL := realm + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return "", fmt.Errorf("token endpoint %s: status %d: %s", tokenURL, resp.StatusCode, string(body))
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decoding token: %w", err)
	}
	if payload.Token != "" {
		return payload.Token, nil
	}
	if payload.AccessToken != "" {
		return payload.AccessToken, nil
	}
	return "", fmt.Errorf("token endpoint %s: response had no token field", tokenURL)
}

// parseChallenge parses a Www-Authenticate Bearer challenge into its
// key=value parameters. Returns an empty map on malformed input.
func parseChallenge(challenge string) map[string]string {
	out := map[string]string{}
	const prefix = "bearer "
	if len(challenge) < len(prefix) || !strings.EqualFold(challenge[:len(prefix)], prefix) {
		return out
	}
	rest := challenge[len(prefix):]
	for _, part := range splitChallenge(rest) {
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.TrimSpace(part[eq+1:])
		v = strings.Trim(v, `"`)
		out[k] = v
	}
	return out
}

// splitChallenge splits a Bearer challenge body on commas, but only outside
// of double-quoted spans (since scope may contain commas).
func splitChallenge(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range s {
		switch r {
		case '"':
			inQuotes = !inQuotes
			cur.WriteRune(r)
		case ',':
			if inQuotes {
				cur.WriteRune(r)
			} else {
				parts = append(parts, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}
	return parts
}

// firstBlobDigest extracts a referenced blob digest from an image manifest
// body. It picks the config blob first, then falls back to the first layer
// digest. Manifest lists / image indexes are resolved upstream by
// selectPerArchDigest, so this function only sees per-arch image manifests.
// Returns "" if the body has an unknown shape.
func firstBlobDigest(body []byte, contentType string) string {
	_ = contentType // kept for future content-type-aware parsing
	var m struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &m); err == nil && m.Config.Digest != "" {
		return m.Config.Digest
	}
	var layered struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &layered); err == nil && len(layered.Layers) > 0 {
		return layered.Layers[0].Digest
	}
	return ""
}
