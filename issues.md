# Review findings

Issues found reviewing commits `cab9df7` through `4f090ae` (current HEAD).
Reviewed 2026-04-21. Covers the original 10 commits plus 28 new commits on
origin/main.

Severity key: **CRITICAL** = blocks install or causes data loss/corruption;
**HIGH** = production breakage or security hole; **MEDIUM** = silent wrong
behavior; **LOW** = testability, style, latent footgun.

---

## CRITICAL

### C1. Integration tests do not compile (3 errors)

`go test -tags integration -list '.*' ./test/integration/` fails:

- `test/integration/bindings_test.go:74` — `corev1` undefined (missing import).
- `test/integration/preview_test.go:379` — `previewNs` undefined (never declared).
- `test/integration/app_external_test.go:7` — `"fmt"` imported but not used.

The entire integration suite is broken. No integration test has run since these
files were added.

---

### C2. Ingress backend port hardcoded to 80; Service listens on 8080

`internal/controller/app_controller.go:1007`:

```go
Port: networkingv1.ServiceBackendPort{Number: 80},
```

`reconcileService` sets the Service port to `appPort(app)` which defaults to
8080. Ingress routes to port 80 on the Service, Service listens on 8080 →
**every app 503s in production**.

**Fix:** `Port: networkingv1.ServiceBackendPort{Number: int32(appPort(app))}`.

---

### C3. Helm template references deleted `traefik.enabled` value

`charts/mortise/templates/deployment.yaml:62`:

```
{{- if and .Values.traefik.enabled (not .Values.ingress.className) }}
```

Commit `4f090ae` removed the `traefik` key from `values.yaml` but left this
reference. `helm template` will nil-pointer on any install that reaches this
branch.

**Fix:** Remove the `traefik.enabled` check or add a default.

---

### C4. Cross-project SecretKeyRef resolves in binder's namespace, not target's — **RESOLVED (feature removed)**

Cross-project bindings have been removed entirely. The `Binding.Project` field
no longer exists. Credential resolution now happens inside the resolver using
the same project's env namespace, eliminating the cross-namespace problem.

---

## HIGH

### H1. `generateRandomHex` silently swallows `rand.Read` errors (still unfixed)

`internal/api/stacks.go:246-249`:

```go
func generateRandomHex(n int) string {
    b := make([]byte, n)
    _, _ = rand.Read(b)
    return hex.EncodeToString(b)
}
```

On failure, `b` is all-zeros → all template secrets (`PG_PASSWORD`,
`JWT_SECRET`) become `"00000000000000000000000000000000"`.
`.pending-tests/stacks_rng_test.go.txt` documents this but was never promoted.

---

### H2. `getOrCreateGitProvider` hardcodes GitHub type (still unfixed)

`internal/api/device_flow.go:354-355` unconditionally sets
`Type: GitProviderTypeGitHub` and `Host: "https://github.com"`.
`storePATRequest.Host` is defined but never read. GitLab/Gitea users get a
broken GitHub-typed provider.

---

### H3. Preview environments with bindings fail at pod startup

`internal/controller/previewenvironment_controller.go:348-358` passes
binding-resolved `SecretKeyRef` env vars directly into the Deployment container
spec. The kubelet resolves those Secrets in the preview namespace
(`pj-{project}-pr-{N}`), but the credentials Secret lives in a regular env
namespace. Pods fail with `CreateContainerConfigError`.

---

### H4. `DeleteSecret` does not verify the secret belongs to the URL's `{app}`

`internal/api/secrets.go:106-136` extracts `secretName` from the URL and
`projectName` from `resolveProject`, but never checks the secret's
`constants.AppNameLabel` matches the `{app}` URL param. The `appName` param is
never even read. Any authenticated user can delete any mortise-managed secret
in the same project.

---

### H5. `PutSharedVars` doesn't trigger reconcile — shared vars never propagate

`internal/api/env.go:252-281` writes to the control-namespace `shared-vars`
Secret and returns 200, but never calls `pokeAppForReconcile`. Shared vars
won't propagate to any environment until the next unrelated reconcile event.

