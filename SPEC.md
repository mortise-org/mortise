# Mortise — Product & Engineering Spec

> Name: **Mortise** — a self-hosted Railway-style deploy target for Kubernetes.
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

**Mortise is a single product, not a platform with optional layers.** The
Helm chart installs one operator that delivers the complete Railway UX. No
addons, no plug-in protocol, no multi-tier product. Everything Mortise does,
Mortise does in core. Users who want capabilities beyond Mortise's scope —
SSO, centralized monitoring, log aggregation, backups, external secret
managers — install those projects directly (Authentik, Prometheus, Loki,
Velero, ExternalSecrets Operator, etc.) and Mortise coexists with them via
standard Kubernetes primitives. See §6 for the scope boundary.

---

## 2. Positioning

**Mortise is a deploy target, not a platform.** It does the Railway UX —
git-to-URL, bindings, previews, domains, TLS, environment variables — on
top of a Kubernetes cluster the user already has. It does not try to be the
"batteries-included internal developer platform" (SSO for every service,
monitoring stack, log aggregation, policy engine, cost allocation, secrets
vault, backup orchestrator). Users who want those capabilities install the
upstream projects directly — Mortise is a polite Kubernetes citizen and
coexists with whatever else runs in the cluster. See §6 for what's out of
scope and why.

This distinction is load-bearing. Every previous attempt in this space
(Otomi, Gimlet, Kubeapps) died because scope accreted into a platform shape
that couldn't be maintained by a small team. Mortise's survival depends on
refusing that shape regardless of how useful the extra features would be
in isolation. See §6.1 for the scope invariants.

Mortise is what a developer installs when there is no platform team. It
produces the Kubernetes manifests (internally, via CRD reconciliation) and
runs the CD itself. The user writes an `App` (or clicks through the UI)
and Mortise handles everything downstream — build, push, deploy, domain,
TLS, metrics, secrets, bindings.

**Not competing with** Argo CD / Flux CD (GitOps CD engines for platform
teams managing manifests-as-code). Mortise coexists with them: platform
teams can GitOps-manage the cluster infra and Mortise itself; developers
deploy apps through Mortise without touching manifest repos.

**Differentiated from:**
- **Kubero** — closest existing product, but manual webhook setup, manual
  DNS/TLS, buildpack-only builds, deprecated Bitnami catalog.
- **Coolify** — excellent UX on Docker/VPS, no Kubernetes mode.
- **Gimlet** — archived March 2025. The niche needs a sustainable plan from day one.
- **Otomi** — dead May 2024; tried to be a batteries-included platform first and
  added developer UX second. Mortise commits to the opposite discipline: ship
  the Railway UX as the whole product, never accrete a platform around it.
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
explicitly out of scope for v1 — "not the problem we're solving." A CLI
wrapper for bootstrap may return later.

---

## 4. Core Design Principles

1. **Kubernetes is invisible to the user.** The App CRD is an internal
   implementation detail. Users interact via UI, CLI, or the REST API — never
   kubectl. No user doc in v1 shows YAML for a Deployment, Service, or Ingress.
2. **Everything is an App.** One unified concept. Source type (`git` | `image`
   in v1) determines how it deploys; bindings and network flags handle
   backing-service cases.
3. **Integration through Kubernetes, not plug-in protocols.** Mortise is one
   operator shipped as one Helm chart. Users who want adjacent capabilities
   (OIDC, monitoring, backups, external secret managers) install the upstream
   projects themselves; Mortise coexists with them through standard
   Kubernetes primitives. No addon subcharts, no plug-in SDK.
4. **API-first.** REST + SSE, full OpenAPI spec, externally accessible.
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

### 5.0 Projects — the top-level grouping

Apps do not stand alone. They live inside a **Project** — the equivalent of
a Railway "project" or a Vercel "team." A project groups related apps
(frontend + backend + database) that deploy together, tear down together,
and bind freely to one another. The Project is the first concept a user
meets after logging in; everything below it (apps, domains, secrets) is
scoped to its containing project.

#### Model

- `Project` is a cluster-scoped CRD.
- When reconciled, the Project controller creates a Kubernetes namespace
  named `project-{project-name}` and sets itself as the namespace's owner
  reference.
- Apps live inside that namespace (`metadata.namespace = project-{name}`).
  Users never type namespace names directly — the UI, API, and CLI accept
  the project name and translate.
- Deleting the Project deletes its namespace, which cascades (via standard
  k8s garbage collection) to every App, Deployment, Service, Ingress, PVC,
  and Secret inside.

**Namespace naming** is prefixed (`project-foo`) to avoid collisions with
system namespaces (`default`, `kube-system`, `mortise-system`). Users do not
see this prefix in day-to-day use; it is surfaced only in CLI/debug output.

**Overriding the derived name.** Two optional spec fields let users opt out
of `project-{name}`:

- `spec.namespaceOverride: my-namespace` — use this name instead of
  `project-{name}`. The controller validates DNS-1123 and enforces
  cluster-wide uniqueness across Projects (two Projects cannot target the
  same namespace). Use cases: match an existing corporate naming
  convention (`corp-team-acme-prod`), migrate an existing namespace under
  Mortise management, keep the short name.
- `spec.adoptExistingNamespace: true` — **admin-only.** Permits the
  controller to adopt a namespace that already exists (from a previous
  install, Argo CD, `kubectl create ns`) instead of refusing to overwrite.
  Without this flag, encountering an existing namespace sets the
  `NamespaceReady` condition to `False` with reason
  `NamespaceAlreadyExists` and halts. Adoption is opt-in because it
  writes an owner reference that will cascade-delete the namespace
  when the Project is deleted.

Both fields are immutable after the Project is `Ready` — renaming a
namespace mid-flight would orphan every resource in it. Change requires
delete + recreate.

#### Default project

On first-user setup (the `/api/auth/setup` flow), the backend automatically
creates a `default` Project before redirecting the user to the dashboard.
The workspace is never empty, the "Create your first project" anti-pattern
is avoided, and users can rename or create additional projects at leisure.

#### Project CRD (v1 surface)

```yaml
apiVersion: mortise.dev/v1alpha1
kind: Project
metadata:
  name: my-saas
spec:
  description: "Core customer-facing SaaS"
  # namespaceOverride: corp-team-acme-prod  # optional; use this name instead of project-my-saas
  # adoptExistingNamespace: false           # optional; admin-only; adopt an existing namespace

  # PR Environments are a project-level toggle (not per-App). When
  # enabled, every git-source App in the project gets a preview per PR.
  # See §5.8 for scope semantics; cron and non-public Apps still
  # reconcile into the preview namespace so bindings resolve, but they
  # don't get a public URL.
  preview:
    enabled: true
    domain: "pr-{number}-{app}.yourdomain.com"  # `{app}` renders per-App
    ttl: 72h
    resources: { cpu: 250m, memory: 256Mi }     # preview default; apps can't override
    botPR: false                                 # opt-in: preview bots' PRs (dependabot etc.)

  # v2+: team, quota, default-domain-suffix, retention policy
status:
  phase: Ready               # Pending | Ready | Terminating | Failed
  namespace: project-my-saas
  appCount: 3
  conditions: []             # NamespaceReady: True | False (reason: NamespaceAlreadyExists | NamespaceOwnedByAnotherProject | NamespaceConflict)
```

#### Cross-project bindings

Bindings default to within-project (same namespace, cheap Service DNS):

```yaml
# Same project — the common case
bindings:
  - ref: my-db
```

Cross-project bindings are allowed but explicit:

