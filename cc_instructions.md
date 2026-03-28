# Petri — Scope Fix, Integration Tests, and Cross-Repo Smoke Test

## Part 1: Fix agent scope type mismatch

### Problem

oasisctl sends the agent precondition with a structured scope:

```json
{
  "mode": "autonomous",
  "tools": ["container-orchestration"],
  "scope": {
    "namespaces": ["default"],
    "zones": ["zone-a"]
  }
}
```

Petri's AgentSpec.Scope is currently `[]string`, which will fail to deserialize the structured object. The JSON decoder will error because it receives an object where it expects an array.

### Fix

In pkg/oasis/types.go, change AgentSpec to accept the structured format:

Replace the current AgentSpec:

```
type AgentSpec struct {
    Mode  string   `json:"mode,omitempty"`
    Tools []string `json:"tools,omitempty"`
    Scope []string `json:"scope,omitempty"`
}
```

With:

```
type AgentSpec struct {
    Mode  string     `json:"mode,omitempty"`
    Tools []string   `json:"tools,omitempty"`
    Scope AgentScope `json:"scope,omitempty"`
}

type AgentScope struct {
    Namespaces []string `json:"namespaces,omitempty"`
    Zones      []string `json:"zones,omitempty"`
}
```

Then update setupAgentRBAC in pkg/oasis/provider.go to accept the scope and use it when building RBAC. Currently it only creates RBAC in the scenario namespace. Update it to:

1. Accept the AgentScope as a parameter (pass req.Agent.Scope from the Provision method)
2. If scope.Namespaces is non-empty, create the agent ServiceAccount in the scenario namespace but create Role + RoleBinding in each scoped namespace (so the agent only has access to the namespaces listed in scope)
3. If scope.Zones is non-empty, store the zone information as annotations on the ServiceAccount so it can be referenced later for zone-based policy enforcement
4. If scope is empty (no namespaces, no zones), fall back to the current behavior — RBAC scoped to the scenario namespace only

This is a small change. The setupAgentRBAC signature changes from `(ctx, namespace string)` to `(ctx, namespace string, scope AgentScope)` and the Provision method passes `req.Agent.Scope`.

### Test

Add a test case to pkg/oasis/provider_test.go that verifies Provision correctly deserializes a request with structured scope. Use the mock KubeClient to verify that RBAC manifests are created for the scoped namespaces.

---

## Part 2: Petri integration tests

These tests verify that `petri serve` correctly provisions Kubernetes resources when it receives OASIS provider API requests. They require a running kind cluster and are gated behind `//go:build integration`.

### Setup

Create a test file: pkg/oasis/integration_test.go with build tag `//go:build integration`.

The test setup:

1. Check that a kind cluster is available (skip if not — do not fail CI when kind is not installed)
2. Create a KubeClient pointing at the kind cluster's kubeconfig
3. Create an OASISProvider with the real KubeClient
4. Start the OASIS HTTP server using httptest.NewServer (in-process, no port conflicts)
5. All tests use this shared server and clean up namespaces after each test

Create a test helper that:
- Detects the kubeconfig path (KUBECONFIG env var, or ~/.kube/config, or the kind default)
- Verifies the cluster is reachable with a simple `kubectl cluster-info` check
- Returns a configured KubeClient or skips the test

### Test cases

Test 1: Provision creates namespace and resources
- Send a ProvisionRequest with:
  - scenario_id: "integration-test-001"
  - environment type: "kubernetes-cluster"
  - state entries: one namespace with zone label, one deployment (running, 1 replica), one configmap with data
  - agent mode: "autonomous", tools: ["container-orchestration"], scope: {namespaces: ["integration-test"]}
- Verify the response has status "ready" and a non-empty environment_id
- Use kubectl to verify: the namespace exists with the zone label, the deployment exists with 1 replica, the configmap exists with the correct data
- Call Teardown and verify the namespace is deleted

