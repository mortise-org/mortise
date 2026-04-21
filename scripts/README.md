# Mortise Scripts

## install.sh — Quick Mortise Installer

Single-command installer that takes a bare server to a running Mortise PaaS.

### Usage

```bash
# Remote (once published)
curl -fsSL https://mortise.dev/install | sh

# Local
sudo bash scripts/install.sh
```

### What it installs

1. **k3s** — lightweight Kubernetes (skipped if k3s or an existing cluster is detected)
2. **Helm** — package manager (skipped if already present)
3. **cert-manager** — TLS certificate management
4. **OCI registry** — in-cluster image storage (in `mortise-deps` namespace)
5. **BuildKit** — container image builds (in `mortise-deps` namespace)
6. **Mortise operator** — via Helm chart (in `mortise-system` namespace)
7. **PlatformConfig** — default configuration with auto-detected build platform

### Supported platforms

| OS    | Architecture |
|-------|-------------|
| Linux | amd64       |
| Linux | arm64       |
| macOS | amd64       |
| macOS | arm64       |

### Configuration

Override defaults via environment variables:

```bash
CERT_MANAGER_VERSION=v1.17.1   # cert-manager release
HELM_VERSION=v3.17.3           # Helm release
MORTISE_CHART_REPO=https://... # Helm chart repository URL
MORTISE_CHART_VERSION=0.1.0    # Pin a specific chart version
```

### Idempotency

The script is safe to run multiple times. Each step checks whether its component
is already installed and skips if so.

### Notes

- k3s includes Traefik as the ingress controller — the script does not install a separate one
- The script requires root or sudo access (k3s installation needs it)
- Build platform (`linux/amd64` or `linux/arm64`) is auto-detected from `uname -m`
