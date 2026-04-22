# Mortise Systems Overview

This document explains how Mortise works in the current implementation.
It is intentionally code-grounded (from `cmd/main.go`, `internal/controller/*`, `internal/api/*`, and `api/v1alpha1/*`), not aspirational.

## Runtime model

Mortise runs as a single binary that hosts:

- Kubernetes controllers (via controller-runtime manager)
- REST API server (default `:8090`)
- Embedded UI static file serving (when UI assets are present)
- Webhook receiver for Git provider events (`/api/webhooks/{provider}`)

The entrypoint is `cmd/main.go`.

## Core data model (CRDs)

Mortise CRDs are defined under `api/v1alpha1/`:

- `Project` (cluster-scoped)
  - Top-level grouping for apps
  - Declares project environments in `spec.environments`
  - Owns one control namespace and one workload namespace per environment
- `App` (namespaced; stored in the project control namespace)
  - Supports `source.type`: `git`, `image`, `external`
  - Supports `kind`: `service`, `cron`
  - Carries per-environment overrides (replicas/resources/env/domains/bindings)
- `PreviewEnvironment` (namespaced)
  - Per-PR preview lifecycle object
- `GitProvider` (cluster-scoped)
  - Git forge configuration (GitHub, GitLab, Gitea)
- `PlatformConfig` (cluster-scoped singleton, name `platform`)
  - Platform-wide domain/build/registry/observability settings
- `ProjectMember` (namespaced; API-managed)
  - Project-scoped membership and role binding

## Namespace model

For project `myproj`:

- Control namespace: `pj-myproj`
  - Holds `App` and `ProjectMember` CRs, plus control-plane artifacts
- Workload namespaces per env:
  - `pj-myproj-production`
  - `pj-myproj-staging`
  - etc.

Preview namespaces follow a PR pattern (controller-managed) and are isolated from control namespace resources.

## Controllers and responsibilities

### Project controller

`internal/controller/project_controller.go`

- Ensures control + environment namespaces exist
- Applies project labels and ownership conventions
- Handles finalizer-driven cleanup on project deletion
- Reconciles environment namespace set when project environments change

### App controller

`internal/controller/app_controller.go`

- Reconciles runtime workloads from `App` spec
- For `service`: Deployment + Service + Ingress (+ PVC/SA/ConfigMap/Secrets as needed)
- For `cron`: CronJob path
- For `external`: external service facade path
- Resolves bindings and environment materialization
- Tracks build/deploy status in `status.environments`

### PreviewEnvironment controller

`internal/controller/previewenvironment_controller.go`

- Handles PR preview lifecycle
- Creates/updates/deletes preview resources
- Applies TTL expiration behavior

### PlatformConfig controller

`internal/controller/platformconfig_controller.go`

- Validates and marks singleton platform config readiness

### GitProvider controller

`internal/controller/gitprovider_controller.go`

- Validates provider resources and secret references

## API server model

The HTTP server is mounted by `internal/api/server.go`.

- Unauthenticated routes:
  - `/api/auth/status`
  - `/api/auth/setup`
  - `/api/auth/login`
  - `/api/webhooks/{provider}`
- Authenticated routes use JWT bearer auth middleware
- Deploy endpoint supports JWT **or** deploy token (`mrt_*`)
- SSE routes support JWT query-token fallback (`?token=`) for EventSource clients

See `docs/api-endpoints.md` and `docs/openapi.yaml` for the full surface.

## AuthN / AuthZ

Auth and authorization are split:

- Authentication:
  - JWT for API access
  - Native auth provider for user storage and password verification
- Authorization:
  - Policy engine in `internal/authz`
  - Platform roles: `admin`, `member`, `viewer`
  - Project roles: `owner`, `developer`, `viewer` via `ProjectMember`

Authorization checks happen in handlers via `s.authorize(...)`.

## Build and deploy pipeline

### Git-source apps

High-level flow:

1. Git webhook arrives at `/api/webhooks/{provider}`
2. Signature is validated against provider secret
3. Matching apps are identified by repo/branch/watch paths
4. App revision annotation is patched to trigger reconcile
5. Build subsystem produces image and app reconciles workloads

### Image-source apps

- API updates `spec.source.image` (for explicit deploy or app updates)
- Reconciler rolls deployment to requested image

### External deploy path

- `POST /api/projects/{project}/apps/{app}/deploy`
- Accepts JWT or deploy token
- Deploy token modes:
  - app+environment scoped token
  - project-scoped deploy token

## Observability path

- Real-time pod metrics use metrics-server API when available
- Historical logs and metrics are proxied through adapter endpoints configured in `PlatformConfig.spec.observability`
- Build logs are available via API and project events SSE stream

## Setup and configuration

### Ingress controller support

- Runtime ingress behavior is not hardcoded to Traefik
- App Ingresses are driven through `IngressProvider` and optional
  `ingressClassName`
- `MORTISE_INGRESS_CLASS` controls the class used on generated Ingresses
- The batteries-included chart bundles Traefik by default, but you can
  disable it and use your own ingress controller

### Registry localhost pull port

- In the batteries-included chart, app pulls use
  `PlatformConfig.spec.registry.pullURL` set to `http://localhost:<port>`
- `<port>` comes from `registry.proxy.hostPort` (default `30500`)
- Change it in Helm values under `registry.proxy.hostPort`; chart templates
  wire the same value into both the DaemonSet hostPort and pullURL

### Platform startup config resolution

`cmd/main.go` boot sequence:

1. Try loading `PlatformConfig` singleton (`platform`)
2. If missing, fall back to `MORTISE_*` env vars
3. Start manager, controllers, and API server

This allows a fresh install to boot before full platform config is created.

### Local development loop

Primary commands:

- `make dev-up` - create local k3d stack and install Mortise
- `make dev-reload` - rebuild/redeploy quickly
- `make dev-down` - tear down cluster
- `make test` - unit + envtest
- `make test-integration` - full integration suite in isolated cluster
- `make test-e2e` - UI/API end-to-end tests

See `DEVELOPMENT.md` for full workflows.

## Notes on docs accuracy

- This document tracks implementation behavior in this repository state.
- Product/spec docs (`SPEC.md`, `PROGRESS.md`) can describe in-progress or future behavior; if they differ, code paths in `internal/api` and controllers are the source of truth for runtime behavior.
