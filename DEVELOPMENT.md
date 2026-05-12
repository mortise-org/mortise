# Development Guide

How to run Mortise locally against a real Kubernetes cluster, and how the
test suite works.

---

## Prerequisites

Install these once (macOS / Linux):

```bash
# Required
brew install go node docker kubebuilder k3d kubectl helm

# Or on Linux, use your distro package manager for each.
# - Go 1.25+
# - Node 22+
# - Docker Desktop or Docker Engine
# - kubebuilder (project scaffolding, test assets)
# - k3d (k3s in Docker)
# - kubectl
# - helm 3
```

Make sure Docker is running before touching anything with k3d.

---

## One-command local stack

The Makefile wraps the whole local loop. From the repo root:

```bash
make dev-up       # create k3d cluster, build image, install Mortise via Helm
```

This takes ~1-2 minutes cold. After it finishes you'll see:

```
✓ Mortise is running!
  API: kubectl port-forward -n mortise-system svc/mortise 8090:80
  Then open http://localhost:8090
```

Run the port-forward in a second terminal (or background it) and open the
URL in a browser:

```bash
kubectl port-forward -n mortise-system svc/mortise 8090:80
# then open http://localhost:8090
```

First-run: if the setup UI is available, create an admin. Otherwise use
curl:

```bash
curl -X POST http://localhost:8090/api/auth/setup \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@local","password":"admin123"}'
```

### Iterating on code

```bash
make dev-reload   # rebuild image, load into k3d, restart Mortise pod
```

About 45 seconds per reload.

### Teardown

```bash
make dev-down     # deletes the k3d cluster entirely (containers, volumes)
```

### Cluster name / image tag overrides

```bash
DEV_CLUSTER=mortise-test DEV_IMG=mortise:pr123 make dev-up
```

---

## Test suite

Three layers, all runnable from the repo root:

### Unit + envtest (fast, default for every change)

```bash
make test
```

- Runs `go vet`, `go fmt`, `controller-gen`, and the Go test suite
- Uses `envtest` (real `kube-apiserver` + `etcd` binaries downloaded to
  `bin/k8s/`, no kubelet, no pods actually run)
- ~7 seconds wall clock
- Currently covers: controller reconciliation, API HTTP handlers, auth
  (JWT + bcrypt), CLI command parsing, binding resolution, deploy
  history, storage reconciliation

### Integration tests (real cluster)

```bash
make test-integration       # ephemeral k3d cluster: create, install, test, tear down
make test-integration-fast  # run suite against existing dev cluster (requires make dev-up)
```

- `make test-integration` creates a dedicated k3d cluster (`mortise-int`),
  builds and loads the operator image, installs CRDs and test dependencies
  (Distribution registry, Gitea, BuildKit), installs the Helm chart, runs the suite, and
  tears down.
- `make test-integration-fast` runs the same Go tests against whatever
  cluster your kubeconfig points at (typically the `make dev-up` cluster).
- Tests live in `test/integration/`: currently 19 `_test.go` files
  (including `suite_test.go`). Coverage spans app image/git sources,
  config files, cron jobs, external/full-stack/multi-service apps,
  bindings, domains, environment cloning, previews + preview webhooks,
  storage, ingress, observer, git provider admin flows, and overall
  project lifecycle behavior.
- `TestMain` in `suite_test.go` asserts the cluster is reachable, the
  Mortise Deployment is available, and the observer is healthy (soft
  check — observer tests skip if unavailable) before any test runs.
- Each test creates its own namespace via `createTestNamespace(t)` and
  cleans up via `t.Cleanup`.
- Integration tests enable Traefik (in `mortise-deps`), metrics-server,
  and the observer with a 5s poll interval for fast data collection.

### UI E2E tests (Playwright)

```bash
make test-e2e               # requires make dev-up: handles port-forward, admin bootstrap, and teardown
```

- Requires `make dev-up` to have been run first (a running dev cluster).
- The target automatically starts a port-forward, waits for the API,
  bootstraps the admin account, runs the Playwright suite, and cleans up.
- Override defaults: `E2E_PORT=9090 E2E_EMAIL=me@local E2E_PASSWORD=secret make test-e2e`
- Tests live in `ui/tests/e2e/`. Config at `ui/playwright.config.ts`.
- Runs in Chromium only, single worker, serial execution.
- **All tests hit the real API**: no mocking of business logic. This
  verifies the full UI → API integration path. Each test creates its own
  projects/apps and cleans up after itself.
- Current snapshot: 20 Playwright spec files with roughly 152
  `test(...)` cases. Exact counts change frequently, so treat this as a
  moving inventory rather than a fixed contract.
- The suite now splits app-detail coverage across focused specs such as
  `app-settings-sections.spec.ts`, `app-variables-full.spec.ts`,
  `deployments.spec.ts`, `domains.spec.ts`, `volumes.spec.ts`, and
  `staged-changes-deploy.spec.ts`; `app-detail.spec.ts` no longer exists.
- The remaining spec layout is broad rather than monolithic:
  `auth.spec.ts`, `navigation.spec.ts`, `projects.spec.ts`,
  `project-settings.spec.ts`, `project-members-and-envs.spec.ts`,
  `admin-settings.spec.ts`, `platform-settings-actions.spec.ts`,
  `apps.spec.ts`, `build-and-deploy.spec.ts`, `bindings.spec.ts`,
  `deploy-tokens.spec.ts`, `environments.spec.ts`,
  `git-providers.spec.ts`, and `journey.spec.ts`, plus the focused app
  management specs above.
