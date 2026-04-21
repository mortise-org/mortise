#!/usr/bin/env bash
# Chart integration tests — validates that both Helm charts and the install
# script work end-to-end on a real k3d cluster.
#
# Usage: make test-chart-integration
#
# Tests:
#   1. Umbrella chart deploys all components (operator, Traefik, cert-manager,
#      BuildKit, registry) and all pods reach Ready.
#   2. Registry PVC persistence — push a test artifact, kill the pod, verify
#      the artifact survives.
#   3. Condition toggles — reinstall with BuildKit disabled, verify it's gone.
#   4. mortise-core standalone — install operator-only chart, verify it runs
#      without any infrastructure dependencies.
#   5. Install script — run scripts/install.sh against the cluster, verify
#      everything comes up as if a real user ran it.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${SCRIPT_DIR}/../.."
CLUSTER_NAME="mortise-chart"
NAMESPACE="mortise-system"
DEPS_NAMESPACE="mortise-deps"
CHART_IMG="mortise:chart-test"

passed=0
failed=0
test_names=()
test_results=()

# ── Helpers ──────────────────────────────────────────────────────────────

info()  { printf '\033[1;34m[chart-test]\033[0m %s\n' "$*"; }
pass()  { printf '\033[1;32m[PASS]\033[0m %s\n' "$*"; passed=$((passed + 1)); test_names+=("$*"); test_results+=("pass"); }
fail()  { printf '\033[1;31m[FAIL]\033[0m %s\n' "$*"; failed=$((failed + 1)); test_names+=("$*"); test_results+=("fail"); }
fatal() { printf '\033[1;31m[FATAL]\033[0m %s\n' "$*" >&2; cleanup; exit 1; }

cleanup() {
    info "Cleaning up..."
    kill "$PF_PID" 2>/dev/null || true
    k3d cluster delete "$CLUSTER_NAME" 2>/dev/null || true
}
trap cleanup EXIT
PF_PID=""

wait_for_deployment() {
    local ns="$1" name="$2" timeout="${3:-120}"
    kubectl -n "$ns" rollout status "deployment/$name" --timeout="${timeout}s" 2>/dev/null
}

wait_for_pods_ready() {
    local ns="$1" timeout="${2:-180}"
    local deadline=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$deadline" ]; do
        local not_ready
        not_ready=$(kubectl get pods -n "$ns" --no-headers 2>/dev/null \
            | grep -v "Running\|Completed\|Succeeded" \
            | grep -v "^$" | wc -l)
        if [ "$not_ready" -eq 0 ] && [ "$(kubectl get pods -n "$ns" --no-headers 2>/dev/null | wc -l)" -gt 0 ]; then
            return 0
        fi
        sleep 3
    done
    return 1
}

# ── Setup ────────────────────────────────────────────────────────────────

info "Creating k3d cluster ${CLUSTER_NAME}..."
k3d cluster delete "$CLUSTER_NAME" 2>/dev/null || true
k3d cluster create --config "${SCRIPT_DIR}/k3d-config.yaml" --wait

info "Building operator image..."
docker build -t "$CHART_IMG" "$REPO_ROOT" -q
k3d image import "$CHART_IMG" -c "$CLUSTER_NAME"

info "Building chart dependencies..."
helm dependency build "${REPO_ROOT}/charts/mortise" 2>/dev/null || true

# ── Test 1: Umbrella chart deploys all components ────────────────────────

info "Test 1: Umbrella chart — full deployment"

helm upgrade --install mortise "${REPO_ROOT}/charts/mortise" \
    --namespace "$NAMESPACE" --create-namespace \
    --set mortise-core.image.repository=mortise \
    --set mortise-core.image.tag=chart-test \
    --set mortise-core.image.pullPolicy=Never \
    --set platformConfig.domain="" \
    --wait --timeout 300s 2>&1 || { fail "Umbrella chart install"; fatal "Cannot continue without chart installed"; }

# Verify each component deployment exists and is available.
components_ok=true
for dep in mortise; do
    if wait_for_deployment "$NAMESPACE" "$dep" 120; then
        info "  ✓ $NAMESPACE/$dep is ready"
    else
        info "  ✗ $NAMESPACE/$dep not ready"
        components_ok=false
    fi
done

