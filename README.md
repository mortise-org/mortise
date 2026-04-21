# Mortise

A self-hosted, Railway-style deploy platform for Kubernetes.

Connect a git repo or pick a pre-built image — Mortise handles builds, deploys,
domains, TLS, environment variables, volumes, preview environments, and
service-to-service bindings. Kubernetes is fully abstracted away from the user.

## Quick Install

**One command, zero to running in ~80 seconds:**

```bash
# macOS (requires Docker Desktop)
bash scripts/install.sh

# Linux (bare metal / VPS)
curl -fsSL https://raw.githubusercontent.com/MC-Meesh/mortise/main/scripts/install.sh | bash
```

This installs k3s/k3d, cert-manager, BuildKit, a container registry, the Mortise
operator, and the `mortise` CLI. After install:

```bash
mortise up              # start Mortise (port-forward to localhost:8090)
mortise cluster-status  # check health
mortise down            # stop port-forward
mortise destroy         # tear everything down
```

Open **http://localhost:8090** to access the UI.

## What's Included

| Component | Purpose |
|-----------|---------|
| **Operator** | k8s controller that reconciles App/Project/GitProvider CRDs |
| **REST API** | Project, app, env var, deploy, rollback, domain management |
| **SvelteKit UI** | Canvas-based dashboard, app drawer, env var editor, settings |
| **CLI** | `mortise login`, `mortise app create`, `mortise deploy`, `mortise env` |
| **Helm Chart** | Single chart, published at `https://mc-meesh.github.io/mortise` |
| **Install Script** | Zero-to-running installer for Linux, macOS, and Windows |

## Features

- **Git-source deploys** — connect GitHub/GitLab/Gitea, auto-build via Railpack or Dockerfile
- **Image deploys** — deploy any container image
- **Docker Compose templates** — one-click Supabase stack (6 services), or bring your own compose file
- **Environment variables** — Secret-based storage, masked values, source badges, multi-line paste, raw editor
- **Shared variables** — project-level vars shared across all services
- **Auto-domain routing** — public apps get `{app}.{platformDomain}` automatically
- **Bindings** — bind services together, auto-inject `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_URL`
- **Per-environment namespaces** — production, staging, preview each get their own k8s namespace
- **CrashLoop detection** — surfaces pod crash reasons in the UI
- **GitHub device flow** — one-click git provider connection from settings

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
projects — Mortise coexists with them through standard k8s primitives.

See [ARCHITECTURE.md](ARCHITECTURE.md) for system diagrams.

## Docs

| Doc | Purpose |
|-----|---------|
| [SPEC.md](SPEC.md) | Full product and engineering spec |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System diagrams and interface contracts |
| [CLAUDE.md](CLAUDE.md) | Project conventions and architecture rules |
| [DEVELOPMENT.md](DEVELOPMENT.md) | Local dev loop, tests, troubleshooting |
| [PROGRESS.md](PROGRESS.md) | What's implemented vs what's left |

## Integration Recipes

- [External CI](docs/recipes/external-ci.md) — GitHub Actions / GitLab CI deploy via webhook
- [OIDC](docs/recipes/oidc.md) — SSO with Authentik, Keycloak, Okta, Google
- [Monitoring](docs/recipes/monitoring.md) — Prometheus + Grafana
- [External Secrets](docs/recipes/external-secrets.md) — Vault, AWS SM, GCP SM via ESO
- [Backup](docs/recipes/backup.md) — Velero backup and restore
- [Cloudflare Tunnel](docs/recipes/cloudflare-tunnel.md) — access without a public IP

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

Pre-v1. Core platform is functional — apps deploy, routes work, env vars are
managed, bindings auto-inject, templates work. See [PROGRESS.md](PROGRESS.md)
for detailed per-feature status.
