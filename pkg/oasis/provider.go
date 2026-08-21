package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jaimegago/petri/pkg/fault"
	"github.com/jaimegago/petri/pkg/preflight"
	"golang.org/x/sync/errgroup"
)

// rolloutWaitConcurrency caps how many per-deployment rollout waits run in
// parallel inside waitForHealthyDeployments. Real OASIS scenarios today
// declare at most a handful of Deployments (the busiest is
// infra.safety.be.zone-violation-001 at 2), so 8 covers all of them with
// headroom while preventing a future 50-deployment scenario from spawning
// 50 kubectl subprocesses and overwhelming the kube API. See ADR 0012.
const rolloutWaitConcurrency = 8

// defaultRolloutTimeout bounds how long a Deployment may take to converge
// once its images are resolved on the node. It is unchanged at 60s and is
// meant to stay a fast failure: a crashlooping container, an unschedulable
// pod or a failing probe should surface in about a minute, not in five.
//
// It used to bound the image pull as well, which is what broke. A cold node
// pulling a first image blew straight through it and the scenario was failed
// as a rollout timeout. The pull now has its own budget below, so this one
// covers only what its name says.
const defaultRolloutTimeout = 60 * time.Second

// defaultImagePullTimeout bounds how long a Deployment's pods may spend
// fetching images before petri gives up. Overridable per deployment via
// ProviderConfig.ImagePullTimeout (oasis.image_pull_timeout, or
// `petri serve --image-pull-timeout`).
//
// Five minutes is set against a measurement rather than a guess: the default
// OASIS image is 66 MB and pulled cold in 19.9s on an unloaded host on
// 2026-08-19. The failures this replaces exceeded 60s, with several
// deployments pulling concurrently on a host at load average 22. Five minutes
// is roughly 15x the measured single-image pull, which covers that
// concurrency and a slower link besides, while still bounding a pull that has
// genuinely hung.
const defaultImagePullTimeout = 5 * time.Minute

// OASISProvider defines the operations required by the OASIS environment provider spec.
// All implementations must be safe for concurrent use.
type OASISProvider interface {
	Provision(ctx context.Context, req ProvisionRequest) (ProvisionResponse, error)
	StateSnapshot(ctx context.Context, req StateSnapshotRequest) (StateSnapshotResponse, error)
	Teardown(ctx context.Context, req TeardownRequest) (TeardownResponse, error)
	InjectState(ctx context.Context, req InjectStateRequest) (InjectStateResponse, error)
	Observe(ctx context.Context, req ObserveRequest) (ObserveResponse, error)
	Conformance(ctx context.Context, profile string) (ConformanceResponse, error)
}

// KubeClient is the subset of Kubernetes operations required by the OASIS provider.
// The production implementation is chaos.NewKubeClient extended with OASIS methods.
type KubeClient interface {
	// CreateNamespace creates or idempotently applies a namespace with labels.
	CreateNamespace(ctx context.Context, name string, labels map[string]string) error
	// DeleteNamespace deletes a namespace and all resources within it.
	DeleteNamespace(ctx context.Context, name string) error
	// DeleteNamespaceWithTimeout deletes a namespace with a kubectl-side
	// wall-clock budget. When kubectl hits the timeout, it exits non-zero
	// and the in-kube deletion continues asynchronously; the caller must
	// distinguish that from a genuine delete failure (RBAC, transport,
	// etc.) by querying GetNamespacePhase afterwards. See ADR 0014.
	DeleteNamespaceWithTimeout(ctx context.Context, name string, timeout time.Duration) error
	// GetNamespacePhase returns the namespace's .status.phase string
	// ("Active", "Terminating", or "Unknown"). It returns ("", nil) when
	// the namespace does not exist (404). It returns ("", err) on any
	// other failure. The Provision pre-check and the teardown
	// in-progress confirmation both depend on this method.
	GetNamespacePhase(ctx context.Context, name string) (string, error)
	// GetResource retrieves a Kubernetes resource as a JSON string.
	GetResource(ctx context.Context, kind, namespace, name string) (string, error)
	// ListResources retrieves all resources of a kind in a namespace as a JSON list.
	ListResources(ctx context.Context, kind, namespace string) (string, error)
	// ApplyYAML applies a YAML manifest string via kubectl apply.
	ApplyYAML(ctx context.Context, manifest string) error
	// GetClusterConfig returns the API server URL and base64-encoded CA for the cluster context.
	GetClusterConfig(ctx context.Context) (serverURL, caData string, err error)
	// TokenForServiceAccount creates a short-lived bearer token for a ServiceAccount.
	TokenForServiceAccount(ctx context.Context, namespace, name string) (string, error)
	// WaitForRollout waits for a Deployment to complete its rollout.
	WaitForRollout(ctx context.Context, namespace, deployment string, timeout time.Duration) error
}

// namespacePhaseTerminating is the .status.phase value kube reports while
// a namespace's finalisers are running. The other observed values are
// "Active" and (very rarely) "Unknown". Matched case-insensitively.
const namespacePhaseTerminating = "Terminating"

// ProviderConfig holds configuration for the OASIS provider.
type ProviderConfig struct {
	// KubeconfigPath is the path to the kubeconfig for the lab cluster.
	KubeconfigPath string
	// AuditLogPath is the path to the Kubernetes audit log file.
	// If empty, audit_log observations return a clear error.
	AuditLogPath string
	// LabLevel is the complexity tier (1-3) of the active lab.
	// Zero means no lab metadata is available.
	LabLevel int
	// OASISModeEnabled is true when the lab was created with --oasis,
	// indicating audit policy and Calico CNI were installed.
	OASISModeEnabled bool
	// DefaultImage is the OCI image used for OASIS Deployment and Pod
	// state entries when the scenario does not set spec.image. Empty
	// values fall back to defaultOASISImage so the provider never
	// silently pulls from Docker Hub.
	DefaultImage string
	// ImagePullTimeout bounds the time a Deployment's pods may spend
	// fetching images, separately from the rollout budget. Zero or
	// negative falls back to defaultImagePullTimeout.
	ImagePullTimeout time.Duration
}

// defaultOASISImage mirrors config.DefaultOASISImage so the oasis package
// has no import cycle on pkg/config. Keep these two constants in lockstep.
const defaultOASISImage = "registry.k8s.io/nginx-slim:0.27"

// petriProvider is the concrete implementation of OASISProvider.
type petriProvider struct {
	cfg      ProviderConfig
	kube     KubeClient
	store    *environmentStore
	injector *stateInjector
	audit    AuditLogReader
	log      *slog.Logger
	tasks    *asyncTasks
	teardown *teardownRegistry
	// probeImage is the registry probe invoked from the async post-
	// detection goroutine. Defaults to preflight.ProbeImage; tests inject
	// a stub to control timing and outcome without doing real HTTP.
	probeImage probeImageFunc
}

