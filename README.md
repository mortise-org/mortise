# Mortise

> A self-hosted, Railway-style developer platform for Kubernetes.
>
> Naming: the core platform is **Mortise**; addons are **tenons**. Joinery
> metaphor — the mortise is the slot that holds everything; tenons slot in.

Connect a git repo (or pick a pre-built image), and Mortise handles builds,
deploys, domains, TLS, environment variables, volumes, preview environments,
and service-to-service bindings — with Kubernetes fully abstracted away from
the user.

- **v1 (Mortise core):** the minimum Railway clone. Installable on any
  k3s/k8s cluster via a single Helm chart.
- **Tenons (post-v1):** optional addon subcharts — Authentik SSO, OpenBao
  secrets, Prometheus/Grafana, Helm/external/catalog sources, backup/restore,
  and more. Each tenon is independently installable.

See [`SPEC.md`](./SPEC.md) for the full product and engineering plan, and
[`ARCHITECTURE.md`](./ARCHITECTURE.md) for system diagrams (component layout,
deploy flow, interface contracts, chart/install topology).

## Status

Pre-code. Spec-locked. Engineering kickoff in progress.
