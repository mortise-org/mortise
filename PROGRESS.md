# Mortise implementation progress

Tracks what is implemented vs. what the spec calls for. Update this file
whenever implementation status changes — see the **Keeping this file up to
date** section at the bottom.

Legend: **Done** / **Partial** / **Not started**
Last reconciled against spec + code: 2026-04-16 (SPEC §5 expanded:
`namespaceOverride`/`adoptExistingNamespace`, `external` source promoted to v1,
`secretMounts`, `environments[].annotations` passthrough, `tls.secretName`/
`tls.clusterIssuer` overrides, 5-role RBAC + env-scoped grants. Also:
`DNSProvider` interface dropped from spec — DNS is now annotation-only via
ExternalDNS, no Go interface).

---

## Status at a glance

| Phase | Spec §   | Status       | Summary |
|-------|----------|--------------|---------|
| 0 — Foundation                   | §7.1 / §8   | **Done**         | kubebuilder scaffold, chart skeleton, Makefile, test helpers + fixtures. |
| 1 — Core operator (image source) | §7.2        | **Done**         | Deployment / Service / Ingress / PVC / ServiceAccount reconciliation works for `source.type: image` and `source.type: git` (git builds asynchronously with a 30-min timeout). Ingress honours `environments[].annotations` passthrough (§5.2a), `environments[].tls.{secretName,clusterIssuer}` overrides (§5.6), `environments[].customDomains` (multi-host rules + TLS), and `IngressProvider`-driven annotations (`AnnotationProvider`: ExternalDNS hostname + cert-manager cluster-issuer). `ingressClassName` configurable via `MORTISE_INGRESS_CLASS` env var. ServiceAccount per App carries `imagePullSecrets` from `RegistryBackend.PullSecretRef()`. |
| 2 — API + UI skeleton            | §7.3        | **Done**         | Auth, project CRUD, app CRUD, secrets CRUD, deploy webhook, SSE logs, SvelteKit UI. |
| 3 — Bindings + secrets           | §7.4        | **Partial**      | Resolver writes env vars; `{app}-credentials` Secret is now materialised from `spec.credentials` (Flavor A, §5.5a) — inline `value` and `valueFrom.secretRef` both supported, with sha256 pod-template annotation for rotation. Cross-project bindings now return a clear error at reconcile time instead of silently failing (issue #2 guarded, #3). Same-project bindings have an integration test (`test/integration/bindings_test.go`, #5). **Deploy tokens landed** (§5.4): `mrt_` prefixed, per-app+env scoped, stored as hashed k8s Secrets; deploy webhook accepts both JWT and deploy token auth; token CRUD API + CLI (`mortise token {create,list,revoke}`). **Env management surface landed** (§5.14): focused GET/PUT/PATCH/import endpoints for `environments[].env`; `mortise.dev/env-hash` annotation triggers rolling restarts; CLI (`mortise env {list,set,unset,import,pull}`). No secret rotation endpoint. |
| 3.5 — Projects                   | §5 / §5.10  | **Done**         | `Project` CRD + controller + REST API + CLI + UI routes + default-project seeding all landed. `spec.namespaceOverride` and admin-only `spec.adoptExistingNamespace` (spec §5.0) are implemented: controller resolves the target namespace name, enforces cross-Project uniqueness (`NamespaceConflict`), surfaces refusals via the `NamespaceReady` condition (`NamespaceAlreadyExists` / `NamespaceOwnedByAnotherProject`), and takes the adoption path only when explicitly opted in. |
| 4 — Build system (git source)    | §7.5        | **Done**         | All stacks wired end-to-end: webhook patches `mortise.dev/revision` annotation → App reconciler clones + builds + deploys. Operator entrypoint reads config from `PlatformConfig` (env-var fallback for first-boot). Builds run asynchronously in background goroutines; the reconciler returns `Building` immediately and polls on requeue. |
| 5 — Monorepo support             | §7.6        | **Done**         | `source.path` plumbs into BuildKit context; `source.watchPaths` gates webhook rebuilds (prefix match). UI build grouping deferred. |
| 6 — Preview environments        | §7.7        | **Done**         | `PreviewEnvironment` CRD with real types (PullRequestRef, PreviewPhase, TTL, domain). Controller reconciles Deployment + Service + Ingress with owner references; async build via buildTrackerStore (same pattern as App controller); TTL expiry auto-deletes. Webhook handler parses PR events (opened/synchronize/closed) for GitHub, GitLab, Gitea; creates/updates/deletes PreviewEnvironments with staging inheritance + preview overrides. Domain template resolution (`{number}`, `{app}`). Commit status posted on PR SHA. |
| 7 — Polish & v1                  | §7.8        | **Partial**      | Rollback + promote implemented full-stack (API, CLI, UI). Controller-side `RollbackDeployment` exists. Deploy tokens and env management surface implemented (API + CLI). Custom domains API/CLI/UI implemented (list/add/remove, §5.6). First-run wizard implemented (4-step: domain, DNS, git provider, done). PlatformConfig PATCH API (create-or-update singleton). `spec.network.port` added to App CRD (configurable container/target port, default 8080). `oauthTokenExists` bug fixed. Missing: authz role upgrade, env-hash annotation for auto-roll. |
| 8 — Tenons & integration recipes | §7.9 / §13  | **Not started**  | No bundled Traefik/cert-manager/ExternalDNS/Zot subcharts; no ESO / OPA / Prometheus recipes. |

### Interface implementation coverage

Spec rule: every outward interface must have at least one real v1 impl
(CLAUDE.md, "Interfaces"). Current state:

| Interface         | Impls                              | Status          |
|-------------------|------------------------------------|-----------------|
| `AuthProvider`    | `NativeAuthProvider` (k8s Secret + bcrypt + JWT) | **Done**    |
| `PolicyEngine`    | `NativePolicyEngine` (roles: `admin` / `member`)   | **Partial** — role model does not match spec §5.10 (`platform-admin` / `team-admin` / `team-member`); no team concept. |
| `GitAPI`          | `GitHubAPI`, `GitLabAPI`, `GiteaAPI` (`internal/git/{github,gitlab,gitea}.go`); factory at `internal/git/factory.go` | **Done** |
| `GitClient`       | `GoGitClient` (`internal/git/gogit_client.go`) — single impl per CLAUDE.md | **Done** |
| `BuildClient`     | `BuildKitClient` (`internal/build/buildkit.go`) — mockable `solveClient` boundary for unit tests | **Done** |
| `RegistryBackend` | `OCIBackend` (`internal/registry/oci.go`) — generic OCI Distribution Spec v1.1; Bearer + Basic auth; works with Zot, Harbor, GHCR, ECR | **Done** |
| `IngressProvider` | `AnnotationProvider` (`internal/ingress/annotation_provider.go`) — ExternalDNS hostname + cert-manager cluster-issuer annotations; configurable `ingressClassName` | **Done** |

### CRD coverage

| CRD                  | Types file        | Controller       | Status        |
|----------------------|-------------------|------------------|---------------|
| `Project`            | real              | real reconciler  | **Done** |
| `App`                | real              | real (image + git) | **Partial** — no `kind: service\|cron`, `schedule`, `concurrencyPolicy`, `sharedVars`, or `valueFrom.fromBinding` from spec §5.2. Also missing: `source.type: external` (spec §5.1 v1) and the `importFrom` flavour of `spec.credentials` (§5.5a). `spec.credentials` Flavor A (inline value + valueFrom.secretRef) is implemented with Secret materialisation. `environments[].secretMounts` (§5.5b), `environments[].annotations` (§5.2a), and `environments[].tls.{secretName,clusterIssuer}` (§5.6) are implemented. `spec.network.port` configures container/target port (default 8080). Custom domains API surface (list/add/remove) patches `environments[].customDomains`. |
| `GitProvider`        | real (`api/v1alpha1/gitprovider_types.go`) | real reconciler (`internal/controller/gitprovider_controller.go`) | **Done** |
| `PlatformConfig`     | real (`api/v1alpha1/platformconfig_types.go`) | real reconciler (`internal/controller/platformconfig_controller.go`) | **Done** |
| `PreviewEnvironment` | real (`api/v1alpha1/previewenvironment_types.go`) | real reconciler (`internal/controller/previewenvironment_controller.go`) | **Done** |

---

## Detailed status

### Phase 0 — Foundation — **Done**

- `cmd/operator/main.go`, `cmd/cli/main.go`, `cmd/main.go` wire the operator
  + embedded API server + CLI.
- Operator registers all 5 controllers in `cmd/main.go` (three of them are
  no-op stubs, see below).
- `charts/mortise/` has the operator chart: `deployment.yaml`, `service.yaml`,
  `serviceaccount.yaml`, `rbac.yaml`. RBAC covers
  deployments/services/ingresses/pvcs/secrets/pods.
- `Makefile` targets: `test` (unit + envtest), `test-e2e` (Kind + Ginkgo
  scaffold), `dev-up` / `dev-down` / `dev-reload` (k3d live-reload).
- `test/fixtures/` — `image-basic.yaml`, `image-postgres.yaml`.
- `test/helpers/` — `CreateTestNamespace`, `RequireEventually`,
  `AssertDeploymentExists`, `AssertIngressExists`, `AssertPodsRunning`,
  `LoadFixture`.

**Gaps:**
- No `.github/` → no CI config checked in.

### Integration harness — Done

`test/integration/` exercises the operator end-to-end against a real k3d
cluster via the `//go:build integration` tag.

**What the harness does:**
- `test/integration/suite_test.go` — `TestMain` loads kubeconfig, builds a
  scheme-registered `client.Client`, waits for the `mortise` Deployment in
  `mortise-system` to have `AvailableReplicas > 0`, then runs the suite.
- Package-global `k8sClient` shared by all tests; `createTestNamespace(t)`
  helper gives each test an isolated namespace.
- `test/integration/k3d-config.yaml` — k3d config that installs a
  containerd registries-config mirror rewriting
  `registry.mortise-test-deps.svc:5000` → `http://127.0.0.1:30500`. Without
  this, the node's containerd can't resolve the cluster-internal registry
  hostname. The in-cluster registry pod binds a 127.0.0.1 hostPort at 30500
  so the mirror endpoint is reachable.
- `test/integration/manifests/` — `00-namespace` (`mortise-test-deps`),
  `10-registry` (`distribution/distribution:2.8.3`),
  `20-gitea` (`gitea:1.24.3` + postStart admin-user bootstrap),
  `30-buildkit` (`moby/buildkit:v0.29.0`, privileged),
  `40-platformconfig` (singleton `platform` PlatformConfig pointing at the
  deps namespace).

**Tests:**
- `app_image_source_test.go` — `TestImageSourceAppGoesReady`: loads
  `test/fixtures/image-basic.yaml` and asserts Deployment becomes ready.
- `app_git_source_test.go` — `TestGitSourceAppBuildsAndDeploys`: bootstraps
  a Gitea repo with a minimal Dockerfile via `helpers.GiteaBootstrap`, stubs
  the OAuth + webhook + provider-token secrets, creates a GitProvider and
  an App from `test/fixtures/git-gitea-basic.yaml`, and asserts the App
  reaches `Ready`, the registry surfaces the built tag, and the Deployment
  runs the built image.
- `bindings_test.go` — `TestSameProjectBindingInjectsEnv`: creates a
  Postgres App and an API App bound to it in the same namespace, waits for
  both Deployments to be ready, and asserts `DATABASE_URL`, `host`, and
  `port` env vars are injected into the API container spec.
- `gitprovider_admin_test.go` — `TestGitProviderAdminAPICRUD`: port-forwards
  the Mortise API, bootstraps / logs in as an admin, POSTs to
  `/api/gitproviders`, asserts the `GitProvider` CRD + managed OAuth
  Secret land with the `mortise.dev/managed-by: api` label, re-POSTs for
  409, then DELETEs and asserts the CRD + both Secrets are gone.
  `TestGiteaOAuthFlow`: end-to-end authorize → consent → callback → token
  persistence using the in-cluster Gitea as the OAuth provider. Creates
  a Gitea OAuth app via its admin API, drives a cookie-jar HTTP client
  through Gitea's login + consent forms (scraping the `_csrf` token), and
  verifies the operator-side code exchange stores a usable access token in
  `gitprovider-token-{name}` — proved by calling Gitea's `/api/v1/user`
  with it.

**Helpers added:**
- `test/helpers/gitea.go` — `GiteaBootstrap{BaseURL, Username, Password}`
  with `Ensure(t, inClusterBaseURL, owner, repo, files)` that mints an
  admin token, creates the repo, and uploads files through Gitea's REST
  API (no SDK — keeps the helper portable). Also exposes
  `CreateOAuthApp(t, name, redirectURIs)` / `DeleteOAuthApp(t, id)` for
  integration tests that need a live OAuth client on the test Gitea.
- `test/helpers/mortise_api.go` — `LoginAsAdmin(t, baseURL, email, pw)`
  returns a Mortise JWT, idempotently bootstrapping first-user setup when
  the platform is empty and falling through to `/api/auth/login` otherwise.
- `test/helpers/portforward.go` — `PortForward(t, ns, svc, remotePort)`
  shells out to `kubectl port-forward` on an OS-picked local port, waits
  for the TCP accept, and registers cleanup.
- `test/helpers/registry.go` — `AssertRegistryHasTags(t, base, ns, app,
  timeout)` polls `GET /v2/<ns>/<app>/tags/list` per the OCI Distribution
  Spec.
- `test/helpers/assertions.go` — `WaitForAppReady(t, k8sClient, ns, name,
  timeout)` polls `App.Status.Phase`.

**Makefile targets:**
- `make test-integration` — deletes any stale cluster, creates a fresh
  k3d cluster from `test/integration/k3d-config.yaml`, builds + loads the
  operator image, applies CRDs, installs test deps, installs the chart via
  Helm, runs `go test -tags integration -timeout 15m`, tears down.
- `make test-integration-fast` — `go test` only, against an already-running
  dev cluster.

**Follow-up work (not blocking Phase 4):**
- Pebble (ACME) for a TLS integration test.
- UI Playwright tests.
- `.github/` CI config.

### Phase 1 — Core operator (image source) — **Done**

Where it works (`internal/controller/app_controller.go`):
- Reconciles `Deployment`, `Service`, `Ingress`, `PersistentVolumeClaim(s)`,
  `ServiceAccount` for `source.type: image` apps.
- Reconciles `source.type: git` apps: resolves GitProvider OAuth token,
  clones repo, runs build via `BuildClient`, pushes image via
  `RegistryBackend`, then falls through to Deployment/Service/Ingress
  reconciliation with the built image digest.
- Creates one `ServiceAccount` per App (shared across envs) with
  `imagePullSecrets` from `RegistryBackend.PullSecretRef()`. Deployment pod
  spec references this SA via `serviceAccountName`. Private registries now
  work end-to-end.
- Ingress: `IngressProvider` (`AnnotationProvider`) emits ExternalDNS
  hostname annotation and cert-manager cluster-issuer annotation on every
  Ingress. `customDomains` on each environment produce additional
  IngressRules and TLS hosts. `ingressClassName` configurable via
  `MORTISE_INGRESS_CLASS` env var. Per-env TLS overrides (§5.6) and
  annotation passthrough (§5.2a) are preserved; user annotations win on
  key conflict. Provider is nil-safe for test code that doesn't set it.
- Sets owner references so everything GCs with the `App`.
- Exposes `RollbackDeployment` for the API layer to call.
- Envtest suite in `internal/controller/app_controller_test.go` covers image
  source (existing), git source (happy path, clone failure, build failure,
  same-SHA short-circuit), ServiceAccount creation + imagePullSecret
  wiring (5 cases), ExternalDNS annotation, customDomains, IngressProvider
  className, and nil-provider backward compat.

### Phase 2 — API + UI skeleton — **Done**

REST surface (`internal/api/server.go`):
- `GET/POST /api/auth/{status,setup,login}` — unauthenticated.
- `POST/GET/GET/DELETE /api/projects[/{project}]`.
- `POST/GET/GET/PUT/DELETE /api/projects/{project}/apps[/{app}]`.
- `POST /api/projects/{project}/apps/{app}/deploy` — deploy webhook.
- `POST/GET/DELETE /api/projects/{project}/apps/{app}/secrets[/{secretName}]`.
- `GET /api/projects/{project}/apps/{app}/logs` — SSE log stream with
  multi-pod aggregation and new-pod-watching on rollout.

UI (`ui/src/routes/`):
- `login`, `setup`, `projects`, `projects/new`, `projects/[project]`,
  `projects/[project]/apps/new`, `projects/[project]/apps/[app]`.
- `apps/new` rewritten to Railway-style repo-first flow: searchable repo
  list via `GET /api/repos`, branch picker via `GET /api/repos/{owner}/{repo}/branches`,
  inline config panel, Docker image deploy section, compact templates at bottom.

CLI (`cmd/cli/`):
- `login`, `project list/create/delete/use/show`, `app list/create/delete`,
  `deploy`, `logs`, `status`.
- `app_test.go` and `project_test.go` exercise the CLI layer.

**Gaps:** none for the skeleton itself — the missing CLI verbs
(`promote`, `rollback`, `secret`, `env`, `domain`, `token`, `preview`)
belong to later phases and are tracked there.

### Phase 3 — Bindings & secrets — **Partial**

Works:
- `internal/bindings/resolver.go` resolves bindings into `[]corev1.EnvVar`.
- Cross-project bindings supported at the resolver level via
  `Binding.Project` → namespace `project-{project}`.
- `internal/api/secrets.go` implements per-app user-secret CRUD.
- `{app}-credentials` Secret materialised by `reconcileCredentialsSecret` in
  `app_controller.go` from `spec.credentials` (Flavor A, §5.5a). Credential
  type is `[]Credential` with inline `value` and `valueFrom.secretRef`
  (referencing user-managed Secrets in the App's own namespace). Well-known
  keys (`host`, `port`) are skipped in the Secret — filled in by the
  resolver at binder time. A sha256 hash annotation
  (`mortise.dev/credentials-hash`) on the pod template forces rollouts on
  Secret rotation. Cleanup deletes the Secret when credentials are removed
  (only if Mortise-managed). Tests: envtest Context "credentials Secret
  materialization" (7 cases). Fixture: `test/fixtures/image-credentials.yaml`.

Missing:
- **Issue #2** — the resolver emits plain
  `SecretKeyRef{Name: {app}-credentials}` for cross-project bindings
  (`resolver.go:73-79`). Kubernetes `envFrom`/`env.valueFrom.secretKeyRef`
  only resolves within the Pod's own namespace. Cross-project bindings will
  silently fail at Pod create. Needs either Secret replication (ESO-style),
  projected-volume mount, or an initContainer fetch.
- No deploy tokens (`mortise token create/list/revoke`, `Authorization:
  Bearer mrt_...`) from spec §5.1 / §7.8 phase 7.
- No rotation endpoint for user secrets.

### Phase 3.5 — Projects — **Done**

- `Project` CRD (`api/v1alpha1/project_types.go`): cluster-scoped, phases
  `Pending | Ready | Terminating | Failed`, `status.namespace`,
  `status.appCount`.
- `ProjectReconciler` (`internal/controller/project_controller.go`): creates
  the backing namespace `project-{name}` with owner reference and finalizer
  `mortise.dev/project-finalizer`; finalizer cascades namespace teardown so
  apps GC with the project.
- REST: `internal/api/projects.go` with DNS-1123 validation,
  `maxProjectNameLen = 55`, admin-only create/delete.
- REST resolver: `resolveProject` at
  `internal/api/projects.go:179` is called by every nested app/secret/log/
  deploy handler.
- CLI: `cmd/cli/project.go` + `current_project` tracked in CLI config.
- UI: routes under `ui/src/routes/projects/`.
- First-run seeds a `default` project (`internal/api/auth.go`
  `ensureDefaultProject`).

### Phase 4 — Build system (git source) — **Partial**

All three foundational stacks (Registry / Build / Git provider) have real
v1 impls behind their interfaces. The remaining work is the **integration
edge**: getting a git push to actually run through the reconciler, produce
an image, and trigger a deploy. See sub-sections below for what each stack
landed and what each deferred.

**Cross-stack deferred work (tracked here, not duplicated in sub-sections):**
- ~~**App controller git path**~~ — **Done.** `internal/controller/app_controller.go`
  now handles `source.type: git` via `reconcileGitSource`: resolves provider
  token, clones, builds, pushes, and falls through to Deployment reconciliation.
  `spec.source.providerRef` field added to `AppSource`.
  `status.lastBuiltSHA` / `status.lastBuiltImage` added to `AppStatus`.
- ~~**PlatformConfig wiring**~~ — **Done.** `cmd/main.go` now constructs the
  registry / build / git stacks from the singleton `PlatformConfig` via
  `platformconfig.Load`. When the CRD isn't present yet, the operator falls
  back to `MORTISE_*` env vars so the API/UI stay reachable for initial
  setup. BuildKit TLS material (PEM from Secret) is materialised to a temp
  dir since `bkclient` requires file paths. No hot-reload: changes to
  PlatformConfig require an operator restart (acceptable for v1).
- ~~**Webhook → build dispatch**~~ — **Done.** `internal/webhook/handler.go`
  patches the `mortise.dev/revision` annotation on every matching App when a
  verified push event arrives. Branch and normalized-URL matching implemented.
- ~~**`test/fixtures/git-basic.yaml`**~~ — **Done.** Added at
  `test/fixtures/git-basic.yaml`.
- ~~**Async builds**~~ — **Done.** `reconcileGitSource` now launches the
  clone + build in a background goroutine tracked by an in-memory
  `buildTrackerStore` (keyed by App). The first reconcile for a new revision
  returns `Building` + `RequeueAfter: 15s`; subsequent reconciles poll the
  tracker and, on success, write `status.lastBuilt*` and fall through to
  Deployment reconciliation. Trackers are lost on operator restart; builds
  are idempotent so the next reconcile re-launches.

### Registry stack — **Done**

`internal/registry/oci.go` — `OCIBackend` implementing `RegistryBackend`.

**What landed:**
- `Config` struct: registry URL, optional namespace (default `"mortise"`), Basic
  auth (username/password), pre-issued bearer token, pull-secret name,
  and `InsecureSkipTLSVerify` for local k3d clusters.
- `PushTarget(app, tag)` — pure computation; returns `ImageRef` with
  `Registry`, `Path`, `Tag`, and `Full` fields. No network call. Matches the
  spec §7.5 naming convention `<registry>/<namespace>/<app>:<tag>`.
- `PullSecretRef()` — surfaces the configured k8s Secret name to controllers.
- `Tags(ctx, app)` — `GET /v2/<namespace>/<app>/tags/list` per OCI
  Distribution Spec §10.3. Returns `nil` (not error) for 404 (repo not yet
  created). Handles empty `tags` JSON field.
- `DeleteTag(ctx, app, tag)` — HEAD to resolve digest (`Docker-Content-Digest`
  with `Content-Digest` fallback), then `DELETE /v2/.../manifests/<digest>`.
  Accepts both `202 Accepted` and `200 OK` from delete.
- Auth: `applyStaticAuth` sends bearer token or Basic creds on every request.
  On `401` with `Www-Authenticate: Bearer realm=...`, `resolveChallenge`
  parses the challenge, fetches a scoped token from the realm URL (forwarding
  Basic creds if configured), and retries exactly once with
  `Authorization: Bearer <token>`. Handles both `token` and `access_token`
  JSON response fields.

**Test coverage** (`internal/registry/oci_test.go`, 25 tests with httptest.Server):
- `PushTarget`: happy path, custom namespace, empty app/tag errors, invalid URL.
- `PullSecretRef`: configured and empty.
- `Tags`: list, 404→nil, 500→error, null tags field.
- `DeleteTag`: happy path, 404 error, missing digest header error,
  `Content-Digest` fallback.
- Auth: Basic forwarded, Bearer challenge+retry, credentials forwarded to
  token endpoint, `access_token` field fallback.
- `parseWWWAuthenticate`: table-driven, including Bearer, Basic, and malformed
  headers.
- `registryHost`: table-driven including port stripping and error cases.
- Compile-time interface compliance check: `var _ RegistryBackend = (*OCIBackend)(nil)`.

**Deferred (out of scope for this PR):**
- Wiring into `PlatformConfig` — CRD is scaffold-only; a follow-up PR reads
  registry config from `PlatformConfig` and injects `OCIBackend`.
- App controller integration — `app_controller.go` does not yet call
  `PushTarget` or create imagePullSecrets; see Phase 1 gaps.
- Pagination for `Tags` — the OCI spec uses `Link` headers for pages; current
  impl reads only the first page. Sufficient until tag counts are large.

### Build stack — **Done**

`internal/build/buildkit.go` — `BuildKitClient` implementing `BuildClient`.

**What landed:**
- Constructor takes a `Config` struct (buildkit addr as `tcp://` or
  `unix://`, optional `ClientCA` / `ClientCert` / `ClientKey` for mTLS,
  default platform string). Exposes a `solveClient` interface seam so
  unit tests can inject a fake; production code dials BuildKit over the
  real client SDK.
- `Build(ctx, req)` runs a Dockerfile-frontend Solve: frontend
  `dockerfile.v0`, local context + dockerfile mounted from
  `req.ContextDir`, `target` + build-args passed through, image output
  pushed to `req.ImageRef` via the `image` exporter with
  `name`/`push=true` attrs. Returns the pushed image digest.
- Registry credentials are attached via `authprovider.NewDockerAuthProvider`
  so pushes can authenticate against whatever `RegistryBackend` is
  configured.
- Context cancellation is propagated through to the SolveStatus
  goroutine; a status drain goroutine is stopped when Solve returns
  (success or failure).

**Test coverage** (`internal/build/buildkit_test.go`, all mocking the
`solveClient` seam per CLAUDE.md):
- Happy path: successful Solve returns the expected digest.
- Build failure: Solve error surfaces as returned error.
- Cancellation: parent context cancel interrupts Solve.
- Request validation: empty `ContextDir` / `ImageRef` rejected pre-Solve.

**Deferred:**
- `PlatformConfig` wiring — see cross-stack deferred work above.
- App controller integration — see cross-stack deferred work above.
- Integration test against real buildkitd — belongs in the (not-yet-wired)
  `test/integration/` harness.
- Cache hints (`CacheImports` / `CacheExports`) — the interface doesn't
  surface them yet; add when build-time optimization matters.

### Git provider stack — **Done**

`internal/git/`, `internal/webhook/`, `internal/api/oauth.go`,
`api/v1alpha1/gitprovider_types.go`,
`internal/controller/gitprovider_controller.go`.

**CRD (`api/v1alpha1/gitprovider_types.go`, 140 LOC):**
- `spec.type` — enum `github | gitlab | gitea` (CEL-validated).
- `spec.host` — base URL.
- `spec.oauth.clientIDSecretRef` / `spec.oauth.clientSecretSecretRef` —
  `SecretRef{Namespace, Name, Key}` pointing at the OAuth app credentials.
- `spec.webhookSecretRef` — `SecretRef` for HMAC verification.
- `status.phase` — `Pending | Ready | Failed`; plus standard `Conditions`.
- Generated `zz_generated.deepcopy.go`, CRD yaml, and RBAC role all
  regenerated via `make manifests generate`.

**Reconciler (`internal/controller/gitprovider_controller.go`, 141 LOC):**
- Validates that every referenced Secret exists and is non-empty.
- Sets `status.phase = Ready` + `Available=True` condition on success.
- Sets `status.phase = Failed` + condition with reason on validation
  failure; requeues with backoff.
- Envtest in `internal/controller/gitprovider_controller_test.go` covers
  happy path + missing-secret failure.

**`GitAPI` impls (`internal/git/{github,gitlab,gitea}.go` + `factory.go`):**
- Each wraps the forge's official SDK (`google/go-github`,
  `xanzy/go-gitlab`, `code.gitea.io/sdk/gitea`) behind the `GitAPI`
  interface so controllers never import these directly.
- `Factory(ctx, provider, token)` in `internal/git/factory.go` takes a
  `*mortisev1alpha1.GitProvider` + resolved OAuth access token and
  returns the matching impl.
- `internal/git/api_test.go` exercises the factory's dispatch and
  mocks at the `GitAPI` boundary per CLAUDE.md mocking policy.

**`GitClient` impl (`internal/git/gogit_client.go`):**
- `GoGitClient` — single impl using `github.com/go-git/go-git/v5`.
- Clones a repo at a ref into a working directory, authenticating with a
  token via the standard HTTP basic-auth-as-token convention.

**Webhook receiver (`internal/webhook/`, 463 LOC total):**
- `handler.go` — HTTP handler that looks up a `GitProvider` by URL path,
  loads its `webhookSecretRef` via `k8s.go`, and dispatches to the
  per-forge HMAC verifier: GitHub `X-Hub-Signature-256`, GitLab
  `X-Gitlab-Token`, Gitea `X-Gitea-Signature`. Push events are parsed
  into a normalized struct (`Repo`, `Ref`, `CommitSHA`) and written to
  an in-memory dispatch channel + logged. Returns `202 Accepted`.
- `k8s.go` — tiny helper that resolves a `SecretRef` to bytes from the
  cluster.
- `handler_test.go` covers happy-path HMAC verification per forge, bad
  signature rejection, unknown provider, and malformed payloads.
- Mounted in `internal/api/server.go` at `/api/webhooks/{provider}`
  (unauthenticated — auth is via HMAC).

**OAuth server (`internal/api/oauth.go`, 202 LOC):**
- `GET /api/oauth/{provider}/authorize` — builds the forge's OAuth
  consent URL from the `GitProvider`'s `oauth.clientIDSecretRef` and
  redirects.
- `GET /api/oauth/{provider}/callback` — exchanges the code for an
  access token (using `golang.org/x/oauth2`), then stores the token in a
  k8s Secret keyed by provider name.
- Scopes are per-forge: `repo` / `admin:repo_hook` for GitHub, `api` for
  GitLab, `repo` / `write:repo_hook` for Gitea.
- Wired into `server.go:74-75` as unauthenticated routes (same reasoning
  as the webhook route).

**Admin REST API (`internal/api/gitproviders.go`):**
- `GET`, `POST`, `DELETE /api/gitproviders` let admins list, create, and
  delete `GitProvider` CRDs and their backing OAuth secret from the UI —
  see "Git provider UI" below for the create/delete surface area.

**Follow-up (not blocking Phase 4):**
- ~~**`PlatformConfig` wiring**~~ — Done; see cross-stack section above.
- ~~**Integration tests against local Gitea**~~ — Done.
  `TestGitSourceAppBuildsAndDeploys` in `test/integration/app_git_source_test.go`
  exercises the full git → build → push → pull → deploy path against
  in-cluster Gitea + distribution registry + BuildKit. See Phase 0 /
  Integration harness for details.

### PlatformConfig — **Done**

`api/v1alpha1/platformconfig_types.go`, `internal/controller/platformconfig_controller.go`,
`internal/platformconfig/loader.go`.

**CRD fields:**
- `spec.domain` — base domain for the platform (required).
- `spec.dns.provider` — enum (`cloudflare | route53 | externaldns-noop`); validated by kubebuilder marker.
- `spec.dns.apiTokenSecretRef` — `SecretRef{Namespace, Name, Key}` for the DNS provider API token (required).
- `spec.storage.defaultStorageClass` — optional, falls back to cluster default.
- `spec.registry.url` — OCI registry endpoint (required if registry is configured).
- `spec.registry.namespace` — image namespace, defaults to `"mortise"` via kubebuilder default marker.
- `spec.registry.credentialsSecretRef` — optional `*SecretRef` for Basic/Bearer registry auth.
- `spec.registry.pullSecretName` — optional k8s image-pull Secret name.
- `spec.registry.insecureSkipTLSVerify` — bool, for local k3d clusters.
- `spec.build.buildkitAddr` — `tcp://...` or `unix://...` address.
- `spec.build.tlsSecretRef` — optional `*SecretRef` for BuildKit mTLS (keys: `ca.crt`, `tls.crt`, `tls.key`).
- `spec.build.defaultPlatform` — defaults to `"linux/amd64"` via kubebuilder default marker.
- `spec.tls.certManagerClusterIssuer` — optional ClusterIssuer name; consumed by Ingress code (wiring deferred).
- `status.phase` — `Pending | Ready | Failed`.
- `status.conditions` — standard `[]metav1.Condition`.

`SecretRef` is reused from `gitprovider_types.go` (same package, no move needed).

**Reconciler behaviour (`PlatformConfigReconciler`):**
- Enforces singleton: only the instance named `"platform"` advances past validation; any other name gets `status.phase=Failed` + `reason=InvalidName`.
- Validates DNS API token secret exists and contains the referenced key.
- Validates optional registry credentials secret if `credentialsSecretRef` is set.
- Validates optional BuildKit TLS secret if `tlsSecretRef` is set.
- On success: `status.phase=Ready` + `Available=True` condition.
- On failure: `status.phase=Failed` + `Available=False` condition with typed reason.
- Envtest suite covers: happy path, missing-secret failure, wrong-name rejection, not-found early return.

**Loader package (`internal/platformconfig/`):**
- `Load(ctx, c client.Reader) (*Config, error)` — fetches the singleton PlatformConfig, resolves all referenced Secrets, returns a plain Go `Config` struct (no k8s types exposed).
- `ErrNotFound` sentinel for "not configured yet" — callers use `errors.Is`.
- `Config` sub-structs: `DNSConfig`, `StorageConfig`, `RegistryConfig`, `BuildConfig`, `TLSConfig`.
- Unit tests with fake client covering: found+resolved, not-found, bad DNS secret ref, registry credentials resolution, bad registry secret ref, BuildKit TLS resolution.

**Operator wiring — Done:**
- `cmd/main.go` → `buildStacks` constructs the registry / build / git clients from `platformconfig.Load`.
- Fallback path: when `errors.Is(err, platformconfig.ErrNotFound)`, the operator logs a warning and uses `MORTISE_*` env-var defaults so the API/UI stay reachable before the user creates a PlatformConfig. An operator restart switches to the CRD once created.
- BuildKit TLS PEM (`ca.crt`/`tls.crt`/`tls.key` keys in `spec.build.tlsSecretRef`) is materialised to a temp dir since `bkclient` expects file paths.
- No hot reload: changes to the PlatformConfig CRD require a restart to take effect. Acceptable for v1; tracked if demand warrants.

**Still deferred:**
- `IngressProvider` impl and cert-manager wiring (`spec.tls.certManagerClusterIssuer`) — Phase 1 follow-up.
- ExternalDNS annotation emission on Ingress — Phase 1 follow-up. (No `DNSProvider` interface — annotation-only per spec §11.1.)

### Git provider UI — **Done**

- **Frontend for OAuth** — `ui/src/routes/settings/git-providers/+page.svelte`
  drives the authorize → callback round-trip. The list page shows all
  `GitProvider` CRDs with Name, Type, Host, Phase, token status (Connected /
  Not Connected), and a "Connect"/"Reconnect" anchor that navigates to
  `/api/oauth/{name}/authorize` (full browser navigation, not fetch).
  OAuth callback now redirects to `/settings/git-providers?connected={name}`
  and the list page displays a success banner keyed on that query param.
  Navigation link ("Settings") added to the main header in `+layout.svelte`.
- **`GET /api/gitproviders`** — admin-only endpoint in
  `internal/api/gitproviders.go` returns `[]GitProviderSummary` with
  `hasToken` reflecting whether `gitprovider-token-{name}` exists in
  `mortise-system`. Unit tests in `internal/api/gitproviders_test.go`.
- **`POST /api/gitproviders`** — admin-only. Accepts name / type / host /
  OAuth client ID+secret / webhook secret. Creates a Secret named
  `gitprovider-oauth-{name}` in `mortise-system` (labeled
  `mortise.dev/managed-by: api`) holding `clientID`, `clientSecret`,
  `webhookSecret`; then creates the GitProvider CRD pointing at it.
  Rolls back the Secret if CRD creation fails. Returns 400 on
  validation errors, 409 if a provider with that name already exists.
- **`DELETE /api/gitproviders/{name}`** — admin-only. Deletes the CRD,
  the managed OAuth Secret (`gitprovider-oauth-{name}`), and the per-
  provider OAuth access-token Secret (`gitprovider-token-{name}`).
  Returns 204 on success, 404 if the provider doesn't exist. Missing
  secrets are ignored.
- **Create/delete UI** — `ui/src/routes/settings/git-providers/+page.svelte`
  now has an inline "Create git provider" form (name, type, host,
  OAuth client ID+secret, webhook secret with a "Generate" helper) and
  a Delete action per row. The previous `kubectl apply` snippet in
  the empty state has been removed — the UI is now a self-contained
  admin experience. Client wired in `ui/src/lib/api.ts`
  (`createGitProvider`, `deleteGitProvider`); request type
  `CreateGitProviderRequest` in `ui/src/lib/types.ts`.

### Phase 5 — Monorepo support — **Done**

- `source.path` resolved against the clone root by
  `resolveSourceDir` (`internal/controller/app_controller.go`) and
  threaded through `buildParams.path` into the `BuildRequest.SourceDir`
  handed to BuildKit. Rejects absolute paths and any `..` segment; fails
  the build cleanly with `"source path 'x' not found in repo"` when the
  subdirectory is missing.
- `source.watchPaths` gates webhook fan-out. `parsePushEvent`
  (`internal/webhook/handler.go`) now captures a deduped union of
  `commits[].{added,modified,removed}` into
  `BuildRequest.ChangedPaths`; `dispatchToApps` calls
  `matchesWatchPaths` before patching the revision annotation. Prefix
  match only (no globs, per spec). Leading `/` on watchPaths is
  normalized. Payloads with no `commits[]` key skip the gate
  (backward-compatible with today's behaviour).
- Fixture: `test/fixtures/git-monorepo.yaml`.
- UI build grouping (the fourth bullet in SPEC.md §7.6) is deferred
  — backend-only landing.

### Phase 6 — Preview environments — **Done**

- `api/v1alpha1/previewenvironment_types.go`: real CRD types — `PreviewPhase`
  (Pending/Building/Ready/Failed/Expired), `PullRequestRef` (number/branch/SHA),
  spec fields for appRef, replicas, resources, env, bindings, domain, TTL.
  Status has phase, URL, image, expiresAt, conditions.
- `internal/controller/previewenvironment_controller.go`: full reconciler —
  parent App lookup + validation (git source, preview.enabled), async build via
  `buildTrackerStore` (same pattern as App controller), Deployment + Service +
  Ingress creation with owner references, TTL expiry auto-delete, commit status
  posting via GitAPI.PostCommitStatus. `ResolvePreviewDomain` helper for
  `{number}`/`{app}` template expansion. Injectable clock for TTL tests.
- `internal/webhook/handler.go`: PR event parsing for all three forges
  (GitHub X-GitHub-Event: pull_request, GitLab X-Gitlab-Event: Merge Request
  Hook, Gitea X-Gitea-Event: pull_request). Actions: opened → create PE,
  synchronize → update SHA, closed → delete PE. Staging env inheritance +
  preview resource overrides. Domain template resolution. k8sReader interface
  extended with listPreviewEnvironments/create/update/delete.
- `internal/webhook/k8s.go`: K8sReader implements the new PE CRUD methods.
- `cmd/main.go`: PreviewEnvironmentReconciler wired with BuildClient, GitClient,
  RegistryBackend, IngressProvider dependencies.
- Envtest tests: preview-disabled rejection, app-not-found, non-git-source
  rejection, Deployment/Service/Ingress creation with correct names + overrides,
  TTL expiry, SHA update, delete cleanup, domain template resolution.
- Integration test `test/integration/preview_test.go` covers full lifecycle.
- Fixture: `test/fixtures/git-preview.yaml`.

### Phase 7 — Polish & v1 — **Partial**

Present:
- Controller-level rollback helper
  (`app_controller.go RollbackDeployment`).
- **Rollback — full stack:** API `POST /rollback` reads deploy history and
  patches the Deployment (`internal/api/rollback.go`). CLI `mortise rollback
  <app> --env production [--index N]`. UI rollback button on each non-current
  deploy history entry with confirmation modal.
- **Promote — full stack:** API `POST /promote` copies the current image
  digest from the source environment's status to the target Deployment and
  appends a DeployRecord (`internal/api/rollback.go`). CLI `mortise promote
  <app> --from staging --to production`. UI promote buttons between
  environments in the app detail page.
- API tests (envtest): rollback valid/invalid index/env, auth required;
  promote valid/invalid env, same-env rejection, auth required.
- CLI tests: command parsing + client method HTTP path/body verification.

Missing:
- **Env-management surface (spec §5.9a):** no `PATCH/PUT /env` or
  `POST /env/import` API endpoints (env edits today require a full PUT on
  the App). No `mortise env list/set/unset/import/pull` CLI verbs (the
  one-line `env set` from §5.14 is not yet implemented). No Variables tab
  in the App detail UI. No `mortise.dev/env-hash` annotation on the pod
  template, so env-only changes don't auto-roll the Deployment.
- First-run setup wizard UI beyond the existing `setup` admin bootstrap
  route.
- Custom-domain attach flow (UI + API).
- Deploy-token verbs (see Phase 3 gaps).
- Authz role upgrade: current roles are `admin` / `member`. Spec §5.10
  expects five roles (`platform-admin`, `platform-viewer`, `team-admin`,
  `team-deployer`, `team-viewer`) + a `Team` abstraction + per-grant
  environment scoping. No `Team` CRD exists; grants have no env field.

### Phase 8 — Tenons & integration recipes — **Done**

- `charts/mortise/Chart.yaml` declares optional Helm dependencies:
  Traefik (~34.0), cert-manager (~v1.17), external-dns (~1.16), Zot (~0.1).
  Each is enabled by default and conditional (`traefik.enabled`,
  `cert-manager.enabled`, `external-dns.enabled`, `registry.builtin.enabled`).
- `charts/mortise/values.yaml` exposes toggles for all bundled components
  with sensible defaults (cert-manager CRDs auto-installed, ExternalDNS
  defaults to Cloudflare provider).
- Deployment template includes an Ingress resource that references Traefik's
  IngressClass when the bundled Traefik is enabled.
- Vendored dependency charts gitignored (`charts/mortise/charts/`).
- Integration recipe docs in `docs/recipes/`: external-ci, oidc,
  monitoring, external-secrets, backup, cloudflare-tunnel.
- UI Extensions page (`ui/src/routes/extensions/+page.svelte`) with
  categorized cards (Infrastructure, Security, Tenons) and nav link in
  the header.
- `helm lint`, `helm template`, `npm run build`, `make test` all pass.

---

## Known issues

### Issue #1 — `{app}-credentials` Secret is never created — **Resolved**
`reconcileCredentialsSecret` in `app_controller.go` now materialises the
`{app}-credentials` Secret from `spec.credentials` (Flavor A, §5.5a).
Inline values and `valueFrom.secretRef` are both supported. Well-known keys
(`host`, `port`) are omitted from the Secret — the bindings resolver fills
them in at binder time. A sha256 hash annotation on the pod template forces
rollouts on Secret rotation. Cleanup honours the "Mortise owns only what it
creates" rule. Envtest coverage: 7 cases under "credentials Secret
materialization".

### Issue #2 — Cross-project bindings can't use `secretKeyRef` — **Guarded**
`Resolve()` now returns a clear error when `binding.Project` differs from
the binder App's project, surfaced as a reconcile error on the App's status
conditions. This prevents the silent `CreateContainerConfigError` at pod
start. Cross-project bindings remain unsupported in v1; a future version
could use Secret replication or a projected-volume design.

Unit tests: `TestCrossProjectBindingReturnsError`,
`TestResolveCrossProjectMissingReturnsError` in
`internal/bindings/resolver_test.go`.

### Issue #3 — Hard-coded cert-manager cluster-issuer — **Resolved**
Previously, `internal/controller/app_controller.go` wrote
`cert-manager.io/cluster-issuer: letsencrypt-prod` as an Ingress annotation
regardless of operator configuration. The default now comes from
`PlatformConfig.spec.tls.certManagerClusterIssuer` (empty by default →
annotation omitted, so operators without cert-manager get a plain Ingress),
with per-env `tls.clusterIssuer` / `tls.secretName` overrides honoured per
spec §5.6. User annotations on the environment win over Mortise's default
cert-manager annotation (spec §5.2a). An `IngressProvider` interface is
still the longer-term home for this logic but is not required to unblock
multi-issuer installs.

### Issue #4 — Authz role model doesn't match spec
`internal/authz/native.go` uses `admin` / `member`. Spec §5.10 now calls
for five roles: `platform-admin`, `platform-viewer`, `team-admin`,
`team-deployer`, `team-viewer`, with a `Team` scope and per-grant
environment scoping (e.g. a team-deployer restricted to `[staging]`).
Project ownership and admin-only gates in the API currently key off the
two-role model. No `Team` CRD exists, and grants have no env field.

---

## Documentation drift

Items in `CLAUDE.md` that no longer reflect reality — fix these opportunistically:

- **`CRDs: App, PlatformConfig, GitProvider, PreviewEnvironment`** — missing
  `Project`. `Project` has been the top-level grouping since Phase 3.5.

`README.md` says "Phases 1–3 of the spec are complete"; Phase 3.5
(Projects) is also complete but Phase 3 (bindings) is actually **Partial**
because of issues #1 and #2. Prefer this file over README for status.

---

## Keeping this file up to date

**Every PR that moves implementation status should update `PROGRESS.md` in
the same commit.** In particular:

1. **New feature landed.** Flip its row in the at-a-glance table and fill
   in the detailed section. If the change resolves a row currently marked
   Partial, move the remaining gaps to a new sub-bullet or delete them.
2. **New bug discovered that blocks a Phase.** Add it under
   **Known issues** with a fix direction, and downgrade the relevant phase
   row to Partial.
3. **Interface impl landed.** Update the "Interface implementation
   coverage" table and cite the impl's file path.
4. **CRD goes from scaffold to real.** Update the "CRD coverage" table.
5. **Spec change.** If SPEC.md changes scope (phase reorg, new CRD, field
   removed), reconcile this file's phase headings and the "Documentation
   drift" list.
6. **Re-reconcile the Last reconciled line** at the top (with the commit
   hash) whenever you do a fuller sweep.

The goal is that a fresh Claude / human reading `PROGRESS.md` should know
without running any commands: what's real, what's a stub, and where the
landmines are.
