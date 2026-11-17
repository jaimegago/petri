# default-image-io-setup-crash: nginx-slim workloads crash on io_setup() in the local lab

Status: open
Priority:

Every born-into-state workload in the 2026-11-17 local lab ran
`registry.k8s.io/nginx-slim:0.27`, petri's default OASIS image
(`pkg/oasis/provider.go` `defaultOASISImage`, `pkg/workloadstate/render.go`
`HTTPServerImage`, `pkg/config/config.go`). Its containers logged
`[emerg] io_setup() failed (38: Function not implemented)`: nginx's AIO setup
fails against the node's kernel.

Seen in `frontend`, `backend` and `order-service` across C-CA-002 and C-CA-003
(joe-pm `threads/ca-oi-heuristics.md`). The agent under test then finds a real
fault that is not the scenario's fault. In C-CA-002, Joe diagnosed the crash
instead of the NetworkPolicy the scenario stages. A scenario that declares
`status: running` is not getting a running workload.

Not yet established: whether this is specific to this Docker Desktop kernel or
reproduces on every local lab. Check that first. A fix is either an image that
does not need AIO, or an nginx config that disables it (`aio off;`). After it,
add a provision-time check that a `running` workload actually stays up.

## Resolution — 2026-11-17

Delivered by joe-pm `threads/ca-oi-lab-gaps.md`. The title is wrong about the
symptom: nothing crashed. `nginx-slim:0.27` is amd64 only. On an arm64 host it
runs emulated, the emulator does not implement `io_setup()`, and nginx logs
`[emerg] io_setup() failed` once at start and then serves normally (checked
with `docker run`: status running, zero restarts, HTTP 200). It reproduces on
every arm64 lab and on no amd64 one. `aio off;` cannot fix it, because nginx
makes the call at start whatever the directive says.

The default image and `workloadstate.HTTPServerImage` are now
`registry.k8s.io/e2e-test-images/nginx:1.15-4`, which is multi-arch. On arm64
it starts with an empty log and serves the mock and `elevated_error_rate`
configurations unchanged.

No stays-up check was added. The rollout wait already holds provision until a
`running` workload is Ready, and no such check would have caught a log line.
Still open: `infra.capability.au.provenance-tracking-001` in oasis-spec pins
`nginx-slim:0.27` itself, so that scenario's workload still logs the line on
arm64.
