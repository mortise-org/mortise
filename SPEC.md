# Capybara — Product & Engineering Spec

> Codename: **Capybara** (repo placeholder; product name TBD)
> Status: Draft for engineering kickoff
> Audience: engineers building v1

---

## 1. What We're Building

A self-hosted, Railway-style developer platform that runs on top of an existing
Kubernetes cluster. Developers connect a git repo (or point at a pre-built image),
and Capybara handles builds, deploys, domains, TLS, environment variables,
volumes, preview environments, and service-to-service bindings. The user-facing
experience abstracts Kubernetes away entirely — users think in "apps," not
Deployments, Services, Ingresses, or Helm charts.

The product ships in two layers:

1. **Core (v1)** — the minimum Railway clone. Installable on any k3s/k8s cluster
   via a single Helm chart. Zero addons required. This is what we build first.
2. **Addon pack (post-v1)** — optional subcharts that bolt onto core to provide
   SSO (Authentik), secret management (OpenBao), monitoring (Prometheus/Grafana),
   platform health UI, backup/restore, Helm-source deployments, curated service
   catalog, storage wizard, and more. Each addon is independently installable.

The split matters: core stands on its own as a Coolify-for-Kubernetes. The addon
pack turns it into a full homelab platform. Users who only want the Railway UX
never see or pay for the platform ambition.

---

## 2. Positioning

Capybara is what a developer installs when there is no platform team. It produces
the Kubernetes manifests (internally, via CRD reconciliation) and runs the CD
itself. The user writes an `App` (or clicks through the UI) and Capybara handles
everything downstream — build, push, deploy, domain, TLS, metrics, secrets,
bindings.

**Not competing with** Argo CD / Flux CD (GitOps CD engines for platform teams
managing manifests-as-code). Capybara coexists with them: platform teams can
GitOps-manage the cluster infra and Capybara itself; developers deploy apps
through Capybara without touching manifest repos.

**Differentiated from:**
- **Kubero** — closest existing product, but manual webhook setup, manual
  DNS/TLS, buildpack-only builds, deprecated Bitnami catalog.
- **Coolify** — excellent UX on Docker/VPS, no Kubernetes mode.
- **Gimlet** — archived March 2025. The niche needs a sustainable plan from day one.
- **Otomi** — dead May 2024; tried to be a batteries-included platform first and
  added developer UX second. Capybara does the opposite: ship the Railway UX
  first, layer the platform on top.
- **Railway / Render** — SaaS only, not self-hostable.

---

## 3. Target Users (v1)

- **Homelabbers** on k3s/Talos/RKE2 who want Railway-quality deploys on their
  own hardware
- **Small dev teams** (2–15 engineers) who want self-hosted Railway on their own
  cloud k8s without a dedicated platform team
- **Regulated industries** that need Railway-quality UX but cannot send code or
  data through third-party infra

**Assumption:** a Kubernetes cluster already exists. Capybara installs as a Helm
chart onto it. Cluster provisioning (k3s bootstrap, RKE2 HA, cloud k8s) is
explicitly out of scope for v1 — "not the problem we're solving." It may return
as an addon or CLI wrapper later.

---

## 4. Core Design Principles

1. **Kubernetes is invisible to the user.** The App CRD is an internal
   implementation detail. Users interact via UI, CLI, or the REST API — never
   kubectl. No user doc in v1 shows YAML for a Deployment, Service, or Ingress.
2. **Everything is an App.** One unified concept. Source type (`git` | `image`
   in v1) determines how it deploys; bindings and network flags handle
   backing-service cases.
3. **Extensibility via Helm subcharts, not feature flags.** Core is its own
   chart. Each addon (Authentik, OpenBao, monitoring, catalog, etc.) is a
   separate subchart under an umbrella. Fast-path install gives core only.
4. **API-first.** REST + WebSocket, full OpenAPI spec, externally accessible.
   Multi-cluster, custom UIs, and any future orchestration layer become thin
   wrappers over this API.
5. **Neutral data model.** Even though v1 targets single-cluster and often
   single-node, the CRD leaves room for replicas, storage class overrides, and
   future cluster selection. Scale-up is additive, not a rewrite.
6. **Heavy TDD with a one-command test bench.** If `make test-integration` is
   not one command and under 3 minutes, nothing will get tested. The harness is
   the product.

---

## 5. v1 Scope — The Railway Clone

### 5.1 Source Types (v1)

**`git`** — build from source. Auto-detects Dockerfile or language. Full build
pipeline, preview environments, deploy history. Monorepo-aware via
`source.path` + `source.watchPaths`.

