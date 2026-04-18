Task: remove response_content observation type from Petri, swap to value_containment in the conformance capability list

Target repo: petri
Target files:
  primary: pkg/oasis/provider.go, pkg/oasis/types.go
  tests: pkg/oasis/provider_test.go, pkg/oasis/server_test.go, pkg/oasis/integration_test.go

Read first, in order:
1. CLAUDE.md at repo root
2. pkg/oasis/types.go (ObserveRequest, ObserveResponse, ConformanceRequirements)
3. pkg/oasis/provider.go: Observe dispatch around lines 242-267, normalizeObservationType around lines 269-307, Conformance around lines 311-430, observeResponseContent at lines 637-665
4. pkg/oasis/state.go (Environment struct — read-only context, do not modify)

Context

The oasisctl side of the value_containment verification primitive has already landed. Oasisctl now owns both the transcript and the literal-value resolution from scenario preconditions, so Petri is no longer in the value-matching path. The existing response_content observation is obsolete: it accepted a pre-captured transcript as an observation parameter and did trivial substring matching against a list of forbidden values passed in the same request. Oasisctl no longer calls this endpoint.

The scope of this PR is removal of the dead path plus a capability-string swap in the conformance declaration, so the SI profile's required evidence sources match what Petri advertises.

Scope

Part 1: remove the response_content observation path

  - Delete the observeResponseContent function in pkg/oasis/provider.go.
  - Remove the "response_content" case from the Observe dispatch switch.
  - Remove "response_content" from the canonical-type pass-through switch in normalizeObservationType.
  - Remove the normalizer alias branch that maps "response", "agent output", "agent reasoning", "reasoning trace", "agent response" to response_content. Delete the whole case block; do not leave it mapping to anything else.
  - Update the ObservationType doc comment on ObserveRequest in pkg/oasis/types.go to drop response_content from the enumerated list.
  - Check whether paramStrings in types.go is still used after the deletion. If it has no remaining callers, delete it as well. Do not delete paramString — audit_log uses it.

Part 2: conformance capability swap

  - In Conformance at pkg/oasis/provider.go around line 363, replace "response_content" with "value_containment" in the default evidenceSources append.
  - In the same function around line 380, update the error message on the audit_log unmet-requirement path: change the text listing required observation types so it reads "audit_log, resource_state, and value_containment" instead of "audit_log, resource_state, and response_content".
  - Do not change anything else in Conformance (environment_type, complexity tier, state_injection, audit_policy_installation, network_policy_enforcement all stay as-is).

Part 3: test updates

  - In pkg/oasis/integration_test.go, delete the test titled "Test 7: Observe response_content detects forbidden values" around line 351 and its supporting lines. If renumbering subsequent tests is idiomatic in this file, renumber; otherwise leave later tests with their existing numbers.
  - In pkg/oasis/server_test.go around line 440, replace "response_content" with "value_containment" in the EvidenceSourcesAvailable fixture.
  - In pkg/oasis/provider_test.go:
      * Delete the observe-response-content test case around lines 656-663.
      * In the normalizeObservationType test table around lines 771-778, delete every row whose expected output is "response_content": the direct pass-through case and the four alias cases ("agent reasoning trace", "agent response content", "agent output verification", any other). Leave the audit_log, resource_state, state_diff rows untouched.
      * Delete the test around lines 809-822 that asserts "agent reasoning trace" maps to response_content.
      * In the dispatch-error test table around lines 1009-1010, delete the response_content case.
  - Update the Conformance-related tests that check EvidenceSourcesAvailable to expect value_containment instead of response_content.

Rationale for keeping value_containment as an advertised evidence source

The term "evidence source" in the SI provider-conformance spec is used loosely to cover both provider-produced evidence (audit_log, resource_state, state_diff) and verification primitives the provider does not obstruct. The SI profile requires value_containment in its evidence_sources_required list, and oasisctl's profile YAML has already been updated to match. Petri supports the verification block by provisioning the preconditions that hold the declared literal values, even though the matching itself happens in oasisctl. Advertising value_containment here is consistent with how the spec and the SI profile already treat the capability. Do not add any new observe path to back up the advertisement — value_containment is a declared capability, not a served observation.

Out of scope

  - No changes to pkg/oasis/state.go (Environment struct). Petri does not need to persist preconditions or agent scope for this PR; oasisctl owns value resolution end-to-end.
  - No changes to pkg/oasis/translate.go or any resource-injection code.
  - No changes to the audit_log, resource_state, or state_diff observation paths.
  - Do not introduce any new observation type.

Acceptance criteria

  - grep -rn "response_content" in the repo returns zero hits across Go source, YAML, and Markdown (except in a single-line CHANGELOG or release-notes entry if one exists — in that case, add a note saying the capability was replaced with value_containment; do not edit historical entries).
  - grep -rn "observeResponseContent" returns zero hits.
  - grep -rn "value_containment" returns hits in pkg/oasis/provider.go (Conformance) and the test fixtures that exercise Conformance.
  - go build ./..., go vet ./..., go test ./..., and go test -tags=integration ./... all pass.
  - The Observe handler returns a clear "unsupported observation type" error if called with observation_type = "response_content".
  - A new prompt doc docs/prompts/remove-response-content.prompt.md is created with this specification, in the same style as any existing prompts under docs/prompts if the directory exists; otherwise create it.
