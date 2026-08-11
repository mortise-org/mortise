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
K3D_RUNTIME_ULIMIT="${K3D_RUNTIME_ULIMIT:-nofile=1048576:1048576}"
# Single helm --wait budget, matching HELM_WAIT_TIMEOUT in the Makefile (#443).
HELM_WAIT_TIMEOUT="${HELM_WAIT_TIMEOUT:-600s}"

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
            | grep -v "^$" | wc -l || true)
        if [ "$not_ready" -eq 0 ] && [ "$(kubectl get pods -n "$ns" --no-headers 2>/dev/null | wc -l || true)" -gt 0 ]; then
            return 0
        fi
        sleep 3
    done
    return 1
}

# ── Setup ────────────────────────────────────────────────────────────────

info "Creating k3d cluster ${CLUSTER_NAME}..."
k3d cluster delete "$CLUSTER_NAME" 2>/dev/null || true
k3d cluster create --config "${SCRIPT_DIR}/k3d-config.yaml" --runtime-ulimit "${K3D_RUNTIME_ULIMIT}" --wait

info "Building operator image..."
# --target is load-bearing: the Dockerfile's final stage is the observer
# binary, so an untargeted build tags the wrong binary as the operator
# image and the mortise deployment crashloops.
docker build --target operator -t "$CHART_IMG" "$REPO_ROOT" -q
# k3d image import intermittently fails staging its tar into the node
# ("ctr: open /k3d/images/...tar: no such file or directory") on some hosts,
# including GitHub runners. Retry a few times with short backoff; a genuine
# failure (e.g. disk pressure) still exhausts the retries and aborts.
import_attempts=3
for attempt in $(seq 1 "$import_attempts"); do
    if k3d image import "$CHART_IMG" -c "$CLUSTER_NAME"; then
        break
    fi
    if [ "$attempt" -eq "$import_attempts" ]; then
        fatal "k3d image import failed after ${import_attempts} attempts"
    fi
    info "k3d image import attempt ${attempt} failed; retrying in 5s..."
    sleep 5
done

info "Building chart dependencies..."
# The umbrella chart's external subcharts (traefik, cert-manager,
# metrics-server) are fetched by `helm dependency build`, which needs their
# repos configured. A dev box usually has them from prior `make dev-up` runs,
# but a clean runner does not — so add them explicitly rather than relying on
# ambient helm state. mortise-core is vendored as a .tgz and needs no repo.
helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1 || true
helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ >/dev/null 2>&1 || true
helm repo update >/dev/null
# A dependency-build failure must abort — silently proceeding produces a
# "missing in charts/ directory" install failure that hides the real cause.
helm dependency build "${REPO_ROOT}/charts/mortise" || fatal "helm dependency build failed; chart subcharts are missing"

# ── Test 1: Umbrella chart deploys all components ────────────────────────

info "Test 1: Umbrella chart — full deployment"

helm upgrade --install mortise "${REPO_ROOT}/charts/mortise" \
    --namespace "$NAMESPACE" --create-namespace \
    --set mortise-core.image.repository=mortise \
    --set mortise-core.image.tag=chart-test \
    --set mortise-core.image.pullPolicy=Never \
    --set platformConfig.domain=test.mortise.local \
    --wait --timeout "$HELM_WAIT_TIMEOUT" 2>&1 || { fail "Umbrella chart install"; fatal "Cannot continue without chart installed"; }

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

