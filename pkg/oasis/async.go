package oasis

import (
	"context"
	"log/slog"
	"sync"
)

// asyncTasks tracks background goroutines spawned during request handling.
// Two call sites use it today: the post-detection registry probe in
// pull_watcher.go and the post-error namespace cleanup in provider.go.
// Both run after the HTTP response is already in flight, so the request
// context is the wrong lifetime for them — they use detached contexts and
// must instead be tracked here so petri serve's graceful-shutdown path can
// wait for them. See ADR 0011.
type asyncTasks struct {
	wg  sync.WaitGroup
	log *slog.Logger
}

func newAsyncTasks(log *slog.Logger) *asyncTasks {
	return &asyncTasks{log: log}
}

// Go runs fn in a tracked goroutine. A panic in fn is recovered and logged
// at ERROR; it never crashes petri serve. label is used only in the panic
// log line and the recover branch — it has no behavior elsewhere.
func (a *asyncTasks) Go(label string, fn func()) {
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
func (a *asyncTasks) Wait(ctx context.Context) bool {
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