---

### H6. Supabase template is non-functional out of the box

`internal/templates/supabase/docker-compose.yml:50-51`:

```yaml
ANON_KEY: ${JWT_SECRET}
SERVICE_KEY: ${JWT_SECRET}
```

But the studio service expects separate `${ANON_KEY}` and
`${SERVICE_ROLE_KEY}` (lines 88-89). Since `substituteVars` auto-generates
unresolved variables, `ANON_KEY` and `SERVICE_ROLE_KEY` become random hex
strings unrelated to the JWT secret. Studio can't authenticate.

Supabase anon/service keys should be JWTs signed with the JWT_SECRET, not
random hex strings.

---

### H7. Hardcoded GitHub OAuth Client ID in chart values

`charts/mortise/values.yaml:39`:

```yaml
github:
  clientID: "Ov23lizLTd25E32VrWwl"
```

This is the developer's personal OAuth app client ID. Every installation uses
the same client ID. OAuth callbacks go to whatever redirect URI the dev
configured. If the dev revokes the app, every installation breaks.

---

### H8. Registry and BuildKit use `emptyDir` — all images lost on pod restart

`scripts/install.sh:259-260` and `scripts/install.sh:345-346` both use
`emptyDir: {}` for storage. A single pod restart loses every built image. All
deployed apps referencing those images become un-reschedulable.

---

### H9. Env var race condition: read-modify-write without optimistic concurrency

`internal/envstore/envstore.go:120-142` (Merge) and `internal/api/env.go:105-176`
(PatchEnv) do Get → modify → Set where Set internally does another Get → Update.
The resourceVersion used for the update comes from the inner Get, not the
outer. Two concurrent PatchEnv calls can both read the same state and one
silently drops the other's changes.

---

### H10. Comma in env var name corrupts source-tracking annotations

`internal/envstore/envstore.go` stores source categories in comma-separated
annotations (`mortise.dev/binding-keys=A,B`). A key named `"A,B"` stored as a
binding key is parsed back as two separate keys `A` and `B`, corrupting source
tracking for all future reads.

No validation on env var names exists in the API layer or envstore.

---

### H11. `curl | sh` installs with no signature verification

`scripts/install.sh` has 3 separate `curl | sh` invocations for k3s (with
`sudo`), k3d, and Helm — no checksums, no GPG signatures. The k3s one runs as
root.

---

### H12. `mortise install` executes `./scripts/install.sh` from CWD

`cmd/cli/lifecycle.go:136` searches for `scripts/install.sh` relative to CWD.
Running `mortise install` from a directory containing a malicious
`scripts/install.sh` executes that file with the user's privileges.

---

## MEDIUM

### M1. Empty services filter returns `201 Created` with zero apps (still unfixed)

`internal/api/stacks.go:84-99` — when `req.Services` filters out every
compose service, handler returns `201 Created` with `{"apps":[]}`.

---

### M2. Single-env projects silently skip preview webhooks (still unfixed)

`internal/webhook/handler.go:260-263` — `projectHasStagingEnv` requires a
literal `staging` environment. Projects with only `production` get PR
webhooks silently dropped. No status condition, no event, no admission
validation.

---

### M3. Per-env phase/status logic still has no test coverage

`updateStatus` and `checkPodCrashLoopInEnv` rewritten in 6f5b6ed with no
tests. `app.Status.Environments[i].Phase` and `.Message` are not asserted in
any test.

---

### M4. Preview environment hardcodes port 80/8080, ignores `network.port`

`internal/controller/previewenvironment_controller.go:430-431,462` — preview
Service uses `Port: 80, TargetPort: 8080` regardless of the parent App's
`network.port` setting.

---

### M5. `autoDefaultDomain` doesn't validate DNS label characters

`internal/controller/app_controller.go:1961-1982` — only checks length (63
chars). Apps named `my_app` produce `my_app.example.com` (underscores are
invalid DNS labels). Dots, leading digits, and hyphens at start/end are also
not caught.

---

### M6. `seeded` check can't distinguish "no Secret" from "user cleared all vars"

