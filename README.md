# Capybara

> Codename / repo placeholder — product name TBD.

A self-hosted, Railway-style developer platform that runs on top of an existing
Kubernetes cluster. Connect a git repo (or pick a pre-built image), and Capybara
handles builds, deploys, domains, TLS, environment variables, volumes, preview
environments, and service-to-service bindings — with Kubernetes fully abstracted
away from the user.

- **v1:** the minimum Railway clone. Installable on any k3s/k8s cluster.
- **Post-v1:** optional addon subcharts (Authentik SSO, OpenBao secrets,
  Prometheus/Grafana, Helm/external/catalog sources, backup/restore, etc.)

See [`SPEC.md`](./SPEC.md) for the full product and engineering plan.

## Status

Pre-code. Spec-locked. Engineering kickoff in progress.