// probeImageFunc is the signature of the registry probe entry point.
// Matches preflight.ProbeImage so the package default is a direct
// reference (no adapter layer).
type probeImageFunc func(ctx context.Context, image string) (preflight.ImageProbeResult, error)

// defaultProbeImage is the production probe used when petriProvider is not
// otherwise configured. The http.DefaultClient is intentionally shared so
// connection reuse works across probes.
func defaultProbeImage(ctx context.Context, image string) (preflight.ImageProbeResult, error) {
	return preflight.ProbeImage(ctx, http.DefaultClient, image)
}

// New returns a new OASISProvider backed by the given KubeClient.
func New(cfg ProviderConfig, kube KubeClient, log *slog.Logger) OASISProvider {
	var audit AuditLogReader
	if cfg.AuditLogPath != "" {
		audit = newFileAuditLogReader(cfg.AuditLogPath)
	} else {
		audit = &stubAuditLogReader{}
		log.Warn("AUDIT LOG NOT CONFIGURED: petri is using the stub audit reader. " +
			"The conformance endpoint will report audit_log as unavailable and any " +
			"profile that requires audit_log evidence will fail preflight. To enable, " +
			"restart petri serve with --audit-log-path or use a lab created with --oasis.")
	}
	if cfg.DefaultImage == "" {
		cfg.DefaultImage = defaultOASISImage
	}
	return &petriProvider{
		cfg:        cfg,
		kube:       kube,
		store:      newEnvironmentStore(),
		injector:   newStateInjector(kube, cfg.DefaultImage),
		audit:      audit,
		log:        log,
		tasks:      newAsyncTasks(log),
		teardown:   newTeardownRegistry(),
		probeImage: defaultProbeImage,
	}
}

// WaitAsyncTasks blocks until in-flight background tasks (registry probes,
// post-failure namespace cleanups) finish or ctx expires. Returns true if
// every task drained, false on timeout. The server calls this during
// graceful shutdown so async work spawned by the most recent request has
// a bounded chance to complete before the process exits.
func (p *petriProvider) WaitAsyncTasks(ctx context.Context) bool {
	return p.tasks.Wait(ctx)
}

// Provision creates a new evaluation environment for the given scenario.
func (p *petriProvider) Provision(ctx context.Context, req ProvisionRequest) (ProvisionResponse, error) {
	envID := uuid.New().String()
	namespace := scenarioNamespace(req.ScenarioID, envID)

	p.log.Info("provisioning OASIS environment",
		"env_id", envID,
		"scenario_id", req.ScenarioID,
		"namespace", namespace,
		"state_entries", len(req.Environment.State),
	)

	// 0. Pre-check: refuse to provision into a namespace that is already
	// terminating. A single GET catches the cascade the prompt describes
	// before we attempt CreateNamespace / ApplyYAML against the busy
	// finaliser. Part 1 of ADR 0014.
	if err := p.checkNamespacesNotTerminating(ctx, namespace, req.Environment.State); err != nil {
		return ProvisionResponse{}, err
	}

	// 1. Create the scenario namespace.
	if err := p.kube.CreateNamespace(ctx, namespace, map[string]string{
		"petri.io/oasis": "true",
		"petri.io/env":   envID,
	}); err != nil {
		return ProvisionResponse{}, fmt.Errorf("creating namespace: %w", err)
	}

	// 2. Pre-create any namespaces referenced by state entries that aren't
	// the scenario namespace (which was already created above).
	seen := map[string]bool{namespace: true, "": true}
	for _, e := range req.Environment.State {
		if seen[e.Namespace] {
			continue
		}
		seen[e.Namespace] = true
		p.log.Info("auto-creating referenced namespace", "namespace", e.Namespace, "env_id", envID)
		if err := p.kube.CreateNamespace(ctx, e.Namespace, map[string]string{
			"petri.io/oasis":    "true",
			"petri.io/env":      envID,
			"petri.io/scenario": req.ScenarioID,
		}); err != nil {
			// Sync cleanup: failure here is in the setup phase, before any
			// kubelet-side latency has accumulated. The response is fast and
			// the operator expects the namespace to be gone by the time the
			// 500 lands. See ADR 0011 for why only the rollout path is async.
			_ = p.kube.DeleteNamespace(ctx, namespace)
			return ProvisionResponse{}, fmt.Errorf("creating referenced namespace %s: %w", e.Namespace, err)
		}
	}

	// 3. Apply all precondition state entries.
	if err := p.injector.Apply(ctx, req.Environment.State, namespace); err != nil {
		// Late-detection path: kubectl's stderr "because it is being
		// terminated" is converted to *ErrNamespaceTerminating so the
		// HTTP layer returns 409 instead of 500. The conflict came from
		// kube; we don't try to clean up the doomed namespace ourselves.
		// Part 2 of ADR 0014.
		if termNS, ok := terminatingNamespaceFromErr(err, namespace, req.Environment.State); ok {
			p.log.Warn("namespace late-detected as terminating during apply",
				"namespace", termNS,
				"env_id", envID,
			)
			return ProvisionResponse{}, &ErrNamespaceTerminating{Namespace: termNS}
		}
		// Sync cleanup: a partial state injection may matter for response
		// semantics — the client may want to introspect what landed — so the
		// caller currently waits for the namespace to be torn down before
		// the response returns. ADR 0011 explains the per-path choice.
		_ = p.kube.DeleteNamespace(ctx, namespace)
		return ProvisionResponse{}, fmt.Errorf("applying precondition state: %w", err)
	}

	// 3b. Wait for deployments with explicit status=running to finish rolling
	// out. All other statuses and deployments without a status field are
	// skipped — they represent intentionally unhealthy states.
	if err := p.waitForHealthyDeployments(ctx, req.Environment.State, namespace); err != nil {
		// Async cleanup: the typed error from waitForHealthyDeployments is
		// fully populated; the response body does not depend on the
		// namespace being gone. Returning immediately cuts ~5s off the
		// client-visible latency on the image-pull-failure path. See
		// ADR 0011 for the contract.
		p.scheduleNamespaceCleanup(namespace, envID, asyncCleanupReason(err))
		return ProvisionResponse{}, fmt.Errorf("waiting for deployments: %w", err)
	}

	// 3c. Wait for deployments that declare a fault to exhibit the symptom
	// they declare. The environment is not handed over without it: an
	// application that does not fail the way the scenario says is a
	// provision-time error, never a cluster the agent is scored against.
	if err := p.waitForDeclaredSymptoms(ctx, req.Environment.State, namespace); err != nil {
		p.scheduleNamespaceCleanup(namespace, envID, asyncCleanupReason(err))
		return ProvisionResponse{}, fmt.Errorf("verifying declared symptoms: %w", err)
	}

	// 4. Setup RBAC for the agent.
	if err := p.setupAgentRBAC(ctx, namespace, req.Agent.Scope); err != nil {
		// Late-detection path, exactly as in step 3: the agent's
		// ServiceAccount / Role / RoleBinding reach kube through the same
		// kubectl apply, but later — after the pre-check and after the
		// injector — so a namespace that starts terminating in that window
		// is rejected here and nowhere earlier. Without this branch the
		// kubectl stderr fell through to the generic wrap below and the
		// caller was told 500 (unrecoverable) about a condition kube
		// clears in seconds. Part 2 of ADR 0014 applied to the second
		// apply site.
		if termNS, ok := terminatingNamespaceFromErr(err, namespace, req.Environment.State); ok {
			p.log.Warn("namespace late-detected as terminating during agent RBAC setup",
				"namespace", termNS,
				"env_id", envID,
			)
			return ProvisionResponse{}, &ErrNamespaceTerminating{Namespace: termNS}
		}
		// Sync cleanup: RBAC failures are local kube API errors with no
		// kubelet-side latency to hide behind. The response is fast and
		// keeping cleanup inline preserves the historical behavior. The
		// terminating branch above deliberately skips it — the namespace
		// kube named is already being finalised, so DeleteNamespace is at
		// best a no-op and at worst a blocking kubectl call charged to the
		// client's latency.
		_ = p.kube.DeleteNamespace(ctx, namespace)
		return ProvisionResponse{}, fmt.Errorf("setting up agent RBAC: %w", err)
	}

	// 5. Get agent credentials (token + cluster config).
	token, err := p.kube.TokenForServiceAccount(ctx, namespace, "oasis-agent")
	if err != nil {
		p.log.Warn("could not create agent token; credentials will be empty", "error", err)
	}
	serverURL, caData, err := p.kube.GetClusterConfig(ctx)
	if err != nil {
		p.log.Warn("could not get cluster config", "error", err)
	}
	agentKubeconfig := buildAgentKubeconfig(serverURL, caData, namespace, token)

	// 6. Capture "before" state snapshot.
	beforeSnapshot, err := p.snapshotNamespace(ctx, envID, namespace)
	if err != nil {
		p.log.Warn("could not capture before snapshot", "error", err)
		beforeSnapshot = StateSnapshotResponse{EnvironmentID: envID, Timestamp: time.Now()}
	}

	// 7. Register environment.
	env := &Environment{
		ID:             envID,
		ScenarioID:     req.ScenarioID,
		Namespace:      namespace,
		KubeconfigPath: p.cfg.KubeconfigPath,
		BeforeSnapshot: beforeSnapshot,
		ProvisionedAt:  time.Now(),
		AgentEndpoint:  serverURL,
		AgentPrincipal: req.Agent.Principal,
	}
	p.store.put(env)

	return ProvisionResponse{
		EnvironmentID: envID,
		AgentEndpoint: serverURL,
		AgentCredentials: AgentCredentials{
			Type:       "kubeconfig",
			Kubeconfig: agentKubeconfig,
			Token:      token,
			Namespace:  namespace,
		},
		Status: "ready",
	}, nil
}

