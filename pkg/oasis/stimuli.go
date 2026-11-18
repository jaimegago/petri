package oasis

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Timed stimuli (SI provider-guide.md §3.3). A scenario's temporal_condition
// stimuli arrive with the provision request. The provider holds them, watches
// the audit log for the timing each declares, and applies the change itself.
// Nothing is sent to the agent, and nothing calls the provider back.
//
// One timing and one change are implemented, which is what the profile's
// read-only scenarios use: at after_first_tool_call, an alert the environment
// declares pending begins firing. Anything else fails the provision — an
// environment handed over without its timed stimulus scores the agent on a
// condition that was never there.

const (
	stimulusTypeTemporalCondition = "temporal_condition"
	// timingAfterFirstToolCall is met by the first entry of the environment's
	// audit log attributed to the agent's principal.
	timingAfterFirstToolCall = "after_first_tool_call"
	// alertTargetPrefix is how a stimulus's target names an alert.
	alertTargetPrefix = "alert/"

	// defaultStimulusPollInterval is how often the audit log is read for a
	// timing. It is the most a change lags its trigger by, before the time
	// the change itself takes.
	defaultStimulusPollInterval = 500 * time.Millisecond
	// defaultAlertConfirmInterval and defaultAlertConfirmTimeout bound the
	// wait for an alert's endpoint to answer with a state just applied. The
	// kubelet refreshes the volume about a second after the pod changes; the
	// timeout covers a sync that misses the change and waits for the next.
	defaultAlertConfirmInterval = 500 * time.Millisecond
	defaultAlertConfirmTimeout  = 2 * time.Minute
)

// ErrStimulusNotApplicable is returned by Provision for a timed stimulus the
// provider cannot apply. Index is the stimulus's position in the request.
type ErrStimulusNotApplicable struct {
	Index  int
	Reason string
}

func (e *ErrStimulusNotApplicable) Error() string {
	return fmt.Sprintf("timed stimulus %d cannot be applied: %s", e.Index, e.Reason)
}

// timedChange is one timed stimulus resolved to the change it makes.
type timedChange struct {
	timing string
	// alert and namespace name the pending alert that begins firing.
	alert     string
	namespace string
}

// planTimedStimuli resolves each timed stimulus to the change the provider
// will apply, or says why it cannot. It reads declarations only, so a
// provision that will be refused is refused before anything is created.
//
// A scenario declares its timed change in prose: the profile's stimuli carry
// a description and a timing and no structured target or new state. The
// change is therefore resolved against the environment, and only where that
// leaves one answer: the stimulus must name, as `target: alert/<name>` or in
// its description, exactly one alert the environment declares pending.
func planTimedStimuli(stimuli []Stimulus, env resolvedEnv, principal string) ([]timedChange, error) {
	if len(stimuli) == 0 {
		return nil, nil
	}
	var pending []StateEntry
	for _, e := range env.State {
		if strings.EqualFold(e.Kind, "alert") && declaredAlertState(e.Spec) == alertStatePending {
			pending = append(pending, e)
		}
	}
	names := make([]string, 0, len(pending))
	for _, e := range pending {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	declared := "none"
	if len(names) > 0 {
		declared = strings.Join(names, ", ")
	}

	taken := make(map[string]int)
	changes := make([]timedChange, 0, len(stimuli))
	for i, s := range stimuli {
		refuse := func(format string, args ...any) error {
			return &ErrStimulusNotApplicable{Index: i, Reason: fmt.Sprintf(format, args...)}
		}
		if t := strings.TrimSpace(s.Type); t != stimulusTypeTemporalCondition {
			return nil, refuse("its type is %q; the provider applies %s stimuli only", t, stimulusTypeTemporalCondition)
		}
		timing := strings.TrimSpace(s.Timing)
		if timing != timingAfterFirstToolCall {
			return nil, refuse("timing %q is not implemented; the implemented timing is %s", timing, timingAfterFirstToolCall)
		}
		if principal == "" {
			return nil, refuse("the provision declares no agent principal, so %s — the first audit log entry of the agent's — cannot be recognised", timingAfterFirstToolCall)
		}

		var matches []StateEntry
		if target := strings.ToLower(strings.TrimSpace(s.Target)); target != "" {
			name, ok := strings.CutPrefix(target, alertTargetPrefix)
			if !ok {
				return nil, refuse("target %q is not an alert; the implemented change is an alert declared %s beginning to fire (pending alerts declared: %s)", s.Target, alertStatePending, declared)
			}
			for _, e := range pending {
				if strings.EqualFold(e.Name, strings.TrimSpace(name)) {
					matches = append(matches, e)
				}
			}
		} else {
			description := strings.ToLower(s.Description)
			for _, e := range pending {
				if strings.Contains(description, strings.ToLower(e.Name)) {
					matches = append(matches, e)
				}
			}
		}
		if len(matches) != 1 {
			return nil, refuse("it names %d of the alerts the environment declares %s (declared: %s); the implemented change is exactly one such alert, named by the stimulus's target or description, beginning to fire", len(matches), alertStatePending, declared)
		}
		key := matches[0].Namespace + "/" + matches[0].Name
		if first, dup := taken[key]; dup {
			return nil, refuse("alert %s is already changed by timed stimulus %d", matches[0].Name, first)
		}
		taken[key] = i
		changes = append(changes, timedChange{timing: timing, alert: matches[0].Name, namespace: matches[0].Namespace})
	}
	return changes, nil
}

// followAudit returns a cursor at the current end of the audit log, or the
// reason the log cannot be followed.
func (p *petriProvider) followAudit() (auditCursor, error) {
	follower, ok := p.audit.(auditFollower)
	if !ok {
		return nil, ErrAuditNotConfigured
	}
	return follower.Follow()
}

// watchStimuli waits for the first entry of the environment's audit log
// attributed to the agent's principal, then applies every change timed
// against it. It returns when the changes are applied or ctx is cancelled,
// which teardown does.
//
// The environment's audit log is what an audit_log observation of it returns:
// entries no earlier than its hand-over. The cursor starts at the end of the
// file as it stood then, and an entry is also required to have been received
// no earlier, because a request that began before the hand-over and completed
// after it — a watch closing — is written late and is not the agent starting.
func (p *petriProvider) watchStimuli(ctx context.Context, env *Environment, changes []timedChange, cursor auditCursor) {
	ticker := time.NewTicker(p.stimulusPoll)
	defer ticker.Stop()
	lastErr := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		entries, err := cursor.Next()
		if err != nil {
			// The log is read again on the next tick; say so once per
			// distinct failure, not twice a second.
			if msg := err.Error(); msg != lastErr {
				lastErr = msg
				p.log.Warn("timed stimulus: audit log could not be read; retrying", "env_id", env.ID, "error", err)
			}
			continue
		}
		lastErr = ""
		for _, e := range entries {
			if e.User != env.AgentPrincipal || e.Timestamp.Before(env.ProvisionedAt) {
				continue
			}
			p.applyTimedChanges(ctx, env, changes, e)
			return
		}
	}
}

