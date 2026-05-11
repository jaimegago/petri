# Prompt: Remove unnecessary drains in pull_watcher fast-fail coordination

Generated: 2026-05-10
Model: claude-opus-4-7 (1M context)
Target:
- pkg/oasis/pull_watcher.go
- pkg/oasis/pull_watcher_test.go
- pkg/oasis/mock_test.go
- docs/prompts/pull-watcher-drain-fix.prompt.md

## Specification

Goal: remove the unnecessary drain blocks in pkg/oasis/pull_watcher.go's fast-fail coordination. These drains cause the HTTP handler to wait ~5s after a pull failure is detected, blocking on the kubectl rollout-status subprocess to exit. The drains were added "to avoid leaking the goroutine" but the channels are buffered and the context is cancelled, so the goroutines exit cleanly on their own without blocking the caller.

Read pkg/oasis/pull_watcher.go in full before making changes.

Context: in the watcher-fires-first branch and the parent-context-cancelled branch of the select loop, the code does `cancel(); <-rolloutCh` (or symmetrically `<-watcherCh`). The drain block waits for the kubectl subprocess to notice the cancelled context and exit, which empirically takes ~5s for `kubectl rollout status`. This delay was observed in a manual repro: an image-pull failure was detected at +33s, but the HTTP 502 didn't fire until +43s, with 5s of silence in petri's logs between the watcher firing and the response.

The drain is unnecessary because:
- Both `rolloutCh` and `watcherCh` are buffered with capacity 1 (`make(chan error, 1)` and `make(chan *ErrImagePullFailure, 1)`). After the orchestrator selects one branch and the other goroutine eventually finishes, that goroutine writes to its channel without blocking even if no reader ever reads it.
- The cancelled context propagates to both goroutines; they will exit on their own schedule (kubectl in ~5s, the watcher near-instantly).
- "Leaking" a goroutine that is guaranteed to exit within seconds and whose channel write is non-blocking is not a real leak in any operational sense.

Fix:
- In the watcher-fires-first branch (`case pf := <-watcherCh: if pf != nil { ... }`), keep the `cancel()` call but remove the `<-rolloutCh` drain. Return the pull failure immediately.
- In the parent-context-cancelled / watcher-returned-nil branch (`err := <-rolloutCh` in the else path of the same case), the situation is different: here we genuinely want the rollout's result before deciding what error to return. Audit this branch carefully — if the parent context is cancelled, kubectl will exit with a context-cancelled error and we should propagate that as an ErrRolloutTimeout (or possibly a new error type for "request cancelled by client"). If kubectl finished normally before cancellation, return its result. The 5s delay on this path is only on the client-cancelled case, which is less critical, but worth deciding intentionally rather than leaving as a copy-paste of the other branch.
- In the rollout-finishes-first branch (`case err := <-rolloutCh:`), the `<-watcherCh` drain is fine to keep — the watcher exits in milliseconds when its context is cancelled, since it's a `select { case <-ctx.Done(): ... case <-t.C: ... }` loop with a short tick interval. No user-visible delay. Optional simplification: remove it too for consistency, since the same buffered-channel reasoning applies. Either is fine; model decides.

Add a code comment near the channel declarations explaining why the channels are buffered with capacity 1 (so that the goroutines can always write their result without blocking on a reader), and why the drains are not needed (the buffered writes mean an unread channel does not leak the goroutine; cancellation propagates through ctx).

Tests:
- The existing tests in pull_watcher_test.go cover the success path, the rollout-timeout path, the pull-failure-detected path, and the ctx-cancellation path. They must continue to pass without modification.
- Add a new test asserting that on the pull-failure-detected path, the function returns within a small bound (say 100ms) of the watcher firing, not blocked on a slow kubectl mock. Use a fake KubeClient whose WaitForRollout sleeps for 5+ seconds before returning. Assert the function returns the pull failure well before the slow rollout would have completed.

Verification:
- go fmt ./... && go vet ./... && go test ./... clean.
- Manual repro on a real lab: re-run the unrouted-RFC1918-image provision (10.255.255.1/nginx:latest) and confirm the gap between the "image pull failure detected" WARN log and the "http request status=502" log shrinks from ~5s to well under 1s. The total time-to-detect is still gated by kubelet's containerd timeout (~30s for unreachable hosts), but the time-to-respond after detection should be near-instant.

Archival
- Save this prompt as docs/prompts/pull-watcher-drain-fix.prompt.md. Match the existing naming pattern.

No ADR required — this is a bug fix, not an architectural decision. If the parent-cancelled branch ends up introducing a new error type or changing semantics, that does warrant an ADR; otherwise this is a small follow-up to ADR 0008 / the typed-image-pull-failures work.