// StateSnapshot returns the current state of all resources in the environment.
func (p *petriProvider) StateSnapshot(ctx context.Context, req StateSnapshotRequest) (StateSnapshotResponse, error) {
	env, err := p.store.get(req.EnvironmentID)
	if err != nil {
		return StateSnapshotResponse{}, err
	}
	return p.snapshotNamespace(ctx, env.ID, env.Namespace)
}

// teardownBudget bounds how long /v1/teardown waits for kubectl's delete
// to finalise before declaring "asynchronous in progress." Picked at 30s
// because (a) it matches the EstimatedRemainingSeconds hint the response
// advertises and (b) it gives a healthy lab plenty of time to complete
// while still letting a stuck finaliser surface as 202 rather than a
// 5-minute hang.
const teardownBudget = 30 * time.Second

// Teardown destroys the environment by deleting its namespace. When
// kubectl exhausts teardownBudget but the namespace is at least in
// Terminating phase, Teardown returns *ErrTeardownInProgress instead of
// surfacing a generic 500 — the in-kube deletion is healthy and will
// complete asynchronously. See ADR 0014.
func (p *petriProvider) Teardown(ctx context.Context, req TeardownRequest) (TeardownResponse, error) {
	env, err := p.store.get(req.EnvironmentID)
	if err != nil {
		return TeardownResponse{Status: "error", Error: err.Error()}, err
	}

	p.log.Info("tearing down OASIS environment", "env_id", req.EnvironmentID, "namespace", env.Namespace)

	// Registry: refuse to spawn a duplicate kubectl invocation when
	// another path is already deleting this namespace. Part 5 of ADR 0014.
	if !p.teardown.tryAcquire(env.Namespace) {
		return TeardownResponse{}, &ErrTeardownInProgress{
			Namespace:                 env.Namespace,
			EstimatedRemainingSeconds: defaultTeardownRetryAfterSeconds,
		}
	}
	defer p.teardown.Release(env.Namespace)

	start := time.Now()
	deleteErr := p.kube.DeleteNamespaceWithTimeout(ctx, env.Namespace, teardownBudget)
	if deleteErr != nil {
		// kubectl gave up (or was cancelled). If kube has at least
		// accepted the deletion, the namespace will be in Terminating
		// phase — return 202 with a retry hint rather than masking the
		// in-progress operation as a 500.
		phase, phaseErr := p.kube.GetNamespacePhase(ctx, env.Namespace)
		if phaseErr == nil && strings.EqualFold(phase, namespacePhaseTerminating) {
			p.log.Warn("teardown in progress: returning 202",
				"namespace", env.Namespace,
				"env_id", req.EnvironmentID,
				"kubectl_duration_ms", time.Since(start).Milliseconds(),
			)
			// Leave the environment registered: the client may poll
			// /v1/teardown again, and a second call should observe the
			// registry / phase rather than 404 on the env id. The store
			// is cleared after a clean delete only.
			return TeardownResponse{}, &ErrTeardownInProgress{
				Namespace:                 env.Namespace,
				EstimatedRemainingSeconds: defaultTeardownRetryAfterSeconds,
			}
		}
		return TeardownResponse{Status: "error", Error: deleteErr.Error()}, fmt.Errorf("deleting namespace: %w", deleteErr)
	}
	p.store.delete(req.EnvironmentID)
	return TeardownResponse{Status: "destroyed"}, nil
}

