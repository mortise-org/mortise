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

See [`SPEC.md`](./SPEC.md) for the full product and engineering plan, and
[`ARCHITECTURE.md`](./ARCHITECTURE.md) for system diagrams (component layout,
deploy flow, interface contracts, chart/install topology).

## Status

Pre-code. Spec-locked. Engineering kickoff in progress.
