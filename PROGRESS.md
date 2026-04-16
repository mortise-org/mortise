# Mortise implementation progress

Tracks what is implemented vs. what the spec calls for. Update this file
whenever implementation status changes — see the **Keeping this file up to
date** section at the bottom.

Legend: **Done** / **Partial** / **Not started**
Last reconciled against spec + code: 2026-04-16 (commit TBD — PlatformConfig promotion).

---

## Status at a glance

| Phase | Spec §   | Status       | Summary |
|-------|----------|--------------|---------|
| 0 — Foundation                   | §7.1 / §8   | **Done**         | kubebuilder scaffold, chart skeleton, Makefile, test helpers + fixtures. |
| 1 — Core operator (image source) | §7.2        | **Partial**      | Deployment / Service / Ingress / PVC reconciliation works for `source.type: image`. No ServiceAccount/imagePullSecret, no IngressProvider/DNSProvider interfaces, cluster-issuer is hard-coded. |
| 2 — API + UI skeleton            | §7.3        | **Done**         | Auth, project CRUD, app CRUD, secrets CRUD, deploy webhook, SSE logs, SvelteKit UI. |
| 3 — Bindings + secrets           | §7.4        | **Partial**      | Resolver writes env vars, but the credential Secret it references is never created and cross-namespace `secretKeyRef` is invalid in k8s (see issues #1 + #2). No deploy tokens, no secret rotation endpoint. |
| 3.5 — Projects                   | §5 / §5.10  | **Done**         | `Project` CRD + controller + REST API + CLI + UI routes + default-project seeding. |
| 4 — Build system (git source)    | §7.5        | **Partial**      | `GitProvider` CRD + reconciler, three forge `GitAPI` impls, `GitClient` (go-git), `BuildClient` (BuildKit), `RegistryBackend` (OCI), webhook receiver with per-forge HMAC, OAuth authorize/callback server. Remaining: wire git-source path into `app_controller.go` so pushes actually build+deploy; `PlatformConfig` hasn't been promoted from scaffold so all three stacks are configured via ad-hoc structs. |
| 5 — Monorepo support             | §7.6        | **Not started**  | No `watchPaths` handling, no per-path routing. |
| 6 — Preview environments        | §7.7        | **Not started**  | `PreviewEnvironment` CRD is scaffold-only; controller empty. |
| 7 — Polish & v1                  | §7.8        | **Partial**      | Controller-side `RollbackDeployment` exists, but no CLI/UI for rollback, no promote, no first-run wizard, no custom-domain UI. |
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
| `IngressProvider` | —                                  | **Not started** — App controller writes `networkingv1.Ingress` directly with hardcoded annotations. |
| `DNSProvider`     | —                                  | **Not started** |

### CRD coverage

| CRD                  | Types file        | Controller       | Status        |
|----------------------|-------------------|------------------|---------------|
| `Project`            | real              | real reconciler  | **Done**      |
| `App`                | real              | real (image)     | **Partial**   — no `kind: service\|cron`, `schedule`, `concurrencyPolicy`, `sharedVars`, or `valueFrom.fromBinding` from spec §4. |
| `GitProvider`        | real (`api/v1alpha1/gitprovider_types.go`) | real reconciler (`internal/controller/gitprovider_controller.go`) | **Done** |
| `PlatformConfig`     | real (`api/v1alpha1/platformconfig_types.go`) | real reconciler (`internal/controller/platformconfig_controller.go`) | **Done** |
| `PreviewEnvironment` | scaffold (`Foo *string`) | empty TODO | **Not started** |

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
- No `test/integration/` directory. CLAUDE.md references `make test-integration`
  and `make test-integration-fast`, which do **not** exist in the Makefile.
- CLAUDE.md claims the layout contains `internal/webhook/`; it does not.

### Phase 1 — Core operator (image source) — **Partial**

Where it works (`internal/controller/app_controller.go`):
- Reconciles `Deployment`, `Service`, `Ingress`, `PersistentVolumeClaim(s)`
  for `source.type: image` apps.
- Sets owner references so everything GCs with the `App`.
- Exposes `RollbackDeployment` for the API layer to call.
- Envtest suite in `internal/controller/app_controller_test.go`.

Known gaps against spec §7.2 / §11:
- **No ServiceAccount** per App, no `imagePullSecret` wiring — spec §11 says
  the operator creates both. Private registries will not work end-to-end.
- **No `{app}-credentials` Secret** is ever written. The bindings resolver
  projects from this Secret, so backing services never surface
  user/password env vars. See **Known issues** below.
- Ingress annotation `cert-manager.io/cluster-issuer: letsencrypt-prod` is
  **hard-coded** at `internal/controller/app_controller.go:231`. Violates the
  "standards, not implementations" rule and should move behind
  `IngressProvider` + read from `PlatformConfig`.
- `IngressProvider` / `DNSProvider` interfaces exist (`internal/ingress/`,
  `internal/dns/`) but have no impls; the controller bypasses them.
- `Reconcile` returns early for any `source.type` other than `image` — git
  path is unimplemented.

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

Missing:
- **Issue #1** — the `{app}-credentials` Secret the resolver projects from
  (`resolver.go:56`) is never created by any controller. Any `App` that
  declares `spec.credentials` will not expose those keys to binders.
- **Issue #2** — even if #1 is fixed, the resolver emits plain
  `SecretKeyRef{Name: {app}-credentials}` for cross-project bindings
  (`resolver.go:73-79`). Kubernetes `envFrom`/`env.valueFrom.secretKeyRef`
  only resolves within the Pod's own namespace. Cross-project bindings will
  silently fail at Pod create. Needs either Secret replication (ESO-style),
  projected-volume mount, or an initContainer fetch.
- No deploy tokens (`mortise token create/list/revoke`, `Authorization:
  Bearer mrt_...`) from spec §5.1 / §7.8 phase 7.
- No rotation endpoint for user secrets.
- `App.Spec.Credentials` is currently `[]string` — spec §4 Apps-as-DB
  example supplies richer per-credential metadata; revisit when backing
  services land.

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
- **App controller git path** — `internal/controller/app_controller.go`
  returns early for `source.type != image`. It needs to call
  `GitClient.Clone` → `BuildClient.Build` → `RegistryBackend.PushTarget`
  and then flow the resulting image digest into the Deployment it already
  knows how to create.
- **PlatformConfig wiring** — `PlatformConfig` CRD and loader are now real
  (see "PlatformConfig — Done" section). Each stack still takes its config
  via a plain Go struct at construction time; the follow-up PR rewires the
  operator entrypoint (`cmd/main.go`) to call `platformconfig.Load` and
  inject config into each stack.
- **Webhook → build dispatch** — the webhook receiver parses events and
  logs/queues them (placeholder) but does not yet actually call the build
  pipeline. Wiring belongs in the same PR that adds the app-controller
  git path.
- **`test/fixtures/git-basic.yaml`** — still missing; add alongside the
  app-controller git path test.

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

### Git provider stack — **Partial**

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

**Deferred — moves this to Partial not Done:**
- **Frontend for OAuth** — no SvelteKit page drives the authorize →
  callback round-trip yet. The server endpoints work when hit directly
  but a user can't onboard a forge from the UI.
- **Webhook → build dispatch** — see cross-stack deferred work above.
- **App controller git path** — see cross-stack deferred work above.
- **`PlatformConfig` wiring** — see cross-stack deferred work above.
- **Integration tests against local Gitea** — belongs in the
  (not-yet-wired) `test/integration/` harness.

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

**Deferred — operator rewiring (explicit follow-up PR):**
- `internal/registry/`, `internal/build/`, and `internal/git/` stacks still take config via ad-hoc Go structs at construction time. When the follow-up PR lands, the operator entrypoint (`cmd/main.go`) will call `platformconfig.Load` and inject config into each stack.
- `IngressProvider` and cert-manager wiring (`spec.tls.certManagerClusterIssuer`) remain deferred to that same follow-up.

### Phase 5 — Monorepo support — **Not started**

- `App` spec has no `watchPaths` field.
- No mechanism to gate rebuilds on path-filtered diffs.

### Phase 6 — Preview environments — **Not started**

- `api/v1alpha1/previewenvironment_types.go` is scaffold only.
- `internal/controller/previewenvironment_controller.go` is empty.
- No PR-event handling (blocked on Phase 4's webhook receiver).

### Phase 7 — Polish & v1 — **Partial**

Present:
- Controller-level rollback helper
  (`app_controller.go RollbackDeployment`).

Missing:
- CLI `mortise rollback <app> [--env]` — not registered in `newRootCmd`.
- CLI `mortise promote <app> --from staging --to production` — not
  implemented.
- UI rollback / promote / history view.
- First-run setup wizard UI beyond the existing `setup` admin bootstrap
  route.
- Custom-domain attach flow (UI + API).
- Deploy-token verbs (see Phase 3 gaps).
- Authz role upgrade: current roles are `admin` / `member`. Spec §5.10
  expects `platform-admin` / `team-admin` / `team-member` + a `Team`
  abstraction. No `Team` CRD exists.

### Phase 8 — Tenons & integration recipes — **Not started**

- `charts/mortise/Chart.yaml` has no `dependencies:` — no bundled
  Traefik / cert-manager / ExternalDNS / Zot subcharts.
- `charts/mortise/values.yaml` exposes only operator image / resources /
  service. No toggles for bundled components (spec §13).
- No documented recipes for ESO / OPA / Prometheus.

---

## Known issues

### Issue #1 — `{app}-credentials` Secret is never created
`internal/bindings/resolver.go:56` projects env vars from a Secret named
`{boundApp.Name}-credentials`. No controller creates this Secret. Backing
services declared via `spec.credentials` will never expose their
credentials to binders.

**Fix direction:** have the App controller (or a new `credentials` package)
generate/rotate the `{app}-credentials` Secret when `spec.credentials` is
non-empty, and own it via `controllerutil.SetControllerReference`.

### Issue #2 — Cross-project bindings can't use `secretKeyRef`
`resolver.go:73-79` returns `EnvVar{ValueFrom: SecretKeyRef{...}}` for
every credential key regardless of whether the binding is cross-project.
`secretKeyRef` is resolved by the kubelet in the **Pod's** namespace — it
cannot reach a Secret in another namespace. Cross-project-bound Pods will
fail to start with `CreateContainerConfigError`.

**Fix direction:** replicate the bound Secret into the consumer's namespace
(ESO-style reflector), or switch cross-project bindings to a projected
volume populated by an initContainer that calls the API with the consumer's
ServiceAccount token.

### Issue #3 — Hard-coded cert-manager cluster-issuer
`internal/controller/app_controller.go:231` writes
`cert-manager.io/cluster-issuer: letsencrypt-prod` as an Ingress
annotation. Violates the "standards, not implementations" rule and breaks
for anyone using a different issuer. Should be sourced from
`PlatformConfig` once that CRD is real, and should flow through
`IngressProvider`.

### Issue #4 — Authz role model doesn't match spec
`internal/authz/native.go` uses `admin` / `member`. Spec §5.10 calls for
`platform-admin` / `team-admin` / `team-member` with a `Team` scope.
Project ownership and admin-only gates in the API currently key off the
two-role model.

---

## Documentation drift

Items in `CLAUDE.md` that no longer reflect reality — fix these opportunistically:

- **`CRDs: App, PlatformConfig, GitProvider, PreviewEnvironment`** — missing
  `Project`. `Project` has been the top-level grouping since Phase 3.5.
- **`make test-integration`** and **`make test-integration-fast`** — not
  defined in the Makefile.
- **Testing Layers table** claims an "Integration" layer with a harness at
  `test/integration/` — the directory does not exist.

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