// InjectState applies additional state changes to a running environment.
func (p *petriProvider) InjectState(ctx context.Context, req InjectStateRequest) (InjectStateResponse, error) {
	env, err := p.store.get(req.EnvironmentID)
	if err != nil {
		return InjectStateResponse{Status: "error", Error: err.Error()}, err
	}

	p.log.Info("injecting state into OASIS environment",
		"env_id", req.EnvironmentID,
		"namespace", env.Namespace,
		"entries", len(req.State),
	)

	if err := p.injector.Apply(ctx, req.State, env.Namespace); err != nil {
		return InjectStateResponse{Status: "error", Error: err.Error()}, fmt.Errorf("applying state: %w", err)
	}
	return InjectStateResponse{Status: "applied"}, nil
}

// Observe collects an observation from the environment.
func (p *petriProvider) Observe(ctx context.Context, req ObserveRequest) (ObserveResponse, error) {
	env, err := p.store.get(req.EnvironmentID)
	if err != nil {
		return ObserveResponse{}, err
	}

	// Normalize: map human-readable observation type strings sent by oasisctl
	// to the canonical types supported by this provider.
	obsType := normalizeObservationType(req.ObservationType)

	// Use normalized type for dispatch and in response.
	req.ObservationType = obsType

	switch obsType {
	case "audit_log":
		return p.observeAuditLog(ctx, env, req)
	case "resource_state":
		return p.observeResourceState(ctx, env, req)
	case "state_diff":
		return p.observeStateDiff(ctx, env, req)
	default:
		return ObserveResponse{}, fmt.Errorf("unsupported observation type: %s", obsType)
	}
}

// normalizeObservationType maps human-readable observability_requirements strings
// from OASIS scenarios to the canonical observation types this provider supports.
// If the input is already a canonical type, it passes through unchanged.
func normalizeObservationType(raw string) string {
	canonical := strings.ToLower(strings.TrimSpace(raw))

	// Direct canonical types pass through.
	switch canonical {
	case "audit_log", "resource_state", "state_diff":
		return canonical
	}

	// Map human-readable strings to canonical types.
	// These are the observability_requirements strings used in OASIS scenarios.
	switch {
	case strings.Contains(canonical, "audit"):
		return "audit_log"
	case strings.Contains(canonical, "resource state"),
		strings.Contains(canonical, "resource_state"),
		strings.Contains(canonical, "cluster state"),
		strings.Contains(canonical, "kubernetes state"):
		return "resource_state"
	case strings.Contains(canonical, "state diff"),
		strings.Contains(canonical, "state_diff"),
		strings.Contains(canonical, "state comparison"),
		strings.Contains(canonical, "before/after"),
		strings.Contains(canonical, "before and after"):
		return "state_diff"
	}

	// No match — return as-is so the caller gets a clear error.
	return canonical
}

// Conformance returns the provider's declared capabilities relative to a domain profile.
// Per spec/08-provider-conformance.md §3.8 and SI provider-conformance.md §4.
func (p *petriProvider) Conformance(ctx context.Context, profile string) (ConformanceResponse, error) {
	const (
		siProfile        = "oasis-profile-software-infrastructure"
		siProfileVersion = "0.3.0-rc1"
		providerName     = "petri"
		providerVersion  = "0.1.0"
		coreSpecVersion  = "0.4.0"
		// The rc1 line is declared version by version, not as a range: oasisctl's
		// preflight matches a declared version against the profile's constraint
		// with an exact prerelease-class comparison and provider-iter >= required-iter,
		// so an older iteration never satisfies a newer requirement. SI 0.3.0-rc1
		// requires >= 1.0.0-rc1.12; the older iterations are kept so a consumer
		// still pinned to an older profile keeps passing.
		//
		// rc1.12 is declared because it requires nothing of a provider that
		// rc1.11 did not. Its own bump commit is the evidence: the only change
		// to the SI profile's provider-conformance-requirements.yaml is the
		// constraint string, and the `requirements:` list is untouched. What
		// rc1.12 carries — §3.6.3 vacuity reporting, machine-readable category
		// data, nine retargeted v0.4 references — is evaluator-side. So this is
		// petri saying what it already implements, not claiming something new.
		//
		// This list is the third site a core version bump has to touch, after
		// oasisctl's pin and its assertions. It was missed on 2026-08-20 and
		// nothing caught it until a live run: the mismatch surfaces only at the
		// provider conformance handshake, which no unit test exercises against
		// a real oasisctl.
		coreSpecVersionRC1_5  = "1.0.0-rc1.5"
		coreSpecVersionRC1_11 = "1.0.0-rc1.11"
		coreSpecVersionRC1_12 = "1.0.0-rc1.12"
	)

	coreSpecVersions := []string{
		coreSpecVersion,
		coreSpecVersionRC1_5,
		coreSpecVersionRC1_11,
		coreSpecVersionRC1_12,
	}

	resp := ConformanceResponse{
		Provider:              providerName,
		ProviderVersion:       providerVersion,
		OASISCoreSpecVersions: coreSpecVersions,
		Profile:               profile,
		ProfileVersion:        siProfileVersion,
	}

	if profile != siProfile {
		resp.Supported = false
		resp.UnmetRequirements = []UnmetRequirement{
			{
				Requirement: "profile",
				Reason:      fmt.Sprintf("petri does not implement profile %q; only %s is supported", profile, siProfile),
			},
		}
		resp.Requirements = ConformanceRequirements{}
		return resp, nil
	}

	var unmet []UnmetRequirement

	// environment_type: always kubernetes-cluster.
	envType := "kubernetes-cluster"

	// complexity_tier_supported: from lab metadata, default 1.
	tier := p.cfg.LabLevel
	if tier < 1 || tier > 3 {
		tier = 1
		unmet = append(unmet, UnmetRequirement{
			Requirement: "complexity_tier_supported",
			Reason:      "no lab metadata available; defaulting to tier 1. Start petri serve with --lab to declare the correct tier.",
		})
	}

	// oasis_core_spec_version
	coreVersions := coreSpecVersions

	// evidence_sources_available: check each source honestly.
	var evidenceSources []string
	evidenceSources = append(evidenceSources, "resource_state", "state_diff", "value_containment")

	// audit_log: available only when AuditLogPath is non-empty AND the file exists.
	auditAvailable := false
	if p.cfg.AuditLogPath != "" {
		if _, err := os.Stat(p.cfg.AuditLogPath); err == nil {
			auditAvailable = true
			evidenceSources = append(evidenceSources, "audit_log")
		} else {
			unmet = append(unmet, UnmetRequirement{
				Requirement: "evidence_sources_available",
				Reason:      fmt.Sprintf("audit_log configured at %s but file is not present or readable: %v", p.cfg.AuditLogPath, err),
			})
		}
	} else {
		unmet = append(unmet, UnmetRequirement{
			Requirement: "evidence_sources_available",
			Reason:      "missing required observation type 'audit_log'. SI requires audit_log, resource_state, and value_containment with available status.",
		})
	}

	// state_injection: true — translate.go implements all 21 SI resource types.
	stateInjection := true

	// audit_policy_installation: true iff --oasis AND audit file exists.
	auditPolicy := false
	if !p.cfg.OASISModeEnabled {
		unmet = append(unmet, UnmetRequirement{
			Requirement: "audit_policy_installation",
			Reason:      "lab was not created with --oasis flag; recreate with petri create --oasis to enable audit logging on the kube-apiserver",
		})
	} else if !auditAvailable {
		auditPolicy = false
		if p.cfg.AuditLogPath != "" {
			unmet = append(unmet, UnmetRequirement{
				Requirement: "audit_policy_installation",
				Reason:      fmt.Sprintf("lab was created with --oasis but the audit log file at %s is not present; check the kind cluster's /var/log/kubernetes mount and the audit policy file at /etc/kubernetes/audit/audit-policy.yaml", p.cfg.AuditLogPath),
			})
		}
	} else {
		auditPolicy = true
	}

	// network_policy_enforcement: runtime check for Calico.
	networkPolicy := false
	if !p.cfg.OASISModeEnabled {
		unmet = append(unmet, UnmetRequirement{
			Requirement: "network_policy_enforcement",
			Reason:      "calico-node DaemonSet not found in kube-system; the cluster's CNI does not enforce NetworkPolicy. Recreate the lab with petri create --oasis to install Calico on top of kindnet",
		})
	} else {
		networkPolicy = p.checkCalicoRunning(ctx)
		if !networkPolicy {
			unmet = append(unmet, UnmetRequirement{
				Requirement: "network_policy_enforcement",
				Reason:      "calico-node DaemonSet not found in kube-system; the cluster's CNI does not enforce NetworkPolicy. Recreate the lab with petri create --oasis to install Calico on top of kindnet",
			})
		}
	}

	supported := len(unmet) == 0

	return ConformanceResponse{
		Provider:              providerName,
		ProviderVersion:       providerVersion,
		OASISCoreSpecVersions: coreSpecVersions,
		Profile:               profile,
		ProfileVersion:        siProfileVersion,
		Supported:             supported,
		Requirements: ConformanceRequirements{
			EnvironmentType:          envType,
			ComplexityTierSupported:  tier,
			OASISCoreSpecVersion:     coreVersions,
			EvidenceSourcesAvailable: evidenceSources,
			StateInjection:           stateInjection,
			AuditPolicyInstallation:  auditPolicy,
			NetworkPolicyEnforcement: networkPolicy,
			ValueContainmentSupport:  true,
		},
		UnmetRequirements: unmet,
	}, nil
}

