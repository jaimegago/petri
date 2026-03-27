package chaos

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/google/uuid"
)

// RunnerConfig holds all dependencies and settings required to create a ChaosRunner.
type RunnerConfig struct {
	// Profile describes which faults to inject and the timing policy.
	Profile ChaosProfile
	// Faults maps FaultType to its implementation. When nil, DefaultFaults() is used.
	// Callers may extend or replace this map to add custom fault types without
	// modifying existing code.
	Faults map[FaultType]Fault
	// Emitter receives every FaultEvent produced during the session.
	Emitter EventEmitter
	// Kube provides Kubernetes operations for fault execution.
	Kube KubeClient
	// Log is the structured logger. Fields "profile" and "fault_type" are added per operation.
	Log *slog.Logger
	// Seed initialises the random number generator for reproducible runs.
	// Zero means use a time-based seed (non-reproducible).
	Seed int64
}

// ChaosRunner executes faults randomly according to a ChaosProfile's probability
// distribution and timing constraints. Stop it by cancelling the context passed
// to Run. All fault events are emitted through the EventEmitter provided in RunnerConfig.
type ChaosRunner struct {
	profile ChaosProfile
	faults  map[FaultType]Fault
	emitter EventEmitter
	kube    KubeClient
	log     *slog.Logger
	rng     *rand.Rand
}

// NewRunner constructs a ChaosRunner, validating the profile and resolving all
// fault implementations. Returns an error if any FaultSpec references an unknown
// FaultType or has a probability outside [0.0, 1.0].
func NewRunner(cfg RunnerConfig) (*ChaosRunner, error) {
	if cfg.Faults == nil {
		cfg.Faults = DefaultFaults()
	}

	for _, spec := range cfg.Profile.Faults {
		if _, ok := cfg.Faults[spec.Type]; !ok {
			return nil, fmt.Errorf(
				"unknown fault type %q: register an implementation in RunnerConfig.Faults",
				spec.Type,
			)
		}
		if spec.Probability < 0 || spec.Probability > 1 {
			return nil, fmt.Errorf(
				"fault %q has invalid probability %f: must be in [0.0, 1.0]",
				spec.Type, spec.Probability,
			)
		}
	}

	if cfg.Profile.MinInterval <= 0 {
		cfg.Profile.MinInterval = 10 * time.Second
	}
	if cfg.Profile.MaxInterval <= cfg.Profile.MinInterval {
		cfg.Profile.MaxInterval = cfg.Profile.MinInterval * 2
	}

	seed := cfg.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}

	return &ChaosRunner{
		profile: cfg.Profile,
		faults:  cfg.Faults,
		emitter: cfg.Emitter,
		kube:    cfg.Kube,
		log:     cfg.Log,
		rng:     rand.New(rand.NewSource(seed)), //nolint:gosec // not cryptographic
	}, nil
}

// Run starts the chaos session and blocks until ctx is cancelled or, when
// ChaosProfile.Duration is non-zero, that duration elapses. It is safe to call
// Run in a goroutine. The returned error is always nil (context cancellation is
// treated as a normal session end, not an error).
func (r *ChaosRunner) Run(ctx context.Context) error {
	r.log.Info("chaos session started",
		"profile", r.profile.Name,
		"fault_specs", len(r.profile.Faults),
		"target_count", len(r.profile.Targets),
		"min_interval", r.profile.MinInterval,
		"max_interval", r.profile.MaxInterval,
		"duration", r.profile.Duration,
	)

	sessionCtx := ctx
	var sessionCancel context.CancelFunc
	if r.profile.Duration > 0 {
		sessionCtx, sessionCancel = context.WithTimeout(ctx, r.profile.Duration)
		defer sessionCancel()
	}

	for {
		interval := r.nextInterval()

		select {
		case <-sessionCtx.Done():
			r.log.Info("chaos session ended",
				"profile", r.profile.Name,
				"reason", sessionCtx.Err().Error(),
			)
			return nil
		case <-time.After(interval):
		}

		spec, ok := r.selectFault()
		if !ok {
			// No fault fired this round — all probability rolls failed.
			continue
		}

		target, ok := r.selectTarget()
		if !ok {
			r.log.Warn("no targets configured in chaos profile; skipping round",
				"profile", r.profile.Name,
			)
			continue
		}

		r.injectFault(sessionCtx, spec, target)
	}
}

// injectFault executes one fault and emits the resulting FaultEvent.
// It never panics; errors from fault execution are recorded in the event.
func (r *ChaosRunner) injectFault(ctx context.Context, spec FaultSpec, target TargetResource) {
	fault := r.faults[spec.Type]

	event := FaultEvent{
		ID:        uuid.New().String(),
		FaultType: spec.Type,
		Target:    target,
		StartedAt: time.Now(),
		Metadata:  make(map[string]string),
	}

	r.log.Info("injecting fault",
		"fault_type", string(spec.Type),
		"namespace", target.Namespace,
		"target", target.Name,
		"kind", target.Kind,
	)

	err := fault.Execute(ctx, r.kube, target, spec.Parameters)
	event.EndedAt = time.Now()

	if err != nil {
		event.Success = false
		event.Error = err.Error()
		r.log.Warn("fault injection failed",
			"fault_type", string(spec.Type),
			"namespace", target.Namespace,
			"target", target.Name,
			"error", err,
		)
	} else {
		event.Success = true
		r.log.Info("fault injection succeeded",
			"fault_type", string(spec.Type),
			"namespace", target.Namespace,
			"target", target.Name,
			"duration_ms", event.EndedAt.Sub(event.StartedAt).Milliseconds(),
		)
	}

	r.emitter.Emit(event)
}

// selectFault picks a FaultSpec from the profile's pool using independent
// probability rolls. Each spec's Probability is tested independently; the first
// spec whose roll passes is returned. Specs are shuffled before rolling to avoid
// always favouring earlier entries. Returns false when all rolls fail.
func (r *ChaosRunner) selectFault() (FaultSpec, bool) {
	specs := make([]FaultSpec, len(r.profile.Faults))
	copy(specs, r.profile.Faults)
	r.rng.Shuffle(len(specs), func(i, j int) { specs[i], specs[j] = specs[j], specs[i] })

	for _, spec := range specs {
		if r.rng.Float64() <= spec.Probability {
			return spec, true
		}
	}
	return FaultSpec{}, false
}

// selectTarget picks a random TargetResource from the profile's target pool.
// Returns false when the pool is empty.
func (r *ChaosRunner) selectTarget() (TargetResource, bool) {
	if len(r.profile.Targets) == 0 {
		return TargetResource{}, false
	}
	return r.profile.Targets[r.rng.Intn(len(r.profile.Targets))], true
}

// nextInterval computes a random wait duration uniformly distributed between
// MinInterval and MaxInterval.
func (r *ChaosRunner) nextInterval() time.Duration {
	spread := r.profile.MaxInterval - r.profile.MinInterval
	if spread <= 0 {
		return r.profile.MinInterval
	}
	return r.profile.MinInterval + time.Duration(r.rng.Int63n(int64(spread)))
}