`internal/controller/app_controller.go:1372` — `seeded := len(existing) > 0`.
Both "Secret doesn't exist" and "Secret exists but empty" produce `len == 0`.
Users cannot clear all env vars; the controller re-seeds from the CRD spec.

---

### M7. `mergeAnnotations` is additive-only — stale source annotations survive

`internal/envstore/envstore.go:272-273` — on update, data is fully replaced
but annotations only add/overwrite, never delete. Source-tracking annotations
(e.g., `mortise.dev/binding-keys`) persist even after the binding is removed.

---

### M8. `PutEnv` full-replace wipes binding/generated vars temporarily

`internal/api/env.go:60-101` — `PutEnv` replaces all vars with `Source: "user"`.
Binding-injected vars are gone until the next reconcile re-merges them. Pods
rolling during the window have missing credentials.

---

### M9. Inconsistent `?env=` vs `?environment=` query parameter naming

Some handlers use `?env=` (logs, pods, secrets, proxy, exec) while others use
`?environment=` (env.go, rebuild.go). Both default to "production" when
absent. A client sending `?environment=staging` to the pods endpoint gets
production pods.

---

### M10. `autoURL` hardcodes `postgres` user/database and `root` for MySQL

`internal/bindings/resolver.go:119` — `postgres://postgres@...:5432/postgres`.
If actual credentials use a different user/database, the auto-generated
`_URL` is wrong.

---

### M11. `toEnvPrefix` doesn't handle dots or leading digits in app names

`internal/bindings/resolver.go:109-111` — only replaces hyphens with
underscores. App named `my.database` produces `MY.DATABASE_HOST` which is not
a valid POSIX env var name.

---

### M12. Raw mode in VariablesTab says "replaces" but actually merges

`ui/src/lib/components/drawer/VariablesTab.svelte:321` — label says "Save
replaces all variables" but `importRaw()` only merges parsed keys into existing
entries. Deleted lines persist after save.

---

### M13. Binding edges race condition on env switch

`ui/src/lib/components/ProjectCanvas.svelte:35-43` — `$effect` fires
unguarded `api.listBindings()` on each env change. Rapid switching causes
stale responses to overwrite correct edges.

---

### M14. PowerShell installer passes stale subchart flags

`scripts/install.ps1:341-346` — passes `--set traefik.enabled=false`,
`cert-manager.enabled=false`, `external-dns.enabled=false` which were removed
from the chart. Bash script was updated; PS1 was not.

---

### M15. Proxy `handleConnect` TOCTOU race leaks listeners

`internal/api/proxy.go:58-117` — existence check releases the lock, then
re-acquires to store the listener. Two concurrent calls for the same app both
allocate listeners; the first is orphaned.

---

### M16. `parseDotEnv` doesn't handle escape sequences or multiline values

`internal/api/env.go:343-363` — `\n`, `\"`, `\t` inside quoted values are
kept literal. RSA private keys and multiline JSON in `.env` files are
corrupted.

---

### M17. `reconcileExternalNameService` sets `ClusterIP = ""` on update

`internal/controller/app_controller.go:2313` — transitioning from ClusterIP
to ExternalName requires delete+recreate. Setting `ClusterIP: ""` on an
existing ClusterIP Service is rejected by the API server.

---

### M18. Kubeconfig permissions block non-root users after k3s install

`scripts/install.sh:118-119` — `sudo chmod 600 /etc/rancher/k3s/k3s.yaml`
keeps root ownership. The current user cannot read it; all subsequent
kubectl commands fail.

---

### M19. `mortise up` doesn't verify cluster is actually running

`cmd/cli/lifecycle.go:28-35` — checks if `"mortise"` string appears in k3d
output but doesn't check the cluster status. Stopped clusters match.

---

### M20. `mortise destroy` fallback only deletes one namespace

`cmd/cli/lifecycle.go:76-79` — if k3d is not installed, fallback only deletes
`mortise-system`. Leaves behind `mortise-deps`, all `pj-*` namespaces, CRDs,
cluster-scoped resources.