- `journey.spec.ts` still provides the chained login → deploy → manage →
  delete flows that are most valuable for catching cross-page regressions.

### Live cluster (manual smoke test)

After `make dev-up`, port-forward and drive the UI / CLI / API by hand
against a real cluster. This catches integration issues envtest can't
(kubelet behavior, image pulls, Ingress controller wiring, real pod
scheduling).

---

## Repository layout

```
api/v1alpha1/                 Go types for CRDs (generated + hand-written)
cmd/
  main.go                     Operator + API server entrypoint
  cli/                        Mortise CLI (cobra)
internal/
  api/                        REST HTTP handlers, SSE log streaming, auth middleware
  auth/                       NativeAuthProvider, JWT helper
  authz/                      NativePolicyEngine (admin/member roles)
  bindings/                   Credential resolution for App-to-App bindings
  build/                      BuildClient interface (BuildKit impl)
  controller/                 Reconcilers (Project, App, PlatformConfig, GitProvider, PreviewEnvironment)
  git/                        GitAPI + GitClient interfaces (GitHub/GitLab/Gitea)
  ingress/                    IngressProvider interface (annotation-driven)
  registry/                   RegistryBackend interface (generic OCI)
  ui/                         SvelteKit static files embedded via //go:embed
charts/mortise/               Batteries-included umbrella chart (operator + Traefik + cert-manager + BuildKit + registry)
charts/mortise-core/          Operator-only chart (CRDs, RBAC, Deployment, Service)
test/
  fixtures/                   Canonical App CRDs for tests
  helpers/                    Test utilities (envtest bootstrap, assertions)
ui/                           SvelteKit source (built to ui/build, copied to internal/ui/build)
```

---

## Common tasks

### Regenerate CRD manifests after editing api/v1alpha1/*.go

```bash
make manifests       # updates config/crd/bases/*.yaml
make generate        # updates api/v1alpha1/zz_generated.deepcopy.go
```

### Build the operator binary locally

```bash
make build           # go build -o bin/manager cmd/main.go
```

### Build the CLI binary

```bash
make build-cli       # go build -o bin/mortise ./cmd/cli
```

### Build only the UI

```bash
make build-ui        # cd ui && npm install && npm run build
                     # then copies ui/build → internal/ui/build for embedding
```

### Run the operator against a remote cluster (without Helm)

```bash
# Assumes kubeconfig points at your target cluster
make run             # go run ./cmd/main.go
```

Useful when iterating on reconciler logic and you don't want to rebuild
the Docker image each time.

---

## Troubleshooting

### `make dev-up` fails with "Cannot connect to the Docker daemon"

Docker isn't running. Open Docker Desktop.

### `make dev-up` fails building the Docker image with "pattern all:build: no matching files found"

The UI hasn't been built yet. Run `make build-ui` first, then `make dev-up`.

### Pod stuck in `InvalidImageName`

The Helm chart reference got mangled. Check:

```bash
kubectl -n mortise-system get deployment mortise -o yaml | grep image:
```

Should read `image: mortise:dev`, not `mortise:dev:` with a trailing colon.

### Port 8090 already in use

Another port-forward is still alive.

```bash
pkill -f "port-forward.*mortise" || true
```

### CRDs out of sync after editing types

```bash
make manifests && make generate
# then restart the operator:
make dev-reload
```

### Reset everything and start fresh

```bash
make dev-down
docker system prune -f         # optional: frees disk
make dev-up
```

---

## Running tests in CI-like mode

CI runs `make test`, `make test-charts`, Go vet/staticcheck, and a UI
typecheck on every PR (`.github/workflows/ci.yml`). You can replicate
the CI environment locally:

```bash
# From a clean shell, no env overrides:
make test
```

If that passes, your change is likely CI-clean.

---

## Helpful one-liners

```bash
# Watch pods across all Mortise-managed namespaces
kubectl get pods -A -l app.kubernetes.io/managed-by=mortise -w

# Stream operator logs
kubectl -n mortise-system logs -l app.kubernetes.io/name=mortise -f --tail=50

# Create an App in the default project via the API (admin already set up)
TOKEN=$(curl -s -X POST http://localhost:8090/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@local","password":"admin123"}' | jq -r .token)

# List projects (default should exist)
curl -s http://localhost:8090/api/projects -H "Authorization: Bearer $TOKEN" | jq

# Create an app inside the default project
curl -X POST http://localhost:8090/api/projects/default/apps \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"nginx-test","spec":{"source":{"type":"image","image":"nginx:1.27"},"network":{"public":false},"environments":[{"name":"production","replicas":1,"resources":{"cpu":"50m","memory":"64Mi"}}]}}'

# Inspect the created k8s resources. The App lives in the control ns
# (pj-default); the Deployment/Service/Ingress live in the per-env ns
# (pj-default-production).
kubectl get app -n pj-default
kubectl get deployment,service,ingress -n pj-default-production

# Create a second project
curl -X POST http://localhost:8090/api/projects \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"staging","spec":{"description":"Staging environment"}}'

# Tear down a whole project (cascades to everything inside)
curl -X DELETE http://localhost:8090/api/projects/staging \
  -H "Authorization: Bearer $TOKEN"
```