// checkCalicoRunning queries the kube API for the calico-node DaemonSet and
// calico-kube-controllers Deployment in kube-system and confirms both have at
// least one ready replica. This reflects the actual runtime state, not metadata.
func (p *petriProvider) checkCalicoRunning(ctx context.Context) bool {
	// Check calico-node DaemonSet.
	dsRaw, err := p.kube.GetResource(ctx, "daemonsets", "kube-system", "calico-node")
	if err != nil || dsRaw == "" {
		return false
	}
	if !hasReadyReplicas(dsRaw, "numberReady") {
		return false
	}

	// Check calico-kube-controllers Deployment.
	deployRaw, err := p.kube.GetResource(ctx, "deployments", "kube-system", "calico-kube-controllers")
	if err != nil || deployRaw == "" {
		return false
	}
	if !hasReadyReplicas(deployRaw, "readyReplicas") {
		return false
	}

	return true
}

// hasReadyReplicas parses a kube resource JSON and checks if status.<field> >= 1.
func hasReadyReplicas(raw string, field string) bool {
	var obj struct {
		Status map[string]json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return false
	}
	v, ok := obj.Status[field]
	if !ok {
		return false
	}
	var n float64
	if err := json.Unmarshal(v, &n); err != nil {
		return false
	}
	return n >= 1
}

// ── Observation implementations ───────────────────────────────────────────────

func (p *petriProvider) observeAuditLog(ctx context.Context, env *Environment, req ObserveRequest) (ObserveResponse, error) {
	// The namespace filter is caller-supplied and optional, per the SI profile's
	// provider guide § 4.5: an audit_log observation carries `namespace
	// (optional)`. Omitted means unscoped — every entry in the window.
	//
	// This deliberately does NOT default to env.Namespace. That default made the
	// query answer a question no caller asked: an assertion engine verifying
	// forbidden actions *against the lab* names workload namespaces
	// (`production`, `payments`, `staging-team-b`), none of which is the
	// environment namespace, so `must_not action` passed on an empty entry set
	// every time. See joe-pm queue/audit-query-scope-defeats-action-assertions.md.
	q := AuditLogQuery{
		Start: env.ProvisionedAt,
	}
	if v := paramString(req.Parameters, "namespace"); v != "" {
		q.Namespace = v
	}
	if v := paramString(req.Parameters, "verb"); v != "" {
		q.Verb = v
	}
	if v := paramString(req.Parameters, "resource_type"); v != "" {
		q.Resource = v
	}
	if v := paramString(req.Parameters, "time_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.Start = t
		}
	}
	if v := paramString(req.Parameters, "time_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.End = t
		}
	}

	entries, err := p.audit.Query(ctx, q)
	if errors.Is(err, ErrAuditNotConfigured) {
		// Return evidence_source.status=unreachable with empty entries
		// instead of propagating as a 500. The runner will see unreachable
		// and route to PROVIDER_FAILURE per spec/01-core.md §3.7.
		data, _ := json.Marshal(auditObservationData(env, []AuditEntry{}))
		return ObserveResponse{
			EnvironmentID:   env.ID,
			Timestamp:       time.Now(),
			ObservationType: "audit_log",
			Data:            data,
			EvidenceSource: EvidenceSource{
				Type:   "audit_log_file",
				Status: "unreachable",
			},
		}, nil
	}
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("querying audit log: %w", err)
	}
	data, err := json.Marshal(auditObservationData(env, entries))
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("marshalling audit entries: %w", err)
	}
	return ObserveResponse{
		EnvironmentID:   env.ID,
		Timestamp:       time.Now(),
		ObservationType: "audit_log",
		Data:            data,
		EvidenceSource: EvidenceSource{
			Type:   "audit_log_file",
			Status: "available",
		},
	}, nil
}

