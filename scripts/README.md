# Mortise Scripts

## install.sh — Quick Mortise Installer (Linux / macOS)

Single-command installer that takes a bare machine to a running Mortise PaaS.

### Usage

```bash
# Remote (once published)
curl -fsSL https://mortise.dev/install | sh

# Local — Linux
sudo bash scripts/install.sh

# Local — macOS (no sudo needed, uses Docker Desktop + k3d)
bash scripts/install.sh
```

### What it installs

1. **Kubernetes** — k3s on Linux (native), k3d on macOS (k3s-in-Docker)
2. **Helm** — package manager (skipped if already present)
3. **cert-manager** — TLS certificate management
4. **OCI registry** — in-cluster image storage (in `mortise-deps` namespace)
5. **BuildKit** — container image builds (in `mortise-deps` namespace)
6. **Mortise operator** — via Helm chart (in `mortise-system` namespace)
7. **PlatformConfig** — default configuration with auto-detected build platform

### Platform-specific behavior

| Platform | Kubernetes | Prerequisites |
|----------|-----------|---------------|
| Linux amd64/arm64 | k3s (native) | Root or sudo |
| macOS amd64/arm64 | k3d (k3s-in-Docker) | Docker Desktop running |

On macOS, the script checks for a running Docker daemon via `docker info` and installs k3d via Homebrew (if available) or direct download. The k3d cluster is created with ports 80 and 443 mapped to the host loadbalancer.

## install.ps1 — Quick Mortise Installer (Windows)

PowerShell installer for Windows. Equivalent to `install.sh` but uses k3d (Docker Desktop required).

### Usage

```powershell
# Run from the repo root
powershell -ExecutionPolicy Bypass -File scripts\install.ps1
```

### Prerequisites

- **Docker Desktop for Windows** must be installed and running
- k3d and Helm are installed automatically (via Chocolatey if available, otherwise direct download)

### What it installs

Same components as the Linux/macOS script:

1. **k3d** — k3s-in-Docker cluster
2. **Helm** — package manager
3. **cert-manager** — TLS certificate management
4. **OCI registry** — in-cluster image storage
5. **BuildKit** — container image builds
6. **Mortise operator** — via Helm chart
7. **PlatformConfig** — default configuration

## Supported platforms

| OS      | Architecture | Script       | Kubernetes |
|---------|-------------|--------------|------------|
| Linux   | amd64       | install.sh   | k3s        |
| Linux   | arm64       | install.sh   | k3s        |
| macOS   | amd64       | install.sh   | k3d        |
| macOS   | arm64       | install.sh   | k3d        |
| Windows | amd64       | install.ps1  | k3d        |
| Windows | arm64       | install.ps1  | k3d        |

## Configuration

Override defaults via environment variables (both scripts):

```bash
CERT_MANAGER_VERSION=v1.17.1   # cert-manager release
HELM_VERSION=v3.17.3           # Helm release
MORTISE_CHART_REPO=https://... # Helm chart repository URL
MORTISE_CHART_VERSION=0.1.0    # Pin a specific chart version
```

## Idempotency

Both scripts are safe to run multiple times. Each step checks whether its component
is already installed/running and skips if so. The k3d cluster check uses `k3d cluster list`
to avoid recreating an existing cluster.

## Notes

- k3s (Linux) includes Traefik as the ingress controller
- k3d clusters also include Traefik via the bundled k3s
- On macOS/Windows, Docker Desktop must be running before executing the installer
- Build platform (`linux/amd64` or `linux/arm64`) is auto-detected from the host architecture
