# Mortise

> A self-hosted, Railway-style deploy target for Kubernetes.

Connect a git repo (or pick a pre-built image), and Mortise handles builds,
deploys, domains, TLS, environment variables, volumes, preview environments,
and service-to-service bindings — with Kubernetes fully abstracted away from
the user.

Mortise is one operator, shipped as one Helm chart. No addons, no plug-in
protocol, no multi-tier product. Adjacent capabilities (OIDC, monitoring,
backups, external secret managers) are upstream projects users install
themselves; Mortise coexists with them through standard Kubernetes
primitives.

## Docs

- [`SPEC.md`](./SPEC.md) — full product and engineering plan
- [`ARCHITECTURE.md`](./ARCHITECTURE.md) — system diagrams (component
  layout, deploy flow, interface contracts, chart topology)
- [`CLAUDE.md`](./CLAUDE.md) — project conventions and architecture rules
- [`DEVELOPMENT.md`](./DEVELOPMENT.md) — local dev loop, tests, troubleshooting

### Integration recipes

- [External CI](./docs/recipes/external-ci.md) — build in GitHub Actions / GitLab CI, deploy via webhook
- [OIDC](./docs/recipes/oidc.md) — SSO with Authentik, Keycloak, Okta, Google
- [Monitoring](./docs/recipes/monitoring.md) — Prometheus + Grafana setup
- [External Secrets](./docs/recipes/external-secrets.md) — Vault, AWS SM, GCP SM via ESO
- [Backup](./docs/recipes/backup.md) — Velero backup and restore
- [Cloudflare Tunnel](./docs/recipes/cloudflare-tunnel.md) — access without a public IP

## Try it locally (one command)

Requires Docker, Go 1.25+, Node 22+, kubebuilder, k3d, kubectl, helm.

```bash
make dev-up
kubectl port-forward -n mortise-system svc/mortise 8090:80 &
open http://localhost:8090
```

See [DEVELOPMENT.md](./DEVELOPMENT.md) for the full guide, including
first-run admin setup, iterating on code (`make dev-reload`), and
troubleshooting.

## Status

Pre-v1. Phases 1–7 of the spec are implemented. Phase 8 (infrastructure
bundle, integration recipes, extensions page) is complete. See
[`PROGRESS.md`](./PROGRESS.md) for detailed status.
