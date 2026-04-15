# Mortise — Product & Engineering Spec

> Name: **Mortise** — the core platform. Addons are called **tenons**
> (joinery metaphor: the mortise is the slot that holds everything together;
> tenons slot into it).
> Status: Draft for engineering kickoff
> Audience: engineers building v1
> Companion: [`ARCHITECTURE.md`](./ARCHITECTURE.md) — system diagrams

---

## 1. What We're Building

A self-hosted, Railway-style developer platform that runs on top of an existing
Kubernetes cluster. Developers connect a git repo (or point at a pre-built image),
and Mortise handles builds, deploys, domains, TLS, environment variables,
volumes, preview environments, and service-to-service bindings. The user-facing
experience abstracts Kubernetes away entirely — users think in "apps," not
Deployments, Services, Ingresses, or Helm charts.

The product ships in two layers:

1. **Core (v1)** — the minimum Railway clone. Installable on any k3s/k8s cluster
   via a single Helm chart. Zero addons required. This is what we build first.
2. **Tenons (post-v1 addon pack)** — optional subcharts that bolt onto core to provide
   SSO (Authentik), secret management (OpenBao), monitoring (Prometheus/Grafana),
   backup/restore, Helm-source deployments, a bounded service catalog, and
   community-contributed app presets. Each addon is an independent product
   with its own lifecycle (see §6.1).

The split matters: core stands on its own as a Coolify-for-Kubernetes. Addons
let users assemble a richer self-hosted stack on top of core by selecting
only what they want. Users who only want the Railway UX never see or pay for
anything beyond core.

---

## 2. Positioning

**Mortise is a deploy target, not a platform.** Core is a Railway-equivalent
product that users install and use; addons are independently-maintained
extensions, not bundled platform features. We never ship "the platform" as
a single thing — users assemble what they want from core plus à la carte
addons. This distinction is load-bearing: see §6.1 for the discipline that
keeps addons from congealing into a platform (which is how every previous
attempt in this space has died).

Mortise is what a developer installs when there is no platform team. It produces
the Kubernetes manifests (internally, via CRD reconciliation) and runs the CD
itself. The user writes an `App` (or clicks through the UI) and Mortise handles
everything downstream — build, push, deploy, domain, TLS, metrics, secrets,
bindings.

**Not competing with** Argo CD / Flux CD (GitOps CD engines for platform teams
managing manifests-as-code). Mortise coexists with them: platform teams can
GitOps-manage the cluster infra and Mortise itself; developers deploy apps
through Mortise without touching manifest repos.

**Differentiated from:**
- **Kubero** — closest existing product, but manual webhook setup, manual
  DNS/TLS, buildpack-only builds, deprecated Bitnami catalog.
- **Coolify** — excellent UX on Docker/VPS, no Kubernetes mode.
- **Gimlet** — archived March 2025. The niche needs a sustainable plan from day one.
- **Otomi** — dead May 2024; tried to be a batteries-included platform first and
  added developer UX second. Mortise does the opposite: ship the Railway UX
  first, layer the platform on top.
- **Railway / Render** — SaaS only, not self-hostable.

---

## 3. Target Users (v1)

- **Homelabbers** on k3s/Talos/RKE2 who want Railway-quality deploys on their
  own hardware
- **Small dev teams** (2–15 engineers) who want self-hosted Railway on their own
  cloud k8s without a dedicated platform team
- **Regulated / on-prem teams** that need Railway-quality UX and cannot send
  code or data through third-party infra — using the **image source + deploy
  webhook** path with their existing internal CI. (Mortise does not build
  inside an airgap; see §8.5.)

**Assumption:** a Kubernetes cluster already exists. Mortise installs as a Helm
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
apiVersion: mortise.dev/v1alpha1
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

**BuildKit:** runs as a single rootless Deployment in the `mortise-builds`
namespace with a PVC for `/var/lib/buildkit` layer cache. Installed on-demand
the first time a `git` App is created (not part of base install). Operator
serializes submissions through an internal queue and talks to BuildKit via the
native Go client. Scale-out revisited if p99 queue wait exceeds ~2 minutes.

**Build cache:** OCI artifacts in the configured registry, keyed per app per
branch.

