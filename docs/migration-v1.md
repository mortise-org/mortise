# Migrating from v0.x to v1.0

This guide covers upgrading Mortise from any 0.1.x release to 1.0.x.

## Before you start

- Back up your cluster state: `kubectl get apps,projects,previewenvironments,gitproviders,platformconfigs -A -o yaml > mortise-backup.yaml`
- The upgrade **invalidates all user sessions** (JWT claim changes). All
  users will need to log in again.
- CRDs remain at `mortise.mortise.dev/v1alpha1`. No changes to existing
  resource `apiVersion` fields are needed.

## Upgrade steps

### 1. Update the Helm repo

```bash
helm repo update mortise
```

### 2. Review your values overrides

Check your values file against the changes below, then upgrade:

```bash
helm upgrade mortise mortise/mortise --version 1.0.1 -f values.yaml
```

The new `BuildRun` CRD is installed automatically. Existing App and
PreviewEnvironment resources will be reconciled with the new fields on
the next operator restart.

### 3. Re-authenticate

All sessions are invalidated. Every user must log in again:

```bash
mortise login
```

The CLI will automatically attempt token refresh on 401. If the refresh
window (7 days) has passed, a fresh login is required.

## Breaking changes

### Authentication

**JWT claims**: Tokens now include `iss: mortise` and `aud: mortise-api`.
Tokens issued by v0.x lack these claims and are rejected immediately.
There is no grace period — all sessions are invalidated on upgrade.

**Minimum password length**: User creation now enforces an 8-character
minimum. Existing users with shorter passwords can still log in but
cannot be re-created via the API.

**Password generation tracking**: User secrets now carry a `password_gen`
field (initialized to `"0"`). Changing a password increments this value
and invalidates all tokens issued before the change.

### SSE (server-sent events) authentication

Log streams and live event feeds no longer accept a JWT in the `?token=`
query parameter. Clients must:

1. `POST /api/auth/sse-token` with a Bearer JWT to obtain a short-lived
   opaque token (`msse_...`).
2. Pass that token as `?token=msse_...` on the SSE endpoint.

The UI and CLI handle this automatically. Custom integrations that
consume SSE streams directly must update their auth flow.

### API changes

| Change | Before (v0.x) | After (v1.0) |
|---|---|---|
| Delete app response | `200 {"status":"deleted"}` | `202 {"status":"terminating","app":"..."}` |
| Secrets query param | `?env=production` | `?environment=production` |
| Clone environment (duplicate) | `200` (idempotent) | `409 Conflict` |
| Internal errors | Raw error in response body | `"internal server error"` (details in server logs) |
| k8s Conflict errors | `500` | `409 Conflict` |

### CRD schema changes

**Moved fields** (App status):
- `status.pendingEnvHash` → `status.environments[].pendingEnvHash`
- `status.deployedEnvHash` → `status.environments[].deployedEnvHash`

Any tooling that reads these top-level status fields must update to read
from the per-environment status instead. The operator populates the new
location on first reconcile after upgrade.

**New validation on `EnvVar.name`**:
- Must match `^[a-zA-Z_][a-zA-Z0-9_]*$`
- Maximum 253 characters

Existing Apps with non-conforming env var names will pass initial
reconciliation but **fail validation on the next update**. Audit env var
names before upgrading if you have vars with dashes, dots, or leading
digits.

**New CRD**: `BuildRun` (namespaced). Installed automatically by Helm.
If you run OPA/Kyverno policies that allowlist CRD kinds, add
`buildruns.mortise.mortise.dev`.

### Helm values changes

**`github.clientID` default cleared**: v0.x shipped a hardcoded dev
OAuth client ID (`Ov23lizLTd25E32VrWwl`). v1.0 defaults to `""`. If you
use GitHub device-flow login and relied on the default, add your own
client ID to your values:

```yaml
mortise-core:
  github:
    clientID: "your-github-oauth-app-client-id"
```

**Security contexts now on by default**: The operator pod runs with:
- `seccompProfile: RuntimeDefault`
- `allowPrivilegeEscalation: false`
- `capabilities.drop: [ALL]`

The observer pod additionally runs with `readOnlyRootFilesystem: true`.
If your environment requires privileged pods, override
`podSecurityContext` and `containerSecurityContext` in your values.

### Webhook behavior

**Stricter repo matching**: Webhooks now require a full `owner/repo`
match. The v0.x suffix-based matching could produce false positives
(e.g., `org/app` matching `other-org/app`).

**Provider filtering**: Apps only respond to webhooks from their
configured `spec.source.git.providerRef`. In v0.x, any provider with a
matching repo URL could trigger a build. If you have multiple
GitProviders pointing at the same forge, verify that each app's
`providerRef` is set correctly.