```yaml
# Different project — operator resolves into project-other-proj namespace
bindings:
  - ref: shared-postgres
    project: infra
```

The bindings resolver reads `project:` and resolves `ref` in that project's
namespace. Missing project defaults to the App's own project.

#### What Projects provide in v1

- **Grouping** — UI organizes apps under their project
- **Isolation** — hard Kubernetes namespace boundary between projects
- **Lifecycle** — one-click teardown of a whole stack
- **Bindings scope** — default target for `bindings[].ref`
- **URL scoping** — `/projects/{p}/apps/{a}` paths throughout API, UI, CLI
- **Teams and per-project access control** — Team CRD groups users;
  grants attach roles (`team-admin`, `team-deployer`, `team-viewer`) to a
  (team, project) pair, optionally scoped to specific environments.
  Platform admins bypass grants entirely. See §5.10 for the access
  control model; it lands in the same phase as the Project foundation.

Domain handling is unchanged by Projects: each App still owns its own
domain (the environment-level `domain` and `customDomains` fields). Apps
in the same project routinely serve different domains. The "project
domain" concept does not exist.

#### What Projects do NOT provide in v1

- **Quotas** — no CPU/memory/storage caps per project (post-v1)
- **Project locking / freeze** — no way to make a project read-only yet
- **Project export/import** — post-v1. All the data lives in CRDs, so
  this is mostly a YAML-emission tool; the work is in the restore-side
  (creating a new project with renamed resources, re-injecting secrets).
  Tractable, just not v1 shape.

#### API surface

| Method + path | Purpose |
|---|---|
| `GET /api/projects` | list all projects |
| `POST /api/projects` | create a project (admin only) |
| `GET /api/projects/{p}` | get project details + app count |
| `DELETE /api/projects/{p}` | delete project + every app in it (admin only) |
| `GET /api/projects/{p}/apps` | list apps in project |
| `POST /api/projects/{p}/apps` | create app in project |
| `GET /api/projects/{p}/apps/{a}` | get app |
| `PUT /api/projects/{p}/apps/{a}` | update app |
| `DELETE /api/projects/{p}/apps/{a}` | delete app |
| `POST /api/projects/{p}/apps/{a}/secrets` | upsert secret |
| `GET /api/projects/{p}/apps/{a}/secrets` | list (names only) |
| `DELETE /api/projects/{p}/apps/{a}/secrets/{s}` | delete secret |
| `GET /api/projects/{p}/apps/{a}/logs` | SSE multi-pod log stream |
| `POST /api/projects/{p}/apps/{a}/deploy` | deploy webhook (per-App token) |

The pre-Project `?namespace=` query param is **removed**. This is a
breaking change, but the project is pre-release and there is no existing
user data to migrate.

#### UI routing

- `/` — project dashboard (list of projects with app count badge)
- `/projects/new` — create a new project
- `/projects/{p}` — apps in project (main working surface)
- `/projects/{p}/apps/new` — template picker + new-app form
- `/projects/{p}/apps/{a}` — app detail page

A top-bar project switcher dropdown lets users jump between projects
without returning to the dashboard (Railway pattern).

#### CLI context

The CLI config file (`~/.config/mortise/config.yaml`) adds a
`current_project` field. After `mortise login`, it defaults to `default`.

```bash
mortise project list                      # list projects
mortise project create my-saas            # create new project
mortise project delete my-saas            # delete (admin only; prompts confirmation)
mortise project use my-saas               # set current project context
mortise project show                      # show current project details

# App commands scope to current_project unless --project is given
mortise app list                          # apps in current project
mortise app list --project infra          # override
mortise app create --source image --image nginx:1.27 --name web
mortise deploy web --env production --image registry/web:abc123
```

#### Lifecycle details

**Project creation flow:**
1. User submits `POST /api/projects` with name + description
2. Controller creates `Project` CRD; status=Pending
3. Controller reconciles: creates namespace `project-{name}` with owner ref
4. Status → Ready; appCount=0
5. UI surfaces project, user can start creating apps

**Project deletion flow:**
1. User submits `DELETE /api/projects/{p}` (admin only)
2. API returns 202 Accepted, status → Terminating
3. Controller deletes the namespace (`kubectl delete namespace project-{name}`)
4. Kubernetes garbage collector cascades: all Apps, Deployments, Services,
   Ingresses, PVCs, Secrets in the namespace are deleted
5. When namespace deletion completes, Project CRD is removed

**Failure modes:**
- Name collision (project named `default` already exists) → 409
- Invalid name (not a valid DNS label) → 400
- Non-admin tries to create/delete → 403
- Deleting `default` while it contains apps → allowed (apps go with it),
  but user is warned; if `default` is deleted, the next project they visit
  becomes their new `current_project` context
- Apps exist outside any project namespace (legacy or manually created) →
  surfaced as "Orphan apps" in an admin view (future; v1 returns 404 for them)

### 5.1 App Kinds and Source Types (v1)

**Kinds** — set via `spec.kind`:

- **`service`** (default) — long-running workload reconciled to a Deployment.
  HTTP/TCP services, backing services, anything that stays up.
- **`cron`** — scheduled workload reconciled to a CronJob. See §5.8a.

**Source types** — set via `spec.source.type`:

- **`git`** — build from source. Auto-detects Dockerfile or language. Full
  build pipeline, preview environments, deploy history. Monorepo-aware via
  `source.path` + `source.watchPaths`.
- **`image`** — pre-built image. No build. Covers self-hosted apps
  (Paperless-ngx, Vaultwarden, etc.) and the v1 Postgres/Redis path
  (image + PVC + manual credentials block).
- **`external`** — no Deployment, no pods. The App is a facade over an
  already-running service (a DB on a VM, a cloud-managed Redis, an S3
  bucket, an API at another company). Mortise owns the domain, TLS, and
  bindable credentials; it does not run the workload. See §5.5a.

Kinds and source types compose orthogonally: a `cron` App can use a `git` or
`image` source; same build pipeline either way. `external` is incompatible
with `kind: cron` (nothing to schedule) and with `build:` fields (nothing
to build).

Explicitly **deferred post-v1** (see §6): `helm`, `catalog` source types.

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

  kind: service                       # service (default) | cron
  # schedule: "0 3 * * *"             # cron only; stdlib cron expression
  # concurrencyPolicy: Forbid         # cron only; Forbid | Allow | Replace (default Forbid)

  sharedVars:                         # project-scoped vars visible to every env
    - name: LOG_LEVEL
      value: info
    - name: SENTRY_DSN
      valueFrom: { secretRef: project-sentry-dsn }

  environments:
    - name: production
      replicas: 2
      resources: { cpu: 500m, memory: 512Mi }
      env:
        - name: PORT
          value: "3000"
        - name: API_KEY
          valueFrom: { secretRef: my-app-api-key }
        - name: DB_HOST
          valueFrom: { fromBinding: { ref: my-db, key: host } }   # explicit projection
      bindings:
        - ref: my-db                  # short form: inject every declared credential as env
      secretMounts:                   # mount existing k8s Secrets as files (see §5.5b)
        - name: tls-bundle
          secret: my-app-tls
          path: /etc/ssl/app
      annotations:                    # passthrough to every resource (see §5.2a)
        linkerd.io/inject: enabled
        eks.amazonaws.com/role-arn: arn:aws:iam::123456789:role/app-prod
        prometheus.io/scrape: "true"
      domain: my-app.yourdomain.com
      customDomains: [app.theirdomain.com]
    - name: staging
      replicas: 1
      domain: my-app-staging.yourdomain.com

  # PR Environments are configured on the parent Project (§5.0), not on
  # the App. There is no `spec.preview` on an App in v1.
