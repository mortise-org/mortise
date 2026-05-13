# PR Review Checklist

Quick-reference for reviewing Mortise PRs. This is the reviewer's
counterpart to [CLAUDE.md](CLAUDE.md) (coding conventions) and
[SPEC.md](SPEC.md) (product spec). It does not duplicate them — it
tells you what to check and where to look.

## How to use this

Skim the section headings. Check every box that applies to the diff.
Any unchecked box that applies is a reason to request changes.

---

## 1. Architecture

- [ ] No third-party SDKs imported in `internal/controller/` files —
      only Mortise packages (`internal/`, `api/`) and stdlib/k8s
      (see CLAUDE.md "Controllers never import third-party SDKs")
- [ ] External calls go through interfaces in `internal/<name>/`
      (BuildClient, GitAPI, GitClient, IngressProvider, RegistryBackend)
- [ ] No Traefik-specific CRDs (IngressRoute, Middleware) — standard
      k8s Ingress only (see CLAUDE.md "Standards, not implementations")
- [ ] No new CRD kinds for things that should be Apps — backing
      services are Apps with `network.public: false`
- [ ] No addon/plug-in architecture, subcharts, or extension points
      (see SPEC.md §6.1)
- [ ] No new interface without an in-tree implementation behind it
- [ ] Resources only modified after ownership check — Mortise never
      touches resources it didn't create

## 2. Namespace & Naming

- [ ] Namespace prefixes use `internal/constants` helpers
      (`ControlNamespace()`, `EnvNamespace()`), never hardcoded strings
- [ ] No legacy `project-` prefix — current convention is `pj-`
- [ ] Workload resources (Deployment, Service, Ingress, Pod, PVC,
      env-scoped Secret/ConfigMap) placed in per-env namespace
      `pj-{project}-{env}`, NOT control namespace `pj-{project}`
- [ ] Project-scoped CRDs (App, PreviewEnvironment) and project-level
      resources (activity ConfigMap, registry creds) in control namespace
- [ ] `constants.ValidateProjectEnvLengths()` called when accepting
      new project or environment names (63-char DNS label limit)
- [ ] Label keys use defined constants (`mortise.dev/project`,
      `mortise.dev/environment`, `app.kubernetes.io/name`) — no inline
      string literals for label keys, finalizer names, or annotation keys

## 3. CRDs & API Types

- [ ] New fields have `kubebuilder:validation` markers (Required, Enum,
      Pattern, MinLength, MaxLength) where applicable
- [ ] `make manifests` regenerated if types changed — CRD YAML in
      `charts/mortise-core/crds/` matches Go types
- [ ] `make generate` run — `zz_generated.deepcopy.go` is up to date
- [ ] Status phase transitions well-defined (Pending/Ready/Terminating/
      Failed) — no new phases without justification
- [ ] Schema changes are additive and optional — do not break existing
      resources on upgrade

## 4. Controllers

- [ ] Reconcile functions are idempotent (safe to re-run at any time)
- [ ] Owner references set via `controllerutil.SetControllerReference`
      for garbage collection
- [ ] Finalizers added when cross-namespace cleanup is needed, removed
      only after cleanup completes
- [ ] Uses `clock.Clock` for time-dependent logic, never `time.Now()`
- [ ] Status conditions updated with meaningful reasons and messages
- [ ] No long-running operations in the reconcile loop — requeue with
      backoff instead