**Preview reconciliation model**: Preview environments now converge
against the forge's open-PR state on every reconcile, not just on
webhook events. This is self-healing but means:
- Previews for PRs closed while the operator was down are cleaned up
  automatically.
- Previews for PRs opened while the operator was down are created
  automatically.
- The operator makes `ListOpenPullRequests` API calls to the forge
  during reconciliation. Ensure your git provider token has read access
  to pull requests.

**Preview environment rework (post-1.0.1)**: Preview environments are
now real clone environments (`pr-{number}`) managed through the app
controller, replacing the per-app `previewsync` model. This changes the
`PreviewEnvironment` CRD significantly:

- **Removed spec fields**: `appRef`, `replicas`, `resources`, `env`,
  `bindings`, `domain`, `ttl`. The spec now carries only `projectRef`,
  `sourceEnv`, and `pullRequest` metadata.
- **Removed status fields**: `url`, `image`, `currentBuildRunRef`,
  `lastSuccessfulBuildRunRef`, `expiresAt`. Status now has only
  `environmentName` and `conditions`.
- **Phase enum reduced**: `Pending|Building|Ready|Failed|Expired` →
  `Pending|Ready|Failed`.
- **PreviewConfig changes**: `domain`, `ttl`, `resources` fields
  removed; `sourceEnvironment` (optional string) added.

Tooling that reads PreviewEnvironment resources must be updated. Existing
PreviewEnvironment objects will be re-reconciled automatically on
upgrade; no manual cleanup is needed.

**ProjectEnvironment `preview` field (post-1.0.1)**: The
`ProjectEnvironment` struct now includes a `preview` boolean field.
Preview environments created by the operator have `preview: true`, which
allows the UI and API to distinguish them from user-created environments.
Tooling that lists or inspects `project.spec.environments` should handle
this new field.

**Environment deletion auto-strips overrides (post-1.0.1)**: Deleting a
project environment that has app-level overrides no longer returns a 409
error. The API now removes overrides from all apps referencing the deleted
environment and proceeds with the deletion.

**App Degraded phase (post-1.0.1)**: The `AppPhase` enum now includes
`Degraded`. This phase indicates a build failed but a previously-deployed
image is still serving. Tooling or dashboards that switch on `AppPhase`
must handle the new value — treat it as a warning state between `Ready`
and `CrashLooping`.

### CLI changes

**Confirmation prompts**: `mortise app delete` and `mortise secret delete`
now prompt for confirmation. Scripts that call these commands must pass
`--yes` or `-y` to skip the prompt.

**`secret set --value` deprecated**: Use stdin instead:
```bash
echo "my-value" | mortise secret set MY_KEY
```
The `--value` flag still works but prints a deprecation warning.

## New features to configure

These are optional but recommended.

### External domain

If your Mortise API is reachable at a different hostname than your app
domain (common with tunnels, NAT, or reverse proxies), set
`externalDomain`:

```yaml
platformConfig:
  externalDomain: mortise.example.com
```

Webhook callbacks and deploy token URLs use this hostname. Falls back to
`domain` when unset.

### Automatic TLS via ACME

The umbrella chart can now create a cert-manager ClusterIssuer
automatically:

```yaml
tls:
  acme:
    email: ops@example.com
    # issuerName: letsencrypt-prod    (default)
    # server: https://acme-v02.api.letsencrypt.org/directory  (default)
```

This requires `cert-manager.enabled: true` (the default in the umbrella
chart).

### Viewer role

v1.0 adds a `viewer` role that can read project resources but not modify
them. Assign via the members API or UI.

## Verifying the upgrade

After upgrading:

1. Confirm the operator is running:
   ```bash
   kubectl get pods -n mortise-system -l app.kubernetes.io/name=mortise
   ```

2. Check CRDs are updated (BuildRun should be present):
   ```bash
   kubectl get crd | grep mortise
   ```

3. Verify an existing app reconciles:
   ```bash
   kubectl get apps -A
   ```
   All apps should show `Ready` phase within a few minutes.

4. Log in and confirm UI access:
   ```bash
   mortise login
   ```

## Rolling back

If you need to roll back to v0.x:

```bash
helm rollback mortise
```

Note that `BuildRun` resources created by v1.0 will remain in the
cluster as orphans. Clean them up manually if needed:

```bash
kubectl delete buildruns -A --all
```

The `pendingEnvHash` / `deployedEnvHash` fields will revert to top-level
status on the next v0.x reconcile. No data is lost.
