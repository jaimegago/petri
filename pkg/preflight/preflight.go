// Package preflight runs substrate-readiness checks before an OASIS evaluation
// is started. It verifies that the configured kubeconfig is parseable, the
// cluster is reachable, the kubeconfig identity has the RBAC needed to manage
// namespaces, the default OCI images are pullable from this host, and (when
// configured) the audit log path is writable.
//
// The package is the single source of truth for substrate-readiness checks in
// petri. Both `petri verify` and `petri serve --verify` call Run and render
// the same Report. New ad-hoc reachability probes elsewhere in the codebase
// should be migrated here over time.
package preflight

import (
	"context"
	"net/http"
	"time"
)

// Status is the outcome of a single Check.
type Status string

// Status values written into the Report. They are stable strings — JSON
// consumers in CI may match on them.
const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Check is the result of running one preflight verification.
type Check struct {
	// Name is a short, stable identifier (e.g. "kubeconfig").
	Name string `json:"name"`
	// Title is a human-readable description shown in the rendered report.
	Title string `json:"title"`
	// Status is pass/fail/skip.
	Status Status `json:"status"`
	// Duration is wall-clock time spent on this check.
	Duration time.Duration `json:"duration"`
	// Summary is a one-line outcome message (always set).
	Summary string `json:"summary,omitempty"`
	// Diagnostic is a longer explanation, multi-line allowed. Set on failures.
	Diagnostic string `json:"diagnostic,omitempty"`
	// NextSteps is an actionable hint shown after a failed check.
	NextSteps string `json:"next_steps,omitempty"`
	// Platform, when set, is the resolved per-arch platform (e.g.
	// "linux/amd64") for a multi-arch image probe. Empty for non-image
	// checks and for single-arch images.
	Platform string `json:"platform,omitempty"`
}

// Report is the result of one Run. It carries every Check the run executed
// plus aggregate timing and pass/fail status.
type Report struct {
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	Deep      bool          `json:"deep"`
	Passed    bool          `json:"passed"`
	Checks    []Check       `json:"checks"`
}

// Failed reports whether any individual Check failed.
func (r *Report) Failed() bool { return !r.Passed }

// Options drives a single Run. All fields are optional — Run picks safe
// defaults when zero-valued. Tests inject KubeClient and HTTPClient to avoid
// real cluster / network IO.
type Options struct {
	// KubeconfigPath is the path to the kubeconfig file. Empty means use the
	// usual loading rules (KUBECONFIG env, then ~/.kube/config).
	KubeconfigPath string

	// AuditLogPath, when non-empty, triggers the audit-log writability check.
	AuditLogPath string

	// DefaultImage is the OASIS default Pod/Deployment image to verify. This
	// is normally cfg.OASIS.DefaultImage.
	DefaultImage string

	// UtilImage is the internal busybox-equivalent used by builders for
	// unhealthy state. Normally registry.k8s.io/e2e-test-images/busybox:...
	UtilImage string

	// Deep enables the cluster-side image pull test. Costs ~30-60s per image.
	Deep bool

	// PerCheckTimeout caps a single check. Default 5s.
	PerCheckTimeout time.Duration

	// DeepPullTimeout caps the wait for a pull-test pod to become Running.
	// Default 60s.
	DeepPullTimeout time.Duration

	// HTTPClient is used for registry HEAD requests. nil → http.DefaultClient
	// with a sensible timeout.
	HTTPClient *http.Client

	// KubeClient is the cluster client used by all kube-touching checks.
	// nil → built from KubeconfigPath via newClientGoKubeClient. Tests pass a
	// fake.
	KubeClient KubeClient

	// Now returns the current time. Tests inject a fake clock; nil → time.Now.
	Now func() time.Time
}

// defaultedOptions returns a copy of opts with zero fields filled with safe
// defaults. The original is not mutated.
func (o Options) defaulted() Options {
	if o.PerCheckTimeout == 0 {
		o.PerCheckTimeout = 5 * time.Second
	}
	if o.DeepPullTimeout == 0 {
		o.DeepPullTimeout = 60 * time.Second
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: o.PerCheckTimeout}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Run executes all preflight checks in the canonical order and returns a
// Report. Run only returns a Go error for catastrophic failure (e.g. cannot
// instantiate a kube client even from a parseable kubeconfig path passed by
// the caller); individual check failures are reflected on the Report.
func Run(ctx context.Context, opts Options) (*Report, error) {
	opts = opts.defaulted()
	report := &Report{
		StartedAt: opts.Now(),
		Deep:      opts.Deep,
	}
	runStart := opts.Now()

	// Order matters: each check assumes earlier ones passed. A failing
	// kubeconfig parse short-circuits server reachability and RBAC. Image
	// checks are normally independent of the cluster checks (registry-side
	// mode) — except when the kubeconfig FILE itself is missing, in which
	// case the user almost certainly typo'd a flag and probing registries
	// gives them nothing useful.
	kubecfg, kubeCheck, kubeconfigFileMissing := runKubeconfigCheck(ctx, opts)
	report.Checks = append(report.Checks, kubeCheck)

	// Build (or reuse) a KubeClient only if kubeconfig parsed.
	var kc KubeClient
	if kubeCheck.Status == StatusPass {
		if opts.KubeClient != nil {
			kc = opts.KubeClient
		} else {
			c, err := newClientGoKubeClient(kubecfg)
			if err != nil {
				// Catastrophic — kubeconfig parsed but client construction
				// failed. Surface as a Run error: we can't proceed.
				return nil, err
			}
			kc = c
		}
	}

	report.Checks = append(report.Checks, runServerReachableCheck(ctx, opts, kc))
	report.Checks = append(report.Checks, runRBACCheck(ctx, opts, kc))

	// Image and audit checks: when the kubeconfig file is missing we skip
	// them with a distinct reason (the run is almost certainly a typo).
	// When the kubeconfig parsed but the cluster turned out unreachable, we
	// still run them because registry/disk reachability is genuinely
	// independent of cluster health.
	const fileMissingReason = "skipped: kubeconfig file does not exist"
	for _, ic := range imageChecksFor(opts) {
		if kubeconfigFileMissing {
			report.Checks = append(report.Checks, skippedCheck(ic.Name, ic.Title, fileMissingReason))
			continue
		}
		report.Checks = append(report.Checks, runImageCheck(ctx, opts, ic))
	}

	if kubeconfigFileMissing {
		report.Checks = append(report.Checks, skippedCheck("audit_log_path", "Audit log path writable", fileMissingReason))
	} else {
		report.Checks = append(report.Checks, runAuditLogPathCheck(ctx, opts))
	}

	// Deep mode: cluster-side pull test, only if cluster is reachable.
	if opts.Deep && kc != nil {
		for _, ic := range imageChecksFor(opts) {
			report.Checks = append(report.Checks, runDeepPullCheck(ctx, opts, kc, ic))
		}
	}

	report.Duration = opts.Now().Sub(runStart)
	report.Passed = !anyFailed(report.Checks)
	return report, nil
}

// anyFailed returns true if any Check has Status fail.
func anyFailed(cs []Check) bool {
	for _, c := range cs {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}
