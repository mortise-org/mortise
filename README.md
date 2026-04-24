# Mortise

A self-hosted, Railway-style deploy platform for Kubernetes.

Connect a git repo or pick a pre-built image: Mortise handles builds, deploys,
domains, TLS, environment variables, volumes, preview environments, and
service-to-service bindings. Kubernetes is fully abstracted away from the user.

## Install

Two paths: pick one, then run through the [Quickstart](docs/quickstart.md)
to create an admin account and deploy your first app.

### Quick install (no cluster yet)

One command. Installs k3s on Linux or k3d on macOS/Windows, then helm-installs
the full stack and port-forwards the UI to `localhost:8090`.

```bash
# macOS (requires Docker Desktop)
curl -fsSL https://mortise.me/install | bash

# Linux (requires sudo; installs k3s natively)
curl -fsSL https://mortise.me/install | bash

# Windows 10+ (requires Docker Desktop)
iwr -useb https://mortise.me/install.ps1 | iex
```

### Helm (existing cluster)

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace
```

`mortise` is the batteries-included chart: operator + Traefik + cert-manager
+ BuildKit + OCI registry. Traefik is a default dependency, not a runtime
hard requirement: you can disable it and use any ingress controller by setting
`traefik.enabled=false` and `mortise-core.operator.ingressClassName=<class>`.
For operator-only (BYO ingress / cert-manager / registry / buildkit), use
`mortise/mortise-core` instead.

Full flow with prereqs, toggles, upgrade, and uninstall:
**[docs/install.md](docs/install.md)**.

## What's Included

| Component | Purpose |
|-----------|---------|
| **Operator** | k8s controller that reconciles App/Project/GitProvider CRDs |
| **REST API** | Project, app, env var, deploy, rollback, domain management |
| **SvelteKit UI** | Canvas-based dashboard, app drawer, env var editor, settings |
| **CLI** | `mortise login`, `mortise app create`, `mortise deploy`, `mortise env` |
| **Helm Charts** | `mortise` (batteries-included) and `mortise-core` (operator only), published at `https://mortise-org.github.io/mortise` |

## Features

- **Git-source deploys**: connect GitHub/GitLab/Gitea, auto-build via Railpack or Dockerfile
- **Image deploys**: deploy any container image
- **Docker Compose templates**: one-click Supabase stack (6 services), or bring your own compose file
- **Environment variables**: Secret-based storage, masked values, source badges, multi-line paste, raw editor
- **Shared variables**: project-level vars shared across all services
- **Auto-domain routing**: public apps get `{app}.{platformDomain}` automatically
- **Bindings**: bind services together, auto-inject `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_URL`
- **Per-environment namespaces**: production, staging, preview each get their own k8s namespace
- **CrashLoop detection**: surfaces pod crash reasons in the UI
- **GitHub device flow**: one-click git provider connection from settings

## Architecture

One operator, one Helm chart. No addons, no plug-in protocol.

```
User -> UI / CLI / API
         |
    Mortise Operator (controller-runtime)
         |
    k8s primitives: Deployment, Service, Ingress, Secret, ConfigMap, PVC
```

External capabilities (OIDC, monitoring, backups, external secrets) are upstream
projects: Mortise coexists with them through standard k8s primitives.

See [ARCHITECTURE.md](ARCHITECTURE.md) for system diagrams.

## Docs

**For users:**

| Doc | Purpose |
|-----|---------|
| [Quickstart](docs/quickstart.md) | Zero to deployed app in 10 minutes |
| [Install](docs/install.md) | Helm install, values reference, uninstall |
| [Cluster setup](docs/cluster-setup.md) | Getting a k8s cluster running (k3d, k3s, EKS, GKE, AKS) |
| [Configuration](docs/configuration.md) | Domain, git providers, HTTPS, storage, environments |
| [API quickstart](docs/api-quickstart.md) | End-to-end API workflow with curl |
| [API endpoints](docs/api-endpoints.md) | Endpoint-by-endpoint implementation reference |
| [OpenAPI spec](docs/openapi.yaml) | Machine-readable OpenAPI 3.0 spec |
| [Systems overview](docs/systems-overview.md) | Runtime architecture, controllers, and reconciliation |

**For contributors:**

| Doc | Purpose |
|-----|---------|
| [SPEC.md](SPEC.md) | Full product and engineering spec |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System diagrams and interface contracts |
| [CLAUDE.md](CLAUDE.md) | Project conventions and architecture rules |
| [DEVELOPMENT.md](DEVELOPMENT.md) | Local dev loop, tests, troubleshooting |
| [RELEASING.md](RELEASING.md) | How to cut a release; image/chart conventions |


## Integration Recipes

- [External CI](docs/recipes/external-ci.md): GitHub Actions / GitLab CI deploy via webhook
- [OIDC](docs/recipes/oidc.md): SSO with Authentik, Keycloak, Okta, Google
- [Monitoring](docs/recipes/monitoring.md): Prometheus + Grafana
- [External Secrets](docs/recipes/external-secrets.md): Vault, AWS SM, GCP SM via ESO
- [Backup](docs/recipes/backup.md): Velero backup and restore
- [Cloudflare Tunnel](docs/recipes/cloudflare-tunnel.md): access without a public IP

## Development

```bash
# Dev cluster with live reload
make dev-up
make dev-reload   # rebuild and redeploy without recreating cluster

# Tests
make test              # unit + envtest
go test ./internal/... # specific packages
```

See [DEVELOPMENT.md](DEVELOPMENT.md) for the full guide.

## Status

Pre-v1. Core platform is functional: apps deploy, routes work, env vars are
managed, bindings auto-inject, templates work.
