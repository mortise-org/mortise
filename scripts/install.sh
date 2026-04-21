#!/usr/bin/env bash
# quick-mortise: single-command installer for the Mortise PaaS
# Usage: curl -fsSL https://mortise.dev/install | sh
#
# Installs k3s (or k3d on macOS), Helm, and runs `helm install mortise`.
# The Helm chart handles everything else: operator, Traefik, cert-manager,
# BuildKit, registry, and default PlatformConfig.
#
# Idempotent — safe to run multiple times.
# Supports: Linux (amd64/arm64), macOS (amd64/arm64)
set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
HELM_VERSION="${HELM_VERSION:-v3.17.3}"
MORTISE_CHART_REPO="${MORTISE_CHART_REPO:-https://mc-meesh.github.io/mortise}"
MORTISE_CHART_VERSION="${MORTISE_CHART_VERSION:-}"
MORTISE_NAMESPACE="mortise-system"

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

detect_build_platform() {
    case "$(uname -m)" in
        x86_64)         BUILD_PLATFORM="linux/amd64" ;;
        aarch64|arm64)  BUILD_PLATFORM="linux/arm64" ;;
        *)              BUILD_PLATFORM="linux/amd64" ;;
    esac
}

# ---------------------------------------------------------------------------
# Step 1: Ensure root/sudo (Linux only)
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
# ---------------------------------------------------------------------------
install_k3s() {
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
    curl -fsSL https://get.k3s.io | $SUDO sh -

    export KUBECONFIG="/etc/rancher/k3s/k3s.yaml"

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

    kubectl wait --for=condition=Ready node --all --timeout=120s
    info "k3s is ready"
}

install_k3d() {
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
# Step 3: Install Helm
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
# Step 4: Install Mortise (everything via one Helm chart)
# ---------------------------------------------------------------------------
install_mortise() {
    local chart_ref="mortise/mortise"
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local local_chart="${script_dir}/../charts/mortise"

    detect_build_platform

    # k3s bundles Traefik — disable the chart's copy to avoid conflicts.
    local traefik_flag="--set traefik.enabled=true"
    if command_exists k3s || (command_exists k3d && k3d cluster list mortise >/dev/null 2>&1); then
        traefik_flag="--set traefik.enabled=false"
        info "k3s/k3d detected — using built-in Traefik, skipping chart Traefik"
    fi

    if [ -f "${local_chart}/Chart.yaml" ]; then
        info "Using local chart at ${local_chart}"
        chart_ref="$local_chart"

        # Build dependencies for local chart.
        helm dependency build "$local_chart" 2>/dev/null || true

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

    info "Installing Mortise..."
    # shellcheck disable=SC2086
    helm upgrade --install mortise "$chart_ref" \
        --namespace "$MORTISE_NAMESPACE" --create-namespace \
        --set mortise-core.image.pullPolicy=IfNotPresent \
        --set platformConfig.buildPlatform="${BUILD_PLATFORM}" \
        $traefik_flag \
        --wait --timeout 300s \
        $chart_version_flag

    info "Mortise installed"
}

# ---------------------------------------------------------------------------
# Step 5: Install CLI
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
            if ! echo "$PATH" | grep -q "$install_dir"; then
                info "Add ${install_dir} to your PATH"
            fi
        fi
    else
        info "Skipping CLI install (Go not available or not running from repo)"
        info "Install later: go install github.com/MC-Meesh/mortise/cmd/cli@latest"
    fi
}

# ---------------------------------------------------------------------------
# Step 6: Wait and print success
# ---------------------------------------------------------------------------
wait_and_print_success() {
    info "Waiting for Mortise to be fully ready..."
    kubectl -n "$MORTISE_NAMESPACE" rollout status deployment/mortise --timeout=120s

    local node_ip
    node_ip="$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)"
    if [ -z "$node_ip" ]; then
        node_ip="localhost"
    fi

    local pf_port=8090
    pkill -f "port-forward.*svc/mortise" >/dev/null 2>&1 || true
    kubectl port-forward -n "$MORTISE_NAMESPACE" svc/mortise "${pf_port}:80" >/dev/null 2>&1 &

    printf '\n'
    printf '\033[1;32m============================================\033[0m\n'
    printf '\033[1;32m  Mortise is installed and running!\033[0m\n'
    printf '\033[1;32m============================================\033[0m\n'
    printf '\n'
    printf '  Mortise UI         : http://localhost:%s\n' "$pf_port"
    printf '  Namespace          : %s\n' "$MORTISE_NAMESPACE"
    printf '\n'
    printf '  Port-forward is running in the background.\n'
    printf '  To restart it later: kubectl port-forward -n %s svc/mortise %s:80\n' "$MORTISE_NAMESPACE" "$pf_port"
    printf '\n'
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
    info "Starting Mortise installation..."
    detect_platform
    check_privileges
    install_k3s
    install_helm
    install_mortise
    install_cli
    wait_and_print_success
}

main "$@"