```

### 5.2a Annotations — the escape hatch

Annotations are how everything in the Kubernetes ecosystem talks to
everything else. cert-manager reads `cert-manager.io/cluster-issuer`,
ExternalDNS reads `external-dns.alpha.kubernetes.io/hostname`, Linkerd
reads `linkerd.io/inject`, IRSA reads `eks.amazonaws.com/role-arn`,
Prometheus reads `prometheus.io/scrape`, nginx reads a hundred
`nginx.ingress.kubernetes.io/*` knobs. Mortise already writes several of
these itself (the Ingress carries its own cert-manager and ExternalDNS
annotations; the webhook uses `mortise.dev/revision` to talk to the
reconciler). The platform is annotation-driven by design — see §11.1a.

Users need to add their own. A team on EKS needs IRSA role annotations
on the ServiceAccount. A team running Linkerd needs
`linkerd.io/inject: enabled` on the pod template. A team on
nginx-ingress needs a rate-limit annotation. Locking these out would
force every such team to give up on Mortise-authored resources and
hand-roll their own Deployments — which defeats the point.

`environments[].annotations` is a single flat map that Mortise merges
onto the metadata of **every resource it creates for that environment**:
Deployment, pod template (`spec.template.metadata`), Service, Ingress,
PVCs, ServiceAccount. No filtering, no reserved prefixes, no validation.

```yaml
environments:
  - name: production
    annotations:
      linkerd.io/inject: enabled
      eks.amazonaws.com/role-arn: arn:aws:iam::123:role/app-prod
      nginx.ingress.kubernetes.io/rate-limit: "100"
```

**User wins on conflict.** If the user sets a key Mortise also sets (e.g.
`cert-manager.io/cluster-issuer`), the user's value replaces Mortise's.
This is deliberate — it's how a team overrides Mortise's default
cluster-issuer without dropping out to raw Kubernetes, and it's how
§5.6's `tls.clusterIssuer` override is implemented under the hood.

**Footgun — by design.** Setting `mortise.dev/revision` here will break
the webhook → reconciler handshake that triggers redeploys on git push.
Setting `kubernetes.io/ingress.class` conflicts with the
`ingressClassName` field Mortise sets. Setting a malformed
`cert-manager.io/*` value will break TLS issuance. These footguns are
not guardrailed. This field is for advanced users who already know what
each annotation means in their stack — if you don't know what you're
setting, don't set it.

**Scope is intentionally environment-level only.** No app-level
`spec.annotations`, no Project-level, no PlatformConfig-level. Matches
the `env` / `sharedVars` pattern: environment-scoped is the unit of
deploy isolation, which is exactly the granularity annotation-based
tools (Linkerd injection per-env, IRSA role per-env, rate-limit per-env)
need. Cross-env constants go in a YAML anchor.

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
- Build logs stream to UI via SSE in real-time
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
catalog with HA/PITR/backups is post-v1 (see §6.3 — the recommended path is
to install CNPG / a Redis operator directly and point Apps at them).

### 5.5a External Services — binding to things Mortise doesn't run

Not every backing service lives inside the cluster. Teams have DBs on
RDS, Redis on ElastiCache, Postgres on a VM, an internal API that
predates Mortise by five years. They still want the Railway moment:
click → `DATABASE_URL` in my API pod.

An App with `spec.source.type: external` is the Mortise object for these.
No Deployment, no pods, no build — just a domain, TLS, and a bindable
credentials surface. Mortise writes a `Service` of `type: ExternalName`
(or headless Service with a static Endpoints) pointing at the upstream
host so in-cluster binders reach it by cluster-internal DNS, and a
`Secret` holding the credentials. Everything else (bindings, projected
env, `{app}-credentials` Secret) works identically to any other App.

**Flavor A — user-provided credentials (the common case).**
The user types the hostname, port, and credentials into the Mortise UI
or CRD. Mortise writes a new `Secret` it owns. Useful when the external
service exists but has no corresponding Kubernetes resource yet.

```yaml
apiVersion: mortise.dev/v1alpha1
kind: App
metadata: { name: prod-db }
spec:
  source:
    type: external
    external:
      host: db.internal.corp.example       # user-provided
      port: 5432
  credentials: [DATABASE_URL, host, port, user, password]
  environments:
    - name: production
      # credential values supplied inline OR via valueFrom:
      env:
        - { name: password, valueFrom: { secretRef: prod-db-password } }
        - { name: user,     value: "app" }
```

**Flavor B — `importFrom` an existing Service + Secret.**
When the external service was installed by another operator (Crossplane,
CNPG, ACK, the cluster owner), it already exposes a `Service` and a
`Secret`. No point re-typing the same credentials into Mortise. `importFrom`
references them and Mortise wires them straight through to binders:

```yaml
spec:
  source:
    type: external
    external:
      importFrom:
        service: { name: cnpg-prod, namespace: databases }      # takes host/port from this Service
        secret:  { name: cnpg-prod-app, namespace: databases }  # takes user/password/etc. from this Secret
        keyMap:                                                 # optional; remap Secret keys to credential names
          username: user
          password: password
  credentials: [host, port, user, password]                     # which keys to expose to binders
```

Mortise reads the referenced `Service`/`Secret` at reconcile time,
projects the values into its own `{app}-credentials` Secret in the App's
namespace (so cross-namespace `secretKeyRef` works — see Issue #2), and
watches the source Secret for rotation. The source resources are **not**
owned by Mortise; deleting the App does not delete them.

**Constraints (v1):**
- `network.public: true` still means "give it a domain and TLS," routed
  via Ingress → ExternalName Service → upstream host. Useful for
  exposing a behind-the-firewall service at `api.yourdomain.com`.
- No `build:`, `replicas:`, `storage:` — the App is a facade, not a
  workload. Setting these is a validation error.
- No health checks or deploy history — the upstream's lifecycle is the
  user's problem.
- Cross-namespace refs in `importFrom` require the operator's
  ServiceAccount to have read access on the source namespace. Chart
  ships with a `ClusterRole` for `services` + `secrets` GET/WATCH;
  restrictive setups can scope this down with a supplemental RoleBinding.

### 5.5b Secret Mounts — files alongside env-var bindings

Bindings are env-vars-only by design (see §5.5 — the 12-factor contract
is the whole point). But many real apps need credentials as *files*:
Java keystores (`truststore.jks`), mTLS client certs, JWT signing keys
that the underlying library insists on reading from disk, Authelia /
Caddy / Prometheus config files, SSH private keys.

`secretMounts` on an environment mounts an existing k8s Secret as a
volume. The operator adds a `volume` + `volumeMount` to the Deployment
it authors; no other change.

```yaml
environments:
  - name: production
    secretMounts:
      - name: tls-bundle               # volume name (DNS-1123 label)
        secret: my-app-tls             # k8s Secret in the App's namespace
        path: /etc/ssl/app             # mount path inside the container
        # items:                       # optional: project specific keys to specific filenames
        #   - { key: tls.crt, path: cert.pem }
        #   - { key: tls.key, path: key.pem, mode: 0400 }
        # readOnly: true               # default true
```

This is a deliberately separate primitive from bindings:

- **Bindings** — cross-App contract. Names a Mortise App and projects its
  declared credentials as env vars. The resolver owns the Secret name.
- **`secretMounts`** — existing-Secret contract. Names a k8s Secret that
  already exists (because ESO wrote it, because a user ran
  `kubectl create secret`, because an operator produced it) and mounts
  it. The user owns the Secret name.

The two compose: an App might bind to Postgres for `DATABASE_URL` via
env *and* mount an `ssl-client` Secret for mTLS against a second
service. No contradiction; they serve different needs.

### 5.6 Network, Domains, TLS

Operator annotates `Ingress` → ExternalDNS creates DNS record → cert-manager
issues TLS cert. Zero user action. Each environment gets its own subdomain
automatically, rooted at the platform domain configured at install.

Custom domains: user sets CNAME, adds the domain in UI, Ingress rule + TLS
added by operator.

**TLS overrides (per environment).** The platform default is
`PlatformConfig.tls.certManagerClusterIssuer` (typically `letsencrypt-prod`).
Real clusters need escape hatches: a corp PKI with its own `ClusterIssuer`,
a wildcard cert provisioned out-of-band, a smallstep CA for internal
deployments. `environments[].tls` provides two levers:

```yaml
environments:
  - name: production
    # Option A — use a different ClusterIssuer for this env.
    # Mortise still writes the cert-manager annotation; cert-manager
    # talks to the named issuer instead of the platform default.
    tls:
      clusterIssuer: smallstep-internal
```

```yaml
environments:
  - name: production
    # Option B — bring your own cert. Mortise writes the Ingress
    # tls: [{ secretName }] block directly and does NOT add the
    # cert-manager annotation. The Secret must already contain a
    # valid tls.crt / tls.key pair, typically written by ESO,
    # cert-manager's `Certificate` CRD elsewhere, or kubectl.
    tls:
      secretName: app-prod-tls
```

The two are mutually exclusive — setting both is a validation error.
Setting neither inherits the platform default. Custom domains
(`customDomains:`) pick up the same TLS config; per-host overrides are
post-v1 if real demand appears.

### 5.7 Storage (v1)

Mortise is deliberately unopinionated about CSI backends. The App CRD accepts
a list of named volumes; each references a StorageClass (defaulting to the
cluster's default SC). For v1:

- **Single-node / homelab (k3s default):** local-path-provisioner. RWO only.
  Fine for most apps.
- **Multi-node or cloud:** use the cluster's default SC.
- **RWX volumes:** supported if the user picks a StorageClass that provides it
  (NFS, EFS, Longhorn-over-NFS). No storage wizard in v1 — sizing guidance
  is docs (see §6.3).

**v1 simplifications:**
- `accessMode: auto` infers RWO for replicas=1, otherwise reads the SC's capability
- `perReplica` / StatefulSet-per-volume is **deferred post-v1** (§6.2) —
  v1 supports single volume per App with RWO or RWX
- Multi-volume is supported (list of volumes) but automatic tier-detection
  (fast / shared / default) is not in v1

### 5.8 Environments & Deploy Model

- Named environments per App (e.g. `production`, `staging`)
- Independent Deployments, isolated by namespace
- **Promote:** staging → production with no rebuild
- **Rollback:** deploy history (digest + timestamp + SHA); one-click
- **Preview environments (project-level toggle, §5.0 `spec.preview.enabled`).**
  When enabled on the parent Project, PR opens → operator creates one
  `PreviewEnvironment` per App in the project, clones staging's config,
  DNS + TLS handled automatically, bindings live-resolved through the
  preview namespace (no credential copy). PR closes or TTL expires →
  everything deleted. URL posted as PR comment.

  **Scope semantics (option a).** *Every* App in the project reconciles
  into the preview namespace — including `kind: cron` Apps and Apps
  with `network.public: false`. They just don't get a public URL
  (nothing to route to). This keeps bindings coherent: a preview API
  can reach its preview DB and preview worker's cache without manual
  stitching. Users whose crons hit external systems and shouldn't
  run per-PR should split them into a sibling project with previews
  off. A per-App opt-out may be added in v2 as a visible UI toggle.

### 5.8a Cron Apps

An App with `kind: cron` reconciles to a Kubernetes `CronJob` instead of a
`Deployment`. Everything else — source, build pipeline, env/secrets/bindings,
environments, preview envs, rollback, image digest tracking — works the same.

```yaml
kind: App
spec:
  kind: cron
  schedule: "0 3 * * *"               # stdlib cron expression; required for cron
  concurrencyPolicy: Forbid           # Forbid (default) | Allow | Replace
  source: { type: git, repo: ... }
  environments:
    - name: production
      resources: { cpu: 200m, memory: 256Mi }
      env: [...]
      bindings: [ { ref: my-db } ]
```

Constraints:
- `network.public` is ignored; no Ingress, no domain, no TLS.
- `replicas` is ignored (CronJobs don't replicate; concurrency is controlled by `concurrencyPolicy`).
- **Preview environments include cron Apps.** When the parent project
  has `spec.preview.enabled: true`, cron Apps reconcile into each
  preview namespace alongside every other App — they just don't get a
  public URL. They *do* run per their schedule, which means real
  side-effects if the cron hits external systems. Users with
  heavy or side-effecting crons should split them into a sibling
  project with previews off. A per-App preview exclusion toggle may
  arrive in v2.
- Logs and run history are surfaced in the UI the same way as Deployment
  rollouts — per-run stdout/stderr, exit code, duration.

Why this shape: CronJob is a k8s primitive; the only work is reconciling one
instead of a Deployment at the end of the same pipeline. No separate "Jobs"
product, no new lifecycle.

### 5.8b Variable Resolution

Four levels of variable contribute to a pod's final env, resolved in order —
later levels override earlier:

1. **Platform defaults** — a fixed set the operator always injects
   (`MORTISE_APP`, `MORTISE_ENVIRONMENT`, `MORTISE_DEPLOY_ID`).
2. **Bound App credentials** — short-form `bindings: [{ ref: my-db }]`
   projects every key in the bound App's `credentials:` block as env
   (`DATABASE_URL`, `host`, `port`, …). Long-form `valueFrom.fromBinding`
   projects a single key under a different env name.
3. **App-level `sharedVars`** — project/App-wide values visible to every
   environment of this App. Use for `LOG_LEVEL`, `SENTRY_DSN`, feature
   flags. Supports both literal `value:` and `valueFrom.secretRef:`.
4. **Environment-level `env`** — per-environment overrides. Wins on
   conflict.

All three of `value:`, `valueFrom.secretRef:`, and `valueFrom.fromBinding:`
are supported in both `sharedVars` and `env`. Bindings remain the
cross-App contract; `sharedVars` is the answer to "I don't want to
repeat `LOG_LEVEL=info` in every environment." Team-level shared
variables (visible to all Apps in a team) are a natural follow-on once
the team model (§5.10) settles; not in v1.

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

### 5.9a Env-var Editing Surface

Railway-quality env editing is a core differentiator. Mortise provides a
dedicated editing path so users never need to `kubectl edit` YAML or do a
full `PUT` on the App CRD to change a variable.

**API:**
- `GET /api/projects/{project}/apps/{app}/env` — returns all envs, references
  unresolved (shows `@from:db.host`, not the resolved value).
- `GET /api/projects/{project}/apps/{app}/env/{env}` — single environment.
- `PUT /api/projects/{project}/apps/{app}/env/{env}` — full replace of one
  environment's `env` array.
- `PATCH /api/projects/{project}/apps/{app}/env` — op-list (`set`/`unset`)
  with `scope: shared|env`, applied atomically. `fromBinding` validated at
  PATCH time; `secretRef` is not (matches `secretMounts` semantics — Pod
  blocks in `ContainerCreating` if the Secret doesn't exist).
- `POST /api/projects/{project}/apps/{app}/env/import` — bulk import from
  `.env` file body.

**CLI:**
- `mortise env list [--env NAME] [--all] [--app NAME]`
- `mortise env set KEY=VALUE [--env NAME] [--shared]`
- `mortise env unset KEY [--env NAME] [--shared]`
- `mortise env import FILE [--env NAME]`
- `mortise env pull [--env NAME] > .env`
- Shortcuts: `@secret:name` and `@from:app.key` so users don't round-trip
  through a separate secret create.

**UI:**
- Variables tab on App detail page.
- Table with mask/reveal per row, inline edit.
- Raw-editor textarea mode.
- `.env` import with diff preview.

**Auto-redeploy:**
- `mortise.dev/env-hash` annotation on the pod template. Any env change
  through the API/CLI/UI recomputes the hash and triggers a rolling restart.

**GitOps tradeoff (v1):**
- UI/CLI is the canonical writer for env vars.
- Argo CD users add `spec.environments[*].env` + `spec.sharedVars` to
  `ignoreDifferences`.
- Per-app opt-in for GitOps-only env management is post-v1 if real demand
  appears.

### 5.10 Auth (login for Mortise itself)

Auth is scoped to **Mortise's own UI and API** — logging developers and
admins into Mortise. User apps' authentication is the user's problem,
out of scope (see note below).

**Two in-tree implementations cover the realistic space:**

- **Native** (default): username/password accounts stored as k8s Secrets in
  `mortise-system` (hashed, never plaintext). One admin created during
  first-run wizard; invites via generated link.
- **Generic OIDC**: configured with `issuerURL + clientID + clientSecret`.
  Covers every mainstream IdP via the OIDC standard — Authentik, Keycloak,
  Okta, Auth0, Google, GitHub, GitLab, Microsoft Entra, Zitadel, etc.
  One code path, one set of tests.

Selection is per-deployment via `PlatformConfig.auth.mode: {native, oidc}`.
Not pluggable at runtime — if you want OIDC, set the flag and restart the
operator. That's enough flexibility for 90%+ of real users. SAML and LDAP
are out of scope for v1 and can be added in-tree later as separate modes if
demand appears.

**Roles and RBAC (v1):**

Two roles, platform-wide:

| Role | Can do |
|---|---|
| **admin** | Everything — users, providers, DNS, platform settings, all Projects and Apps. Create/delete Projects. |
| **member** | Create/edit/delete Apps within any Project; view logs and metrics; edit env/secrets; deploy, rollback, promote. Cannot create/delete Projects, manage users, or edit platform settings. |

**Implicit default team (v1 forward-compat stub).** The operator creates
a single `Team` CRD named `default-team` at first-run setup and binds
every user to it. The UI renders no team chrome in v1 — users never see
or interact with the Team. The stub exists purely so v2's richer team
model (see below) is additive: splitting the implicit team into N
teams, adding per-team roles, and adding env-scoped grants all happen
without a data migration.

**Deferred to v2 (not in v1 scope):** multi-team installs, the
5-role team model (`platform-admin` / `platform-viewer` / `team-admin` /
`team-deployer` / `team-viewer`), per-grant environment scoping, OIDC
group → team mapping, team-scoped invites. v2 introduces these as
additive extensions on the `Team` CRD already present in v1.

**SSO for user apps is not a Mortise concern.** Mortise-v1's job is what
Railway's is: hand users env vars and a URL. A user who wants their Gitea
to "Login with Authentik" installs Authentik themselves, creates the OIDC
client in Authentik's UI, and pastes the client ID + secret into Mortise's
secrets editor — same flow as on Railway. Forward-auth middleware and
automated OIDC/SAML client provisioning for user apps are post-v1 topics
to be specced if and when real demand shows up; they are not a v1 contract.

### 5.11 Observability (v1)

**Metrics:** `metrics-server` baseline — CPU/memory per pod/environment
surfaced in UI. No Prometheus installed by core. Users who want deeper
observability install kube-prometheus-stack themselves (see §6.3). UI
degrades gracefully — charts show what's available.

**Logs — structured JSON on stdout.** The operator emits all output as
structured JSON to stdout. Three log categories, distinguished by
`component` field:

| Component | Content | Example query (Loki) |
|---|---|---|
| `build` | Build log lines, one per BuildKit stream event | `{app="mortise-operator"} \| json \| component="build" \| app="my-api"` |
| `audit` | User actions: deploy, rollback, secret rotate, team changes | `{app="mortise-operator"} \| json \| component="audit" \| actor="jane@co.com"` |
| `reconciler` | Controller reconciliation events, errors, retries | `{app="mortise-operator"} \| json \| component="reconciler" \| level="error"` |

Every log line includes: `app`, `team`, `environment` (where applicable),
`timestamp`, `level`. Build logs also carry `buildID` and `commitSHA`.
Audit logs carry `actor` and `resource`.

**No log storage in the operator.** Build logs stream to the UI via
SSE during the build. After the build finishes, they exist only in
the operator's stdout — if a log agent (Loki, Fluentd, CloudWatch, etc.)
is collecting, users get full history with retention. If not, live-stream
only. This is the trade-off of the no-PVC architecture.

**User app logs:** standard container stdout/stderr. Any log agent on the
cluster collects them. Mortise does not proxy, aggregate, or store user
app logs. The UI surfaces live `kubectl logs` output via SSE —
same as a `stern` tail. Historical log search requires a log agent.

**Per-project Activity store (convenience, not source of truth).** In
addition to the stdout audit stream, the operator maintains a small
in-cluster store of recent audit events *per project* so the UI
Activity rail (UI_SPEC §12.22) can render history without a log
agent. v1 store: a ConfigMap named `activity-{project-name}` in
`mortise-system`, capped at the last 500 events per project with a
simple ring-buffer trim; event body is JSON (`{timestamp, actor,
verb, resource, summary}`). Written by every write handler in the
REST API (see §5.13). The stdout stream remains authoritative for
compliance retention; the ConfigMap is a cache for UX. If a project
exceeds 500 events/day and the rail starts to feel sparse, users
point a log agent at the stream and get full history — no Mortise
change required. A dedicated `ActivityEvent` CRD or annotation ring
is deferred until real demand; see §10.

### 5.12 Git Providers (v1)

All three supported forges use the same shape: an OAuth application the
admin creates on the forge and pastes into Mortise's Git Provider
settings. Identical mental model across GitHub, GitLab, Gitea.

- **GitHub** — admin creates an OAuth App at
  `github.com/settings/applications/new` (~90s), sets callback URL to
  `https://mortise.yourdomain.com/api/oauth/github/callback`, pastes
  client ID + client secret into Mortise.
- **GitLab** (.com or self-hosted) — admin creates an OAuth application
  at `{gitlab-url}/-/user_settings/applications`, pastes credentials.
- **Gitea / Forgejo** (self-hosted) — admin creates an OAuth2 application
  in user settings, pastes credentials.

After credentials are configured, the per-repo webhook is registered
automatically by the GitProvider controller when a user connects a repo
(POST `/repos/{owner}/{repo}/hooks` on GitHub; equivalent on the others).

**Why OAuth instead of GitHub App:** GitHub Apps offer finer permissions
and per-install rate limits, at the cost of admin setup complexity
(RSA key generation, per-org installation, permission config). Mortise's
target audience (homelabbers, small teams) is better served by OAuth's
~90-second flow. All three forges end up with the same mental model.

GitHub App support can land as a second `spec.mode: githubApp` on the
GitProvider CRD if real demand appears; the interface seam
(`internal/git/github/`) accommodates it. Not v1.

GitProvider CRD: one instance per configured provider (e.g.
`github-main`, `gitea-homelab`). Cluster-scoped. Credentials via
secretRefs.

**Relay-mode Cloudflare Worker is deferred** (§6.4). Admin-configured
OAuth is the v1 path — no third-party infrastructure required.

### 5.13 Web UI (v1)

- **Project dashboard** — list of Projects, each card shows app count,
  status summary, last activity
- **Project workspace** (`/projects/{p}`) — apps in this project, status
  badges, last deploy; primary working surface
- **New app** (`/projects/{p}/apps/new`) — template picker → source picker
  (git | image) → guided form
- **App detail** (`/projects/{p}/apps/{a}`) — deploy history, real-time
  build logs, environment tabs, metrics, custom domains, secrets editor,
  bindings (in-project and cross-project), deploy tokens
- **Create project** — name + description
- **Secret store** — list by app/env, write-only values, rotation
- **Platform settings** — domain, DNS, git providers, user management
- **Activity rail** (project scope only) — toggled by a pulse button in
  the top bar. Closed by default; opens as a slide-out over the
  canvas/drawer. Renders the per-project activity store (§5.11) merged
  with synthesized deploy rows from App `status.deploys` history.
  Filter chips: Deploys / Changes / Members / All. See UI_SPEC §12.22.

Top-bar project switcher lets users jump between projects without
returning to the dashboard.

**Actor capture.** Every write handler in the REST API (POST / PATCH /
PUT / DELETE) captures the authenticated `Principal` and stamps it
into an `actor` field on the resulting audit log line (§5.11) and the
per-project activity store entry. Unauthenticated or service-account
writes (controllers, webhooks) are stamped `system`. This is the
backend foundation for the Activity rail, deploy-history authorship,
and any future per-user audit surface.

UX standard: Railway-quality. Source types abstracted — users see "your apps."

### 5.14 CLI (v1)

Railway-style: short commands, positional args, interactive prompts when
ambiguous. Full flags for scripting/CI.

```bash
# Projects — the top-level context
mortise project list
mortise project create my-saas
mortise project delete my-saas                # prompts confirmation; admin only
mortise project use my-saas                   # set current project
mortise project show                          # show current project details

# App commands scope to current project unless --project is given
mortise app list
mortise app list --project infra
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

Config at `~/.config/mortise/config.yaml` stores `server_url`, `token`, and
`current_project`. `mortise login` sets `current_project=default` after
successful login.

### 5.15 CRDs (v1)

| CRD | Scope | Purpose |
|---|---|---|
| `Project` | Cluster | Top-level grouping; owns a k8s namespace |
| `App` | Namespaced | Deploy anything (git or image in v1); lives in a Project's namespace |
| `PreviewEnvironment` | Namespaced | Ephemeral PR environments (auto-managed) |
| `PlatformConfig` | Cluster | Platform settings (domain, DNS, default SC) |
| `GitProvider` | Cluster | One per configured git provider |

---

## 6. Explicitly Deferred (Post-v1 Scope)

**There are no first-party Mortise addons.** Everything below is either a
post-v1 operator feature in core, a documentation recipe, or a user-installs-
upstream pattern. Mortise ships one product — the operator + its CRDs + the
UI — and that product is the complete deliverable. This is the structural
commitment that keeps §2's positioning honest.

### 6.1 Scope Invariants

Any feature proposal, now or in future, gets checked against these invariants
before scope-in. If it violates any, it doesn't ship — regardless of how
useful it would be in isolation.

1. **Core is the whole product.** Mortise has one install: `helm install
   mortise`. There is no two-tier product, no addon pack, no plug-in
   ecosystem. Every feature that exists in Mortise exists in core.
2. **Integration happens through Kubernetes primitives.** When users want
   capabilities beyond Mortise (SSO, monitoring, logs, backup, external
   secret managers), they install the upstream project directly. Mortise
   consumes standard Kubernetes objects (Secrets, Ingresses, CRDs,
   metrics endpoints) those projects produce. Mortise does not define a
   Mortise-specific plug-in protocol to sit in front of them.
3. **We don't take on long-term upstream-tracking relationships
   speculatively.** Shipping a convenience wrapper for a third-party
   project commits us to tracking its API, schema, and breaking changes
   forever. That commitment is only made when real demand justifies the
   permanent maintenance cost, and it's made sparingly.
4. **Interfaces are for testability, not for extension.** Go interfaces
   inside the operator exist to keep third-party SDKs out of controller
   code and to enable fast unit tests. They are not a plug-in API. Third
   parties do not implement them; there are ~10 total in-tree
   implementations across all interfaces (§11.1).
5. **When an upstream project we rely on becomes unmaintained, we swap,
   document a workaround, or scope the feature out — we do not fork.** We
   are not in the business of maintaining abandoned third-party software.

### 6.2 Post-v1 Operator Features

Capabilities that stay in the core operator but are deliberately deferred
from the v1 cut. Each lands as a core feature flag or CRD extension, not
as a separate chart.

- **Cloudflare Tunnel automation** — creates a CF Tunnel via API and wires
  cloudflared into the cluster; enables Mortise to be reachable from the
  internet without a public IP. Operator feature, not an install chart for
  `cloudflared` (users who want a generic tunnel chart use the upstream).
- **`perReplica` volumes / StatefulSet workloads** — extend the App CRD to
  support StatefulSet-shaped workloads with per-replica PVCs.
- **Multi-cluster** — `Cluster` CRD, bearer-token trust model, aggregated
  UI. Single-cluster remains the primary deployment shape; multi-cluster
  is purely additive.
- **`mortise export` CLI** — export all App / PlatformConfig / GitProvider
  CRs as portable YAML for backup and migration. Pairs with Velero for
  full DR (Velero handles PVCs; `mortise export` handles configuration).
- **Log UI integration** — PlatformConfig field pointing at the user's log
  backend (Loki, CloudWatch, Splunk, Elastic, GCP Logging); Mortise UI
  embeds/links to it per App. Backend-agnostic — no Loki install required.

### 6.3 Integration Recipes (Documentation, not code)

Things users want that Mortise supports by being well-behaved, not by
shipping code. Each is a documentation page with copy-pasteable YAML; none
is a Mortise-maintained artifact.

- **OIDC login via any IdP.** Install Authentik / Keycloak / Zitadel /
  Okta / Google / GitHub (the user's choice) and point `PlatformConfig.auth`
  at its issuer URL. Recipe page per common IdP.
- **Prometheus monitoring.** Install kube-prometheus-stack; apply the
  ServiceMonitor example that targets Mortise's `/metrics` endpoint.
- **Log aggregation.** Install Loki (or any log backend) the standard way;
  its shipper scrapes Mortise and user pods automatically from labels.
- **External secret managers.** Install ExternalSecrets Operator, point it
  at Vault / AWS SM / GCP SM / Azure KV / 1Password / etc., and set
  `PlatformConfig.secrets.externalStore` on Mortise. See §5.9 for the flow.
- **Backup and disaster recovery.** Install Velero with a backup target;
  apply the Schedule example that includes Mortise's namespaces. Pair with
  `mortise export` for configuration portability.
- **Backing services (Postgres, Redis).** Three working paths: (a) `image`
  source with `postgres:16` + PVC for homelab simplicity, (b) install
  CloudNativePG or redis-operator directly and bind via `external` source,
  (c) use managed services (RDS, Upstash) via `external` source. No
  Mortise-maintained catalog.
- **Storage guidance.** Documentation on picking a StorageClass (local-path,
  Longhorn, Ceph, NFS, cloud CSI). Mortise does not install storage
  providers; it detects missing RWX and links to the docs.

### 6.4 Deferred Until Real Demand

Things that could conceivably exist but won't be built speculatively.
Scoped in only when users with that specific need show up.

- **Cloudflare Worker relay for GitHub App OAuth** — infrastructure we'd
  have to host and maintain forever to support GitHub App mode. PAT and
  OAuth-app modes cover 95%+ of git-provider users; this stays off the
  roadmap until there's concrete demand.
- **Cloudflare for SaaS custom hostnames** — genuinely useful for users
  building SaaS on Mortise where end-customers want custom domains. A real
  feature for a narrow audience; specced and built when that audience
  shows up.
- **`helm` source type** — deploying arbitrary Helm charts through the
  Mortise UI. Scope expansion into "generic Helm dashboard" territory
  where other tools (Lens, k9s, Argo CD) already serve users better. Not
  shipped unless the Mortise-specific angle becomes clear.

### 6.5 Community-Maintained (Not Mortise's Scope)

- **App preset repository** — a separate, community-contributed collection
  of App CRD templates for common self-hosted software (Paperless,
  Vaultwarden, etc.). Data, not code. Not maintained by the Mortise team.
  Mortise's side of the integration is a small UI feature that can import
  an App from a URL. Breakage of any preset is a community concern.

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
├── ui/                              # SvelteKit app
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

### 8.1 Helm Chart

```
charts/
└── mortise/               # single chart — not an umbrella
    ├── Chart.yaml          # depends on traefik, cert-manager, external-dns (bundled), zot (conditional)
    ├── values.yaml
    └── templates/          # operator Deployment, RBAC, CRDs, Service, etc.
```

Mortise is one chart. It declares a handful of well-known upstream charts as
dependencies (Traefik, cert-manager, ExternalDNS, Zot) so a single
`helm install` gives a working cluster out of the box. Those dependencies
can be turned off (§8.3) when the cluster already has equivalents.

There is no umbrella chart, no addon subchart directory, no preset system
to maintain. §6.1 invariant #1 guarantees the chart alone is the whole
product.

### 8.2 Install UX

```bash
helm install mortise oci://ghcr.io/mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set domain=yourdomain.com \
  --set dns.provider=cloudflare \
  --set dns.apiToken=xxx
```

One command. The result is the full Railway-equivalent product: deploy from
git or image, bindings, previews, TLS, domains, native auth. No second
install step, no addon picker, no "did you remember to enable X?"

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
`RegistryBackend` — or an annotation-driven integration (ExternalDNS).
Disabling a bundled component does not disable the feature; it swaps the
implementation.

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
limitation to be lifted later.

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
- SSE endpoint for log streaming
- SvelteKit UI skeleton: login, dashboard (list Apps), new-app form for
  `image` source only, App detail page with env / secrets / domains
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

### Phase 3.5 — Projects

Introduces the top-level grouping concept before Phase 4 layers builds,
previews, and the rest on top. Landing Projects before Phase 4 avoids
retrofitting the URL scheme, bindings resolver, and UI navigation later.

**Backend:**
- `Project` CRD (cluster-scoped) + controller that reconciles a k8s namespace
  (`project-{name}`) and sets itself as owner
- App controller respects project namespace boundaries (no logic change —
  Apps are already namespace-scoped; the namespace is now named by the Project)
- Bindings resolver honors the optional `project:` field for cross-project
  bindings
- API routes restructured to `/api/projects/{p}/apps/{a}/...` throughout.
  The pre-Project `?namespace=` query form is removed.
- First-run setup auto-creates a `default` Project after admin setup

**Frontend:**
- Dashboard becomes the Project list (was App list)
- New `/projects/{p}` workspace view shows apps in that project
- `/apps/*` URLs replaced by `/projects/{p}/apps/*` everywhere
- Project switcher in top bar for cross-project navigation
- Create Project form

**CLI:**
- `mortise project list/create/delete/use/show`
- `current_project` field in config file, defaulted to `default` after login
- All `mortise app ...` commands scope to current project unless
  `--project` overrides

**Lifecycle:**
- Delete Project → delete namespace → k8s GC cascades all contained
  resources (Apps, Deployments, Services, Ingresses, PVCs, Secrets)

Tests: create Project → namespace exists; create App in Project → App
lands in `project-{name}` namespace; cross-project binding resolves to
correct Service DNS; delete Project → namespace and Apps gone.

**Exit criteria:** user logs in → lands in `default` project → creates a
Postgres App + a bound API App inside that project → creates a second
project `staging` → moves (recreates) the app stack there → deletes
`staging` → everything in it disappears, `default` unaffected.

### Phase 4 — Build System (git source)

- Git provider CRD + controllers: GitHub, GitLab, Gitea — all via admin-configured OAuth apps
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
- Build logs streamed to UI via SSE
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

- **Project-level toggle** — `ProjectSpec.Preview.Enabled`. No per-App
  opt-in. When enabled, every App in the project participates (§5.8).
- `PreviewEnvironment` CRD auto-managed by a dedicated controller
- PR open → for each App in the project with previews enabled,
  clone staging env config; apply project-level `preview.*` overrides;
  DNS + TLS handled by existing ExternalDNS/cert-manager plumbing
- Cron and non-public Apps reconcile into the preview namespace but get
  no public URL (§5.8a, §5.8 scope semantics)
- Bindings live-resolved within the preview namespace (no credential copy)
- PR comment with preview URL(s); monorepo fan-out respected (only Apps
  whose `watchPaths` match the PR's changed files rebuild; every App in
  the project still reconciles into the preview namespace)
- PR close → delete; TTL fallback (72h default)

**Exit criteria:** open a PR on a project with `spec.preview.enabled: true`,
get a live preview URL in the PR comment, close the PR, preview
disappears.

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

### Phase 8 — Tenons & Integration Recipes

Tenons are independent projects that consume Mortise's REST API for specific
workflows or audiences. They live in their own repos, ship as their own Helm
charts, and are not part of the Mortise operator binary. Mortise ships one
or two reference tenons as living documentation; everything else is
community- or user-built.

**Reference tenons** (separate repos under `mortise-tenons/`):
- **cf-for-saas** — customer-managed domains via Cloudflare custom hostnames.
  Web app that accepts signups, creates Apps in per-customer namespaces, wires
  CF custom hostnames. Canonical example of "host other people's apps."
- **backup-tenon** — scheduled App backups (PVs + Secrets) to S3/NFS via
  Velero. Homelab-friendly; replaces a potential core feature.
- **cost-dashboard** (optional) — watches Apps, aggregates resource usage,
  attributes cost per App/team. Uses metrics-server or Prometheus if present.

**Integration recipes** (documentation pages in the main docs site, not code):
- **External CI path** — how to build images in GitHub Actions / GitLab CI /
  Woodpecker / bash / anything, push to any registry, and call the deploy
  webhook. Mortise's built-in BuildKit is an opt-in convenience; teams with
  existing CI or cluster-CPU constraints use this path and skip BuildKit
  entirely. Canonical workflow example alongside the Railpack/Dockerfile path.
- **OIDC setup** against Authentik / Keycloak / Okta / Google Workspace
- **Prometheus + Grafana** via kube-prometheus-stack, using Mortise's
  standard `ServiceMonitor` output
- **Log aggregation via Loki** — Mortise pods emit stdout logs; Loki
  collects; UI optionally surfaces via a Loki endpoint configured in
  platform settings
- **External secret managers** (Vault, AWS Secrets Manager) via
  ExternalSecrets Operator — ESO writes k8s Secrets, Mortise reads them.
  No Mortise-side changes.
- **Policy enforcement** via OPA/Kyverno — gate Mortise's admission writes
  with cluster-wide policies
- **Custom ingress controllers** (Gateway API, Istio, NGINX) — Mortise emits
  standard Ingress resources; the user's chosen controller reconciles them
- **Backing services** (Postgres via CNPG, Redis via redis-operator, MinIO,
  Supabase) — users install the upstream project, Mortise Apps bind to
  services via Service DNS + Secret refs
- **Storage sizing guidance** — RWX StorageClass options for homelab vs cloud

**Platform polish within the single binary** (last-mile v1 work):
- **First-run wizard** — admin setup, platform domain, DNS provider, storage
  class detection/recommendation
- **Rollback UI** — deploy history browser with one-click rollback (backend
  already supports)
- **Promote** — staging → production without rebuild (re-tag the digest)
- **Custom domains UI** — add CNAME-based custom domains per environment
- **Metrics in UI** — CPU/memory per pod via `metrics-server`
- **User/team management UI** — invite flow, role management (API already
  supports)
- **Infrastructure bundle** — cert-manager, ExternalDNS, Traefik as optional
  Helm chart dependencies for "one-command install with working TLS/DNS"

**Exit criteria for Phase 8 / full v1:**
- Fresh cluster → `helm install mortise` → first-run wizard → deploy a git
  App with preview envs → working HTTPS URL, in under 15 minutes
- At least one reference tenon (cf-for-saas or backup-tenon) published in
  its own repo, demonstrating API consumption
- Integration recipe docs for: external CI, OIDC, monitoring, external
  secret managers, Cloudflare Tunnel

### Post-v1

Organized by §6's taxonomy — operator features, integration recipes, deferred
items, and community-maintained data. None of these are "addons"; none add a
new install surface. Order depends on user demand after v1 ships.

**Operator features** (§6.2) — code changes inside the single binary:
- **`perReplica` volumes / StatefulSet support**
- **Multi-cluster** — Cluster CRD, bearer-token trust, aggregated UI
- **`mortise export` CLI** — render the managed resources for airgap inspection
- **Log UI integration** — if a Loki endpoint is configured, surface logs
  in the App view (no bundling of Loki itself)
- **Cloudflare Tunnel automation** — operator manages Tunnel + DNS for users
  who opt in

**Integration recipes** (§6.3) — documentation only, no code:
- OIDC setup against Authentik / Keycloak / Okta / Google / etc.
- Monitoring via kube-prometheus-stack
- Log aggregation via Loki
- External secret managers via ExternalSecrets Operator
- Backup/restore via Velero
- Backing services via CNPG / redis-operator
- Storage sizing guidance (RWX StorageClass options)

**Deferred until real demand** (§6.4):
- Cloudflare Worker relay for GitHub App OAuth
- Cloudflare for SaaS custom hostname automation
- `helm` source type

**Community-maintained** (§6.5):
- App preset repository — data-only CRD templates, not core-team owned

---

## 10. Open Questions

1. **Product name.** `[NAME]` / Mortise is a placeholder. The name gets baked
   into CRD apiVersions (`mortise.dev/v1alpha1`), Helm chart, CLI binary,
   config path, and the domain for any hosted assets (relay Worker later).
   Pick before tagging v1.
2. ~~**Operator datastore.**~~ **Resolved — no external datastore in v1.**
   The operator is stateless. Deploy history lives in `App` CRD status
   (bounded list per environment). Users and sessions are k8s Secrets in
   `mortise-system`. Audit events are structured JSON on stdout — the
   user's log aggregation (Loki, CloudWatch, etc.) handles retention and
   querying; Mortise does not store or index them. Build logs stream via
   SSE during the build and are emitted to stdout for collection.
   This removes the PVC requirement, enables multi-replica operator from
   day one (leader election for controllers, stateless API layer), and
   means backup = etcd snapshot. If deep audit querying or analytics
   becomes a real need post-v1, add an optional Postgres projection —
   CRD status remains source of truth.
3. **UI packaging.** Served by the operator binary (embed via `embed.FS`) or
   separate Deployment? Recommendation: embed — one fewer pod, simpler install.
4. **Helm chart distribution.** OCI (ghcr.io) or classic Helm repo? OCI is the
   direction; ghcr.io fits if the source lives on GitHub.
5. **Minimum supported Kubernetes version.** Propose 1.28+ (matches k3s latest
   and all cloud-managed providers).
6. **Activity store scale.** v1 stores the last 500 events per project
   in a ConfigMap (§5.11). A project that generates >500 events/day
   will see entries fall off the rail faster than users can read them.
   Defer a richer store (dedicated `ActivityEvent` CRD, sharded
   ConfigMaps, or a Postgres projection) until real demand — until
   then, users who need long-range audit point a log agent at the
   stdout stream, which is already the source of truth. Flag for
   re-evaluation once first large deployment lands.

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
Mortise controller  →  PolicyEngine     →  team-scoped RBAC (5 roles, env-scopable grants)
Mortise controller  →  GitAPI           →  GitHub | GitLab | Gitea
Mortise controller  →  GitClient        →  go-git (single impl, all forges)
Mortise controller  →  BuildClient      →  BuildKit (single impl)
Mortise controller  →  RegistryBackend  →  generic OCI (config-driven)
Mortise controller  →  IngressProvider  →  generic annotation-driven
```

**DNS is annotation-driven — no Go interface.** The `IngressProvider` emits
`external-dns.alpha.kubernetes.io/hostname` on every Ingress; ExternalDNS
picks it up. Swapping DNS providers (Cloudflare → Route53 → Infoblox) is a
Helm value change on ExternalDNS, not a code change in Mortise. A
`DNSProvider` interface would re-create the abstraction ExternalDNS was
chosen to provide, violating both "no interface without a real v1 impl" and
"Kubernetes IS the contract."

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
// two in-tree impls: native (k8s-Secret-backed) and genericOIDC

// internal/authz/policy.go
type PolicyEngine interface {
    Authorize(ctx context.Context, p Principal, resource Resource, action Action) (bool, error)
}
// one in-tree impl: team-scoped RBAC (platform-admin / platform-viewer / team-admin / team-deployer / team-viewer), with grants optionally scoped to specific environments — see §5.10

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
```

**Total in-tree impls across all contracts: ~10.** Not a plug-in ecosystem.

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

**Why these are not extension points:** with ~10 impls total and ~1 realistic
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
- Optimized for 4–8GB clusters as a baseline; much heavier once the user
  also runs monitoring / log aggregation / etc. alongside (which they install
  themselves — see §6.3)
- **Limited workload-kind surface.** v1 supports long-running services
  (Deployment) and scheduled jobs (CronJob via `kind: cron` — see §5.8a).
  One-off Jobs, StatefulSets, and DaemonSets are not supported. `perReplica`
  volumes and StatefulSet-per-App are post-v1 (§6.2).
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
- **Not a hard-isolation multi-tenant platform.** v1 has team-scoped RBAC
  (five roles plus env-scopable grants — see §5.10) but namespace
  isolation is soft (no NetworkPolicy generation, no ResourceQuota per
  team). Shared-cluster-with-untrusted-users scenarios need additional
  hardening beyond what v1 provides.
