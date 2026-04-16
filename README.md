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

Pre-v1. Phases 1–3 of the spec are complete (operator, API, auth, UI
skeleton, bindings, secrets). Remaining work tracked as Phase 4–8 in the
spec.