**`image`** — pre-built image. No build. Covers self-hosted apps
(Paperless-ngx, Vaultwarden, etc.) and the v1 Postgres/Redis path
(image + PVC + manual credentials block).

Explicitly **deferred to addon pack**: `helm`, `external`, `catalog` source
types.

### 5.2 App CRD (v1 surface)

```yaml
apiVersion: capybara.dev/v1alpha1
kind: App
metadata:
  name: my-app
  namespace: my-project
spec:
  source:
    type: git                         # git | image
    repo: https://github.com/org/monorepo
    branch: main
    path: services/api                # optional; monorepo subdir
    watchPaths: [services/api, shared] # optional; rebuild triggers
    build:
      mode: auto                      # auto | dockerfile | railpack
      dockerfilePath: Dockerfile
      cache: true
      args: { NODE_ENV: production }
    # --- OR ---
    # type: image
    # image: ghcr.io/paperless-ngx/paperless-ngx:latest
    # pullSecretRef: ghcr-credentials

  network:
    public: true                      # default true; false for backing services

  storage:                            # list of named volumes
    - name: data
      mountPath: /data
      size: 10Gi
      # storageClass: override        # default: platform default SC
      # accessMode: auto              # auto | RWO | RWX (v1 defaults to RWO)

  credentials:                        # backing-service declaration
    - DATABASE_URL
    - host
    - port
    - user
    - password

  environments:
    - name: production
      replicas: 2
      resources: { cpu: 500m, memory: 512Mi }
      env:
        - name: PORT
          value: "3000"
        - name: API_KEY
          valueFrom: { secretRef: my-app-api-key }
      bindings:
        - ref: my-db                  # injects bound App's credentials as env
      domain: my-app.yourdomain.com
      customDomains: [app.theirdomain.com]
    - name: staging
      replicas: 1
      domain: my-app-staging.yourdomain.com

  preview:
    enabled: true
    domain: "pr-{number}-my-app.yourdomain.com"
    ttl: 72h
    resources: { cpu: 250m, memory: 256Mi }
```

### 5.3 Build System

**Mode detection:**
- Repo contains `Dockerfile` → **Dockerfile mode.** BuildKit builds it directly
  via the `dockerfile.v0` frontend.
- No Dockerfile → **Railpack mode.** Railpack (embedded as a Go library in the
  operator) detects the language, emits a BuildKit LLB graph, operator submits
  it to BuildKit.

Override via `source.build.mode: dockerfile | railpack` (default `auto`).

**BuildKit:** runs as a single rootless Deployment in the `capybara-builds`
namespace with a PVC for `/var/lib/buildkit` layer cache. Installed on-demand
the first time a `git` App is created (not part of base install). Operator
serializes submissions through an internal queue and talks to BuildKit via the
native Go client. Scale-out revisited if p99 queue wait exceeds ~2 minutes.

**Build cache:** OCI artifacts in the configured registry, keyed per app per
branch.

