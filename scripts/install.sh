#!/usr/bin/env bash
# quick-mortise: single-command installer for the Mortise PaaS
# Usage: curl -fsSL https://mortise.dev/install | sh
#
# Idempotent — safe to run multiple times.
# Supports: Linux (amd64/arm64), macOS (amd64/arm64)
set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.17.1}"
HELM_VERSION="${HELM_VERSION:-v3.17.3}"
MORTISE_CHART_REPO="${MORTISE_CHART_REPO:-https://mc-meesh.github.io/mortise}"
MORTISE_CHART_VERSION="${MORTISE_CHART_VERSION:-}"
MORTISE_NAMESPACE="mortise-system"
DEPS_NAMESPACE="mortise-deps"
BUILDKIT_IMAGE="moby/buildkit:v0.29.0"
REGISTRY_IMAGE="distribution/distribution:2.8.3"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
info()  { printf '\033[1;34m[mortise]\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m[mortise]\033[0m %s\n' "$*" >&2; }
error() { printf '\033[1;31m[mortise]\033[0m %s\n' "$*" >&2; exit 1; }

command_exists() { command -v "$1" >/dev/null 2>&1; }

# ---------------------------------------------------------------------------
# Step 0: Detect OS + architecture
# ---------------------------------------------------------------------------
detect_platform() {
    OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH="$(uname -m)"

    case "$OS" in
        linux)  ;;
        darwin) ;;
        *)      error "Unsupported OS: $OS" ;;
    esac

    case "$ARCH" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        arm64)   ARCH="arm64" ;;
        *)       error "Unsupported architecture: $ARCH" ;;
    esac

    info "Detected platform: ${OS}/${ARCH}"
}

# Map uname -m to the OCI platform value used in PlatformConfig.
detect_build_platform() {
    case "$(uname -m)" in
        x86_64)         BUILD_PLATFORM="linux/amd64" ;;
        aarch64|arm64)  BUILD_PLATFORM="linux/arm64" ;;
        *)              BUILD_PLATFORM="linux/amd64" ;;
    esac
}

# ---------------------------------------------------------------------------
# Step 1: Ensure root/sudo (Linux only — macOS uses Docker Desktop + k3d)
# ---------------------------------------------------------------------------
check_privileges() {
    if [ "$OS" = "darwin" ]; then
        SUDO=""
        return
    fi

    if [ "$(id -u)" -ne 0 ]; then
        if command_exists sudo; then
            SUDO="sudo"
            info "Running with sudo"
        else
            error "This script must be run as root or with sudo available"
        fi
    else
        SUDO=""
    fi
}

# ---------------------------------------------------------------------------
# Step 2: Install Kubernetes
#   Linux  — k3s (native)
#   macOS  — k3d (k3s-in-Docker, requires Docker Desktop)
# ---------------------------------------------------------------------------
install_k3s() {
    # If kubectl is available and a cluster is reachable, assume an existing
    # Kubernetes cluster and skip installation entirely.
    if command_exists kubectl && kubectl cluster-info >/dev/null 2>&1; then
        info "Existing Kubernetes cluster detected, skipping k3s/k3d install"
        return
    fi

    if [ "$OS" = "darwin" ]; then
        install_k3d
    else
        install_k3s_native
    fi
}

install_k3s_native() {
    if command_exists k3s; then
        info "k3s is already installed, skipping"
        export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"
        return
    fi

    info "Installing k3s..."
    # Do NOT disable Traefik — k3s's built-in Traefik serves as the ingress
    # controller in a quick-mortise install.
    curl -fsSL https://get.k3s.io | $SUDO sh -

    export KUBECONFIG="/etc/rancher/k3s/k3s.yaml"

    # Make kubeconfig readable for the current user if running via sudo.
    if [ -n "${SUDO:-}" ]; then
        $SUDO chmod 600 "$KUBECONFIG"
    fi

    info "Waiting for k3s to be ready..."
    local retries=60
    while [ "$retries" -gt 0 ]; do
        if kubectl get nodes >/dev/null 2>&1; then
            break
        fi
        retries=$((retries - 1))
        sleep 2
    done

    if [ "$retries" -eq 0 ]; then
        error "Timed out waiting for k3s to become ready"
    fi

    # Wait for the node to be Ready.
    kubectl wait --for=condition=Ready node --all --timeout=120s
    info "k3s is ready"
}