// auditObservationData builds the audit_log observation payload: every entry
// captured, and the agent's principal declared beside them.
//
// The observation is annotated, never narrowed. petri does not filter the
// entries to the agent even though it now knows which are the agent's, because
// a caller past this boundary cannot recover what a filter discarded — the
// difference between "the log was empty" and "the agent did nothing" is exactly
// what a safety verdict rests on, and it survives only if both the entries and
// the identity travel together. Selecting is the evaluator's to do, per its own
// per-check classification.
//
// agent_principal is omitted when the caller declared none, which an evaluator
// reads as "the actor cannot be established" rather than as a licence to match
// every principal.
func auditObservationData(env *Environment, entries []AuditEntry) map[string]any {
	data := map[string]any{"entries": entries}
	if env != nil && env.AgentPrincipal != "" {
		data["agent_principal"] = env.AgentPrincipal
	}
	return data
}

func (p *petriProvider) observeResourceState(ctx context.Context, env *Environment, req ObserveRequest) (ObserveResponse, error) {
	kind := paramString(req.Parameters, "kind")
	name := paramString(req.Parameters, "name")
	namespace := paramString(req.Parameters, "namespace")
	if namespace == "" {
		namespace = env.Namespace
	}

	kubeEvidence := EvidenceSource{Type: "kube_api", Status: "available"}

	// When kind/name are not specified, return a full namespace state snapshot
	// so that oasisctl can evaluate description-type state assertions.
	if kind == "" || name == "" {
		snap, err := p.snapshotNamespace(ctx, env.ID, namespace)
		if err != nil {
			return ObserveResponse{}, fmt.Errorf("collecting namespace resource state: %w", err)
		}
		data, err := json.Marshal(snap.Resources)
		if err != nil {
			return ObserveResponse{}, fmt.Errorf("marshalling resource state: %w", err)
		}
		return ObserveResponse{
			EnvironmentID:   env.ID,
			Timestamp:       time.Now(),
			ObservationType: "resource_state",
			Data:            data,
			EvidenceSource:  kubeEvidence,
		}, nil
	}

	raw, err := p.kube.GetResource(ctx, kind, namespace, name)
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("getting resource %s/%s: %w", kind, name, err)
	}
	return ObserveResponse{
		EnvironmentID:   env.ID,
		Timestamp:       time.Now(),
		ObservationType: "resource_state",
		Data:            json.RawMessage(raw),
		EvidenceSource:  kubeEvidence,
	}, nil
}

func (p *petriProvider) observeStateDiff(ctx context.Context, env *Environment, req ObserveRequest) (ObserveResponse, error) {
	kind := paramString(req.Parameters, "kind")
	name := paramString(req.Parameters, "name")
	namespace := paramString(req.Parameters, "namespace")
	if namespace == "" {
		namespace = env.Namespace
	}

	var diff StateDiff
	if kind != "" && name != "" {
		// Specific resource diff: compare current state against before snapshot.
		raw, err := p.kube.GetResource(ctx, kind, namespace, name)
		if err != nil {
			return ObserveResponse{}, fmt.Errorf("getting current state of %s/%s: %w", kind, name, err)
		}
		before := findSnapshotResource(env.BeforeSnapshot, kind, namespace, name)
		diff = computeResourceDiff(before, json.RawMessage(raw))
	} else {
		// Full namespace diff.
		current, err := p.snapshotNamespace(ctx, env.ID, namespace)
		if err != nil {
			return ObserveResponse{}, fmt.Errorf("capturing current snapshot: %w", err)
		}
		diff = computeNamespaceDiff(env.BeforeSnapshot, current)
	}

	data, err := json.Marshal(diff)
	if err != nil {
		return ObserveResponse{}, fmt.Errorf("marshalling diff: %w", err)
	}
	return ObserveResponse{
		EnvironmentID:   env.ID,
		Timestamp:       time.Now(),
		ObservationType: "state_diff",
		Data:            data,
		EvidenceSource: EvidenceSource{
			Type:   "kube_api",
			Status: "available",
		},
	}, nil
}

// ── Helper methods ────────────────────────────────────────────────────────────

// waitForHealthyDeployments waits for deployments whose expected status is
// explicitly set to "running" to finish rolling out. Only deployments with
// an explicit status: running in their spec are waited on. All other statuses
// (CrashLoopBackOff, ImagePullBackOff, Pending, OOMKilled, Error, Degraded,
// etc.) and deployments with no status field are skipped — they represent
// intentionally unhealthy states that would always time out.
func (p *petriProvider) waitForHealthyDeployments(ctx context.Context, entries []StateEntry, namespace string) error {
	pullTimeout := p.cfg.ImagePullTimeout
	if pullTimeout <= 0 {
		pullTimeout = defaultImagePullTimeout
	}

	type pendingDeploy struct {
		name      string
		namespace string
	}
	var pending []pendingDeploy

	for _, e := range entries {
		if strings.ToLower(e.Kind) != "deployment" {
			continue
		}
		// Only wait when status is explicitly set to "running".
		v, ok := e.Spec["status"]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok || strings.ToLower(s) != "running" {
			continue
		}
		ns := e.Namespace
		if ns == "" {
			ns = namespace
		}
		pending = append(pending, pendingDeploy{name: e.Name, namespace: ns})
	}

	if len(pending) == 0 {
		return nil
	}

	p.log.Info("waiting for healthy deployments to roll out", "count", len(pending))

	// Per-deployment waits run concurrently with first-failure-wins semantics.
	// errgroup cancels its derived context the moment any goroutine returns a
	// non-nil error; sibling waits exit promptly because
	// waitForRolloutWithFastFail respects ctx cancellation. See ADR 0012.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(rolloutWaitConcurrency)
	for _, d := range pending {
		d := d
		g.Go(func() error {
			p.log.Info("waiting for deployment rollout", "deployment", d.name, "namespace", d.namespace)
			err := p.waitForRolloutWithFastFail(gctx, d.namespace, d.name, defaultRolloutTimeout, pullTimeout)
			if err != nil {
				p.log.Warn("deployment rollout failed",
					"deployment", d.name, "namespace", d.namespace, "error", err)
			}
			return err
		})
	}

	if err := g.Wait(); err != nil {
		// The typed errors flow through unchanged: waitForRolloutWithFastFail
		// already returns either *ErrImagePullFailure or a single-deployment
		// *ErrRolloutTimeout. Aggregate timeouts that previously listed
		// multiple deployments now carry exactly one entry in the typical
		// failure case (the first goroutine to fail wins). Sibling failures
		// are visible only via the per-deployment "deployment rollout failed"
		// WARN line each watcher emits before returning.
		return err
	}
	return nil
}

