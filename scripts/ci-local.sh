#!/usr/bin/env bash
# ci-local: run every gate .github/workflows/ci.yml runs, locally.
#
# Usage:
#   scripts/ci-local.sh                     # all six gates (needs Docker + k3d)
#   scripts/ci-local.sh --skip-integration  # fast mode: skip the two
#                                           # cluster-backed gates
#                                           # (integration, ui-e2e)
#
# Gates mirror the CI jobs one-to-one and are version-matched to them (the
# staticcheck pin is read out of ci.yml so it cannot drift). The cheap gates
# run first so a broken unit test doesn't cost a 10-minute cluster build;
# CI runs all jobs regardless of each other's outcome, and so does this
# script — every gate runs, failures are collected, and the summary at the
# end is the verdict. Exit is non-zero if any gate failed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CI_YML="${REPO_ROOT}/.github/workflows/ci.yml"
cd "$REPO_ROOT"

SKIP_CLUSTER_GATES=false
for arg in "$@"; do
    case "$arg" in
        --skip-integration) SKIP_CLUSTER_GATES=true ;;
        *) echo "unknown flag: $arg (only --skip-integration is supported)" >&2; exit 2 ;;
    esac
done

# Version-match staticcheck to the CI pin (the 'go install …staticcheck@vX'
# line in ci.yml's lint job). A missing pin is a hard error: silently falling
# back to 'latest' would reintroduce exactly the drift this parse prevents.
STATICCHECK_VERSION="$(grep -oE 'go install honnef\.co/go/tools/cmd/staticcheck@[^ "]+' "$CI_YML" | head -1 | cut -d@ -f2)"
if [ -z "$STATICCHECK_VERSION" ]; then
    echo "ci-local: could not find the staticcheck install line in $CI_YML — the lint gate cannot be version-matched to CI. Fix the parse or the workflow." >&2
    exit 2
fi

gate_names=()
gate_results=()
gate_times=()

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
green() { printf '\033[1;32m%s\033[0m\n' "$*"; }
red()   { printf '\033[1;31m%s\033[0m\n' "$*"; }

run_gate() {
    local name="$1"; shift
    local start=$SECONDS rc=0
    bold ""
    bold "━━━ ci-local gate: ${name} ━━━"
    "$@" || rc=$?
    gate_names+=("$name")
    gate_times+=($((SECONDS - start)))
    if [ "$rc" -eq 0 ]; then
        gate_results+=("pass")
    else
        gate_results+=("FAIL")
        red "gate '${name}' failed (exit ${rc})"
    fi
}

skip_gate() {
    gate_names+=("$1")
    gate_results+=("skip")
    gate_times+=(0)
}

# ── Gates (cheap first; cluster-backed last) ─────────────────────────────

gate_unit()   { make test; }

gate_charts() {
    helm repo add traefik https://traefik.github.io/charts --force-update
    helm repo add jetstack https://charts.jetstack.io --force-update
    helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ --force-update
    helm repo update
    make test-charts
}

gate_lint() {
    go vet ./... || return 1
    echo "staticcheck ${STATICCHECK_VERSION} (pin from ci.yml)"
    go run "honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}" ./...
}

gate_ui() {
    (cd ui && npm ci && npm run check && npm run build)
}

gate_integration() { make test-integration; }

gate_ui_e2e() {
    local rc=0 preexisting=false
    # A live dev cluster belongs to the developer — reconverge it via dev-up
    # but do NOT tear it down afterwards. Only clusters this gate created get
    # dev-downed.
    if k3d cluster list 2>/dev/null | grep -q "^${DEV_CLUSTER:-mortise-dev}\b"; then
        preexisting=true
        echo "note: dev cluster already exists — reusing it and leaving it up afterwards"
    fi
    # CI=true mirrors the hosted runners: Playwright's config keys retries
    # (2 vs 0) and workers (4 vs 8) off process.env.CI, so this is the
    # CI-parity run the push gate wants. For flake-hunting, run the suite
    # directly WITHOUT CI=true — zero retries surfaces what parity hides.
    make dev-up && CI=true make test-e2e || rc=$?
    if ! $preexisting; then
        make dev-down || true
    fi
    return "$rc"
}

run_gate "unit+envtest"    gate_unit
run_gate "helm lint"       gate_charts
run_gate "vet+staticcheck" gate_lint
run_gate "ui build"        gate_ui
if $SKIP_CLUSTER_GATES; then
    skip_gate "integration"
    skip_gate "ui-e2e"
else
    run_gate "integration" gate_integration
    run_gate "ui-e2e"      gate_ui_e2e
fi

# ── Summary ──────────────────────────────────────────────────────────────

bold ""
bold "━━━ ci-local summary ━━━"
failed=0
for i in "${!gate_names[@]}"; do
    line="$(printf '%-16s %-4s %4ss' "${gate_names[$i]}" "${gate_results[$i]}" "${gate_times[$i]}")"
    case "${gate_results[$i]}" in
        pass) green "$line" ;;
        skip) echo  "$line" ;;
        *)    red   "$line"; failed=$((failed + 1)) ;;
    esac
done
bold ""
if [ "$failed" -gt 0 ]; then
    red "ci-local: ${failed} gate(s) failed — do not push"
    exit 1
fi
if $SKIP_CLUSTER_GATES; then
    green "ci-local: all non-cluster gates green (integration + ui-e2e skipped — run full before pushing)"
else
    green "ci-local: all gates green — safe to push"
fi
