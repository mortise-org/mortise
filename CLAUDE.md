# Mortise

## What this is

Self-hosted Railway-style deploy platform for Kubernetes. One operator, one
Helm chart. Users connect a git repo or pick an image → Mortise handles
builds, deploys, domains, TLS, env vars, volumes, preview envs, and bindings.
Kubernetes is fully abstracted away from the user.

Read SPEC.md for the full product spec. Read ARCHITECTURE.md for system
diagrams. This file is the operating manual for working in this codebase.

## Progress tracking

`PROGRESS.md` at the repo root is the single source of truth for what is
implemented vs. what the spec still calls for. **Read it at the start of
any task that touches feature scope** — it records phase-by-phase status,
interface / CRD coverage, and known landmines that the code itself will
not tell you about (e.g. resolver bugs, doc drift, hard-coded values that
should go behind an interface).

When your work changes implementation status, update `PROGRESS.md` in the
same commit as the code change. The "Keeping this file up to date" section
at the bottom of `PROGRESS.md` lists the specific triggers (feature
landed, bug found, interface impl added, CRD promoted from scaffold,
spec scope change). If you finish a task and `PROGRESS.md` still shows the
old state, your task isn't done.

Do not write progress info into this file (`CLAUDE.md`) or into
`README.md` — they drift. Put it in `PROGRESS.md`.

## Tech stack

- **Operator + API:** Go, kubebuilder, controller-runtime
- **UI:** SvelteKit + TypeScript (embedded in operator binary via embed.FS)
- **CLI:** Go (cobra)
- **Helm chart:** charts/mortise/
- **CRDs:** App, PlatformConfig, GitProvider, PreviewEnvironment
- **Bundled tools:** BuildKit (image builds), Zot (OCI registry), Traefik
  (ingress), cert-manager (TLS), ExternalDNS (DNS records)

## Architecture rules

These are non-negotiable. Violating any of them is a bug.

### Standards, not implementations

Mortise couples to standards, not specific tools:
- k8s Ingress API — not Traefik-specific annotations
- OCI Distribution Spec — not Zot-specific APIs
- OIDC — not Authentik-specific APIs
- ACME (via cert-manager) — not Let's Encrypt-specific

If you're about to write code that only works with one specific tool (Traefik,
Zot, GitHub), it must be behind an interface in internal/<name>/.

### Controllers never import third-party SDKs

All external calls go through interfaces defined in internal/<name>/.
Controllers import only Mortise's own types. Never import go-github,
moby/buildkit/client, or any other third-party SDK in a controller file.

### Interfaces (internal seams, not plug-in APIs)

These exist for testability and version-bump isolation. They are NOT extension
points for outside implementers.

```
controller  →  AuthProvider     →  native DB | generic OIDC
controller  →  PolicyEngine     →  native (admin/member)
controller  →  GitAPI           →  GitHub | GitLab | Gitea
controller  →  GitClient        →  go-git (single impl)
controller  →  BuildClient      →  BuildKit (single impl)
controller  →  RegistryBackend  →  generic OCI (config-driven)
controller  →  IngressProvider  →  generic annotation-driven
```

DNS is annotation-driven via ExternalDNS (no Go interface — see SPEC §11.1).

No interface without a real v1 implementation behind it.

### Everything is an App, grouped under Projects

One CRD for workloads, one concept. Source type (git | image) determines
how it deploys. No separate ServiceInstance, DatabaseInstance, or similar
CRDs. Backing services (Postgres, Redis) are Apps with
`network.public: false` and a `credentials` block. Other Apps bind to them.

**Apps live inside Projects, not free-floating.** Every App belongs to
exactly one Project; its namespace is `project-{project-name}`. Users,
URLs, and CLI commands scope to a current project context. When in doubt
about where something goes: does it exist PER PROJECT (apps, secrets,
domains, preview envs) or PER PLATFORM (users, git providers, DNS config,
platform domain)? Per-project → lives under the project namespace.
Per-platform → lives in `mortise-system` or a cluster-scoped CRD.

### Mortise owns only what it creates

Never touch resources Mortise didn't create. Coexist with Argo CD, Flux,
manually-deployed resources, other operators. Check for ownership labels
before modifying or deleting any resource.

### No addon/plug-in architecture

Mortise is one operator, one Helm chart. No subcharts, no plug-in SDK, no
extension registry. Third-party integration happens through Kubernetes
primitives (ESO writes k8s Secrets, OPA gates admission, Prometheus scrapes
standard metrics). If you're tempted to add a plug-in system, stop.

## Behavioral guidelines

### Think before coding

- State assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them — don't pick silently.
- If a simpler approach exists, say so.

### Simplicity first

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

### Surgical changes

- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style.
- Every changed line should trace directly to the task.
- Remove imports/variables/functions YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

### Goal-driven execution

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
```

## Testing

### Commands

```bash
make test                 # unit + envtest, <10s, run before every commit
make test-integration     # spins up k3d, installs chart, runs suite, tears down
make dev-up               # persistent k3d + tilt live-reload for active dev
make dev-down             # tear down dev cluster
make test-integration-fast # run integration suite against existing dev cluster
```

### Layers

| Layer | Tool | What it tests | Speed |
|---|---|---|---|
| Unit | go test + fake client | Pure logic, no cluster | <10s |
| envtest | controller-runtime envtest | Reconcile loops, real apiserver+etcd | ~2s/test |
| Integration | k3d + Helm install | Real cluster, real pods, real networking | <3min total |
| UI E2E | Playwright | Critical user flows against k3d | Per PR |

### Conventions

- `//go:build integration` tag separates integration from unit tests.
- Every integration test creates its own namespace via `CreateTestNamespace(t)`.
  Registered with `t.Cleanup`. Tests are independent.