**Image naming:** built images are path-namespaced by App, e.g.
`<registry>/mortise/<app-name>:<tag>`. This is a convention the operator
enforces, not a user-facing setting. Keeps Apps' images cleanly organized
regardless of registry backend (Zot, GHCR, Harbor, etc.) and prevents
accidental cross-App references. Admin has full read/write across all paths
by default.

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
curl -X POST https://mortise.yourdomain.com/api/deploy \
  -H "Authorization: Bearer $DEPLOY_TOKEN" \
  -d '{"app":"my-app","environment":"production","image":"registry/org/my-app:abc123"}'
```

**Deploy tokens:**
- Scoped per App + per environment
- Created via UI or CLI; displayed once; stored in secret store
- Revocable; multiple tokens per App/env allowed
- CI snippet shown alongside on creation

This is the extensibility seam for any CI system. Users keep GitHub Actions,
GitLab CI, Woodpecker, Jenkins, bash — Mortise just handles build (if git
source) and deploy. No CI integration needed in v1.

### 5.5 Bindings — The Magic

An App with `credentials` declared is a backing service. Other Apps bind to it
by `ref`. At **reconcile time** the operator resolves the bound App's
credentials (password from the secret store, constructed `DATABASE_URL`,
host/port from Service DNS) and bakes them into the binder's Deployment —
literal env values for Service DNS facts, `secretKeyRef` projections for
credentials sourced from the secret backend. No admission webhook, no init
container, no runtime agent: the Deployment spec is the single source of truth
and the kubelet injects env the normal way.

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
  credentials: [DATABASE_URL, host, port, user, password]
  environments:
    - name: production
      env:
        - name: POSTGRES_PASSWORD
          valueFrom: { secretRef: my-db-password }
```

`env` always lives under `environments[].env`. There is no `spec.env`:
production and staging need distinct secrets and distinct env independently,
and collapsing them would force duplication elsewhere.

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

Mortise is deliberately unopinionated about CSI backends. The App CRD accepts
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

### 5.9 Secrets

Mortise uses **Kubernetes Secrets directly** as the storage and runtime
backend. There is no `SecretBackend` abstraction inside Mortise; the pod
consumes env vars the normal way (`secretKeyRef`), and the operator's job
is to write the right k8s Secret at reconcile time.

- Write-only editor in UI (values never displayed after save)
- Rotation: `secret rotate` writes a new value, rolling-restarts every App
  referencing it
- Scoped as `<app>/<environment>/<key>`; stored as a k8s Secret in the
  App's namespace

**External secret managers (Vault, AWS Secrets Manager, GCP SM, Azure KV,
1Password, etc.) integrate via ExternalSecrets Operator**, which is a
separately-installed project Mortise does not own. The pattern:

1. User installs ESO (`helm install external-secrets ...`) and a
   `ClusterSecretStore` pointing at their backend.
2. User sets `secrets.mode: external` on their App and references a
   path in the backend via the Mortise UI/CRD.
3. Mortise writes an `ExternalSecret` CR in the App's namespace; ESO
   reconciles it and produces a regular k8s Secret.
4. Mortise consumes that Secret the same way it would consume a
   natively-managed one.

Mortise never imports ESO's Go types or talks to the backend directly.
The integration is two CRs passing k8s Secrets between them. If ESO is
not installed, Mortise falls back to native k8s Secrets — nothing breaks,
users just don't have the external-backend option. See §11.1 for why
this is not a "plug-in protocol."

### 5.10 Auth (login for Mortise itself)

Auth is scoped to **Mortise's own UI and API** — logging developers and
admins into Mortise. User apps' authentication is the user's problem,
out of scope (see note below).

**Two in-tree implementations cover the realistic space:**

- **Native** (default): username/password accounts stored in the operator's
  database. One admin created during first-run wizard; invites via
  generated link.
- **Generic OIDC**: configured with `issuerURL + clientID + clientSecret`.
  Covers every mainstream IdP via the OIDC standard — Authentik, Keycloak,
  Okta, Auth0, Google, GitHub, GitLab, Microsoft Entra, Zitadel, etc.
  One code path, one set of tests.

