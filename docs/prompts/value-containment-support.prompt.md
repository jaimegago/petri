# Prompt: Advertise value_containment_support in OASIS conformance

Generated: 2026-04-24
Model: claude-opus-4-7 (1M context)
Target:
- pkg/oasis/types.go
- pkg/oasis/provider.go
- pkg/oasis/provider_test.go

## Specification

Add `ValueContainmentSupport` support to Petri's OASIS conformance response.

### Scope

All changes are confined to `pkg/oasis/`. Do not modify `translate.go`, `server.go`, observation handlers, state injection logic, or any file outside `pkg/oasis/`. Do not add new observation types or new evidence source strings.

### Change 1 — schema (pkg/oasis/types.go)

Add a `ValueContainmentSupport bool` field to the `ConformanceRequirements` struct with JSON tag `value_containment_support`. Place it next to the other capability booleans (`StateInjection`, `AuditPolicyInstallation`, `NetworkPolicyEnforcement`) — the order of the existing three must be preserved, and the new field belongs alongside them. Update the struct's doc comment to reflect the new count of SI provider-conformance requirement keys.

### Change 2 — handler (pkg/oasis/provider.go)

In the `Conformance` method, populate the new field on the supported-profile path (after the early-return branch for unsupported profiles). The value must be `true` unconditionally.

Rationale: Petri structurally supports value containment because state injection of secrets and env vars is already implemented, which is what populates the precondition values `oasisctl` resolves from. The capability is declared, not measured — so no runtime check, no `UnmetRequirement` entry.

The early-return path for unsupported profiles already emits a zero-value `ConformanceRequirements` struct; leave that alone. The zero value of the new boolean (`false`) is the correct behavior there.

Do not modify `EvidenceSourcesAvailable`. The string `value_containment` is already present in that slice and stays — it advertises the evidence source. The new boolean is a separate, spec-required dimension of the same capability; both are required.

### Change 3 — tests (pkg/oasis/provider_test.go)

Extend the existing conformance tests in place. Do not add new test files, do not refactor existing tests.

- In the SI-profile test (supported path): assert `Requirements.ValueContainmentSupport` is `true`.
- In the unsupported-profile test (early-return path): assert `Requirements.ValueContainmentSupport` is `false` (the struct zero value).

### Acceptance criteria

- `go vet ./...` passes.
- `go build ./...` passes.
- `go test ./pkg/oasis/...` passes.
- The JSON output of `/v1/conformance` for the SI profile includes `"value_containment_support": true` as a sibling of `state_injection` and `network_policy_enforcement`.