- Fixtures live in `test/fixtures/`. Tests load by path via `LoadFixture(t, path)`,
  mutate, then apply. Never hand-write App YAML in test code.
- Assertion helpers in `test/helpers/`: `RequireEventually`, `AssertPodsRunning`,
  `AssertIngressExists`, `WaitForAppReady`.
- `TestMain` in `test/integration/suite_test.go` asserts cluster + chart ready.
  No per-test cluster lifecycle.

### Mocking policy

- **BuildKit:** mock BuildClient interface in unit tests. Real BuildKit only in
  integration tests.
- **Git providers:** mock GitAPI at the interface boundary for unit tests. Local
  Gitea in integration tests.
- **ACME:** Pebble (local ACME server) in integration tests. Never hit real
  Let's Encrypt.
- **DNS:** ExternalDNS in-memory provider in integration tests.
- **Registry:** real Zot in the k3d cluster for integration tests.
- **Time:** any TTL/timeout logic takes `clock.Clock` (k8s.io/utils/clock).
  Tests inject fake clock. Never call `time.Now()` directly in controllers.

### Writing new tests

1. Find the nearest existing test that's similar.
2. Copy it.
3. Change the fixture reference.
4. Change the assertions.
5. Run it.

This is the expected workflow. Tests should be boring and repetitive, not
clever. If a test requires novel infrastructure, that's a sign the harness
needs extending — fix the harness first.

## Repo layout

```
cmd/
  operator/                  # operator + API + embedded UI entrypoint
  cli/                       # CLI entrypoint
api/v1alpha1/                # CRD type definitions
controllers/                 # reconcile logic
  app_controller.go
  app_controller_test.go     # envtest beside controllers
  platformconfig_controller.go
  gitprovider_controller.go
  previewenvironment_controller.go
internal/
  auth/                      # AuthProvider interface + impls
  authz/                     # PolicyEngine interface + impl
  build/                     # BuildClient interface + BuildKit impl
  registry/                  # RegistryBackend interface + OCI impl
  ingress/                   # IngressProvider interface + impl
  git/                       # GitAPI interface (per-forge) + GitClient (shared)
  bindings/                  # credential resolution logic
  webhook/                   # git webhook receiver + HMAC verification
  api/                       # REST API handlers
test/
  integration/               # k3d-based integration tests
    suite_test.go
    git_source_test.go
    image_source_test.go
    bindings_test.go
  fixtures/                  # canonical App CRDs for tests
    image-basic.yaml
    image-postgres.yaml
    git-basic.yaml
  helpers/                   # test utilities
    namespace.go
    fixtures.go
    assertions.go
charts/
  mortise/                   # single Helm chart
ui/                          # SvelteKit app
Makefile
```

## CRD quick reference

```yaml
# Project — top-level grouping, cluster-scoped
apiVersion: mortise.dev/v1alpha1
kind: Project
metadata:
  name: my-saas
spec:
  description: "Customer-facing SaaS stack"
status:
  phase: Ready               # Pending | Ready | Terminating | Failed
  namespace: project-my-saas
  appCount: 3
```

```yaml
# App — the user-facing workload resource
# Apps live in their Project's namespace: metadata.namespace = project-{project-name}
apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: my-app
  namespace: project-my-saas
spec:
  source:
    type: git | image
    # git: repo, branch, path, watchPaths, build
    # image: image, pullSecretRef
  network:
    public: true             # false for backing services
  storage: [...]             # named volumes
  credentials: [...]         # backing-service credential declaration
  environments:               # named envs with replicas, resources, env, bindings
    - name: production
      bindings:
        - ref: my-db                    # same project (default)
        - ref: shared-cache
          project: infra                # cross-project binding
  preview: { ... }           # PR-driven preview config (git source only)
```

```yaml
# PlatformConfig — cluster-scoped, one per install
apiVersion: mortise.dev/v1alpha1
kind: PlatformConfig
metadata: { name: platform }
spec:
  domain: yourdomain.com
  dns: { provider: cloudflare, apiTokenSecret: ... }
  storage: { defaultStorageClass: ... }
```

```yaml
# GitProvider — cluster-scoped, one per git host
apiVersion: mortise.dev/v1alpha1
kind: GitProvider
metadata: { name: github-main }
spec:
  type: github | gitlab | gitea
  # credentials via secretRefs
```

## What Mortise creates per App

When an App is reconciled, the operator creates and manages:
- Namespace (one per App)
- Deployment (image, replicas, resources, env vars, volume mounts)
- Service (stable internal DNS for the pods)
- Ingress (domain routing, TLS annotations, ingress class)
- PVC(s) (one per storage entry)
- Secret(s) (projected from secret store for env vars)
- ServiceAccount + imagePullSecret (registry credentials)

All owned by the App via controller-runtime's `controllerutil.SetControllerReference`.
Deleting the App garbage-collects everything.

## Common mistakes to avoid

- Adding Traefik-specific IngressRoute CRDs instead of standard Ingress
- Importing third-party SDKs in controller files
- Creating new CRD kinds for things that should be Apps
- Using `time.Now()` instead of injected clock
- Writing integration tests that depend on test execution order
- Adding "helpful" abstractions or plug-in machinery
- Using `:latest` tags anywhere — always pin digests or specific versions
- Adding comments that describe WHAT instead of WHY
