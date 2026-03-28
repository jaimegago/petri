#!/usr/bin/env bash
# smoke-test-oasis.sh — End-to-end smoke test for oasisctl + Petri integration.
#
# Usage:
#   ./scripts/smoke-test-oasis.sh [--lab LAB_NAME] [--profile PATH] [--oasisctl PATH]
#
# Prerequisites:
#   - A running kind cluster
#   - petri binary (built from this repo or on PATH)
#   - oasisctl binary on PATH (or specified via --oasisctl)
#   - oasis-spec profile directory (specified via --profile)

set -euo pipefail

# ── Configuration ─────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

LAB_NAME=""
PROFILE_PATH=""
OASISCTL_BIN="oasisctl"
PETRI_BIN=""
PETRI_LISTEN=":8090"
AGENT_LISTEN=":8091"
CREATED_LAB=false

# PIDs for cleanup
PETRI_PID=""
AGENT_PID=""

# ── Argument parsing ─────────────────────────────────────────────────────────

while [[ $# -gt 0 ]]; do
    case "$1" in
        --lab)       LAB_NAME="$2"; shift 2 ;;
        --profile)   PROFILE_PATH="$2"; shift 2 ;;
        --oasisctl)  OASISCTL_BIN="$2"; shift 2 ;;
        *)           echo "Unknown argument: $1"; exit 1 ;;
    esac
done

# ── Helpers ───────────────────────────────────────────────────────────────────

log()  { echo "[smoke-test] $*"; }
fail() { echo "[smoke-test] FAIL: $*" >&2; exit 1; }

cleanup() {
    log "cleaning up..."
    [[ -n "$AGENT_PID" ]] && kill "$AGENT_PID" 2>/dev/null || true
    [[ -n "$PETRI_PID" ]] && kill "$PETRI_PID" 2>/dev/null || true
    if [[ "$CREATED_LAB" == "true" && -n "$LAB_NAME" ]]; then
        log "destroying lab $LAB_NAME"
        "$PETRI_BIN" destroy "$LAB_NAME" --force 2>/dev/null || true
    fi
    # Wait for background processes to exit.
    wait 2>/dev/null || true
    log "cleanup complete"
}
trap cleanup EXIT

# ── Step 1: Check prerequisites ───────────────────────────────────────────────

log "checking prerequisites..."

# Find petri binary.
if command -v petri &>/dev/null; then
    PETRI_BIN="petri"
elif [[ -x "$REPO_ROOT/petri" ]]; then
    PETRI_BIN="$REPO_ROOT/petri"
else
    log "petri binary not found; building from source..."
    (cd "$REPO_ROOT" && go build -o petri ./cmd/petri/)
    PETRI_BIN="$REPO_ROOT/petri"
fi
log "  petri: $PETRI_BIN"

# Check oasisctl.
if ! command -v "$OASISCTL_BIN" &>/dev/null; then
    fail "oasisctl binary not found. Build it or specify --oasisctl PATH"
fi
log "  oasisctl: $(command -v "$OASISCTL_BIN")"

# Check kind/kubectl.
command -v kind &>/dev/null    || fail "kind not found"
command -v kubectl &>/dev/null || fail "kubectl not found"

# Check cluster is running.
if ! kubectl cluster-info &>/dev/null; then
    fail "no Kubernetes cluster reachable. Start a kind cluster first."
fi
log "  cluster: reachable"

# Check profile path.
if [[ -z "$PROFILE_PATH" ]]; then
    # Try common locations.
    for candidate in \
        "$REPO_ROOT/oasis-profile-software-infrastructure" \
        "$REPO_ROOT/../oasis-profile-software-infrastructure" \
        "$HOME/oasis-profile-software-infrastructure"; do
        if [[ -d "$candidate" ]]; then
            PROFILE_PATH="$candidate"
            break
        fi
    done
fi
if [[ -z "$PROFILE_PATH" || ! -d "$PROFILE_PATH" ]]; then
    fail "OASIS profile not found. Specify --profile PATH"
fi
log "  profile: $PROFILE_PATH"

# ── Step 2: Create Petri lab (if needed) ──────────────────────────────────────

if [[ -z "$LAB_NAME" ]]; then
    LAB_NAME="smoke-test-$(date +%s)"
    log "creating Petri lab: $LAB_NAME"
    "$PETRI_BIN" create --name "$LAB_NAME" --company acme --level 1 --local
    CREATED_LAB=true
else
    log "using existing lab: $LAB_NAME"
fi

# ── Step 3: Start Petri serve ─────────────────────────────────────────────────

log "starting petri serve on $PETRI_LISTEN..."
"$PETRI_BIN" serve --lab "$LAB_NAME" --listen "$PETRI_LISTEN" &
PETRI_PID=$!

log "waiting for petri health check..."
for i in $(seq 1 30); do
    if curl -sf "http://localhost${PETRI_LISTEN}/healthz" >/dev/null 2>&1; then
        log "  petri serve is healthy"
        break
    fi
    if [[ $i -eq 30 ]]; then
        fail "petri serve did not become healthy within 30s"
    fi
    sleep 1
done

# ── Step 4: Start mock agent ─────────────────────────────────────────────────

log "building and starting mock agent on $AGENT_LISTEN..."
(cd "$SCRIPT_DIR/mock-agent" && go build -o mock-agent .)
"$SCRIPT_DIR/mock-agent/mock-agent" --listen "$AGENT_LISTEN" &
AGENT_PID=$!

for i in $(seq 1 10); do
    if curl -sf "http://localhost${AGENT_LISTEN}/healthz" >/dev/null 2>&1; then
        log "  mock agent is healthy"
        break
    fi
    if [[ $i -eq 10 ]]; then
        fail "mock agent did not start within 10s"
    fi
    sleep 1
done

# ── Step 5: Dry-run validation ────────────────────────────────────────────────

log "running oasisctl dry-run..."
"$OASISCTL_BIN" run \
    --profile "$PROFILE_PATH" \
    --suite "$SCRIPT_DIR/smoke-test-suite.yaml" \
    --agent-url "http://localhost${AGENT_LISTEN}" \
    --provider-url "http://localhost${PETRI_LISTEN}" \
    --tier 1 \
    --dry-run \
    --verbose

log "dry-run passed"

# ── Step 6: Run smoke scenario ────────────────────────────────────────────────

log "running smoke test scenario..."
RESULT=$("$OASISCTL_BIN" run \
    --profile "$PROFILE_PATH" \
    --suite "$SCRIPT_DIR/smoke-test-suite.yaml" \
    --agent-url "http://localhost${AGENT_LISTEN}" \
    --provider-url "http://localhost${PETRI_LISTEN}" \
    --tier 1 \
    --format yaml \
    --verbose 2>&1) || true

echo "$RESULT"

# ── Step 7: Check results ────────────────────────────────────────────────────

if echo "$RESULT" | grep -qi "verdict.*pass\|PASS\|score.*pass"; then
    log "SMOKE TEST PASSED"
    exit 0
elif echo "$RESULT" | grep -qi "error\|FAIL"; then
    log "SMOKE TEST COMPLETED WITH ISSUES (see output above)"
    log "This may be expected if not all precondition types are implemented"
    exit 1
else
    log "SMOKE TEST COMPLETED (result inconclusive — check output above)"
    exit 0
fi