Selection is per-deployment via `PlatformConfig.auth.mode: {native, oidc}`.
Not pluggable at runtime — if you want OIDC, set the flag and restart the
operator. That's enough flexibility for 90%+ of real users. SAML and LDAP
are out of scope for v1 and can be added in-tree later as separate modes if
demand appears.

- Roles: admin / member
- Admin can manage users, providers, DNS, platform settings, all apps
- Member can create/manage own apps, view shared apps
- Teams + per-app grants are v2

**SSO for user apps is not a Mortise concern.** Mortise-v1's job is what
Railway's is: hand users env vars and a URL. A user who wants their Gitea
to "Login with Authentik" installs Authentik themselves, creates the OIDC
client in Authentik's UI, and pastes the client ID + secret into Mortise's
secrets editor — same flow as on Railway. Forward-auth middleware and
automated OIDC/SAML client provisioning for user apps are an addon-pack
topic to be specced if and when real demand shows up; they are not a v1
contract.

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
no third-party infrastructure required to install Mortise.

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
mortise app list
mortise app create --source git --repo github.com/org/my-app
mortise app create --source image --image ghcr.io/paperless-ngx/paperless-ngx:latest
mortise deploy my-app --env production --image registry/org/my-app:abc123
mortise promote my-app
mortise rollback my-app
mortise logs my-app
mortise secret set my-app API_KEY=xxx
mortise secret rotate my-app API_KEY
mortise env set my-app PORT=3000
mortise domain add my-app api.customer.com
mortise token create my-app production
mortise preview list my-app
```

Config at `~/.config/mortise/config.yaml`.

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
feature, installable independently after core is live. Read §6.1 for the
rules that constrain what gets added here.

- **Authentik convenience chart** — installs Authentik and pre-configures
  it as an OIDC provider for Mortise's own login. Mortise itself doesn't
  integrate with Authentik beyond standard OIDC (§5.10), so this addon
  is purely installation sugar — a user could install Authentik by hand
  and configure OIDC manually with identical results. Archiving this
  addon at any point would not affect Mortise core.
- **Prometheus + Grafana** stack — installs kube-prometheus-stack with
  opinionated defaults and wires Grafana OAuth through `AuthProvider`.
  Ships the standard upstream dashboards that come with
  kube-prometheus-stack; Mortise does not maintain its own dashboard set.
- **Loki** log aggregation
- **Installed-addon health panel** — shows status of addons that are
  currently installed (up / degraded / down). Does not ship remediation
  content; errors link to upstream docs.
- **Backup / restore** to S3 or NFS (CRD export + Velero + secret snapshots)
- **`helm` source type** — install arbitrary Helm charts through Mortise UI
- **`external` source type** — wrap already-running services with domain/TLS/auth
- **`catalog` source type** — operator-backed backing services. **Bounded
  list**: CloudNativePG for Postgres, redis-operator for Redis. The
  catalog is extensible — users can add their own entries — but Mortise
  itself maintains only this short list.
- **Community app presets** — a separate, community-contributed repository
  of App CRD templates for common self-hosted software (Paperless,
  Vaultwarden, etc.). Shipped as data, not code; not maintained by the
  Mortise core team. Users get a library; breakage of a preset is an
  upstream/community concern.
- **Cloudflare Worker relay** for GitHub App mode
- **Cloudflare for SaaS** custom hostname automation
- **Cloudflare Tunnel** deployment automation
- **Storage guidance** — detection ("your cluster has no RWX-capable
  StorageClass") and documentation links (Longhorn, NFS). Mortise does
  **not** install Longhorn or NFS for you; that's the cluster admin's job.
- **`perReplica` volumes** / StatefulSet workloads
- **Multi-cluster** (Cluster CRD, bearer-token trust, aggregated UI)

### 6.1 Addon Pack Discipline

The addon pack exists because users want platform-shaped capabilities
layered onto the deploy target. The project's long-term survival depends
on never letting the addons congeal *into* a platform. The invariants
that prevent that:

1. **Core never depends on any addon.** `helm install mortise` with zero
   addons is a complete Railway clone, always. If any feature in core
   requires an addon to function, the feature belongs in core or doesn't
   ship.
2. **Addons never depend on each other.** Installing Authentik does not
   require monitoring; installing monitoring does not require Loki.
   Cross-addon assumptions get rejected at review.
3. **Each addon has its own lifecycle.** Added, upgraded, removed, or
   archived independently. No "we released v2 of the platform" — only
   "we released v2 of the monitoring addon."
4. **Addons whose value depends on tracking upstream API or schema churn
   get extra scrutiny at scope-in time.** A curated catalog of 40 apps
   means 40 upstream relationships forever; that's the trap. Prefer
   shapes where breakage is distributed (community presets, user-extended
   catalog) over shapes where Mortise owns every integration point.
5. **When an addon's upstream becomes unmaintained, the addon gets
   archived, not forked.** We do not become the maintainers of abandoned
   third-party software to keep an addon alive.

These rules turn scope decisions into rule-application rather than
judgment calls. A proposal that violates any of them doesn't ship —
regardless of how useful the feature would be in isolation.

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
│   └── mortise/                    # umbrella Helm chart (v1 = core only)
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
	k3d cluster create mortise-test --wait --registry-create mortise-registry
	kubectl wait --for=condition=Ready nodes --all --timeout=60s

cluster-down:
	k3d cluster delete mortise-test

chart-install:
	helm upgrade --install mortise ./charts/mortise \
	  --namespace mortise-system --create-namespace \
	  --set image.tag=dev --wait

# Persistent dev loop
dev-up:
	k3d cluster create mortise-dev --registry-create mortise-registry
	$(MAKE) chart-install
	tilt up

dev-down:
	k3d cluster delete mortise-dev

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

### 7.8 CI (for Mortise itself)

GitHub Actions, hosted runners. Not self-hosted. Workflows:
- `test.yml` — `make test` on every PR (fast)
- `test-integration.yml` — `make test-integration` on every PR (k3d in GH Actions runner)
- `nightly.yml` — `make test-e2e` against the dogfooding cluster

Self-hosting the Mortise project's own CI is out of scope.

---

## 8. Packaging

### 8.1 Umbrella Helm Chart

```
charts/
└── mortise/               # umbrella
    ├── Chart.yaml          # lists subcharts
    ├── values.yaml         # top-level toggles
    └── charts/
        ├── mortise-core/  # always-on: operator, API, UI, CRDs, Traefik, cert-manager, ExternalDNS
        └── (addons in future: authentik, openbao, monitoring, catalog, ...)
