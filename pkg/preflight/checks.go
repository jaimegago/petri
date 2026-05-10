package preflight

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/rest"
)

// Each runXxxCheck function builds a single Check, taking responsibility for
// timing it and writing a useful Summary/Diagnostic/NextSteps. Run wires
// them together in order.
//
// All check functions tolerate a nil KubeClient: when the kubeconfig check
// failed and we never built a client, downstream cluster checks are recorded
// as Skip (with an explanation) rather than crashing.
//
// All runXxxCheck functions use NAMED return values for the Check (and any
// other returned values they mutate in defers). This is deliberate: each
// function has a `defer func() { c.Duration = ... }()` that needs to write
// to the same Check the caller will receive. With an unnamed return like
// `return c`, Go evaluates `c` at the return statement — the deferred
// assignment then writes into a copy that nobody reads, and the rendered
// report shows `(0µs)` for every check. Do not "simplify" these signatures
// to unnamed returns; ADR 0006 captures the surrounding context for why
// per-check durations matter.

// runKubeconfigCheck verifies the kubeconfig at opts.KubeconfigPath exists
// and parses. It returns the parsed *rest.Config so the caller can construct
// a KubeClient without re-loading.
//
// fileMissing is true only when the kubeconfig path was supplied but the file
// does not exist. The caller uses this to short-circuit downstream checks
// (registry probes, audit-log writability) that have no value when the user
// almost certainly typo'd the path. Other failure shapes (parse error,
// unreachable server later) leave fileMissing false so registry checks still
// run independently.
//
// Named return values are required so the deferred Duration assignment
// reaches the caller — `return X, c` would otherwise copy `c` before the
// defer fires, leaving Duration zero in the report.
func runKubeconfigCheck(_ context.Context, opts Options) (cfg *rest.Config, c Check, fileMissing bool) {
	c = Check{Name: "kubeconfig", Title: "Kubeconfig readable and parseable"}
	start := opts.Now()
	defer func() { c.Duration = opts.Now().Sub(start) }()

	// When the caller passed a KubeClient directly (tests), we treat the
	// kubeconfig step as passing — the client is already wired.
	if opts.KubeClient != nil {
		c.Status = StatusPass
		c.Summary = "kubeconfig provided by caller"
		return nil, c, false
	}

	if opts.KubeconfigPath != "" {
		if _, err := os.Stat(opts.KubeconfigPath); err != nil {
			c.Status = StatusFail
			c.Summary = fmt.Sprintf("kubeconfig at %s: %v", opts.KubeconfigPath, err)
			c.NextSteps = "Run `petri create` to provision a lab, or set --kubeconfig to an existing config."
			return nil, c, errors.Is(err, fs.ErrNotExist)
		}
	}

	cfg, err := loadKubeconfig(opts.KubeconfigPath)
	if err != nil {
		c.Status = StatusFail
		c.Summary = "kubeconfig failed to parse"
		c.Diagnostic = err.Error()
		c.NextSteps = "Inspect the kubeconfig with `kubectl config view --kubeconfig=<path>` and fix any malformed fields."
		return nil, c, false
	}

	c.Status = StatusPass
	host := cfg.Host
	if host == "" {
		host = "(default context)"
	}
	c.Summary = fmt.Sprintf("kubeconfig parsed (server: %s)", host)
	return cfg, c, false
}

// runServerReachableCheck calls ServerVersion. A successful call confirms
// network reachability, TLS trust, and basic authentication.
func runServerReachableCheck(ctx context.Context, opts Options, kc KubeClient) (c Check) {
	c = Check{Name: "cluster_reachable", Title: "Cluster reachable (ServerVersion)"}
	start := opts.Now()
	defer func() { c.Duration = opts.Now().Sub(start) }()

	if kc == nil {
		c.Status = StatusSkip
		c.Summary = "skipped: kubeconfig check did not pass"
		return c
	}

	cctx, cancel := context.WithTimeout(ctx, opts.PerCheckTimeout)
	defer cancel()
	v, err := kc.ServerVersion(cctx)
	if err != nil {
		c.Status = StatusFail
		kind, summary := classifyKubeError(err)
		c.Summary = summary
		c.Diagnostic = err.Error()
		switch kind {
		case errKindNetwork:
			c.NextSteps = "Check the API server URL in your kubeconfig and confirm the cluster is running (`kind get clusters` for local labs)."
		case errKindTLS:
			c.NextSteps = "The kubeconfig's certificate-authority no longer matches the cluster. Re-export the kubeconfig from the lab (e.g. `kind export kubeconfig --name <lab>`)."
		case errKindAuth:
			c.NextSteps = "The kubeconfig user is not authorized. Verify the credentials and current context with `kubectl config current-context`."
		default:
			c.NextSteps = "Run `kubectl version --kubeconfig=<path>` to reproduce and inspect the underlying error."
		}
		return c
	}
	c.Status = StatusPass
	c.Summary = fmt.Sprintf("server version %s", v)
	return c
}