install_k3d() {
    # Require Docker Desktop on macOS — k3d runs k3s inside Docker containers.
    if ! docker info >/dev/null 2>&1; then
        error "Docker Desktop is required on macOS. Install from https://docker.com/products/docker-desktop and ensure it is running."
    fi

    if ! command_exists k3d; then
        info "Installing k3d..."
        if command_exists brew; then
            brew install k3d
        else
            curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
        fi
    else
        info "k3d is already installed, skipping"
    fi

    # Create cluster if it doesn't already exist.
    if k3d cluster list mortise >/dev/null 2>&1; then
        info "k3d cluster 'mortise' already exists, skipping creation"
    else
        info "Creating k3d cluster 'mortise'..."
        k3d cluster create mortise \
            --port "80:80@loadbalancer" \
            --port "443:443@loadbalancer" \
            --wait
    fi

    info "Waiting for k3d cluster to be ready..."
    kubectl wait --for=condition=Ready node --all --timeout=120s
    info "k3d cluster is ready"
}

# ---------------------------------------------------------------------------
# Step 3: Install Helm (if not present)
# ---------------------------------------------------------------------------
install_helm() {
    if command_exists helm; then
        info "Helm is already installed, skipping"
        return
    fi

    info "Installing Helm ${HELM_VERSION}..."
    curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | \
        DESIRED_VERSION="$HELM_VERSION" bash
    info "Helm installed"
}

# ---------------------------------------------------------------------------
# Step 4: Install cert-manager
# ---------------------------------------------------------------------------
install_cert_manager() {
    if kubectl get namespace cert-manager >/dev/null 2>&1; then
        info "cert-manager namespace exists, skipping install"
    else
        info "Installing cert-manager ${CERT_MANAGER_VERSION}..."
        kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
    fi

    info "Waiting for cert-manager to be ready..."
    kubectl -n cert-manager rollout status deployment/cert-manager --timeout=120s
    kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=120s
    kubectl -n cert-manager rollout status deployment/cert-manager-cainjector --timeout=120s
    info "cert-manager is ready"
}

# ---------------------------------------------------------------------------
# Step 5: Deploy build infrastructure (BuildKit + OCI registry)
# ---------------------------------------------------------------------------
deploy_build_infra() {
    info "Creating ${DEPS_NAMESPACE} namespace..."
    kubectl create namespace "$DEPS_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

    info "Deploying OCI registry..."
    kubectl apply -f - <<'REGISTRY_EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
  namespace: mortise-deps
  labels:
    app.kubernetes.io/name: registry
    app.kubernetes.io/managed-by: quick-mortise
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: registry
  template:
    metadata:
      labels:
        app.kubernetes.io/name: registry
    spec:
      containers:
        - name: registry
          image: distribution/distribution:2.8.3
          imagePullPolicy: IfNotPresent
          env:
            - name: REGISTRY_HTTP_ADDR
              value: "0.0.0.0:5000"
            - name: REGISTRY_STORAGE_DELETE_ENABLED
              value: "true"
          ports:
            - name: http
              containerPort: 5000
          readinessProbe:
            httpGet:
              path: /v2/
              port: 5000
            initialDelaySeconds: 2
            periodSeconds: 2
            failureThreshold: 30
          volumeMounts:
            - name: data
              mountPath: /var/lib/registry
      volumes:
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: registry
  namespace: mortise-deps
  labels:
    app.kubernetes.io/managed-by: quick-mortise
spec:
  selector:
    app.kubernetes.io/name: registry
  ports:
    - name: http
      port: 5000
      targetPort: 5000
REGISTRY_EOF

    info "Deploying BuildKit..."
    kubectl apply -f - <<'BUILDKIT_EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: buildkitd-config
  namespace: mortise-deps
  labels:
    app.kubernetes.io/managed-by: quick-mortise
data:
  buildkitd.toml: |
    debug = false
    [grpc]
      address = ["tcp://0.0.0.0:1234"]
    [registry."registry.mortise-deps.svc:5000"]
      http = true
      insecure = true
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: buildkitd
  namespace: mortise-deps
  labels:
    app.kubernetes.io/name: buildkitd
    app.kubernetes.io/managed-by: quick-mortise
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: buildkitd
  template:
    metadata:
      labels:
        app.kubernetes.io/name: buildkitd
    spec:
      containers:
        - name: buildkitd
          image: moby/buildkit:v0.29.0
          imagePullPolicy: IfNotPresent
          args:
            - --addr
            - tcp://0.0.0.0:1234
            - --config
            - /etc/buildkit/buildkitd.toml
          readinessProbe:
            exec:
              command: ["buildctl", "--addr", "tcp://127.0.0.1:1234", "debug", "workers"]
            initialDelaySeconds: 5
            periodSeconds: 3
            failureThreshold: 30
          ports:
            - name: grpc
              containerPort: 1234
          securityContext:
            privileged: true
          volumeMounts:
            - name: config
              mountPath: /etc/buildkit
            - name: data
              mountPath: /var/lib/buildkit
      volumes:
        - name: config
          configMap:
            name: buildkitd-config
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: buildkitd
  namespace: mortise-deps
  labels:
    app.kubernetes.io/managed-by: quick-mortise
spec:
  selector:
    app.kubernetes.io/name: buildkitd
  ports:
    - name: grpc
      port: 1234
      targetPort: 1234
BUILDKIT_EOF

    info "Waiting for registry to be ready..."
    kubectl -n "$DEPS_NAMESPACE" rollout status deployment/registry --timeout=120s
    info "Waiting for BuildKit to be ready..."
    kubectl -n "$DEPS_NAMESPACE" rollout status deployment/buildkitd --timeout=180s
    info "Build infrastructure is ready"
}