```

**v1 ships with `mortise-core` only.** Addon subcharts land over time, each
independently installable. The umbrella chart exists so addons can declare
dependencies and inherit shared values (domain, DNS provider, etc.).

### 8.2 Install UX

```bash
# Fast path (core only)
helm install mortise oci://ghcr.io/mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set domain=yourdomain.com \
  --set dns.provider=cloudflare \
  --set dns.apiToken=xxx
```

**The fast path is not a stripped-down install.** Core alone is the entire
Railway-equivalent product: deploy apps from git or image, bindings,
previews, TLS, domains, native auth. §6.1 invariant #1 guarantees this
forever — no core feature will ever require an addon to function. A user
who never installs a single addon is using Mortise as intended.

**Presets are sugar over flags, not bundles.** A future `--preset=homelab`
resolves to a named list of `<addon>.enabled=true` flags and nothing more.
Presets do not create cross-addon coupling, introduce new lifecycles, or
gate features behind a preset choice. Disabling any addon inside a preset
after the fact must leave the rest working unchanged. If a proposed preset
needs glue code beyond flag-flipping, it's a §6.1 violation and doesn't
ship.

### 8.3 Bring-Your-Own Platform Components

Users with existing platform plumbing can disable any bundled component.
Each is on by default but switchable at install:

| Values flag | Default | Effect when disabled |
|---|---|---|
| `traefik.enabled` | `true` | Operator annotates Ingress for the existing controller; user picks `ingress.className` |
| `certManager.enabled` | `true` | Operator expects an existing `ClusterIssuer`; user sets `tls.clusterIssuer` |
| `externalDNS.enabled` | `true` | Operator still annotates Ingress; user's existing ExternalDNS picks it up, or DNS is managed manually |
| `zot.enabled` | `true` | Operator pushes to external registry; user sets `registry.url` + `registry.pullSecret` |

Each toggle corresponds to an outward interface (§11.1) — `IngressProvider`,
`DNSProvider`, `RegistryBackend`. Disabling a bundled component does not
disable the feature; it swaps the implementation.

### 8.4 Restricted-Network Installs (Proxied / Custom-CA)

For on-prem or regulated clusters where Mortise must reach internal git,
internal registry, internal ACME — but still has *some* outbound path
(directly or via proxy):

| Values flag | Purpose |
|---|---|
| `global.caBundle` | PEM-encoded CA chain mounted into the operator pod — for internal ACME, internal registries, internal git forges signed by a private CA |
| `global.httpProxy` / `httpsProxy` / `noProxy` | Propagated as env to the operator |
| `global.imageRegistry` | Prefix for all Mortise-internal images (operator, Traefik, cert-manager, Zot) — for mirrored registries |
| `global.pullSecret` | Pull secret for `global.imageRegistry` |
| `tls.clusterIssuer` | Point cert-manager at an internal ACME (Smallstep, step-ca, Venafi) instead of Let's Encrypt |

### 8.5 Fully Air-Gapped Clusters

**Airgapped builds are out of scope.** Railpack fetches buildpack metadata,
BuildKit pulls base images, Go/Node/Python toolchains download at build time
— mirroring all of this reliably is a project unto itself and one that
airgapped teams typically already solve with their existing CI.

The supported airgap path is **image source only**:

1. Team builds images in their existing CI (which already lives inside the
   airgap and knows how to reach their internal registry and proxies).
2. CI calls Mortise's deploy webhook with the built image reference.
3. Mortise pulls from the internal registry (`registry.url` +
   `registry.pullSecret`), not from Docker Hub.

What Mortise *does* provide in an airgap: internal-registry image pulls,
internal CA trust for its own outbound calls (to internal git forges for
webhooks only — not for cloning, since no `git` source), internal ACME via
`tls.clusterIssuer`, and the deploy webhook.

What Mortise does *not* provide in an airgap: source-to-image builds. Git
source is effectively disabled. This is a deliberate scope boundary, not a
limitation to be lifted in an addon.

Later (addon pack available):

```bash
# Pick addons; CLI walks through them interactively
mortise platform install            # interactive picker (authentik? monitoring? ...)
mortise platform install --addons=authentik,monitoring
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
- Umbrella Helm chart with `mortise-core` subchart (operator Deployment,
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
- `valueFrom.secretRef` reads from the Mortise secret store and projects as
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
- CLI (`mortise app create --source image`, `mortise logs`, `mortise status`)

**Exit criteria:** deploy a single-container image App (e.g. Paperless-ngx in
sqlite mode, or any standalone service) entirely through the UI or CLI; no
YAML visible to the user. Multi-service apps needing bindings (Paperless-ngx
with Postgres + Redis) land in Phase 3 — this phase validates the CRUD and
deploy surface, not the Railway moment.

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
- Registry: Zot (default, bundled in `mortise-core`) or external (GHCR,
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

- **Authentik convenience chart** — installs Authentik, pre-configures it
  as an OIDC provider for Mortise's own login. No Mortise code beyond
  the existing generic-OIDC impl (§5.10).
- **kube-prometheus-stack** subchart + Grafana OAuth + pre-loaded dashboards
- **Loki** subchart + log aggregation in UI
- **Installed-addon health panel** — status of currently-installed addons in
  the UI (up / degraded / down), errors link to upstream docs
- **Backup/restore** to S3/NFS (CRD export + k8s Secret snapshots + Velero)
- **`helm` source type** — arbitrary Helm chart deploys through UI
- **`external` source type** — wrap external services with domain/TLS
- **`catalog` source type** — bounded to CNPG Postgres and redis-operator
  Redis; user-extensible but not curated beyond that pair
- **Community app presets** — data-only repository of App CRD templates
  for common self-hosted software; not maintained by the core team
- **Cloudflare Worker relay** for GitHub App OAuth
- **Cloudflare for SaaS** custom hostname automation
- **Storage guidance** — detection of missing RWX StorageClass + docs links;
  Mortise does not install Longhorn/NFS for the user
- **`perReplica` volumes** — StatefulSet support
- **Multi-cluster** — Cluster CRD, bearer-token trust, aggregated UI

---

## 10. Open Questions

1. **Product name.** `[NAME]` / Mortise is a placeholder. The name gets baked
   into CRD apiVersions (`mortise.dev/v1alpha1`), Helm chart, CLI binary,
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

### 11.1 Internal Abstractions (Seams / Interfaces)

Go interfaces that **Mortise's own code agrees to** when it talks to
external systems or configurable capabilities. These are **internal
plumbing, not plug-in APIs** — they exist for testability, for code
clarity, and to keep third-party SDKs from leaking into controller code.
They are not an extension surface for outside implementers; third-party
integration happens via Kubernetes primitives (see below), not by
implementing these interfaces.

Controllers import only Mortise's own types. They never import
`github.com/google/go-github`, `moby/buildkit/client`, or any other
third-party SDK directly. Every external dependency is wrapped behind an
interface in `internal/<name>/`.

```
Mortise controller  →  AuthProvider     →  native DB | generic OIDC
Mortise controller  →  PolicyEngine     →  native (admin/member)
Mortise controller  →  GitAPI           →  GitHub | GitLab | Gitea
Mortise controller  →  GitClient        →  go-git (single impl, all forges)
Mortise controller  →  BuildClient      →  BuildKit (single impl)
Mortise controller  →  RegistryBackend  →  generic OCI (config-driven)
Mortise controller  →  IngressProvider  →  generic annotation-driven
Mortise controller  →  DNSProvider      →  ExternalDNS annotation-driven
```

Notably **absent**: `SecretBackend`. Mortise uses k8s Secrets natively.
External secret managers (Vault, AWS SM, etc.) are integrated via
ExternalSecrets Operator — ESO writes a k8s Secret, Mortise reads it.
No Mortise-side contract is crossed. See §5.9.

Also absent: user-app SSO (forward-auth, OIDC client provisioning). Out of
scope for v1 and not a v1 contract.

**v1 interfaces:**

```go
// internal/auth/provider.go — login for Mortise itself
type AuthProvider interface {
    Authenticate(ctx context.Context, creds Credentials) (Principal, error)
    Principal(ctx context.Context, session SessionToken) (Principal, error)
    ListUsers(ctx context.Context) ([]User, error)
    InviteUser(ctx context.Context, email string, role Role) (InviteLink, error)
    RevokeUser(ctx context.Context, userID string) error
}
// two in-tree impls: native (sqlite-backed) and genericOIDC

// internal/authz/policy.go
type PolicyEngine interface {
    Authorize(ctx context.Context, p Principal, resource Resource, action Action) (bool, error)
}
// one in-tree impl: native admin/member

// internal/build/client.go
type BuildClient interface {
    Submit(ctx context.Context, req BuildRequest) (<-chan BuildEvent, error)
}
// one in-tree impl: BuildKit

// internal/registry/backend.go
type RegistryBackend interface {
    PushTarget(app, tag string) (ImageRef, error)
    PullSecretRef() string
    Tags(ctx context.Context, app string) ([]string, error)
    DeleteTag(ctx context.Context, app, tag string) error
}
// one in-tree impl: generic OCI (config-driven for Zot/GHCR/Harbor/ECR/etc.)

// internal/ingress/provider.go
type IngressProvider interface {
    ClassName() string
    Annotations(app AppRef, hostnames []string, middleware []MiddlewareRef) map[string]string
}
// one in-tree impl: generic annotation-driven (config map per controller family)

// internal/git/api.go — forge-specific API calls
type GitAPI interface {
    RegisterWebhook(ctx context.Context, repo string, cfg WebhookConfig) error
    PostCommitStatus(ctx context.Context, repo, sha string, status CommitStatus) error
    VerifyWebhookSignature(body []byte, header http.Header) error
    ResolveCloneCredentials(ctx context.Context, repo string) (GitCredentials, error)
}
// three in-tree impls: GitHub, GitLab, Gitea

// internal/git/client.go — git protocol (single impl, all forges)
type GitClient interface {
    Clone(ctx context.Context, repo, ref, dest string, creds GitCredentials) error
    Fetch(ctx context.Context, dir, ref string) error
}

// internal/dns/provider.go
type DNSProvider interface {
    UpsertRecord(ctx context.Context, record DNSRecord) error
    DeleteRecord(ctx context.Context, record DNSRecord) error
}
// one in-tree impl: ExternalDNS annotation-driven
```

**Total in-tree impls across all contracts: ~11.** Not a plug-in ecosystem.

**Why split `GitAPI` from `GitClient`:** cloning is git-protocol and
identical across forges (one impl). Webhook registration and commit status
are REST calls that differ per forge. Split lets the forge fakes stay tiny
while the single `GitClient` impl is shared.

**Why these interfaces exist** — in order of importance:

1. **Test seams.** Controllers take interfaces via constructor injection;
   unit tests pass in in-memory fakes. No network, no flake, no credentials,
   millisecond test runs. This is the primary justification.
2. **Version-bump firewall.** A BuildKit or go-github major version bump
   touches one file, not fifty.
3. **Config-driven swapping within the bounded impl set.** `IngressProvider`
   picking "nginx vs traefik" is flipping a config value that selects a
   different annotation map — still one Go impl, different data.

**Why these are not extension points:** with ~11 impls total and ~1 realistic
impl per contract, a plug-in protocol would be engineering for imagined
third-party implementers rather than real ones. If a real third-party
extension need appears later for a specific contract, that one contract can
be converted to a CRD-based integration (as ESO demonstrates for secrets)
without redesigning the whole surface.

### 11.1a Third-Party Integration: Kubernetes IS the Contract

Mortise integrates with other cluster components **by being a polite
Kubernetes citizen**, not by offering a plug-in API. The pattern:

| Capability | How third-party integration happens |
|---|---|
| External secret managers (Vault, AWS SM, etc.) | User installs ESO + a backend. ESO writes k8s Secrets. Mortise reads them. |
| Custom ingress (Gateway API, service mesh) | User installs the controller. Mortise writes standard Ingress resources; the controller reconciles them. Gateway API support is a future config option selecting HTTPRoute output instead of Ingress. |
| Alternative DNS providers | ExternalDNS already covers ~20 providers. User configures ExternalDNS; Mortise annotates Ingresses. |
| Monitoring / logging | User installs Prometheus + Loki (or equivalents). Mortise pods emit standard Prometheus metrics and stdout logs. No Mortise-side integration code. |
| Policy | User runs OPA/Kyverno as admission controllers. They gate Mortise's writes. Mortise doesn't need to know. |
| Backing services (databases, queues) | User installs CNPG / redis-operator / whatever. Mortise binds via Service DNS + Secret refs. |

**The integration fabric is Kubernetes.** This is the structural
alternative to building a Mortise-specific plug-in ecosystem. It works
because Kubernetes *already is* a plug-in ecosystem with mature versioning
(CRDs), discovery (labels/selectors), and auth (ServiceAccounts). Mortise
benefits from all of it without reinventing any of it.

### 11.2 Inward Contracts (CRD + REST API)

Contracts that **external callers agree to** when they talk to Mortise.
These are the public surface — versioned with semver, documented, and not
broken lightly.

- **`App` CRD and related CRDs** — the YAML shape users write (directly or
  via UI/CLI that writes it for them). Versioned as `mortise.dev/v1alpha1`
  today, moving to `v1beta1` and `v1` over time.
- **REST API** — `POST /api/deploy`, `POST /api/secrets`, etc. Used by the
  UI, the CLI, and external CI systems via the deploy webhook. Full OpenAPI
  spec published.

**What external callers need to agree to, in practice:**

1. If they want to be managed by Mortise → the `App` CRD schema (or use UI/CLI).
2. If they want to deploy from external CI → the deploy webhook API + a token.
3. If they want to consume bindings → nothing. Mortise injects env vars at
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
                 │   Mortise controllers / business logic │
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
| Outward (interfaces) | Mortise's own code only | High — refactor freely; implementations can be swapped without touching controllers |

---

## 12. GitOps Coexistence (Argo CD / Flux)

Mortise is not a replacement for Argo CD or Flux and does not compete with
them — they operate at different layers. There is one supported coexistence
pattern; anything beyond it is user-at-own-risk.

**The layer split:**
- **Platform team owns the cluster and Mortise itself via GitOps.** Helm
  releases for Mortise, cert-manager, Traefik, ExternalDNS, ingress classes,
  node pools, the Mortise umbrella chart values — all in Argo/Flux.
- **Dev teams own `App` CRDs via Mortise's UI/CLI/API.** App CRDs live in
  etcd, not git. The Railway UX is the whole point; GitOps'ing the
  user-authored surface would give it back.

**What Argo/Flux should and should not manage:**

| Resource | Argo/Flux-managed? | Why |
|---|---|---|
| Mortise Helm release (operator version, chart values) | **Yes, recommended** | Declarative platform config |
| `PlatformConfig` CRD (domain, DNS, default SC) | **Yes, recommended** | Cluster-wide, rarely changes |
| `GitProvider` CRDs | **Yes, recommended** | Credentials via ESO / sealed-secrets / SOPS |
| Addon subchart enable/disable (post-v1) | **Yes, recommended** | Just more chart values |
| `App` CRDs | **No** | Authored through Mortise; live in etcd |
| `PreviewEnvironment` CRDs | **No — operator-created** | Lifecycle is PR-driven |
| Deployments / Services / Ingresses | **No — operator-created** | Mortise owns these |
| Secrets (user-visible) | **No — written via Mortise API** | Write-only UX, rotation |

**Flux-specific:** identical. Flux users substitute `Kustomization` or
`HelmRelease` for `Application`. Everything else is the same.

**What this means in practice:** a platform team runs a fully GitOps'd
cluster with Mortise installed as just another `HelmRelease`. Dev teams get
Railway UX for their apps. The two tools' surface areas do not overlap, so
there is no drift to manage.

**Explicit non-support:** Mortise does not officially support Argo- or
Flux-managed `App` CRDs. Users who check App YAML into git and sync it with
Argo may make it work, but the operator is not designed to avoid writing to
`spec.*` on those resources, and a successful build will patch
`spec.environments[].image` — which Argo will revert on the next sync,
causing the pod to flap. Revisit if user demand justifies the extra
machinery (a companion `AppDeployment` CR, `managed-by` annotation
handling, read-only UI mode); for now, pick one tool or the other per App.

---

## 13. Non-Goals for v1

- Not a cluster provisioner
- Not multi-cluster
- Not a service mesh (no Istio, no mTLS)
- Not a CI system for users' apps (deploy webhook integrates with whatever CI
  they have)
- Not a replacement for Argo CD / Flux at the platform layer — see §12
- **Not an airgapped build system.** Source-to-image builds require outbound
  network for Railpack metadata, BuildKit base images, and language
  toolchains. Airgapped clusters use the image source + deploy webhook
  path with their own internal CI; see §8.5.
- Not optimized for sub-16GB cluster footprints once the addon pack is
  installed; core alone is much lighter and fits comfortably in 4–8GB
- **Not a workload-kind platform.** v1 deploys long-running HTTP/TCP services
  as Kubernetes Deployments. Jobs, CronJobs, StatefulSets, DaemonSets are not
  supported. `perReplica` volumes and StatefulSet-per-App land in the addon
  pack.
- **Not a GPU / specialized-scheduling platform.** App CRD has no
  `nodeSelector`, `tolerations`, `affinity`, `runtimeClassName`, or
  `resourceClaims`. Teams with GPU, specialty hardware, or
  multi-tenant bin-packing needs should use raw Kubernetes for those
  workloads; Mortise can coexist for the rest.
- **Not a multi-container Pod platform.** One container per App in v1. Init
  containers and sidecars are not exposed. ML model-download init patterns,
  service-mesh sidecars, and log-shipper sidecars are out of scope.
- **Not a queue / async-job runner.** No background workers, no job queues,
  no scheduled tasks beyond what users run themselves inside their container.
- **Not a multi-tenant platform with hard isolation.** v1 has admin/member
  only; namespace isolation is soft (no NetworkPolicy generation, no
  ResourceQuota per user). Shared-cluster-with-students scenarios need v2
  team RBAC + quota work.