// runRBACCheck confirms the kubeconfig identity can create and delete
// namespaces. We use SelfSubjectAccessReview rather than the create-then-
// delete pattern (which is racy and leaks on mid-flight failure).
func runRBACCheck(ctx context.Context, opts Options, kc KubeClient) (c Check) {
	c = Check{Name: "rbac", Title: "RBAC: can create + delete namespaces"}
	start := opts.Now()
	defer func() { c.Duration = opts.Now().Sub(start) }()

	if kc == nil {
		c.Status = StatusSkip
		c.Summary = "skipped: kubeconfig check did not pass"
		return c
	}

	verbs := []string{"create", "delete"}
	for _, verb := range verbs {
		cctx, cancel := context.WithTimeout(ctx, opts.PerCheckTimeout)
		allowed, reason, err := kc.CanI(cctx, verb, "namespaces")
		cancel()
		if err != nil {
			c.Status = StatusFail
			c.Summary = fmt.Sprintf("SelfSubjectAccessReview failed: %v", err)
			c.NextSteps = "The cluster rejected the authorization check. Verify the kubeconfig identity has at least authorization-api access."
			return c
		}
		if !allowed {
			c.Status = StatusFail
			c.Summary = fmt.Sprintf("not authorized to %s namespaces", verb)
			if reason != "" {
				c.Diagnostic = "API server reason: " + reason
			}
			c.NextSteps = fmt.Sprintf("Bind a ClusterRole with %q on namespaces to the kubeconfig user, or switch to an admin context.", verb)
			return c
		}
	}
	c.Status = StatusPass
	c.Summary = "create + delete on namespaces both allowed"
	return c
}

// skippedCheck builds a synthetic Skip Check with the given reason. Used by
// Run to short-circuit downstream checks (e.g. when the kubeconfig file is
// missing) without invoking each check's runner.
func skippedCheck(name, title, reason string) Check {
	return Check{Name: name, Title: title, Status: StatusSkip, Summary: reason}
}

// imageCheckSpec describes one image to verify.
type imageCheckSpec struct {
	// Name is the stable Check.Name suffix (e.g. "default_image").
	Name string
	// Title is the human description.
	Title string
	// Image is the OCI reference to probe.
	Image string
	// Role is "default" or "util" — used in messages and the deep mode pod
	// name.
	Role string
}

// imageChecksFor returns the image checks to run, in stable order.
func imageChecksFor(opts Options) []imageCheckSpec {
	specs := []imageCheckSpec{}
	if opts.DefaultImage != "" {
		specs = append(specs, imageCheckSpec{
			Name:  "image_default",
			Title: "Default OASIS image is pullable (registry-side)",
			Image: opts.DefaultImage,
			Role:  "default",
		})
	}
	if opts.UtilImage != "" {
		specs = append(specs, imageCheckSpec{
			Name:  "image_util",
			Title: "Internal util image is pullable (registry-side)",
			Image: opts.UtilImage,
			Role:  "util",
		})
	}
	return specs
}

