# Changelog

All notable changes to Mortise are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Mortise uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Observer reliability + PVC usage** (obs-v2 O1, #214): two new observer
  endpoints — `GET /v1/pvc` (per-PVC capacity/usage series, collected from
  the kubelet Summary API) and `GET /v1/health/collectors` (per-collector
  last tick/success/error plus tailer and dropped-line gauges). `/v1/metrics`
  and `/v1/pvc` responses gain a `coverage` array marking observed vs
  unobserved windows so UIs render gaps instead of interpolating. The
  observer ClusterRole widens by read-only `nodes` get/list and
  `nodes/proxy` get for kubelet stats (justified inline in the chart).
  Retention change: raw metrics downsample to 5-minute averages after 24
  hours (was raw for the full 72h window); log tailers now resume from a
  stored cursor across restarts instead of re-reading (and duplicating)
  the last 100 lines. Full audit: `docs/observer-reliability.md`.
- **Published-chart verification in the release pipeline** (#446, #379
  residual): after publishing to gh-pages, `release.yml` pulls the chart
  back through the public repo URL, asserts it is byte-identical to what
  the run packaged, and renders it to prove the registry-proxy DaemonSet
  and buildruns RBAC are present. Publishing now also refuses to
  overwrite an already-published version (chart artifacts are immutable)
  and refuses to publish the in-repo placeholder version.

- **`valueFrom.fromBinding` env var projection** (SPEC §5.8b): New CRD field
  lets users project specific keys from bound app credentials into custom-named
  env vars (e.g. `DB_PASS` from `pg.password`). Includes new
  `GET /binding-keys` API endpoint and BindingsPicker UI rewrite.

### Changed

- **Finalizer GC now requires the `app.kubernetes.io/managed-by=mortise`
  label and is scoped to project-labelled namespaces**: resources created
  before the appLabels era (or by anything else that reuses Mortise's
  name/project label keys without the managed-by label) are no longer
  deleted by App-deletion GC. Operators relying on GC to clean up
  pre-appLabels leftovers must remove those by hand once.
- **In-repo chart placeholder version is now `0.0.0-dev`** (was `0.1.0`,
  which collided with the genuinely published `mortise-0.1.0` release
  artifact). CI stamps real versions at release time as before; see
  RELEASING.md "Placeholder version policy".

### Fixed

- **`install.sh` dev-path built the wrong binary**: the repo-clone flow's
  untargeted `docker build` produced the Dockerfile's final stage — the
  observer binary — tagged as the operator image, so the installed
  operator crashlooped. Now builds `--target operator` (same fix in the
  chart-integration harness, which had the identical bug).
- **`install.sh` no longer disables metrics-server with a dead key**
  (#454): the script passed `--set metricsServer.enabled=false`, but the
  subchart key was renamed `metrics-server` in 1.0.4 and Helm silently
  ignores unknown keys — so metrics-server deployed anyway. Fixed to the
  new key; a CI guard now fails on any `metricsServer` reference outside
  the changelog.
- **`valueFrom.secretRef` resolution**: Field existed on the CRD but the
  controller never resolved it. Now reads the referenced Secret from the env
  namespace.
- **Bindings/Credentials `+` button flash bug**: SSE-triggered `$effect`
  re-runs reset UI state, causing the add-row form to appear then immediately
  disappear. Fixed with state guards.

## [1.0.4] - 2026-07-26

### Changed

- **Umbrella chart values key rename**: the metrics-server subchart is now
  configured under `metrics-server` (kebab-case) instead of `metricsServer`.
  Values under the old key are silently ignored — upgraders who disabled or
  configured metrics-server must move those values to the new key (#433).
- **Webhook registration requires `externalDomain`** (#450): the App
  controller no longer falls back to `spec.domain` when
  `spec.externalDomain` is unset. The app wildcard domain does not route
  to the Mortise API, so the fallback registered (and, via stale-hook
  cleanup, replaced working hooks with) callbacks that could never
  deliver. When `externalDomain` is empty, registration is skipped and
  the App records a `WebhookConfigured=False` condition. Installs that
  serve Mortise on the platform domain must set `externalDomain` to the
  same value.

### Fixed

- **Secretless webhook registration** (#451): the App controller no
  longer registers webhooks for a GitProvider without a usable
  `webhookSecretRef`. The webhook handler rejects unsigned deliveries,
  so such hooks could never deliver; registration is now skipped with a
  `WebhookConfigured=False` condition until the secret is configured.
- **GitProvider self-heal** (#451): the GitProvider reconciler
  re-attaches a lost `spec.webhookSecretRef` when the managed
  `gitprovider-webhook-{name}` Secret still exists, and reports a
  `WebhookSecretConfigured` status condition either way.
- **Registry pull URL warning** (#449): the operator logs a startup
  warning when `spec.registry.url` is cluster-internal and
  `spec.registry.pullURL` is empty, instead of silently templating
  image refs kubelets cannot pull.
- **Operator pod fails to start under `runAsNonRoot`** (#433): the chart
  now sets numeric `runAsUser`/`runAsGroup` (65532) and the Dockerfile
  uses a numeric `USER`, so kubelet can verify `runAsNonRoot` instead of
  failing with `CreateContainerConfigError`.
- **RBAC escalation-prevention deadlock** (#433): the operator ClusterRole
  gains the `bind` verb (pinned to `mortise-controller-ns`) so the Project
  controller can create per-namespace RoleBindings on RBAC-enforcing
  clusters, plus `projectmembers/status` for member management.
- **Builds under `readOnlyRootFilesystem`** (#433): `DOCKER_CONFIG` points
  at the writable emptyDir so BuildKit's auth provider can create its
  config dir; RBAC-propagation races between namespace creation and
  RoleBinding stamping no longer surface as 500s or wedge App deletion.

## [1.0.2] - 2026-05-19

### Added

- **Preview environments as clone environments**: preview environments are now
  real project environments (`pr-{number}`) managed through the app controller's
  existing environment fan-out. Previews appear in the environment switcher,
  use standard domain templates, and are cleaned up immediately on PR close
  with no TTL delay.
- **Degraded app phase**: when the latest build fails but an older image is
  still serving successfully, the app phase is now `Degraded` (warning) instead
  of `Failed`. CrashLooping takes priority over Degraded when both conditions
  are true.
- **Fatal log stream banner**: log viewer and deploy log components now display
  an inline error banner when the SSE stream encounters a fatal error, instead
  of silently stopping.
- **Preview switcher opens new tab**: preview entries in the environment
  switcher now open in a new browser tab instead of switching the current view.

### Fixed

- **Normalized project not-found errors**: all project lookup API paths now
  return a consistent `project "name" not found` message (404 JSON) instead of
  leaking raw Kubernetes API error strings.
- **Preview envs duplicate in env switcher** (#374): preview environments
  created via PR webhooks appeared twice in the environment switcher (once as
  a normal env, once as a PR env). The `ProjectEnvironment` type now carries a
  `preview` boolean so the UI can filter them from the standard env list.
- **Env deletion blocked by app overrides** (#375): deleting a project
  environment that had app-level overrides returned a 409 error. The API now
  auto-strips overrides from all apps referencing the deleted environment and
  proceeds with deletion.
- **Drawer tabs show stale data on env switch** (#376): switching environments
  in the app drawer left the Variables and Metrics tabs showing data from the
  previous environment. Both tabs now reset state and re-fetch when the active
  environment changes.
- **Preview envs don't converge on reconcile** (#373): preview environments
  were only created in response to webhook events; missed webhooks left the
  cluster out of sync with the forge. A new `PreviewConvergenceReconciler`
  watches Project changes and re-queues every 10 minutes, polling the forge
  for open PRs and creating/deleting `PreviewEnvironment` CRs to match.
- **Preview environment flag backfill**: existing `pr-N` project environments
  are now backfilled with `preview: true` so legacy preview entries still render
  and behave like previews in the UI.
- **Vendored chart drift detection**: chart tests now fail when the packaged
  `mortise-core` dependency drifts from `charts/mortise-core`, preventing stale
  RBAC from silently shipping in umbrella chart installs.
- **Preview convergence stability**: multi-repo preview naming now strips
  invalid repo suffixes, stays within DNS limits, and no longer aborts project
  convergence when a single repo PR listing fails.
- **GitHub error wrapping**: preview convergence no longer trips the
  nil-sensitive GitHub error wrapping path during provider failures.
- **DEV_CLUSTER targeting in local workflows**: `make dev-up`, `make dev-reload`,
  and dev E2E port-forwarding now target the cluster named by `DEV_CLUSTER`
  instead of whichever kube context happens to be active.
- **Preview namespace teardown**: deleting a `PreviewEnvironment` now explicitly
  deletes the preview namespace instead of leaving closed-PR workloads running.
- **BuildRun retention and log GC**: terminal `BuildRun` objects now retain the
  newest history per app/environment and delete their durable build-log
  ConfigMaps when old runs are retired.
- **App-spec env var pruning**: environment variables removed from
  `spec.environments[].env` are now pruned from the backing env Secret only
  when Mortise still owns the prior spec-applied value, preserving user
  overrides and non-user sources.
- **CrashLoop false positives during startup**: apps no longer flip to
  `CrashLooping` during normal `ContainerCreating`, `PodInitializing`, or image
  pull waits; `CrashLooping` now reflects real `CrashLoopBackOff` states only.

### Changed

- **PreviewEnvironment CRD simplified**: the `PreviewEnvironment` spec now
  carries only `projectRef`, `sourceEnv`, and `pullRequest` metadata. Removed
  fields: `appRef`, `replicas`, `resources`, `env`, `bindings`, `domain`,
  `ttl`. Status reduced to `environmentName` and `conditions`; removed `url`,
  `image`, `currentBuildRunRef`, `lastSuccessfulBuildRunRef`, `expiresAt`.
  Phase enum reduced from `Pending|Building|Ready|Failed|Expired` to
  `Pending|Ready|Failed`.
- **PreviewConfig simplified**: removed `domain`, `ttl`, `resources` fields;
  added `sourceEnvironment` (optional string).
- **App environment branch override**: `spec.environments[].branch` is a new
  optional field allowing per-environment git branch overrides (used internally
  by preview cloning).
- **AppPhase enum**: added `Degraded` value. Consumers of the phase enum must
  handle this new value.

## [1.0.1] - 2026-05-12

Bug fix release addressing issues found after the 1.0.0 GA launch. All 49
commits are stability, security, and correctness fixes with no new features.

### Fixed

- **Reserved runtime secrets**: prevent user writes to operator-managed secrets
  (`PORT`, `DATABASE_URL`, etc.) via the env-var API.
- **Stack create rollback**: partial failures during multi-app stack creation
  now roll back already-created apps instead of leaving orphans.
- **Domain self-collision**: auto-generated domains no longer conflict with
  themselves when re-reconciling an unchanged app.
- **Viewer token leak**: project viewers can no longer list deploy token
  inventories (previously returned full token metadata).
- **Deploy token cleanup**: deleting an app now removes its deploy tokens;
  ordering fixed so token secrets are gone before the app finalizer completes.
- **Pull credential ownership**: mutation ordering and cleanup fixed so stale
  pull secrets don't block app deletion or cause dangling image-pull references.
- **Exec container targeting**: `POST /api/.../exec` now targets the correct
  container when pods have sidecars.
- **Env clone secret ownership**: cloned environment secrets are now owned by
  the correct environment, preventing cross-env garbage collection.
- **Preview shared-env fallback**: preview environments correctly inherit from
  the source environment when shared env vars are missing.
- **Preview rebuild on seeded images**: previews with a pre-seeded ready image
  no longer skip the build step on refresh.
- **Preview env reconcile without source secrets**: previews no longer fail when
  the source environment has no secrets configured.
- **SSE token revalidation**: token issue-time is checked on refresh, rejecting
  tokens minted before a password change.
- **Git token cache**: stale git provider tokens are no longer reused across
  preview reconcile loops.
- **GitProvider webhook secrets**: webhook HMAC secrets are now read from the
  correct key in the provider Secret.
- **BuildRun recovery**: orphaned BuildRuns from a crashed operator are
  detected and re-adopted on startup.
- **Webhook latching**: duplicate webhook deliveries within a short window
  are deduplicated instead of creating duplicate builds.
- **Activity backfill**: project creation no longer produces duplicate
  "project created" activity entries.
- **Log stream stability**: SSE log streams handle observer connection errors
  gracefully instead of dropping the client connection.
- **Env redaction**: secret values in env-var API responses are redacted;
  reserved keys are rejected on write.
- **API conflict handling**: k8s Conflict errors now map to HTTP 409 instead
  of 500; delete operations return 202 Accepted with a `"terminating"` status.
- **Proxy teardown**: `mortise proxy disconnect` now passes the environment
  parameter correctly.
- **Dev cluster reliability**: `make dev-up` now polls for readiness instead
  of a fixed sleep, and handles fresh clusters without pre-existing state.

## [1.0.0] - 2026-05-08

First general-availability release.

### Added

#### BuildRun CRD
- New `BuildRun` custom resource for durable build execution tracking. Each
  build produces a BuildRun object with full lifecycle (Pending, Running,
  Succeeded, Failed), retry attempts, log references, and image digest.
- `BuildRunReconciler` controller manages build jobs and updates status.
- REST API: `GET /projects/{p}/build-runs`, `GET /projects/{p}/build-runs/{name}`,
  `GET /projects/{p}/build-runs/{name}/logs`.

#### Preview environment sync
- State-reconciliation model replaces the old event-driven webhook dispatch.
  The operator now queries the git forge for open PRs and converges preview
  state, making previews self-healing after missed webhooks.
- New `previewsync` package implements the reconciliation loop.
- `ListOpenPullRequests` method added to GitHub, GitLab, and Gitea providers.
- GitLab MR `reopened` action now correctly maps to `opened`, enabling
  preview recreation on PR reopen.

#### SSE token system
- Server-sent event streams now authenticate via short-lived, single-use
  opaque tokens obtained from `POST /api/auth/sse-token`. Replaces direct
  JWT-in-query-param pattern.

#### Pull credentials API
- `GET/POST/DELETE /projects/{p}/apps/{a}/pull-credentials` for managing
  private registry image-pull credentials per app.

#### Environment cloning
- `POST /projects/{p}/environments/{env}/clone` creates a copy of an
  environment with all its env vars and configuration. Returns 409 Conflict
  if the target already exists.

#### Domain validation
- `POST /api/domains/validate` checks domain availability and detects
  cross-app collisions before assignment.

#### Per-environment build args and image overrides
- `spec.environments[].buildArgs` allows per-env build arguments.
- `spec.environments[].image` allows per-env image override (set by the
  deploy handler for environment-specific deploys).

#### Certificate status tracking
- `EnvironmentStatus` now surfaces `certificateStatus` (Ready, Pending,
  Failed) and `certificateMessage` from cert-manager Certificate resources.
- Operator RBAC expanded with read access to `cert-manager.io/certificates`.

#### Platform configuration
- `PlatformConfig.spec.externalDomain`: separate hostname for where the
  Mortise API is publicly reachable (webhook callbacks, deploy tokens, UI).
  Falls back to `spec.domain` when unset.
- Platform-level default CPU and memory (`PATCH /api/platform`).
- Platform-level GitHub OAuth client ID configuration.
- Platform-level domain template customization.

#### Auth improvements
- JWT tokens now carry `iss: mortise` and `aud: mortise-api` claims.
- Token refresh endpoint (`POST /api/auth/refresh`) with 7-day leeway.
- Password management: `POST /api/me/password` (self-service),
  `POST /api/admin/users/{email}/password` (admin reset).
- Minimum 8-character password enforcement.
- Last-admin protection: API rejects demoting or deleting the sole
  remaining admin.

#### Viewer role
- `viewer` role added alongside `admin` and `member`. Viewers can read
  project resources but cannot modify apps, secrets, or settings.

#### Auto-redeploy with stale detection
- Per-environment hash tracking (`pendingEnvHash` / `deployedEnvHash`) moved
  from top-level AppStatus to per-environment EnvironmentStatus.
- `POST /projects/{p}/apps/{a}/redeploy-stale` triggers redeployment only
  for environments with unapplied env-var changes.
- UI shows a per-environment stale banner with individual redeploy buttons.

#### Helm chart
- New `ClusterIssuer` template: set `tls.acme.email` to auto-create a
  cert-manager ClusterIssuer for automatic TLS.
- Security hardening defaults: `podSecurityContext` and
  `containerSecurityContext` in mortise-core values. Observer pod also
  hardened (non-root, read-only root FS, drop all capabilities).
- New `operator.tmpSizeLimit` value for `/tmp` emptyDir sizing.

#### CLI
- `app delete` and `secret delete` now prompt for confirmation; use
  `--yes`/`-y` to skip.
- Automatic JWT refresh on 401 with `"session expired"` fallback message.

#### UI
- Settings and Variables tabs decomposed from monolithic components
  (~1800 lines) into 12 focused section components.
- Preview environments appear in the environment switcher dropdown.
- Previews page wired to real API data.
- Real-time project name availability checking on the new-project form.
- Per-environment stale redeploy controls.
- Platform settings expanded: external domain, domain template, default
  resources, GitHub OAuth client ID.
- App detail pages subscribe to SSE for live updates.

#### CI / supply chain
- Cosign keyless image signing for operator and observer images.
- SBOM and provenance attestations on release builds.
- Playwright E2E tests run in CI.

#### Observability
- Operator RBAC now includes read access to `batch/jobs` (for BuildRun
  state) and `cert-manager.io/certificates` (for TLS status).

### Changed

- Webhook repo matching requires full owner/repo match (was suffix-based).
- Apps only trigger from their configured `providerRef` webhook (was any
  matching provider).
- `DELETE /projects/{p}/apps/{a}` returns 202 Accepted with
  `{"status":"terminating"}` instead of 200.
- Internal server errors no longer leak raw error messages; logged via
  `slog.Error`, user sees `"internal server error"`.
- `X-Forwarded-Proto` validated against `"http"` or `"https"`.
- `req.Host` HTML-escaped in OG image URLs (XSS fix).
- Error helpers (`writeError`) now include the request for structured logging.
- RBAC rules in Helm chart restructured for auditability (functionally
  equivalent, split into per-resource rules with comments).
- Secrets API query parameter renamed from `?env=` to `?environment=`.

### Removed

- Hardcoded GitHub OAuth client ID default (`Ov23lizLTd25E32VrWwl`) cleared
  from chart values. Must be set explicitly if using GitHub device flow.

## [0.1.4] - 2026-05-05

### Added

- Domain editing, password management, build args in Variables tab.
- Preview environment inheritance and environment clone API.
- Observer integration tests (metrics, traffic, logs).
- Full-stack integration test with git-source backend + bindings.
- Domain collision detection and multi-label template validation.
- BYO Traefik access logs documentation, `llms.txt`, local-only recipe.

### Fixed

- Security hardening for password management and session invalidation.
- Credential Secret NotFound retries instead of partial reconciliation.
- Preview env secret-before-build gate.
- Domain collision status reporting.
- Clone reads Secret-level env vars correctly.
- Observer traffic mismatch, SQLite contention, chart coupling.
- Webhook `.git` suffix handling, stale hook cleanup.
- Nil-replicas rollout detection, conflict windows.
- Deploy history ordering, redeploy visibility, rebuild cache bypass.

## [0.1.3] - 2026-05-05

### Fixed

- Integration test reliability: parallel execution, pre-pulled images,
  increased timeouts, verbose output.
- k3d install via direct binary download.
- Helm repo + dependency build in integration target.
- Network port in test fixtures for probe compatibility.

## [0.1.2] - 2026-05-05

### Added

- Integration test CI job.
- Observer poll interval tuning.

### Fixed

- PSA namespace labels.
- Webhook `.git` suffix and stale hook cleanup.
- Log channel backpressure with drop counter.
- specOverride race and build args key collisions.
- UI save clobber, CPU defaults, build args UI.

## [0.1.1] - 2026-05-05

### Fixed

- Observer traffic mismatch, SQLite contention.
- Chart coupling and CPU default handling.

## [0.1.0] - 2026-05-05

Initial release.

### Added

- Project and App CRDs with full lifecycle management.
- Git and image source types with automatic builds via BuildKit.
- Multi-environment support (production, staging, custom).
- Preview environments from pull requests.
- Domain management with automatic TLS via cert-manager.
- Environment variables and secrets management.
- Service bindings for backing services.
- Bundled infrastructure: BuildKit, OCI registry, Traefik, cert-manager.
- SvelteKit UI with canvas-based project visualization.
- CLI (`mortise`) with login, project, app, secret, proxy commands.
- Observer binary for metrics, logs, and traffic collection.
- GitHub, GitLab, and Gitea git provider support.
- Webhook-driven automatic deployments.
- Native auth with JWT tokens and role-based access (admin, member).

[1.0.1]: https://github.com/mortise-org/mortise/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/mortise-org/mortise/compare/v0.1.4...v1.0.0
[0.1.4]: https://github.com/mortise-org/mortise/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/mortise-org/mortise/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/mortise-org/mortise/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/mortise-org/mortise/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mortise-org/mortise/releases/tag/v0.1.0