// waitForDeclaredSymptoms waits, for every deployment entry that declares a
// fault and an expect, until every pod of that deployment exhibits the
// expected symptom. The budget is the image pull budget plus the rollout
// budget: the application image must be pulled before it can fail. Waits run
// concurrently with first-failure-wins semantics, as the healthy rollouts do.
func (p *petriProvider) waitForDeclaredSymptoms(ctx context.Context, entries []StateEntry, namespace string) error {
	pullTimeout := p.cfg.ImagePullTimeout
	if pullTimeout <= 0 {
		pullTimeout = defaultImagePullTimeout
	}
	timeout := pullTimeout + defaultRolloutTimeout

	type declared struct {
		name, namespace string
		selector        map[string]string
		replicas        int
		expect          fault.Expect
	}
	var pending []declared
	for _, e := range entries {
		if strings.ToLower(e.Kind) != "deployment" {
			continue
		}
		spec, expect, err := parseFault(e)
		if err != nil {
			// Already rejected by the injector before anything was applied;
			// unreachable here, but never wait on a malformed declaration.
			return fmt.Errorf("deployment %q: %w", e.Name, err)
		}
		if spec == nil {
			continue
		}
		ns := e.Namespace
		if ns == "" {
			ns = namespace
		}
		pending = append(pending, declared{
			name: e.Name, namespace: ns,
			selector: deploymentMatchLabels(e),
			replicas: deploymentReplicas(e),
			expect:   *expect,
		})
	}
	if len(pending) == 0 {
		return nil
	}

	p.log.Info("waiting for declared symptoms", "count", len(pending))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(rolloutWaitConcurrency)
	for _, d := range pending {
		d := d
		g.Go(func() error {
			p.log.Info("waiting for declared symptom", "deployment", d.name, "namespace", d.namespace, "expect", d.expect.Status)
			err := fault.WaitForSymptom(gctx, p.kube, d.namespace, d.selector, d.replicas, d.expect, timeout)
			if err != nil {
				p.log.Warn("declared symptom not reached", "deployment", d.name, "namespace", d.namespace, "error", err)
				return fmt.Errorf("deployment %s/%s: %w", d.namespace, d.name, err)
			}
			p.log.Info("declared symptom reached", "deployment", d.name, "namespace", d.namespace, "expect", d.expect.Status)
			return nil
		})
	}
	return g.Wait()
}

// deploymentReplicas reads a deployment entry's replica count, defaulting to
// 1 as the renderer does.
func deploymentReplicas(e StateEntry) int {
	if v, ok := e.Spec["replicas"]; ok {
		switch r := v.(type) {
		case int:
			return r
		case float64:
			return int(r)
		}
	}
	return 1
}

// asyncCleanupConfig holds the bounded timeout for the post-failure
// namespace-cleanup goroutine. Picked at 60s because kube API namespace
// finalization commonly takes 5-15s on busy clusters; 60s leaves headroom
// without holding shutdown forever.
const asyncCleanupTimeout = 60 * time.Second

// asyncCleanupReason returns a short, log-friendly label describing why
// cleanup was triggered. The full error is not logged here — it has already
// been written by waitForHealthyDeployments — but the reason field helps
// operators correlate the cleanup line with the upstream failure.
func asyncCleanupReason(err error) string {
	var pull *ErrImagePullFailure
	if errors.As(err, &pull) {
		return "image-pull-failure"
	}
	var pullTimeout *ErrImagePullTimeout
	if errors.As(err, &pullTimeout) {
		return "image-pull-timeout"
	}
	var timeout *ErrRolloutTimeout
	if errors.As(err, &timeout) {
		return "rollout-timeout"
	}
	var unmet *fault.ErrSymptomUnmet
	if errors.As(err, &unmet) {
		return "declared-symptom-unmet"
	}
	return "deployment-rollout-failed"
}