// runImageCheck does the host-side registry probe for one image.
func runImageCheck(ctx context.Context, opts Options, spec imageCheckSpec) (c Check) {
	c = Check{Name: spec.Name, Title: spec.Title}
	start := opts.Now()
	defer func() { c.Duration = opts.Now().Sub(start) }()

	cctx, cancel := context.WithTimeout(ctx, opts.PerCheckTimeout*2)
	defer cancel()

	probe, err := probeImagePull(cctx, opts.HTTPClient, spec.Image)
	if err != nil {
		c.Status = StatusFail
		c.Summary = fmt.Sprintf("image reference invalid: %v", err)
		c.NextSteps = fmt.Sprintf("Set %s to a valid registry/repo:tag reference.", configKeyForRole(spec.Role))
		return c
	}

	if !probe.ManifestOK {
		c.Status = StatusFail
		c.Summary = fmt.Sprintf("manifest fetch failed for %s", spec.Image)
		c.Diagnostic = fmt.Sprintf("HEAD/GET %s\n  error: %s", probe.ManifestURL, probe.ManifestErr)
		c.NextSteps = fmt.Sprintf("Confirm the image exists in the registry (`docker pull %s`) and that DNS for the registry host resolves.", spec.Image)
		return c
	}

	if probe.PerArchErr != "" {
		c.Status = StatusFail
		c.Summary = fmt.Sprintf("could not resolve per-arch manifest for %s", spec.Image)
		diag := fmt.Sprintf("manifest list reachable: %s\n  resolution error: %s",
			probe.ManifestURL, probe.PerArchErr)
		if probe.PerArchURL != "" {
			diag += fmt.Sprintf("\n  attempted per-arch manifest: %s", probe.PerArchURL)
		}
		c.Diagnostic = diag
		c.NextSteps = "Inspect the manifest list with `crane manifest " + spec.Image + "` and confirm a linux/amd64 entry exists."
		return c
	}

	if probe.BlobURL != "" && !probe.BlobOK {
		c.Status = StatusFail
		switch probe.BlobErrKind {
		case blobErrKindHTTP:
			// The blob HEAD reached the registry and got a 4xx/5xx. That
			// almost always means we asked for a digest that wasn't a blob
			// (probe bug). It is NOT a CDN/network problem — do not show R2
			// messaging here.
			c.Summary = fmt.Sprintf("image pull check failed: blob HEAD returned an HTTP error for %s", spec.Image)
			c.Diagnostic = strings.TrimSpace(fmt.Sprintf(`%s
manifest endpoint: %s (OK)
blob endpoint:     %s
blob error:        %s

The blob HEAD returned an HTTP error. The probe could not locate a valid
blob digest in the manifest — most likely a petri bug, not a CDN/network
problem. Please report this with the image reference and the manifest
output (`+"`crane manifest <image>`"+`).`,
				spec.Image, probe.ManifestURL, probe.BlobURL, probe.BlobErr))
			c.NextSteps = "Capture `crane manifest " + spec.Image + "` and file an issue against petri."
		default:
			// blobErrKindTCP (or unset, e.g. legacy paths): the connection
			// itself failed before the registry could respond. This is the
			// canonical R2/CDN-block case.
			c.Summary = "image pull check failed: manifest reachable but blob fetch failed"
			c.Diagnostic = strings.TrimSpace(fmt.Sprintf(`%s
manifest endpoint: %s (OK)
blob endpoint:     %s
blob error:        %s

This commonly indicates an upstream block on the registry's CDN. Docker
Hub blobs are served from Cloudflare R2 (172.64.0.0/13), which is null-
routed by some ISPs, corporate networks, and mobile carriers. If the
manifest works but the blob does not, the petri host cannot reach the
CDN even though it can reach the registry.`,
				spec.Image, probe.ManifestURL, probe.BlobURL, probe.BlobErr))
			c.NextSteps = "Test from the host: `curl -I " + probe.BlobURL + "`. " +
				"If it hangs, route 172.64.0.0/13 around the block, switch to a registry not backed by R2 (registry.k8s.io), or pre-pull the image from a working network."
		}
		return c
	}

	c.Status = StatusPass
	switch {
	case probe.BlobURL == "":
		c.Summary = fmt.Sprintf("manifest reachable for %s (no blob to verify)", spec.Image)
	case probe.PerArchURL != "":
		c.Summary = fmt.Sprintf("manifest list + per-arch (%s) + blob reachable for %s",
			probe.PerArchPlatform, spec.Image)
		c.Platform = probe.PerArchPlatform
	default:
		c.Summary = fmt.Sprintf("manifest + blob reachable for %s", spec.Image)
	}
	return c
}

// configKeyForRole returns the user-facing config key used in NextSteps.
func configKeyForRole(role string) string {
	switch role {
	case "default":
		return "oasis.default_image"
	case "util":
		return "the internal util image"
	}
	return "the image"
}

// runAuditLogPathCheck verifies the parent of the configured audit log path
// exists and is writable. It is skipped when no path is configured.
func runAuditLogPathCheck(_ context.Context, opts Options) (c Check) {
	c = Check{Name: "audit_log_path", Title: "Audit log path writable"}
	start := opts.Now()
	defer func() { c.Duration = opts.Now().Sub(start) }()

	if opts.AuditLogPath == "" {
		c.Status = StatusSkip
		c.Summary = "no audit_log_path configured"
		return c
	}

	dir := filepath.Dir(opts.AuditLogPath)
	if fi, err := os.Stat(dir); err != nil {
		c.Status = StatusFail
		c.Summary = fmt.Sprintf("audit log parent directory does not exist: %s", dir)
		c.Diagnostic = err.Error()
		c.NextSteps = fmt.Sprintf("Create the directory: `mkdir -p %s`.", dir)
		return c
	} else if !fi.IsDir() {
		c.Status = StatusFail
		c.Summary = fmt.Sprintf("audit log parent is not a directory: %s", dir)
		c.NextSteps = "Pick a different audit_log_path whose parent is a directory."
		return c
	}

	// Probe writability with a temp file in the parent.
	f, err := os.CreateTemp(dir, ".petri-verify-*")
	if err != nil {
		c.Status = StatusFail
		c.Summary = fmt.Sprintf("audit log parent directory not writable: %s", dir)
		c.Diagnostic = err.Error()
		c.NextSteps = fmt.Sprintf("Adjust permissions on %s so the current user can write to it.", dir)
		return c
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)

	c.Status = StatusPass
	c.Summary = fmt.Sprintf("parent directory %s writable", dir)
	return c
}

