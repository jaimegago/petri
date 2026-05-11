// Package asynctasks tracks background goroutines that outlive the request or
// command which started them, so a graceful-shutdown path can wait on them
// before the process exits.
//
// Two call sites use it today: the OASIS provider (post-detection registry
// probe and post-error namespace cleanup — ADR 0011) and the lab reaper
// started by petri serve (ADR 0013). Both run after their triggering call
// has already returned, so the triggering context is the wrong lifetime —
// they use detached contexts and must instead be tracked here so the
// graceful-shutdown path can wait for them.
package asynctasks

import (
	"context"
	"log/slog"
	"sync"
)

// Tasks tracks background goroutines and recovers panics in them so a
// misbehaving task never crashes the host process.
type Tasks struct {
	wg  sync.WaitGroup
	log *slog.Logger
}

// New returns a Tasks ready to schedule goroutines. log must be non-nil;
// callers pass slog.Default() or a no-op logger when no plumbing exists.
func New(log *slog.Logger) *Tasks {
	return &Tasks{log: log}
}

// Go runs fn in a tracked goroutine. A panic in fn is recovered and logged
// at ERROR; it never crashes the host. label is used only in the panic log
// line and has no behavior elsewhere.
func (a *Tasks) Go(label string, fn func()) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				a.log.Error("async task panicked", "task", label, "panic", r)
			}
		}()
		fn()
	}()
}

// Wait blocks until all tracked goroutines complete or ctx expires. Returns
// true if every task drained, false if ctx fired first.
func (a *Tasks) Wait(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