# Traefik deployment name is release-prefixed by the subchart.
traefik_dep=$(kubectl get deployment -n "$NAMESPACE" -l "app.kubernetes.io/name=traefik" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$traefik_dep" ] && wait_for_deployment "$NAMESPACE" "$traefik_dep" 120; then
    info "  ✓ $NAMESPACE/$traefik_dep (traefik) is ready"
else
    info "  ✗ Traefik deployment not found or not ready"
    components_ok=false
fi

# cert-manager deploys to its own or the release namespace depending on chart config.
cm_dep=$(kubectl get deployment -A -l "app.kubernetes.io/name=cert-manager,app.kubernetes.io/component=controller" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
cm_ns=$(kubectl get deployment -A -l "app.kubernetes.io/name=cert-manager,app.kubernetes.io/component=controller" -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null || true)
if [ -n "$cm_dep" ] && wait_for_deployment "$cm_ns" "$cm_dep" 120; then
    info "  ✓ $cm_ns/$cm_dep (cert-manager) is ready"
else
    info "  ✗ cert-manager deployment not found or not ready"
    components_ok=false
fi

for dep in registry buildkitd; do
    if wait_for_deployment "$DEPS_NAMESPACE" "$dep" 180; then
        info "  ✓ $DEPS_NAMESPACE/$dep is ready"
    else
        info "  ✗ $DEPS_NAMESPACE/$dep not ready"
        components_ok=false
    fi
done

if $components_ok; then
    pass "Umbrella chart deploys all components"
else
    fail "Umbrella chart deploys all components"
    kubectl get pods -A --no-headers 2>/dev/null | grep -v "Running\|Completed" || true
fi

# Verify PlatformConfig was created.
if kubectl get platformconfigs.mortise.mortise.dev platform >/dev/null 2>&1; then
    pass "PlatformConfig auto-created"
else
    fail "PlatformConfig auto-created"
fi

# Verify PVCs were created (not emptyDir).
registry_pvc=$(kubectl get pvc -n "$DEPS_NAMESPACE" registry-data --no-headers 2>/dev/null | wc -l)
buildkit_pvc=$(kubectl get pvc -n "$DEPS_NAMESPACE" buildkitd-data --no-headers 2>/dev/null | wc -l)
if [ "$registry_pvc" -eq 1 ] && [ "$buildkit_pvc" -eq 1 ]; then
    pass "PVCs created for registry and BuildKit"
else
    fail "PVCs created for registry and BuildKit (registry=$registry_pvc, buildkit=$buildkit_pvc)"
fi

# ── Test 2: Registry PVC persistence ────────────────────────────────────

info "Test 2: Registry PVC persistence"

# Port-forward to the registry.
kubectl port-forward -n "$DEPS_NAMESPACE" svc/registry 15000:5000 >/dev/null 2>&1 &
PF_PID=$!
sleep 2

# Push a test blob via the OCI distribution API.
# Create a small test layer.
TEST_CONTENT="mortise-chart-test-$(date +%s)"
TEST_DIGEST="sha256:$(echo -n "$TEST_CONTENT" | sha256sum | awk '{print $1}')"
TEST_SIZE=${#TEST_CONTENT}

# Initiate upload, push blob, create manifest.
push_ok=true
upload_url=$(curl -sf -X POST "http://localhost:15000/v2/chart-test/blobs/uploads/" \
    -D - -o /dev/null 2>/dev/null | grep -i "^location:" | tr -d '\r' | awk '{print $2}') || push_ok=false

if $push_ok && [ -n "$upload_url" ]; then
    # Handle relative URLs.
    if [[ "$upload_url" == /* ]]; then
        upload_url="http://localhost:15000${upload_url}"
    fi
    curl -sf -X PUT "${upload_url}&digest=${TEST_DIGEST}" \
        -H "Content-Type: application/octet-stream" \
        -d "$TEST_CONTENT" >/dev/null 2>/dev/null || push_ok=false
fi

if $push_ok; then
    # Create a minimal OCI manifest referencing our blob.
    MANIFEST="{\"schemaVersion\":2,\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"config\":{\"mediaType\":\"application/vnd.oci.image.config.v1+json\",\"digest\":\"${TEST_DIGEST}\",\"size\":${TEST_SIZE}},\"layers\":[]}"
    curl -sf -X PUT "http://localhost:15000/v2/chart-test/manifests/latest" \
        -H "Content-Type: application/vnd.oci.image.manifest.v1+json" \
        -d "$MANIFEST" >/dev/null 2>/dev/null || push_ok=false
fi

# Stop port-forward.
kill "$PF_PID" 2>/dev/null || true
PF_PID=""
sleep 1

if ! $push_ok; then
    fail "Registry PVC persistence (could not push test artifact)"
else
    # Delete the registry pod and wait for it to come back.
    info "  Deleting registry pod..."
    kubectl delete pod -n "$DEPS_NAMESPACE" -l app.kubernetes.io/name=registry --wait=false 2>/dev/null
    sleep 3
    wait_for_deployment "$DEPS_NAMESPACE" "registry" 120 || { fail "Registry PVC persistence (pod did not come back)"; }

    # Port-forward again and check if the manifest survived.
    kubectl port-forward -n "$DEPS_NAMESPACE" svc/registry 15000:5000 >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3

    if curl -sf "http://localhost:15000/v2/chart-test/manifests/latest" \
        -H "Accept: application/vnd.oci.image.manifest.v1+json" >/dev/null 2>/dev/null; then
        pass "Registry PVC persistence — data survives pod restart"
    else
        fail "Registry PVC persistence — data lost after pod restart"
    fi

    kill "$PF_PID" 2>/dev/null || true
    PF_PID=""
fi

# ── Test 3: Condition toggles ───────────────────────────────────────────

info "Test 3: Condition toggles — disable BuildKit"

helm upgrade mortise "${REPO_ROOT}/charts/mortise" \
    --namespace "$NAMESPACE" \
    --set mortise-core.image.repository=mortise \
    --set mortise-core.image.tag=chart-test \
    --set mortise-core.image.pullPolicy=Never \
    --set buildkit.enabled=false \
    --wait --timeout 120s 2>&1 >/dev/null || { fail "Condition toggle upgrade"; }

sleep 5
buildkit_exists=$(kubectl get deployment -n "$DEPS_NAMESPACE" buildkitd --no-headers 2>/dev/null | wc -l)
if [ "$buildkit_exists" -eq 0 ]; then
    pass "Condition toggle — BuildKit disabled removes deployment"
else
    fail "Condition toggle — BuildKit deployment still exists after disable"
fi

# Re-enable for next tests.
helm upgrade mortise "${REPO_ROOT}/charts/mortise" \
    --namespace "$NAMESPACE" \
    --set mortise-core.image.repository=mortise \
    --set mortise-core.image.tag=chart-test \
    --set mortise-core.image.pullPolicy=Never \
    --wait --timeout 120s 2>&1 >/dev/null

# ── Test 4: mortise-core standalone ─────────────────────────────────────

info "Test 4: mortise-core standalone"

helm uninstall mortise -n "$NAMESPACE" --wait 2>&1 >/dev/null || true
# Clean up namespaces the umbrella created.
kubectl delete namespace "$DEPS_NAMESPACE" --ignore-not-found --wait=false 2>/dev/null || true
sleep 5

helm upgrade --install mortise-core "${REPO_ROOT}/charts/mortise-core" \
    --namespace "$NAMESPACE" --create-namespace \
    --set image.repository=mortise \
    --set image.tag=chart-test \
    --set image.pullPolicy=Never \
    --wait --timeout 120s 2>&1 >/dev/null || { fail "mortise-core standalone install"; }

if wait_for_deployment "$NAMESPACE" "mortise" 60; then
    pass "mortise-core standalone — operator runs without infrastructure"
else
    fail "mortise-core standalone — operator not ready"
fi

# Clean up for install script test.
helm uninstall mortise-core -n "$NAMESPACE" --wait 2>&1 >/dev/null || true
sleep 3

# ── Test 5: Install script ─────────────────────────────────────────────

info "Test 5: Install script"

# The install script detects the existing k3d cluster and skips k3s install.
# It installs Helm (already present, skips), then runs helm install with the
# umbrella chart. This validates the real user flow.
if bash "${REPO_ROOT}/scripts/install.sh" 2>&1; then
    script_ok=true
else
    script_ok=false
fi

if $script_ok; then
    # Verify the operator is running.
    if wait_for_deployment "$NAMESPACE" "mortise" 120; then
        pass "Install script — operator running"
    else
        fail "Install script — operator not ready after script"
    fi

    # Verify build infrastructure came up.
    infra_ok=true
    for dep in registry buildkitd; do
        if wait_for_deployment "$DEPS_NAMESPACE" "$dep" 120; then
            info "  ✓ $DEPS_NAMESPACE/$dep is ready"
        else
            info "  ✗ $DEPS_NAMESPACE/$dep not ready"
            infra_ok=false
        fi
    done

    if $infra_ok; then
        pass "Install script — build infrastructure deployed"
    else
        fail "Install script — build infrastructure incomplete"
    fi

    # Verify PlatformConfig exists.
    if kubectl get platformconfigs.mortise.mortise.dev platform >/dev/null 2>&1; then
        pass "Install script — PlatformConfig created"
    else
        fail "Install script — PlatformConfig missing"
    fi
else
    fail "Install script — exited with error"
fi

# ── Summary ─────────────────────────────────────────────────────────────

echo ""
echo "============================================"
printf "  Chart integration tests: \033[1;32m%d passed\033[0m" "$passed"
if [ "$failed" -gt 0 ]; then
    printf ", \033[1;31m%d failed\033[0m" "$failed"
fi
echo ""
echo "============================================"

for i in "${!test_names[@]}"; do
    if [ "${test_results[$i]}" = "pass" ]; then
        printf "  \033[1;32m✓\033[0m %s\n" "${test_names[$i]}"
    else
        printf "  \033[1;31m✗\033[0m %s\n" "${test_names[$i]}"
    fi
done
echo ""

if [ "$failed" -gt 0 ]; then
    exit 1
fi