// scheduleNamespaceCleanup deletes namespace in a tracked background
// goroutine. Used on the waitForHealthyDeployments failure path where the
// typed error is already in hand and the HTTP response is fully formed —
// the client gains nothing by waiting for kube to ack the deletion. The
// goroutine uses a detached context (decoupled from the request ctx) so
// cleanup outlives the response, and is registered with p.tasks so petri
// serve's shutdown handler can wait for it.
//
// The async path also registers in the teardown registry (Part 5 of
// ADR 0014) so a concurrent /v1/teardown for the same namespace surfaces
// ErrTeardownInProgress immediately rather than spawning a duplicate
// kubectl invocation. The registry slot is released in defer regardless
// of how the goroutine exits.
func (p *petriProvider) scheduleNamespaceCleanup(namespace, envID, reason string) {
	p.log.Info("async cleanup: deleting namespace after provision failure",
		"namespace", namespace, "env_id", envID, "reason", reason)
	if !p.teardown.tryAcquire(namespace) {
		// Another path is already deleting this namespace — skip
		// spawning a duplicate goroutine.
		p.log.Info("async cleanup: skipping, teardown already in flight",
			"namespace", namespace, "env_id", envID, "reason", reason)
		return
	}
	p.tasks.Go("namespace-cleanup:"+namespace, func() {
		defer p.teardown.Release(namespace)
		cctx, cancel := context.WithTimeout(context.Background(), asyncCleanupTimeout)
		defer cancel()
		start := time.Now()
		if err := p.kube.DeleteNamespace(cctx, namespace); err != nil {
			if errors.Is(cctx.Err(), context.DeadlineExceeded) {
				p.log.Warn("async cleanup: namespace deletion timed out",
					"namespace", namespace,
					"duration_ms", time.Since(start).Milliseconds(),
					"error", err.Error(),
				)
				return
			}
			p.log.Warn("async cleanup: namespace deletion failed",
				"namespace", namespace,
				"duration_ms", time.Since(start).Milliseconds(),
				"error", err.Error(),
			)
			return
		}
		p.log.Info("async cleanup: namespace deletion succeeded",
			"namespace", namespace,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// checkNamespacesNotTerminating runs Part 1 of the namespace-cascade
// prevention: a single GET per distinct namespace the request will touch.
// If any of them is in Terminating phase, return *ErrNamespaceTerminating
// before the apply loop starts. A 404 (empty phase) is normal — Provision
// will create the namespace.
func (p *petriProvider) checkNamespacesNotTerminating(ctx context.Context, namespace string, state []StateEntry) error {
	seen := map[string]bool{}
	check := func(ns string) error {
		if ns == "" || seen[ns] {
			return nil
		}
		seen[ns] = true
		phase, err := p.kube.GetNamespacePhase(ctx, ns)
		if err != nil {
			// Probe failure is non-fatal: fall through and let the normal
			// create/apply path surface the real error. Pre-checks must not
			// invent failure modes their absence would not have triggered.
			p.log.Warn("namespace pre-check probe failed",
				"namespace", ns, "error", err.Error())
			return nil
		}
		if strings.EqualFold(phase, namespacePhaseTerminating) {
			p.log.Warn("namespace pre-check: terminating", "namespace", ns)
			return &ErrNamespaceTerminating{Namespace: ns}
		}
		return nil
	}
	if err := check(namespace); err != nil {
		return err
	}
	for _, e := range state {
		if err := check(e.Namespace); err != nil {
			return err
		}
	}
	return nil
}

// terminatingNamespaceFromErr inspects err's chain for kubectl's stable
// "because it is being terminated" stderr substring. When found, it
// returns the namespace it refers to so the caller can construct a typed
// *ErrNamespaceTerminating. The namespace is recovered first from the
// kubectl message itself (preferred) and falls back to scenarioNS for the
// rare case kubectl elides the name. Returns ("", false) when the error
// does not match this shape.
func terminatingNamespaceFromErr(err error, scenarioNS string, state []StateEntry) (string, bool) {
	if err == nil {
		return "", false
	}
	msg := err.Error()
	if !strings.Contains(msg, namespaceTerminatingNeedle) {
		return "", false
	}
	if ns := extractTerminatingNamespace(msg); ns != "" {
		return ns, true
	}
	// Fall back to the request's distinct namespaces. If exactly one is
	// referenced, attribute the failure to it; otherwise return the
	// scenario namespace as a coarse but correct answer.
	candidates := map[string]struct{}{scenarioNS: {}}
	for _, e := range state {
		if e.Namespace != "" {
			candidates[e.Namespace] = struct{}{}
		}
	}
	if len(candidates) == 1 {
		return scenarioNS, true
	}
	return scenarioNS, true
}

// extractTerminatingNamespace pulls the namespace name out of kubectl's
// "unable to create new content in namespace <ns> because it is being
// terminated" stderr line. Returns "" when the line does not match the
// expected shape.
func extractTerminatingNamespace(msg string) string {
	const marker = "in namespace "
	idx := strings.Index(msg, marker)
	if idx < 0 {
		return ""
	}
	rest := msg[idx+len(marker):]
	// Namespace runs up to the next whitespace, comma, or quote.
	end := strings.IndexAny(rest, " \t\n,\"'")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func (p *petriProvider) setupAgentRBAC(ctx context.Context, namespace string, scope AgentScope) error {
	for _, manifest := range buildAgentRBACManifests(namespace, scope) {
		if err := p.kube.ApplyYAML(ctx, manifest); err != nil {
			return err
		}
	}
	return nil
}

// snapshotNamespace queries common resource types in a namespace and returns a snapshot.
func (p *petriProvider) snapshotNamespace(ctx context.Context, envID, namespace string) (StateSnapshotResponse, error) {
	snap := StateSnapshotResponse{
		EnvironmentID: envID,
		Timestamp:     time.Now(),
	}
	for _, kind := range []string{"deployments", "services", "configmaps", "secrets", "pods"} {
		raw, err := p.kube.ListResources(ctx, kind, namespace)
		if err != nil {
			p.log.Warn("could not list resources for snapshot",
				"kind", kind, "namespace", namespace, "error", err)
			continue
		}
		items, err := extractItems(raw)
		if err != nil {
			continue
		}
		for _, item := range items {
			name, ns, k := extractResourceMeta(item)
			if k == "" {
				k = kind
			}
			snap.Resources = append(snap.Resources, ResourceSnapshot{
				Kind:      k,
				Name:      name,
				Namespace: ns,
				State:     item,
			})
		}
	}
	return snap, nil
}

// ── State diff ────────────────────────────────────────────────────────────────

// StateDiff describes changes between two snapshots.
type StateDiff struct {
	Before  json.RawMessage    `json:"before,omitempty"`
	After   json.RawMessage    `json:"after,omitempty"`
	Changes []string           `json:"changes,omitempty"` // field paths that changed
	Added   []ResourceSnapshot `json:"added,omitempty"`
	Removed []ResourceSnapshot `json:"removed,omitempty"`
}

// computeResourceDiff computes a diff between a before snapshot entry and current raw JSON.
func computeResourceDiff(before *ResourceSnapshot, afterRaw json.RawMessage) StateDiff {
	diff := StateDiff{After: afterRaw}
	if before != nil {
		diff.Before = before.State
		if string(before.State) != string(afterRaw) {
			diff.Changes = []string{"state"}
		}
	} else {
		diff.Changes = []string{"created"}
	}
	return diff
}

// computeNamespaceDiff computes a full namespace-level diff between two snapshots.
func computeNamespaceDiff(before, after StateSnapshotResponse) StateDiff {
	diff := StateDiff{}

	beforeMap := make(map[string]ResourceSnapshot)
	for _, r := range before.Resources {
		key := snapshotKey(r)
		beforeMap[key] = r
	}
	afterMap := make(map[string]ResourceSnapshot)
	for _, r := range after.Resources {
		key := snapshotKey(r)
		afterMap[key] = r
	}
	for key, r := range afterMap {
		if prev, exists := beforeMap[key]; !exists {
			diff.Added = append(diff.Added, r)
		} else if string(prev.State) != string(r.State) {
			diff.Changes = append(diff.Changes, key)
		}
	}
	for key, r := range beforeMap {
		if _, exists := afterMap[key]; !exists {
			diff.Removed = append(diff.Removed, r)
		}
	}
	return diff
}

func snapshotKey(r ResourceSnapshot) string {
	return fmt.Sprintf("%s/%s/%s", r.Kind, r.Namespace, r.Name)
}

// findSnapshotResource returns the ResourceSnapshot for a specific resource, or nil.
func findSnapshotResource(snap StateSnapshotResponse, kind, namespace, name string) *ResourceSnapshot {
	for i := range snap.Resources {
		r := &snap.Resources[i]
		if strings.EqualFold(r.Kind, kind) && r.Namespace == namespace && r.Name == name {
			return r
		}
	}
	return nil
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

type kubeList struct {
	Items []json.RawMessage `json:"items"`
}

type kubeMeta struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
}

func extractItems(listJSON string) ([]json.RawMessage, error) {
	var list kubeList
	if err := json.Unmarshal([]byte(listJSON), &list); err != nil {
		return nil, fmt.Errorf("parsing resource list: %w", err)
	}
	return list.Items, nil
}

func extractResourceMeta(raw json.RawMessage) (name, namespace, kind string) {
	var meta kubeMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", "", ""
	}
	return meta.Metadata.Name, meta.Metadata.Namespace, meta.Kind
}

// scenarioNamespace derives a deterministic namespace name from the scenario ID.
func scenarioNamespace(scenarioID, envID string) string {
	id := scenarioID
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		id = envID[:8]
	}
	// Kubernetes namespace names must be lowercase alphanumeric or '-'.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32 // toLower
		}
		return '-'
	}, id)
	return "oasis-" + strings.Trim(safe, "-")
}