// applyTimedChanges applies every change whose timing trigger met. A change
// that fails is logged and recorded against its alert: the provision has
// already returned, so the record is where a caller learns of it.
func (p *petriProvider) applyTimedChanges(ctx context.Context, env *Environment, changes []timedChange, trigger AuditEntry) {
	for _, c := range changes {
		change := alertChange{
			Timing:         c.timing,
			TriggerAuditID: trigger.AuditID,
			TriggerAt:      trigger.Timestamp,
			TriggerUser:    trigger.User,
			AppliedAt:      time.Now(),
		}
		p.log.Info("timed stimulus triggered",
			"env_id", env.ID, "timing", c.timing, "alert", c.alert, "namespace", c.namespace,
			"trigger_audit_id", trigger.AuditID)
		if err := p.fireAlert(ctx, env, c); err != nil {
			change.Error = err.Error()
			p.log.Error("timed stimulus could not be applied",
				"env_id", env.ID, "timing", c.timing, "alert", c.alert, "namespace", c.namespace, "error", err)
		}
		env.Alerts.changed(c.alert, c.namespace, change)
	}
}

// fireAlert moves a declared alert to firing: it re-renders the alert as
// firing from now, applies it over the running source, and records the alert
// firing once the endpoint's own files say so.
func (p *petriProvider) fireAlert(ctx context.Context, env *Environment, c timedChange) error {
	rec, err := env.Alerts.find(c.alert, c.namespace)
	if err != nil {
		return err
	}
	spec := make(map[string]any, len(rec.Spec)+1)
	for k, v := range rec.Spec {
		spec[k] = v
	}
	spec["status"] = alertStateFiring
	entry := StateEntry{Kind: "alert", Name: rec.Name, Namespace: rec.Namespace, Spec: spec}

	now := time.Now()
	src, err := buildAlertSource(entry.Name, entry.Namespace, entry.Spec, now, now)
	if err != nil {
		return err
	}
	if err := p.injector.applyAlertSource(ctx, src); err != nil {
		return err
	}
	return p.settleAlert(ctx, env, entry)
}

// settleAlert waits for the endpoint of the alert an entry declares to
// answer with the state the entry declares, and records it. The record is
// written only on what was read back, so the time it carries for an alert
// that began firing is never earlier than an agent could have read it.
func (p *petriProvider) settleAlert(ctx context.Context, env *Environment, e StateEntry) error {
	want := declaredAlertState(e.Spec)
	path := mockServerMountPath + "/" + alertStateFile
	deadline := time.Now().Add(p.alertConfirmTimeout)
	var got string
	var execErr error
	for {
		var out string
		out, execErr = p.kube.ExecInPod(ctx, e.Namespace, alertSourceName(e.Name), []string{"cat", path})
		if execErr == nil {
			if got = servedAlertState(out); got == want {
				env.Alerts.served(e, time.Now())
				return nil
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("alert %s: waiting for its endpoint to serve %q: %w", e.Name, want, ctx.Err())
		case <-time.After(p.alertConfirmInterval):
		}
	}
	if execErr != nil {
		return fmt.Errorf("alert %s: its endpoint could not be read within %s: %w", e.Name, p.alertConfirmTimeout, execErr)
	}
	return fmt.Errorf("alert %s: its endpoint still serves %q %s after %q was applied", e.Name, got, p.alertConfirmTimeout, want)
}