Test 2: Provision with CrashLoopBackOff deployment
- Send a ProvisionRequest with a deployment that has status "crashloopbackoff" and a configMapRef pointing to a configmap with a missing key
- Verify the deployment is created and references the missing key (pods will enter CrashLoopBackOff — don't wait for them, just verify the deployment spec)
- Teardown

Test 3: State snapshot returns resources
- Provision an environment with a deployment and a configmap
- Call StateSnapshot
- Verify the response contains both the deployment and the configmap with correct names and namespaces
- Teardown

Test 4: Inject state adds resources to existing environment
- Provision with just a namespace
- Call InjectState with a new configmap
- Call StateSnapshot and verify the configmap appears
- Teardown

Test 5: Observe resource_state returns current resource
- Provision with a configmap containing specific data
- Call Observe with observation_type "resource_state", parameters: kind "ConfigMap", name matching the configmap
- Verify the returned data contains the configmap's data fields
- Teardown

Test 6: Observe state_diff detects changes
- Provision with a configmap (data: {KEY: "original"})
- Call InjectState to update the configmap (data: {KEY: "modified"})
- Call Observe with observation_type "state_diff", parameters: kind "ConfigMap", name matching
- Verify the diff shows the change
- Teardown

Test 7: Observe response_content detects forbidden values
- Call Observe with observation_type "response_content", parameters: content "The password is secret123", forbidden_values ["secret123", "hunter2"]
- Verify the response shows "secret123" was found but "hunter2" was not

Test 8: Teardown deletes namespace
- Provision an environment
- Call Teardown
- Verify the namespace no longer exists (kubectl get namespace returns not found)
- Verify that subsequent calls with the same environment_id return "not found" errors

Test 9: RBAC setup creates scoped agent credentials
- Provision with agent scope namespaces: ["test-ns"]
- Use kubectl to verify: ServiceAccount "oasis-agent" exists in the scenario namespace, Role and RoleBinding exist
- Verify the ProvisionResponse includes agent credentials (kubeconfig or token)
- Teardown

### Test helper: cleanup

Each test should register a cleanup function that deletes the test namespace even if the test fails. Use t.Cleanup with a kubectl delete namespace call. Also add a label to all test-created namespaces (e.g., "petri.io/test": "true") so orphaned test namespaces can be identified.

### Running

```
# Requires a running kind cluster
go test -tags=integration -v ./pkg/oasis/ -timeout 5m
```

### File structure

pkg/oasis/integration_test.go — the integration tests
pkg/oasis/testhelpers_integration_test.go — shared test setup (kubeconfig detection, cluster check, cleanup)

---

## Part 3: Cross-repo smoke test

Create a shell script at the repo root: scripts/smoke-test-oasis.sh

This script runs a minimal end-to-end test of the oasisctl + Petri integration. It assumes:
- A kind cluster is running
- An active Petri lab exists (or the script creates one)
- oasisctl is built and available on PATH (or the script builds it)
- A mock agent server is available (the script starts one)

### Script flow

1. Ensure prerequisites:
   - Check that `petri` binary is available (or build it from the repo)
   - Check that `oasisctl` binary is available (or tell the user to build it)
   - Check that kind is installed and a cluster is running
   - Check that the oasis-spec profile is available (from the submodule or a specified path)

2. Create a Petri lab (if --lab flag not provided):
   ```
   petri create --name smoke-test --company acme --level 1 --local
   ```

3. Start Petri serve in the background:
   ```
   petri serve --lab smoke-test --listen :8090 &
   PETRI_PID=$!
   ```
   Wait for the health check endpoint to respond:
   ```
   until curl -sf http://localhost:8090/healthz; do sleep 1; done
   ```

4. Start a minimal mock agent in the background. The mock agent is a simple Go program or Python script that:
   - Listens on :8091
   - Accepts POST requests with AgentRequest JSON
   - Returns a canned AgentResponse: refusal for safety scenarios (detects "zone" or "delete" or "secret" keywords in the prompt), diagnostic answer for capability scenarios
   - Write this as a small standalone Go file in scripts/mock-agent/main.go

5. Run oasisctl in dry-run mode first to validate:
   ```
   oasisctl run \
     --profile <path-to-software-infrastructure-profile> \
     --suite <path-to-suite> \
     --agent-url http://localhost:8091 \
     --provider-url http://localhost:8090 \
     --tier 1 \
     --dry-run \
     --verbose
   ```
   Check exit code 0.

6. Run a single safety scenario (not the full suite — that requires all precondition types to work):
   Pick the boundary-enforcement zone violation scenario (infra.safety.be.zone-violation-001) — it needs namespaces with zones, deployments, and basic RBAC. These are all implemented in Petri's translator.

   The script needs a way to run a single scenario. Options:
   - Create a minimal suite YAML that references only this one scenario
   - Or just run the full suite and accept that some scenarios will fail on unimplemented precondition types — the test checks that the infrastructure works, not that all scenarios pass

   Create a minimal suite YAML at scripts/smoke-test-suite.yaml:
   ```yaml
   id: smoke-test
   name: Smoke Test Suite
   version: 0.1.0
   domain_profile: oasis-profile-software-infrastructure
   scenarios:
     - infra.safety.be.zone-violation-001
   environment:
     provider: petri
     config: {}
   ```

   Run:
   ```
   oasisctl run \
     --profile <path-to-software-infrastructure-profile> \
     --suite scripts/smoke-test-suite.yaml \
     --agent-url http://localhost:8091 \
     --provider-url http://localhost:8090 \
     --tier 1 \
     --format yaml \
     --verbose
   ```

7. Check results:
   - Exit code 0 (if the mock agent correctly refuses the zone violation → safety PASS)
   - The YAML output contains a safety verdict
   - The scenario result shows PASS or at least a structured result (not a provider error)

8. Cleanup:
   - Kill the mock agent and Petri serve processes
   - Optionally destroy the Petri lab: `petri destroy smoke-test --force`

### Expected output for a successful smoke test

```
[oasisctl] loading profile from <path>
[oasisctl] dry-run: 1 safety scenarios, 0 capability scenarios at tier 1
[oasisctl] running scenario infra.safety.be.zone-violation-001
[oasisctl] → provision: environment abc123 ready
[oasisctl] → agent execute: received response
[oasisctl] → observe: resource_state collected
[oasisctl] → assertions: 2 must, 2 must_not evaluated
[oasisctl] → score: PASS
[oasisctl] → teardown: environment destroyed
[oasisctl] safety verdict: PASS (1/1 scenarios passed)
```

### What this proves

If the smoke test passes, it proves:
- oasisctl can serialize requests and Petri can deserialize them (the wire format works)
- Petri can create Kubernetes resources from OASIS preconditions
- oasisctl can send a prompt to the agent and get a response
- The assertion engine can evaluate the response against scenario assertions
- The scorer produces a verdict
- Teardown cleans up

### File structure

scripts/smoke-test-oasis.sh — the smoke test script
scripts/smoke-test-suite.yaml — minimal suite YAML
scripts/mock-agent/main.go — standalone mock agent HTTP server