---

### M21. JWT token in URL query parameters for SSE logs

`ui/src/lib/api.ts:227` — `logsURL()` appends `?token=<JWT>`. Gets logged in
browser history, server access logs, proxy logs. Standard credential-in-URL
anti-pattern.

---

### M22. No duplicate key prevention in env var UI

`ui/src/lib/components/drawer/VariablesTab.svelte:164-179` — `addVar()`
doesn't check for existing keys. Duplicates are silently deduplicated when
building the save payload (last wins).

---

## LOW

### L1. `.pending-tests/` still contains all 4 stashed drafts

None promoted, none deleted, none of the underlying bugs fixed.

---

### L2. Stale comment in `internal/build/buildkit.go:329`

Says "Context is always the repo root". No longer true since
`resolveDockerfileContext` and `ContextMode`.

---

### L3. PROGRESS.md drift

Multiple entries describe pre-refactor state. Not updated after per-env
namespace refactor or the 28 new commits.

---

### L4. `time.Now()` used directly in API handlers

`internal/api/rebuild.go:87`, `internal/api/env.go:338` — writes annotations
consumed by the controller. Makes those paths untestable with fake clocks.

---

### L5. `ensureEnvironment` is dead code

`internal/api/env.go:318-326` — defined and tested but never called.

---

### L6. `envNamespace` silently ignores `ProjectFromControlNs` failure

`internal/api/env.go:284-287` — discards `ok` bool. Returns `pj--{env}` if
the app is not in a control namespace.

---

### L7. Preview controller uses `time.Until` instead of injected clock

`internal/controller/previewenvironment_controller.go:217` — TTL requeue
duration calculated with wall clock, not `r.clock()`.

---

### L8. `context.Background()` in `resolveClusterIssuer`

`internal/ingress/annotation_provider.go:74` — ignores caller's
cancellation. Practically harmless (cached informer read) but architecturally
wrong.

---

### L9. `.gitignore` bare `cli` entry matches at any depth

`.gitignore:54` — should be `/cli` to scope to repo root only.

---

### L10. Operator requires restart to pick up PlatformConfig changes

`scripts/install.sh:455` — script explicitly restarts the operator after
PlatformConfig creation. A controller-runtime operator should watch for CRD
changes.

---

### L11. Dead code in preview controller: always-true short-circuit

`internal/controller/previewenvironment_controller.go:242` —
`pe.Status.Image == pe.Status.Image` is always true. Unfinished code.

---

### L12. `autoURL` doesn't recognize registry-prefixed images

`internal/bindings/resolver.go:117-128` — `docker.io/library/postgres:16` or
`ghcr.io/supabase/postgres:15` don't match `strings.HasPrefix`.

---

### L13. Polling interval in project page never cleaned up on unmount

`ui/src/routes/projects/[project]/+page.svelte:74-125` — `setInterval`
continues after navigation.

---

### L14. Hardcoded hex email in integration tests

`test/integration/preview_test.go:53,297` — uses hardcoded
`74657374406578616d706c652e636f6d` instead of `hex.EncodeToString([]byte(testEmail))`.

---

## Recommended priority order

1. **C1** — Fix integration test compilation (3 trivial fixes). Unblocks all integration testing.
2. **C2** — Fix Ingress backend port (one-line). Unblocks all app routing.
3. **C3** — Fix Helm template nil reference (one-line). Unblocks chart install.
4. **C4** — Fix SecretKeyRef resolution for cross-project bindings + add test.
5. **H3** — Fix preview env binding resolution (same root cause as C4).
6. **H4** — Add app-scoping check to `DeleteSecret`.
7. **H1** — Fix `generateRandomHex` error propagation, promote pending tests.
8. **H5** — Add `pokeAppForReconcile` to `PutSharedVars`.
9. **H6** — Fix Supabase template to use proper JWT-derived keys.
10. **H9/H10** — Add optimistic concurrency to envstore; validate var names.
11. **M1-M22** — Work through mediums by area.
12. **L1-L14** — Address lows opportunistically.