- [ ] Error handling distinguishes transient vs permanent failures
      (transient: requeue; permanent: set Failed status, don't retry)
- [ ] No overcomplicated reconcile logic — if a reconcile function is
      growing past a few hundred lines, it likely needs decomposition
      into helper methods, not abstraction layers

## 5. REST API

- [ ] Endpoints protected by auth middleware unless explicitly public
      (setup, login, health)
- [ ] Authorization checked via `PolicyEngine.Authorize()` — admin vs
      member vs viewer scoping correct for the operation
- [ ] Input validated before use (lengths, patterns, required fields)
- [ ] Errors return structured JSON via `writeError` helpers, not raw
      strings or leaked internal details
- [ ] New endpoints documented in SPEC.md API route table and swagger
      annotations updated
- [ ] No assumptions about request shape — validate, don't trust

## 6. Helm Charts

- [ ] `Chart.yaml` version/appVersion NOT manually edited — CI owns
      these (see RELEASING.md)
- [ ] New `values.yaml` keys have sensible defaults — existing
      installs must not break on upgrade
- [ ] Templates reference `.Values`, not hardcoded values
- [ ] Security contexts preserved (`podSecurityContext`,
      `containerSecurityContext` from values)
- [ ] No `:latest` image tags — pinned versions or digest references
- [ ] `make test-charts` passes (helm lint + template tests)
- [ ] CRD YAML in `charts/mortise-core/crds/` matches `config/crd/bases/`

## 7. UI

- [ ] `make check-ui` passes (svelte-check, no TypeScript diagnostics)
- [ ] No `page.route()` mocking of Mortise business logic in E2E — use
      real API via helpers (`loginViaAPI`, `createProjectViaAPI`, etc.)
- [ ] `getByRole` calls use `{ exact: true }` — without it, canvas
      AppNode divs silently match and tests time out
- [ ] Headings located via `getByRole('heading')`, not `getByText`
      (getByText does partial substring matching)
- [ ] Variable placeholders use correct names: `'VARIABLE_NAME'` for
      key, `'value or binding ref'` for value
- [ ] New interactive elements have E2E coverage

## 8. Tests

- [ ] **Happy AND sad paths covered** — every new code path needs tests
      for success, expected failures, edge cases, and error conditions.
      Golden-path-only coverage is insufficient.
- [ ] Unit tests beside the code they test (`_test.go` in same package)
- [ ] Integration tests in `test/integration/` with
      `//go:build integration` tag
- [ ] Test fixtures loaded from `test/fixtures/` via `LoadFixture()` —
      never hand-write App YAML in test code
- [ ] Integration tests create own namespace via `CreateTestNamespace(t)`
      with `t.Cleanup` — no shared state between tests
- [ ] Tests do not depend on execution order — each test stands alone
- [ ] Mock policy followed: mock interfaces in unit tests, real services
      (BuildKit, Gitea, Pebble, registry) in integration tests
- [ ] `make test` passes (<10s), `make test-charts` passes (<30s)
- [ ] Error messages in test assertions are descriptive — when a test
      fails, the message should make the failure obvious without reading
      the test source

## 9. Code Quality

- [ ] Comments explain WHY, not WHAT — if the code is clear, no
      comment needed
- [ ] No unrelated formatting changes, refactors, or cleanups mixed in
- [ ] No overcomplicated code — if 200 lines could be 50, it needs
      rewriting. Simple beats clever.
- [ ] No abstraction for the sake of abstraction — single-use code
      does not need a helper function. Three similar lines is better
      than a premature abstraction.
- [ ] No unjustified assumptions — if the change assumes something
      about system behavior, state it explicitly or validate it. Don't
      silently pick one interpretation when multiple exist.
- [ ] Constants used where available — `internal/constants` provides
      helpers for namespace prefixes, label keys, finalizer names, and
      naming conventions. Use them.
- [ ] Imports cleaned up — no unused imports left by the change
- [ ] Every changed line traces directly to the task — no drive-by
      improvements
- [ ] CI checks pass: `make test`, `make test-charts`, `go vet`,
      `staticcheck`, `svelte-check`
- [ ] PR template checklist is filled out honestly

## 10. Security

- [ ] Webhook payloads verified via HMAC before processing
- [ ] No secrets, credentials, or tokens logged or returned in API
      responses
- [ ] RBAC (ClusterRole) changes are minimal — new verbs or resources
      need explicit justification in the PR description
- [ ] Admission webhooks validate user input at the k8s API boundary
- [ ] Bearer tokens validated before any protected operation
- [ ] No hardcoded secrets, passwords, or API keys anywhere in code,
      charts, or test fixtures

---

## Red Flags

Any one of these blocks the PR. These are bugs, not style preferences.

1. Third-party SDK imported in `internal/controller/`
2. Traefik-specific IngressRoute instead of standard k8s Ingress
3. New CRD kind for something that should be an App
4. `time.Now()` in controller code instead of injected `clock.Clock`
5. `:latest` image tag anywhere in code or charts
6. Hardcoded `project-` or `pj-` prefix instead of `constants` helpers
7. Workload resources written to control namespace `pj-{project}`
8. `page.route()` mocking Mortise business logic in E2E tests
9. `getByRole` without `{ exact: true }` in Playwright tests
10. Resources modified without ownership check
11. Plugin/addon architecture (subcharts, extension registry, plug-in SDK)
12. Manual `Chart.yaml` version/appVersion edits
13. Tests covering only happy path with no error/edge-case coverage
14. Inline string literals for values that exist in `internal/constants`
15. Unjustified assumptions — silently choosing one interpretation
    without stating alternatives