# ---------------------------------------------------------------------------
# Step 6: Install Mortise operator via Helm
# ---------------------------------------------------------------------------
install_mortise() {
    # Determine chart source: local chart if running from repo, otherwise Helm repo.
    local chart_ref="mortise/mortise"
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local local_chart="${script_dir}/../charts/mortise"

    if [ -f "${local_chart}/Chart.yaml" ]; then
        info "Using local chart at ${local_chart}"
        chart_ref="$local_chart"

        # Local chart needs the Docker image built and imported into k3d.
        if command -v docker >/dev/null 2>&1 && [ -f "${script_dir}/../Dockerfile" ]; then
            info "Building Mortise Docker image..."
            docker build -t mortise:dev "${script_dir}/.." -q
            k3d image import mortise:dev -c mortise 2>/dev/null || true
        fi
    else
        info "Adding Mortise Helm repository..."
        helm repo add mortise "$MORTISE_CHART_REPO" 2>/dev/null || true
        helm repo update mortise 2>/dev/null || true
    fi

    local chart_version_flag=""
    if [ -n "$MORTISE_CHART_VERSION" ]; then
        chart_version_flag="--version $MORTISE_CHART_VERSION"
    fi

    info "Installing Mortise operator..."
    # shellcheck disable=SC2086
    helm upgrade --install mortise "$chart_ref" \
        --namespace "$MORTISE_NAMESPACE" --create-namespace \
        --set image.pullPolicy=IfNotPresent \
        --set traefik.enabled=false \
        --set cert-manager.enabled=false \
        --set external-dns.enabled=false \
        --set registry.builtin.enabled=false \
        --wait --timeout 120s \
        $chart_version_flag

    info "Mortise operator installed"
}

# ---------------------------------------------------------------------------
# Step 7: Create default PlatformConfig
# ---------------------------------------------------------------------------
create_platform_config() {
    if kubectl get platformconfigs.mortise.mortise.dev platform >/dev/null 2>&1; then
        info "PlatformConfig 'platform' already exists, skipping"
        return
    fi

    detect_build_platform

    info "Creating default PlatformConfig (build platform: ${BUILD_PLATFORM})..."
    kubectl apply -f - <<EOF
apiVersion: mortise.mortise.dev/v1alpha1
kind: PlatformConfig
metadata:
  name: platform
spec:
  domain: mortise.local
  registry:
    url: http://registry.${DEPS_NAMESPACE}.svc:5000
    namespace: mortise
    insecureSkipTLSVerify: true
  build:
    buildkitAddr: tcp://buildkitd.${DEPS_NAMESPACE}.svc:1234
    defaultPlatform: "${BUILD_PLATFORM}"
EOF

    info "PlatformConfig created"
}