# Traefik deployment name is release-prefixed by the subchart. It deploys
# into the deps namespace, not the release namespace.
traefik_dep=$(kubectl get deployment -n "$DEPS_NAMESPACE" -l "app.kubernetes.io/name=traefik" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$traefik_dep" ] && wait_for_deployment "$DEPS_NAMESPACE" "$traefik_dep" 120; then
    info "  ✓ $DEPS_NAMESPACE/$traefik_dep (traefik) is ready"
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
    --set platformConfig.domain=test.mortise.local \
    --set buildkit.enabled=false \
    --wait --timeout "$HELM_WAIT_TIMEOUT" 2>&1 || { fail "Condition toggle upgrade"; }

sleep 5
buildkit_exists=$(kubectl get deployment -n "$DEPS_NAMESPACE" buildkitd --no-headers 2>/dev/null | wc -l || true)
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
    --set platformConfig.domain=test.mortise.local \
    --wait --timeout "$HELM_WAIT_TIMEOUT" 2>&1 || { fail "Re-enable upgrade"; }

# ── Test 3b: registry-proxy survives --reuse-values (#446) ──────────────
# The #446 report was a registry-proxy DaemonSet that vanished across an
# upgrade. Assert the full lifecycle: present after fresh install, STILL
# present after a bare `--reuse-values` upgrade (no --set flags — exactly
# the upgrade shape that lost it), gone when registry.enabled=false, and
# back on re-enable.

info "Test 3b: registry-proxy survives --reuse-values"

registry_proxy_exists() {
    kubectl get daemonset -n "$DEPS_NAMESPACE" registry-proxy --no-headers 2>/dev/null | wc -l
}

if [ "$(registry_proxy_exists)" -eq 1 ]; then
    pass "registry-proxy DaemonSet present after install"
else
    fail "registry-proxy DaemonSet missing after install"
fi

helm upgrade mortise "${REPO_ROOT}/charts/mortise" \
    --namespace "$NAMESPACE" \
    --reuse-values \
    --wait --timeout "$HELM_WAIT_TIMEOUT" 2>&1 || { fail "--reuse-values upgrade"; }
sleep 5
if [ "$(registry_proxy_exists)" -eq 1 ]; then
    pass "registry-proxy survives --reuse-values upgrade"
else
    fail "registry-proxy lost by --reuse-values upgrade (#446)"
fi

helm upgrade mortise "${REPO_ROOT}/charts/mortise" \
    --namespace "$NAMESPACE" \
    --reuse-values \
    --set registry.enabled=false \
    --wait --timeout "$HELM_WAIT_TIMEOUT" 2>&1 || { fail "registry disable upgrade"; }
sleep 5
if [ "$(registry_proxy_exists)" -eq 0 ]; then
    pass "registry-proxy removed when registry.enabled=false"
else
    fail "registry-proxy still present after registry.enabled=false"
fi

helm upgrade mortise "${REPO_ROOT}/charts/mortise" \
    --namespace "$NAMESPACE" \
    --reuse-values \
    --set registry.enabled=true \
    --wait --timeout "$HELM_WAIT_TIMEOUT" 2>&1 || { fail "registry re-enable upgrade"; }
sleep 5
if [ "$(registry_proxy_exists)" -eq 1 ]; then
    pass "registry-proxy back after re-enable"
else
    fail "registry-proxy missing after re-enable"
fi

# ── Test 4: mortise-core standalone ─────────────────────────────────────

info "Test 4: mortise-core standalone"

helm uninstall mortise -n "$NAMESPACE" --wait >/dev/null 2>&1 || true
# Clean up namespaces the umbrella created.
kubectl delete namespace "$DEPS_NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
sleep 5

helm upgrade --install mortise-core "${REPO_ROOT}/charts/mortise-core" \
    --namespace "$NAMESPACE" --create-namespace \
    --set image.repository=mortise \
    --set image.tag=chart-test \
    --set image.pullPolicy=Never \
    --wait --timeout "$HELM_WAIT_TIMEOUT" 2>&1 || { fail "mortise-core standalone install"; }

if wait_for_deployment "$NAMESPACE" "mortise" 60; then
    pass "mortise-core standalone — operator runs without infrastructure"
else
    fail "mortise-core standalone — operator not ready"
fi

# Clean up for install script test.
helm uninstall mortise-core -n "$NAMESPACE" --wait >/dev/null 2>&1 || true
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
