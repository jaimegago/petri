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