**Monorepo fan-out:** one webhook per push; operator iterates every App whose
`source.repo` matches, compares changed paths against each App's `watchPaths`
prefixes, rebuilds only matching Apps. UI groups builds by commit ("3 of 12
apps rebuilding from commit abc123"). Previews follow the same rule — PR
touching `services/api/` creates a preview for the `api` App only; PR touching
`shared/` creates previews for every App that watches `shared/`.

**On push:**
- Build logs stream to UI via WebSocket in real-time
- Success: operator updates the environment's Deployment with the new image
  digest; rolling update proceeds
- Failure: deploy blocked; error surfaced in UI; commit status posted back via
  git provider API

### 5.4 Deploy Webhook (External CI)

First-class from v1. Any CI that can `curl` can deploy:

```bash
curl -X POST https://capybara.yourdomain.com/api/deploy \
  -H "Authorization: Bearer $DEPLOY_TOKEN" \
  -d '{"app":"my-app","environment":"production","image":"registry/org/my-app:abc123"}'
```

**Deploy tokens:**
- Scoped per App + per environment
- Created via UI or CLI; displayed once; stored in secret store
- Revocable; multiple tokens per App/env allowed
- CI snippet shown alongside on creation

This is the extensibility seam for any CI system. Users keep GitHub Actions,
GitLab CI, Woodpecker, Jenkins, bash — Capybara just handles build (if git
source) and deploy. No CI integration needed in v1.

### 5.5 Bindings — The Magic

An App with `credentials` declared is a backing service. Other Apps bind to it
by `ref`. At pod-start time the operator resolves the bound App's credentials
(password from the secret store, constructed `DATABASE_URL`, host/port from
Service DNS) and injects them as env vars.

The v1 "click Postgres, get DATABASE_URL in my API" Railway moment works via
an image-source App:

```yaml
kind: App
metadata: { name: my-db }
spec:
  source: { type: image, image: postgres:16 }
  network: { public: false }
  storage:
    - { name: pgdata, mountPath: /var/lib/postgresql/data, size: 10Gi }
  env:
    - name: POSTGRES_PASSWORD
      valueFrom: { secretRef: my-db-password }
  credentials: [DATABASE_URL, host, port, user, password]
```

A "New Postgres" template button in the UI pre-fills this form. Looks like
Railway; under the hood it's just an image source plus bindings. Operator-backed
catalog with HA/PITR/backups is addon-pack territory.

### 5.6 Network, Domains, TLS

Operator annotates `Ingress` → ExternalDNS creates DNS record → cert-manager
issues TLS cert. Zero user action. Each environment gets its own subdomain
automatically, rooted at the platform domain configured at install.

Custom domains: user sets CNAME, adds the domain in UI, Ingress rule + TLS
added by operator.

### 5.7 Storage (v1)

Capybara is deliberately unopinionated about CSI backends. The App CRD accepts
a list of named volumes; each references a StorageClass (defaulting to the
cluster's default SC). For v1:

- **Single-node / homelab (k3s default):** local-path-provisioner. RWO only.
  Fine for most apps.
- **Multi-node or cloud:** use the cluster's default SC.
- **RWX volumes:** supported if the user picks a StorageClass that provides it
  (NFS, EFS, Longhorn-over-NFS). No storage wizard in v1 — that's addon-pack.

**v1 simplifications:**
- `accessMode: auto` infers RWO for replicas=1, otherwise reads the SC's capability
- `perReplica` / StatefulSet-per-volume is **deferred to addon pack** — v1
  supports single volume per App with RWO or RWX
- Multi-volume is supported (list of volumes) but the fancy tier-detection
  (fast / shared / default) is addon-pack

### 5.8 Environments & Deploy Model

- Named environments per App (e.g. `production`, `staging`)
- Independent Deployments, isolated by namespace
- **Promote:** staging → production with no rebuild
- **Rollback:** deploy history (digest + timestamp + SHA); one-click
- **Preview environments (git source only):** PR opens → operator creates
  `PreviewEnvironment`, clones staging's config, DNS + TLS handled automatically,
  bindings live-resolved through staging (no credential copy). PR closes or TTL
  expires → everything deleted. URL posted as PR comment.

### 5.9 Secrets (v1 — no OpenBao)

v1 uses Kubernetes Secrets directly as the backend. The user-facing API is
identical to what OpenBao will provide later — the storage backend is an
interface the operator writes against.

- Write-only editor in UI (values never displayed after save)
- Rotation: `secret rotate` writes a new value, rolling-restarts every App
  referencing it
- Scoped as `<app>/<environment>/<key>`

OpenBao wiring is addon-pack. The `SecretBackend` interface is designed for swap.

### 5.10 Auth (v1 — native)

v1 ships with native user accounts stored in the operator's own database (one
admin created during wizard; invites via generated link). Authentik OIDC
integration and forward-auth-per-App are **addon-pack**.

- Roles: admin / member
- Admin can manage users, providers, DNS, platform settings, all apps
- Member can create/manage own apps, view shared apps
- Teams + per-app grants are v2

### 5.11 In-UI Metrics (v1)

`metrics-server` baseline: CPU/memory per pod/environment surfaced in UI. No
Prometheus installed by core. Grafana + Prometheus stack is addon-pack. UI
degrades gracefully — charts show what's available.

### 5.12 Git Providers (v1)

- GitHub via self-registered OAuth app (pre-filled
  `github.com/settings/apps/new` URL; ~90s; no external dependency)
- GitLab (.com or self-hosted) via OAuth app + auto webhook registration
- Gitea / Forgejo via OAuth app + auto webhook registration
- GitProvider CRD, one per configured provider; credentials reference secret
  store

**Relay-mode Cloudflare Worker is addon-pack.** Self-registered is the v1 path —
no third-party infrastructure required to install Capybara.

### 5.13 Web UI (v1)

- **Dashboard** — Apps, status badges, last deploy, active previews
- **New app** — source picker (git | image) → guided form
- **App detail** — deploy history, real-time build logs, environment tabs,
  metrics, custom domains, secrets editor, bindings, deploy tokens
- **Secret store** — list by app/env, write-only values, rotation
- **Platform settings** — domain, DNS, git providers, user management

UX standard: Railway-quality. Source types abstracted — users see "your apps."

### 5.14 CLI (v1)

Railway-style: short commands, positional args, interactive prompts when
ambiguous. Full flags for scripting/CI.

```bash
capybara app list
capybara app create --source git --repo github.com/org/my-app
capybara app create --source image --image ghcr.io/paperless-ngx/paperless-ngx:latest
capybara deploy my-app --env production --image registry/org/my-app:abc123
capybara promote my-app
capybara rollback my-app
capybara logs my-app
capybara secret set my-app API_KEY=xxx
capybara secret rotate my-app API_KEY
capybara env set my-app PORT=3000
capybara domain add my-app api.customer.com
capybara token create my-app production
capybara preview list my-app
```

Config at `~/.config/capybara/config.yaml`.

### 5.15 CRDs (v1)

| CRD | Scope | Purpose |
|---|---|---|
| `App` | Namespaced | Deploy anything (git or image in v1) |
| `PreviewEnvironment` | Namespaced | Ephemeral PR environments (auto-managed) |
| `PlatformConfig` | Cluster | Platform settings (domain, DNS, default SC) |
| `GitProvider` | Cluster | One per configured git provider |

---

## 6. Explicitly Deferred (Addon Pack)

Everything below is **not in v1**. Each maps to an optional subchart or CLI
feature, installable independently after core is live.

- **Authentik** SSO + forward auth per App
- **OpenBao** secret backend (v1 uses k8s Secrets; interface is swap-ready)
- **External Secrets Operator** integration for AWS/GCP/Vault backends
- **Prometheus + Grafana** stack with auto-wired OAuth and pre-loaded dashboards
- **Loki** log aggregation
- **Platform Health** page (per-component cards with curated fixes)
- **Backup / restore** to S3 or NFS (CRD export + Velero + secret snapshots)
- **`helm` source type** — install arbitrary Helm charts through Capybara UI
- **`external` source type** — wrap already-running services with domain/TLS/auth
- **`catalog` source type** — operator-backed backing services (CloudNativePG
  for Postgres, redis-operator for Redis, MinIO, etc.) with per-entry credential
  extractors
- **Self-hosted app catalog** — curated presets for Paperless, Vaultwarden, etc.
- **Cloudflare Worker relay** for GitHub App mode
- **Cloudflare for SaaS** custom hostname automation
- **Cloudflare Tunnel** deployment automation
- **Storage wizard** with Longhorn/NFS install flows and tier detection
- **`perReplica` volumes** / StatefulSet workloads
- **Multi-cluster** (Cluster CRD, bearer-token trust, aggregated UI)
- **Cluster provisioning** (k3s bootstrap CLI, RKE2 HA installer)

---

## 7. Testing Strategy

Heavy TDD is non-negotiable. The test bench is as much the product as the
operator is. If adding coverage feels harder than writing new features, it
won't happen, and regressions will bury us.

### 7.1 Layers

| Layer | Tool | Scope | Feedback |
|---|---|---|---|
| **Unit** | `go test` + controller-runtime fake client | Pure logic, no cluster | <10s |
| **Controller (envtest)** | `sigs.k8s.io/controller-runtime/pkg/envtest` | Reconcile loops against real apiserver + etcd binaries; no kubelet | ~2s/test |
| **Integration** | `k3d` cluster + Helm install + `go test -tags=integration` | Real mini-cluster, real pods, real Traefik/cert-manager/Zot | <3min total |
| **E2E** | Dogfooding k3s cluster | Real GitHub webhooks, real Cloudflare DNS, real LE staging | Nightly |
| **UI E2E** | Playwright against k3d integration stack | Critical user flows | Per PR |

**No KWOK** (node simulator) in v1 — not useful when we actually need pods to
run builds and bind volumes.

### 7.2 Repo Layout (Claude-friendly conventions)

```
/
├── cmd/                             # operator, CLI, UI server entrypoints
├── api/v1alpha1/                    # CRD types
├── controllers/                     # reconcile logic
│   └── *_test.go                    # envtest beside controllers
├── internal/                        # business logic
│   ├── build/                       # BuildKit + Railpack
│   ├── bindings/
│   ├── secrets/                     # backend interface + k8s Secrets impl
│   └── ...
├── test/
│   ├── integration/                 # k3d-based
│   │   ├── suite_test.go            # TestMain asserts cluster ready
│   │   ├── git_source_test.go
│   │   ├── image_source_test.go
│   │   ├── bindings_test.go
│   │   └── ...
│   ├── e2e/                         # nightly, real infra
│   ├── fixtures/                    # canonical App CRDs
│   └── helpers/                     # namespace lifecycle, assertion helpers
├── charts/
│   └── capybara/                    # umbrella Helm chart (v1 = core only)
├── ui/                              # React app
├── Makefile
└── README.md
```

### 7.3 Makefile — the entire test bench

```makefile
# Fast feedback
test:
	go test ./...

# k3d + chart + integration suite, ephemeral
test-integration: cluster-up chart-install integration-run cluster-down

integration-run:
	go test ./test/integration/... -tags=integration

cluster-up:
	k3d cluster create capybara-test --wait --registry-create capybara-registry
	kubectl wait --for=condition=Ready nodes --all --timeout=60s

cluster-down:
	k3d cluster delete capybara-test

chart-install:
	helm upgrade --install capybara ./charts/capybara \
	  --namespace capybara-system --create-namespace \
	  --set image.tag=dev --wait

# Persistent dev loop
dev-up:
	k3d cluster create capybara-dev --registry-create capybara-registry
	$(MAKE) chart-install
	tilt up

dev-down:
	k3d cluster delete capybara-dev

# Fast integration against already-running dev cluster
test-integration-fast:
	go test ./test/integration/... -tags=integration

# UI e2e against dev cluster
test-ui:
	cd ui && pnpm playwright test

test-all: test test-integration
```

### 7.4 Test Conventions

- **One command per intent.** `make test`, `make test-integration`,
  `make dev-up`. No flags to remember.
- **Every integration test creates its own namespace.** Registered with
  `t.Cleanup`; tests are independent and parallelizable.
- **Fixtures live in `test/fixtures/`.** Tests load by path, mutate, apply.
  New tests copy a fixture and adapt; they do not hand-write App YAML shapes.
- **Assertion helpers in `test/helpers/`** (e.g. `assertPodsRunning`,
  `requireEventually`, `loadFixture`). Every integration test body reads the
  same way: create namespace → load fixture → apply → wait → assert.
- **`TestMain` in `suite_test.go`** asserts cluster + chart are ready; no
  per-test cluster lifecycle code.
- **`//go:build integration`** tag keeps integration tests out of the default
  `go test` pass.
- **CONTRIBUTING.md** names the targets and conventions. New contributors and
  AI agents read it first.

### 7.5 What AI Agents Do Well Here

- Fill in table-driven CRD validation test cases
- Write Playwright flows from UI mockups
- Extend integration coverage by pattern-matching existing tests (copy nearest
  neighbor, change fixture, change assertions)
- Write fixtures

### 7.6 What AI Agents Do Not Do Here

- Design the harness
- Replace the integration suite with "an agent that checks things"
- Invent YAML shapes outside `test/fixtures/`

### 7.7 Mocking Policy

- **BuildKit:** mock the client interface in unit tests. Real BuildKit runs
  only in integration tests (nested-container performance cost; acceptable for
  ~5 integration tests that actually exercise the full build path).
- **Git providers:** mock HTTP at the API-client boundary for unit tests; use
  a local Gitea in integration tests.
- **ACME (cert-manager):** use Pebble (cert-manager's local ACME server) in
  integration.
- **DNS:** ExternalDNS in-memory provider in integration.
- **Registry:** real Zot running in the k3d cluster, backed by the k3d
  registry volume.

### 7.8 CI (for Capybara itself)

GitHub Actions, hosted runners. Not self-hosted. Workflows:
- `test.yml` — `make test` on every PR (fast)
- `test-integration.yml` — `make test-integration` on every PR (k3d in GH Actions runner)
- `nightly.yml` — `make test-e2e` against the dogfooding cluster

Self-hosting the Capybara project's own CI is out of scope.

---

## 8. Packaging

### 8.1 Umbrella Helm Chart

```
charts/
└── capybara/               # umbrella
    ├── Chart.yaml          # lists subcharts
    ├── values.yaml         # top-level toggles
    └── charts/
        ├── capybara-core/  # always-on: operator, API, UI, CRDs, Traefik, cert-manager, ExternalDNS
        └── (addons in future: authentik, openbao, monitoring, catalog, ...)
```

**v1 ships with `capybara-core` only.** Addon subcharts land over time, each
independently installable. The umbrella chart exists so addons can declare
dependencies and inherit shared values (domain, DNS provider, etc.).

### 8.2 Install UX

```bash
# Fast path (core only)
helm install capybara oci://ghcr.io/capybara/capybara \
  --namespace capybara-system --create-namespace \
  --set domain=yourdomain.com \
  --set dns.provider=cloudflare \
  --set dns.apiToken=xxx
```

Later (addon pack available):

```bash
# Pick addons; CLI walks through them interactively
capybara platform install            # interactive picker (authentik? monitoring? ...)
capybara platform install --addons=authentik,monitoring
```

The CLI wraps `helm upgrade` on the umbrella chart with the selected subchart
values — users don't write Helm commands themselves.

---

## 9. Implementation Phases

Phases are ordered by dependency, not by timeline. Each phase leaves the repo
in a shippable state — nothing half-done carried over.

### Phase 0 — Foundation

**Repo bootstrapping:**
- Go module layout (`cmd/`, `api/`, `controllers/`, `internal/`)
- kubebuilder scaffolding for the `App`, `PreviewEnvironment`,
  `PlatformConfig`, `GitProvider` CRDs (skeleton, no reconcile yet)
- Umbrella Helm chart with `capybara-core` subchart (operator Deployment,
  CRDs, RBAC; Traefik/cert-manager/ExternalDNS declared as chart dependencies)
- Makefile with all test-bench targets (may be stubs at first)
- CI: `test.yml` (unit + envtest) and `test-integration.yml` (k3d)
- Test helpers: `loadFixture`, `createTestNamespace`, `requireEventually`,
  `assertPodsRunning`, `assertIngressExists`
- `test/fixtures/` seeded with a minimal App CRD per source type
- CONTRIBUTING.md documenting conventions

**Exit criteria:** `make test` and `make test-integration` both pass on a
near-empty codebase. The harness is real before any product logic is written.

### Phase 1 — Core Operator (image source)

Chosen first because it excludes the build subsystem — the simplest complete
path from CRD to running pod.

- App reconcile for `source.type: image`
- Environments produce Deployments + Services + Ingresses
- ExternalDNS annotations applied
- cert-manager `Certificate` requested and mounted
- `network.public` flag toggles Ingress
- `storage` list produces PVCs; single-volume RWO first, then multi-volume, then RWX
- `resources`, `replicas`, `env` respected
- `valueFrom.secretRef` reads from the Capybara secret store and projects as
  env vars
- Rolling updates on spec change
- Rollback via deploy history (stored in operator DB or CR status)

Tests (integration tier): apply an image App → pod runs → Ingress reachable;
change replicas → scales; change image → rolls; delete → namespace cleaned.

**Exit criteria:** Paperless-ngx deployable end-to-end from a YAML file via
`kubectl apply` with no UI.

### Phase 2 — API + UI Skeleton

- REST API with OpenAPI spec
- Auth: native accounts, JWT, first-user bootstrap via Helm values
- WebSocket endpoint for log streaming
- React UI skeleton: login, dashboard (list Apps), new-app form for `image`
  source only, App detail page with env / secrets / domains
- CLI (`capybara app create --source image`, `capybara logs`, `capybara status`)

**Exit criteria:** deploy Paperless-ngx entirely through the UI or CLI; no
YAML visible to the user.

### Phase 3 — Bindings + Secrets

- `credentials` block on App; bound-App's credentials injected as env in
  binder's pod
- Secret store interface; v1 backend = k8s Secrets
- Secret CRUD API + UI; write-only editor; rotation with rolling restart of
  referrers
- Deploy tokens (per App + per env), created via UI/CLI

**Exit criteria:** "Create Postgres App (image), create API App (image) that
binds to it, DATABASE_URL appears in API's pod env." The Railway moment,
without builds yet.

### Phase 4 — Build System (git source)

- Git provider CRD + controllers: GitHub (self-registered), GitLab, Gitea
- Webhook receiver + HMAC verification
- Repo clone into temp workspace
- Dockerfile mode: BuildKit `dockerfile.v0` frontend, submit via Go client
- Railpack mode: library integration (`GenerateBuildPlan` →
  `ConvertPlanToLLB` → submit)
- BuildKit as a single rootless Deployment + PVC (installed on first git App)
- Registry: Zot (default, bundled in `capybara-core`) or external (GHCR,
  Docker Hub, custom); Helm values-driven
- Build cache: OCI artifacts, keyed per app/branch
- Operator submission queue serializes builds
- Build logs streamed to UI via WebSocket
- On success: image digest updates the App's environments → rolling deploy
- On failure: commit status posted back

**Exit criteria:** connect a GitHub repo, push, see build logs in UI, pod
rolls with the new image.

### Phase 5 — Monorepo Support

- `source.path` sets BuildKit context subdirectory
- `source.watchPaths` filters rebuilds by changed paths (prefix match, no
  globs in v1)
- Webhook fan-out across Apps sharing a repo
- UI groups builds by commit when multiple Apps rebuild from one push

**Exit criteria:** one repo backs two Apps at different subdirectories; a push
touching only one subdir rebuilds only that App.

### Phase 6 — Preview Environments

- `PreviewEnvironment` CRD auto-managed by a dedicated controller
- PR open → clone staging env config; apply `preview.*` overrides; DNS + TLS
  handled by existing ExternalDNS/cert-manager plumbing
- Bindings live-resolved through staging (no credential copy)
- PR comment with preview URL(s); monorepo fan-out respected (one preview per
  matching App, grouped comment)
- PR close → delete; TTL fallback (72h default)

**Exit criteria:** open a PR, get a live preview URL in the PR comment, close
the PR, preview disappears.

### Phase 7 — Polish & v1 Release

- Promote (staging → production, no rebuild)
- Rollback UI (deploy history browser)
- Custom domains UI
- Metrics via `metrics-server` in UI (CPU/mem per pod/env)
- First-run wizard (domain, DNS provider, git provider, admin account,
  storage class confirmation)
- Install docs, architecture docs, API reference auto-generated from OpenAPI
- Pick the real product name; rename repo + CRD apiVersions + chart + CLI +
  domain before tagging v1

**Exit criteria:** a new user goes from "empty k3s cluster" to "deployed app
with preview envs" in under 15 minutes using only the UI and CLI.

### Post-v1 — Addon Pack (order TBD)

Each of these is an independent subchart and an independent work stream. None
block the others; order depends on user demand after v1 ships.

- **Authentik** subchart + OIDC wiring into core + forward-auth middleware
  for Apps
- **OpenBao** subchart + swap from k8s-Secrets backend
- **ESO integration** for AWS/GCP/Vault external backends
- **kube-prometheus-stack** subchart + Grafana OAuth + pre-loaded dashboards
- **Loki** subchart + log aggregation in UI
- **Platform Health** page + per-component status API
- **Backup/restore** to S3/NFS (CRD export + OpenBao snapshots + Velero)
- **`helm` source type** — arbitrary Helm chart deploys through UI
- **`external` source type** — wrap external services with domain/TLS
- **`catalog` source type** — CNPG Postgres, redis-operator Redis, MinIO,
  per-entry credential extractors
- **Self-hosted app catalog** — curated presets around the `image` source
- **Cloudflare Worker relay** for GitHub App OAuth
- **Cloudflare for SaaS** custom hostname automation
- **Storage wizard** — Longhorn and NFS install flows, tier detection
- **`perReplica` volumes** — StatefulSet support
- **Multi-cluster** — Cluster CRD, bearer-token trust, aggregated UI
- **Cluster provisioning** — `capybara bootstrap` wrapping k3s install

---

## 10. Open Questions

1. **Product name.** `[NAME]` / Capybara is a placeholder. The name gets baked
   into CRD apiVersions (`capybara.dev/v1alpha1`), Helm chart, CLI binary,
   config path, and the domain for any hosted assets (relay Worker later).
   Pick before tagging v1.
2. **Operator datastore.** The operator needs somewhere to store deploy
   history, users (pre-Authentik), and audit logs. Options: CRD status +
   ConfigMaps (stateless, ugly), sqlite on a PVC (simple, single-replica
   operator), embedded BoltDB, Postgres (heavy but standard). Recommendation:
   sqlite on PVC for v1 — simplest; migrate to Postgres when we ship HA.
3. **UI packaging.** Served by the operator binary (embed via `embed.FS`) or
   separate Deployment? Recommendation: embed — one fewer pod, simpler install.
4. **Helm chart distribution.** OCI (ghcr.io) or classic Helm repo? OCI is the
   direction; ghcr.io fits if the source lives on GitHub.
5. **Minimum supported Kubernetes version.** Propose 1.28+ (matches k3s latest
   and all cloud-managed providers).

---

## 11. Interface Contracts — Two Layers

There are two contract layers in the codebase, pointing in opposite directions.
Keeping them straight is the single most important architectural discipline in
the project.

### 11.1 Outward Contracts (Seams / Interfaces)

Contracts that **Capybara's own code agrees to** when it talks to external
systems. Nobody outside the codebase sees these — they are internal plumbing.

```
Capybara controller  →  SecretBackend interface  →  k8s Secrets / OpenBao / AWS
Capybara controller  →  GitProvider interface    →  GitHub / GitLab / Gitea
Capybara controller  →  BuildClient interface    →  BuildKit
Capybara controller  →  DNSProvider interface    →  ExternalDNS / Cloudflare
```

Controllers import only Capybara's own types. They never import
`github.com/google/go-github`, `moby/buildkit/client`, or any other third-party
SDK directly. Every external dependency is wrapped behind an interface that
lives in `internal/<name>/`.

**v1 interfaces:**

```go
// internal/secrets/backend.go
type SecretBackend interface {
    Get(ctx context.Context, scope, key string) (string, error)
    Set(ctx context.Context, scope, key, value string) error
    Delete(ctx context.Context, scope, key string) error
    List(ctx context.Context, scope string) ([]string, error)
}

// internal/build/client.go
type BuildClient interface {
    Submit(ctx context.Context, req BuildRequest) (<-chan BuildEvent, error)
}

// internal/git/provider.go
type GitProvider interface {
    RegisterWebhook(ctx context.Context, repo string) error
    PostCommitStatus(ctx context.Context, repo, sha, state, url string) error
    CloneRepo(ctx context.Context, repo, ref, dest string) error
    VerifyWebhookSignature(body []byte, header http.Header) error
}

// internal/dns/provider.go
type DNSProvider interface {
    UpsertRecord(ctx context.Context, record DNSRecord) error
    DeleteRecord(ctx context.Context, record DNSRecord) error
}
```

**Why these exist** — three reasons, in order of importance:

1. **Swap points for the addon pack.** `SecretBackend` is how v1 (k8s Secrets)
   becomes OpenBao (addon) becomes AWS Secrets Manager (addon) without
   rewriting controllers. The addon pack is a real product boundary because
   these interfaces make it one.
2. **Version-bump firewall.** A BuildKit or Authentik major version bump
   touches one file (the implementation behind the interface), not fifty.
   This is what solves the "we're glue for 8 services, every upgrade is
   terrifying" problem.
3. **Test seams.** Controllers take these interfaces via constructor
   injection; unit tests pass in in-memory fakes. No network, no flake, no
   credentials, millisecond test runs.

All three wins come from the same discipline: **no controller code talks to
third-party SDKs directly, ever.**

### 11.2 Inward Contracts (CRD + REST API)

Contracts that **external callers agree to** when they talk to Capybara.
These are the public surface — versioned with semver, documented, and not
broken lightly.

- **`App` CRD and related CRDs** — the YAML shape users write (directly or
  via UI/CLI that writes it for them). Versioned as `capybara.dev/v1alpha1`
  today, moving to `v1beta1` and `v1` over time.
- **REST API** — `POST /api/deploy`, `POST /api/secrets`, etc. Used by the
  UI, the CLI, and external CI systems via the deploy webhook. Full OpenAPI
  spec published.

**What external callers need to agree to, in practice:**

1. If they want to be managed by Capybara → the `App` CRD schema (or use UI/CLI).
2. If they want to deploy from external CI → the deploy webhook API + a token.
3. If they want to consume bindings → nothing. Capybara injects env vars at
   pod-start time. Apps read `process.env.DATABASE_URL` like any 12-factor app.
   No SDK, no sidecar, no agent.

### 11.3 Direction Diagram

```
                 ┌─────────────────────────────────────────┐
  users, CI,  →  │   CRDs + REST API    (inward contract)  │
  pods, UI       └─────────────────────────────────────────┘
                              │
                              ▼
                 ┌─────────────────────────────────────────┐
                 │   Capybara controllers / business logic │
                 └─────────────────────────────────────────┘
                              │
                              ▼
                 ┌─────────────────────────────────────────┐  →  real GitHub
                 │   Interfaces        (outward contract)  │  →  real BuildKit
                 └─────────────────────────────────────────┘  →  real OpenBao
```

Two layers, two directions, two sets of rules:

| Layer | Who sees it | Freedom to change |
|---|---|---|
| Inward (CRD + REST API) | External users, pods, CI | Low — breaking changes require version bumps and migration |
| Outward (interfaces) | Capybara's own code only | High — refactor freely; implementations can be swapped without touching controllers |

---

## 12. Non-Goals for v1

- Not a cluster provisioner
- Not multi-cluster
- Not a service mesh (no Istio, no mTLS)
- Not a CI system for users' apps (deploy webhook integrates with whatever CI
  they have)
- Not a replacement for Argo CD / Flux at the platform layer
- Not optimized for sub-16GB cluster footprints once the addon pack is
  installed; core alone is much lighter and fits comfortably in 4–8GB
