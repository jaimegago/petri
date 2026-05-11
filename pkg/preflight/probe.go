package preflight

import (
	"context"
	"net/http"
)

// ImageProbeResult is the public outcome of a registry-side image check.
// It mirrors the package-private imagePullProbe but exposes a stable surface
// so other packages can consume it without lifting any internals from
// pkg/preflight.
//
// Use ProbeImage to populate it.
type ImageProbeResult struct {
	// ManifestURL is the /v2/<repo>/manifests/<tag> URL the probe hit.
	ManifestURL string
	// ManifestOK is true if the top-level manifest GET succeeded.
	ManifestOK bool
	// ManifestError, when non-empty, captures why the manifest GET failed.
	ManifestError string

	// PerArchURL is the /v2/<repo>/manifests/<digest> URL of the per-
	// architecture child manifest selected from a manifest list / image
	// index. Empty when the top-level manifest was already single-arch.
	PerArchURL string
	// PerArchPlatform is "os/arch" of the selected per-arch entry.
	PerArchPlatform string
	// PerArchError captures why per-arch resolution or fetch failed.
	PerArchError string

	// BlobURL is the /v2/<repo>/blobs/<digest> URL the probe hit, if any.
	BlobURL string
	// BlobOK is true if the blob HEAD succeeded.
	BlobOK bool
	// BlobError, when non-empty, captures why the blob HEAD failed.
	BlobError string
	// BlobErrorKind classifies a blob failure as "tcp" (connection /
	// timeout, the genuine R2/CDN-block scenario) or "http" (a 4xx/5xx
	// response, usually a probe bug). Empty when the blob check passed or
	// no blob was probed.
	BlobErrorKind string
}

// ProbeOutcome is a single-word summary of an ImageProbeResult, suitable for
// structured logs and operator-facing diagnostics. It collapses the various
// failure shapes into one of: "pass", "manifest-fail", "perarch-fail",
// "blob-tcp-fail", "blob-http-fail", "invalid-ref".
func (r ImageProbeResult) Outcome() string {
	switch {
	case !r.ManifestOK:
		return "manifest-fail"
	case r.PerArchError != "":
		return "perarch-fail"
	case r.BlobURL != "" && !r.BlobOK && r.BlobErrorKind == blobErrKindTCP:
		return "blob-tcp-fail"
	case r.BlobURL != "" && !r.BlobOK:
		return "blob-http-fail"
	default:
		return "pass"
	}
}

// Detail returns the first non-empty failure message in the result, or the
// empty string if the probe passed. The string is single-line and safe to
// include in a slog field.
func (r ImageProbeResult) Detail() string {
	switch {
	case !r.ManifestOK:
		return r.ManifestError
	case r.PerArchError != "":
		return r.PerArchError
	case r.BlobURL != "" && !r.BlobOK:
		return r.BlobError
	default:
		return ""
	}
}

// ProbeImage performs a registry-side manifest-then-blob probe for ref. A
// non-nil error means the reference itself was unparseable (e.g. empty or
// digest-only); all other failure shapes are reflected in the returned
// ImageProbeResult fields, so callers should inspect the result for HTTP/TCP
// failures rather than relying solely on the error return.
//
// If client is nil, http.DefaultClient is used. Callers that need a timeout
// should supply a context with a deadline or a client with Timeout set.
//
// This function is the single public entry point for cross-package consumers
// (e.g. pkg/oasis's fast-fail image-pull watcher). The internal probe
// machinery is intentionally not exported.
func ProbeImage(ctx context.Context, client *http.Client, ref string) (ImageProbeResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	p, err := probeImagePull(ctx, client, ref)
	if err != nil {
		return ImageProbeResult{}, err
	}
	return ImageProbeResult{
		ManifestURL:     p.ManifestURL,
		ManifestOK:      p.ManifestOK,
		ManifestError:   p.ManifestErr,
		PerArchURL:      p.PerArchURL,
		PerArchPlatform: p.PerArchPlatform,
		PerArchError:    p.PerArchErr,
		BlobURL:         p.BlobURL,
		BlobOK:          p.BlobOK,
		BlobError:       p.BlobErr,
		BlobErrorKind:   p.BlobErrKind,
	}, nil
}