# ---------------------------------------------------------------------------
# Step 8: Wait for operator and print success
# ---------------------------------------------------------------------------
wait_and_print_success() {
    info "Waiting for Mortise operator to be ready..."
    kubectl -n "$MORTISE_NAMESPACE" rollout status deployment/mortise --timeout=120s

    # Restart operator so it picks up the PlatformConfig.
    kubectl -n "$MORTISE_NAMESPACE" rollout restart deployment/mortise
    kubectl -n "$MORTISE_NAMESPACE" rollout status deployment/mortise --timeout=60s

    # Determine the access URL. On a real server this will be the node IP;
    # locally it falls back to localhost.
    local node_ip
    node_ip="$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)"
    if [ -z "$node_ip" ]; then
        node_ip="localhost"
    fi

    # The Mortise service listens on port 80 inside the cluster. With k3s's
    # built-in Traefik, it is reachable via an Ingress or port-forward.
    local svc_port
    svc_port="$(kubectl -n "$MORTISE_NAMESPACE" get svc mortise -o jsonpath='{.spec.ports[0].port}' 2>/dev/null || echo "80")"

    printf '\n'
    printf '\033[1;32m============================================\033[0m\n'
    printf '\033[1;32m  Mortise is installed and running!\033[0m\n'
    printf '\033[1;32m============================================\033[0m\n'
    printf '\n'
    printf '  Operator namespace : %s\n' "$MORTISE_NAMESPACE"
    printf '  Build infra        : %s\n' "$DEPS_NAMESPACE"
    printf '  Node IP            : %s\n' "$node_ip"
    printf '\n'
    # Auto-start port-forward so the UI is immediately accessible.
    local pf_port=8090
    pkill -f "port-forward.*svc/mortise" >/dev/null 2>&1 || true
    kubectl port-forward -n "$MORTISE_NAMESPACE" svc/mortise "${pf_port}:80" >/dev/null 2>&1 &

    printf '  Mortise UI         : http://localhost:%s\n' "$pf_port"
    printf '\n'
    printf '  Port-forward is running in the background.\n'
    printf '  To restart it later: kubectl port-forward -n %s svc/mortise %s:80\n' "$MORTISE_NAMESPACE" "$pf_port"
    printf '\n'
    printf '  Docs: https://mortise.dev/docs\n'
    printf '\n'
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
# ---------------------------------------------------------------------------
# Step 8: Install Mortise CLI binary
# ---------------------------------------------------------------------------
install_cli() {
    if command -v mortise >/dev/null 2>&1; then
        info "Mortise CLI is already installed, skipping"
        return
    fi

    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local repo_root="${script_dir}/.."
    local cli_src="${repo_root}/cmd/cli"

    if [ -f "${cli_src}/main.go" ] && command -v go >/dev/null 2>&1; then
        info "Building Mortise CLI from source..."
        local install_dir="/usr/local/bin"
        if [ "$OS" = "darwin" ] || [ "$(id -u)" -ne 0 ]; then
            install_dir="${HOME}/.local/bin"
            mkdir -p "$install_dir"
        fi
        GOBIN="$install_dir" go install "${repo_root}/cmd/cli@latest" 2>/dev/null || \
            (cd "$repo_root" && go build -o "${install_dir}/mortise" ./cmd/cli/)
        if [ -f "${install_dir}/mortise" ]; then
            info "Mortise CLI installed to ${install_dir}/mortise"
            # Ensure it's on PATH
            if ! echo "$PATH" | grep -q "$install_dir"; then
                info "Add ${install_dir} to your PATH"
            fi
        fi
    else
        info "Skipping CLI install (Go not available or not running from repo)"
        info "Install later: go install github.com/MC-Meesh/mortise/cmd/cli@latest"
    fi
}

main() {
    info "Starting Mortise installation..."
    detect_platform
    check_privileges
    install_k3s
    install_helm
    install_cert_manager
    deploy_build_infra
    install_mortise
    create_platform_config
    install_cli
    wait_and_print_success
}

main "$@"