// runDeepPullCheck creates a throwaway namespace + pod that pulls the image
// on the cluster, waits for Running (or a definite pull failure), and
// cleans up. This catches cases where the petri host can reach the registry
// but cluster nodes cannot.
func runDeepPullCheck(ctx context.Context, opts Options, kc KubeClient, spec imageCheckSpec) (c Check) {
	c = Check{
		Name:  spec.Name + "_deep",
		Title: fmt.Sprintf("Deep: %s pulls on the cluster", spec.Role),
	}
	start := opts.Now()
	defer func() { c.Duration = opts.Now().Sub(start) }()

	if kc == nil {
		c.Status = StatusSkip
		c.Summary = "skipped: cluster not reachable"
		return c
	}

	ns := fmt.Sprintf("petri-verify-%d", time.Now().UnixNano())
	podName := "verify-" + spec.Role

	createCtx, cancel := context.WithTimeout(ctx, opts.PerCheckTimeout)
	if err := kc.CreateNamespace(createCtx, ns); err != nil {
		cancel()
		c.Status = StatusFail
		c.Summary = "failed to create verify namespace"
		c.Diagnostic = err.Error()
		c.NextSteps = "Cluster RBAC may have changed since the earlier RBAC check. Re-run `petri verify` and inspect the namespace creation error."
		return c
	}
	cancel()
	defer func() {
		// Best-effort cleanup with a fresh context — the original may be done.
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = kc.DeleteNamespace(dctx, ns)
	}()

	createCtx2, cancel2 := context.WithTimeout(ctx, opts.PerCheckTimeout)
	if err := kc.CreatePullPod(createCtx2, ns, podName, spec.Image); err != nil {
		cancel2()
		c.Status = StatusFail
		c.Summary = "failed to create verify pod"
		c.Diagnostic = err.Error()
		return c
	}
	cancel2()

	waitCtx, waitCancel := context.WithTimeout(ctx, opts.DeepPullTimeout)
	defer waitCancel()

	var lastStatus PodPullStatus
	err := pollUntil(waitCtx, time.Second, func() (bool, error) {
		s, err := kc.PodPullStatus(waitCtx, ns, podName)
		if err != nil {
			return false, err
		}
		lastStatus = s
		if s.Ready || s.Phase == "Running" {
			return true, nil
		}
		// Definitive pull failure — don't keep waiting.
		if s.Reason == "ImagePullBackOff" || s.Reason == "ErrImagePull" {
			return true, nil
		}
		if s.Phase == "Failed" {
			return true, nil
		}
		return false, nil
	})

	if err != nil {
		// Timeout or polling error.
		c.Status = StatusFail
		if errors.Is(err, context.DeadlineExceeded) {
			c.Summary = fmt.Sprintf("pod did not become Ready within %s", opts.DeepPullTimeout)
		} else {
			c.Summary = "polling pod status failed"
		}
		if lastStatus.Reason != "" || lastStatus.Message != "" {
			c.Diagnostic = fmt.Sprintf("last status: phase=%s reason=%s message=%s",
				lastStatus.Phase, lastStatus.Reason, lastStatus.Message)
		}
		c.NextSteps = "Run `kubectl describe pod -n " + ns + " " + podName + "` to see the kubelet's pull error before petri cleans up the namespace."
		return c
	}

	if lastStatus.Reason == "ImagePullBackOff" || lastStatus.Reason == "ErrImagePull" {
		c.Status = StatusFail
		c.Summary = fmt.Sprintf("kubelet could not pull %s", spec.Image)
		c.Diagnostic = fmt.Sprintf("phase=%s reason=%s message=%s",
			lastStatus.Phase, lastStatus.Reason, lastStatus.Message)
		c.NextSteps = "The host-side registry check passed but the cluster nodes cannot pull this image. Check kind/host network parity (HTTP proxy on host but not in kind, or vice versa)."
		return c
	}

	c.Status = StatusPass
	c.Summary = fmt.Sprintf("pulled and ran %s on the cluster", spec.Image)
	return c
}
