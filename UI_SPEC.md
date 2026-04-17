# Mortise UI Spec

> Status: **draft scaffold**, generated 2026-04-17 from a docs.railway.com
> crawl cross-referenced against `SPEC.md`. Each flow is a stub — we fill
> in the **Screens** and **States** sections as screenshots arrive.
>
> This file is the source of truth for UI intent. Svelte route layout
> follows from this; divergences from this spec should update this file
> first, then the code.

---

## 0. How this document is structured

Each flow gets its own section using a consistent template:

```
### <Flow name>
**Goal.** What the user is trying to accomplish.
**Entry points.** Where this flow starts from (URL, button, shortcut).
**Screens.** Ordered list of screens with: purpose, key UI elements, states.
**States.** Empty / loading / error / success variants worth naming.
**Keyboard / command palette.** Shortcuts that enter or advance the flow.
**Mortise divergence.** Where we differ from Railway and why. See §12 for the full catalog.
```

Railway reference screenshots live in
`docs/ui-spec/screenshots/railway/*.png` (flat, one per screen).
Mortise-implementation screenshots (once built) live in
`docs/ui-spec/screenshots/mortise/<flow-slug>/NN-short-name.png`.

---

## 0a. Screenshot inventory (Railway reference, 2026-04-17)

The 16 captures in `docs/ui-spec/screenshots/railway/` pin the observed
flows. Each entry lists: file → the flow / divergence it informs.

| File | Anchor | What it pins |
|------|--------|--------------|
| `dashboard.png` | §3.3, §12.1 | Project list with workspace sidebar (Projects / Templates / Usage / People / Settings). Trial banner. Project cards show env + services-online count. |
| `project-add.png` | §3.6, §12.26 | **Single-modal create picker.** "What would you like to create?" → GitHub Repository, Database, Template, Docker Image, Function, Bucket, Volume, Empty Service. |
| `canvas.png` | §3.5, §12.4 | **Canvas as primary.** Staged-changes bar top-center, "+ Add" top-right, left rail (canvas / metrics / docs / settings), zoom+layers controls bottom-left. Volume rendered inside App tile; arrow edge = binding. |
| `project-app-select-deployments.png` | §3.7, §12.21 | **App detail is a right-side drawer** over the canvas. Tabs: Deployments / Variables / Metrics / Settings. Deploy row: Active chip, author avatar + trigger ("Appify, 29 min ago via GitHub"), "View logs" button, region + replica badges. |
| `project-app-slect-settings.png` | §3.7 | Drawer Settings tab. Filter Settings input. Right-side anchor nav (Source / Networking / Scale / Build / Deploy / Config-as-code / Feature-flags / Danger). Source block: Disconnect button, Add Root Directory link. |
| `variables.png` | §3.8 | Variables tab in drawer. New-var row: `VARIABLE_NAME | [Add Reference] | VALUE or ${{REF}} | Add | Cancel`. "Trying to connect a database? Add Variable" info banner. **"Suggested Variables" list from source-code scan.** |
| `variable-reference.png` | §3.9, §12.3 | Reference picker dropdown. Filter: "Database, Bucket or Shared Variables". Rows: `DATABASE_URL | Postgres`, `PGHOST | Postgres`, etc. "Show N More" expand. |
| `change-details.png` | §3.8, §12.2 | **"N change to apply" modal.** (Railway has a commit-message field here — Mortise strips it, no git-ops commit of the k8s spec.) Per-app group (`reddit-reply will be updated`), Change / Current Value / New Value columns, per-row Discard. "reddit-reply will redeploy" warning. "Deploy Changes" primary. |
| `activity.png` | §12.22 | **Activity right-rail panel.** Events: "Deployment successful", "Deploy PostgreSQL — 1 service and 1 volume updated", "New environment". Author avatar + timestamp. |
| `settings.png` | §3.17 | Project Settings sub-nav: General / Usage / Environments / Shared Variables / Webhooks / Members / Tokens / Integrations / Danger. General tab: Name, Description, Project ID (copy), Update button, Visibility section. |
| `settings-environment.png` | §3.14, §3.17 | Environments tab. "+ New Environment" button. **"PR Environments" as separate section with "Enable PR Environments" toggle** (i.e. preview envs are opt-in per project). |
| `settings-shared-variables.png` | §3.8 | Shared variables tab — lists env-scoped shared vars (collapsible per env). Copy "Variables that can be referenced by multiple services within an environment." |
| `settings-members.png` | §3.17, §12.24 | Members tab. **Workspace banner:** "All members of {workspace} can access this project." Invite form: Email + Permission (Can Edit) + Invite. **"Copy invite link" affordance.** Project members list (empty state). |
| `settings-usage.png` | §12.25 | Current Usage + Estimated Usage tables (Memory/CPU/Network/Volume → $). "View Cost by Service" link. Railway-specific (billing). |
| `settings-danger.png` | §3.17 | Danger zone. "Manage Services" with per-app Remove. "Delete Project" destructive button. Explicit warning copy. |
| `user-dropdown.png` | §3.17, §12.1 | User menu: Account Settings / Workspace Settings / Project Usage. **Workspace switcher inside the dropdown.** Docs / Support / theme / Log out. |

---

## 1. Terminology map — Railway → Mortise

Railway and Mortise don't use the same words for the same things. This
table is the canonical translation. UI copy must use the **Mortise**
column.

| Railway term           | Mortise term                 | Notes |
|------------------------|------------------------------|-------|
| Workspace              | *(v1: none; v2: Team)*       | Mortise v1 is single-workspace. Hide org chrome. See §12.1. |
| Project                | Project                      | Same. Mortise enforces one k8s namespace per Project. |
| Service                | App                          | Everything the user deploys is an **App**. |
| Plugin / Database      | App with `credentials:`      | Postgres, Redis, etc. are just Apps with `network.public: false` + a credentials block. See §12.11. |
| Deployment             | Deploy / deploy history      | Same concept. |
| Environment            | Environment                  | Same. `production`, `staging`, PR previews. |
| Reference variable `${{X.Y}}` | Binding / fromBinding | **Different model**: Mortise uses an explicit bindings contract, not template substitution. See §12.3. |
| Shared variable        | `sharedVars`                 | Scope is per-App (across all its environments), not project-wide. See §12.3. |
| Sealed variable        | Secret                       | Mortise secrets are sealed (write-only) by default. See §12.15. |
| Volume                 | Storage                      | CRD field is `spec.storage[]`. UI word: "Volume". |
| Template / marketplace | Template (curated, small)    | Mortise has no marketplace. See §12.11. |
| Cron service           | App with `kind: cron`        | See §12.17. |
| Private networking (`.railway.internal`) | Cluster DNS via binding | `<app>.<project-ns>.svc.cluster.local`, surfaced as a single host/port binding. |
| Staged changes         | *(not in Mortise v1)*        | Mortise applies on save. See §12.2. |

---

## 2. Global UI chrome

Elements that persist across flows.

### 2.1 Top bar (all authenticated routes)

Reference: `dashboard.png`, `canvas.png`, `user-dropdown.png`.

- **Left:** Mortise logo → `/` (project list).
- **Project switcher dropdown** (when inside a project) — lists Projects,
  "Create new project" footer item. Matches SPEC §5.0 §5.13. Railway
  renders this as `{project-name} v` to the right of the logo.
- **Environment switcher** (inside a project) — adjacent to the project
  switcher. Lists environments plus active preview envs. Railway shows
  `production v` next to the project name.
- **Staged-changes group (canvas only, dirty state)** — floats center-top:
  `Apply N change | Details | Deploy ⇧+Enter | ⋮`. See §12.2.
- **Right rail:** observability (↗), **Activity pulse button (project
  scope only)** — toggles the Activity right-rail slide-out (§12.22).
  Notifications (🔔). **Agent** (Railway-specific AI, not adopted),
  user menu (email, theme, logout).
- **Far right:** user menu. On click, dropdown contains: Account
  Settings, Workspace / Platform Settings, Usage (if adopted — see
  §12.25), workspace switcher (v2 only), Docs / Support, theme toggle,
  Log out.

### 2.1a Primary (left-rail) nav

Reference: `canvas.png`, `dashboard.png`.

**Dashboard scope** (`/`): Projects (current), Templates, Usage,
People, Settings. Mortise simplifies to **Projects, Extensions,
Platform Settings (admin-only)**. No Templates route (§12.11), no
Usage (§12.25), no People top-level (folded into Platform Settings).

**Project scope** (`/projects/{p}`): Canvas, Metrics, Docs/Logs,
Settings. Mortise mirrors this four-icon column inside the project
workspace.

### 2.2 Command palette (`⌘K` / `Ctrl+K`)

Direct-navigate to Projects, Apps, and global actions. *Scope TBD — is
this v1 or v2?* → see §12.20.

### 2.3 Breadcrumbs

`Projects / {project} / Apps / {app}` — the only location bar the user
needs. Derived from the URL, not stored state.

### 2.4 First-run wizard (pre-auth → admin bootstrap)

Unique to self-hosted. Four steps: domain → DNS provider → first git
provider → admin user. See §3 Onboarding.

---

## 3. Flows

Flows are ordered roughly by frequency, not by onboarding sequence.

### 3.1 Onboarding — first-run admin wizard

**Goal.** Bring a freshly-installed Mortise to the point where an admin
can log in and a `default` Project exists.

**Entry points.** Fresh install → GET `/` → redirect to `/setup`.

**Screens.**
1. `/setup` — single form: admin email + password. Posts `/api/auth/setup`.
2. `/setup/wizard` step 1 — platform domain (e.g. `yourdomain.com`).
3. `/setup/wizard` step 2 — DNS provider + API token (Cloudflare, Route53, noop).
4. `/setup/wizard` step 3 — connect a git provider (optional — skippable).
5. `/setup/wizard` step 4 — done; CTA to dashboard.

**States.** If `/api/auth/status` reports setup already complete, redirect
`/setup` → `/login`.

**Mortise divergence.** Not present in Railway (they're SaaS). §12.18.

### 3.2 Login

**Goal.** Authenticate an existing user (native or OIDC).

**Entry points.** `/login`, or any authed route when the JWT is absent/expired.

**Screens.**
1. Native — email + password.
2. OIDC (when `PlatformConfig.auth.mode = oidc`) — "Continue with {provider}" button redirects to IdP.

### 3.3 Project list (dashboard)

**Goal.** Pick a project to work in, or create a new one.

**Entry points.** `/` (root, authed).

**Screens.** Reference: `dashboard.png`.
1. Header: "Projects" title + "+ New" button (admin only).
2. Sort-by dropdown + grid/list toggle (icon pair right side).
3. Project card: name (bold), service-icon preview chips (Postgres, GitHub
   linked-repo), status footer ("production · 2/2 services online").
4. **No Trial / billing banner** (strip from Railway reference — self-hosted).

**States.** Empty: should never happen (default project is seeded on
first-run setup — §12.18).

**Mortise divergence.** Railway's dashboard also shows recent activity
across all projects; Mortise may defer that. §12.1. Strip workspace
sidebar — Mortise dashboard has only Projects + Extensions + Platform
Settings in the left rail.

### 3.4 Create project

**Goal.** Admin creates a new project.

**Entry points.** Dashboard "New Project" button. `/projects/new`.

**Screens.**
1. Name (DNS-1123, ≤55 chars) + description.
2. Advanced (collapsed): `namespaceOverride`, `adoptExistingNamespace`.
3. Submit → POST `/api/projects` → land on project workspace.

**States.** Validation errors inline (name collision = 409, bad name = 400, non-admin = 403).

### 3.5 Project workspace (list of Apps in a project)

**Goal.** See all apps in a project; pick one to work on; create a new one.

**Entry points.** `/projects/{p}`.

**Screens.**
1. App grid/list. Each row: name, source type badge (git/image/external),
   kind badge (service/cron), status chip (Ready/Building/Failed/Crashed),
   current domain, last deploy time.
2. Sidebar or header: project switcher, "New App" CTA, project settings link.

**Mortise divergence.** Railway shows a **visual canvas** with services
as tiles connected by binding lines. Mortise currently shows a list. §12.4.

### 3.6 New app

**Goal.** Create an App from a git repo, a Docker image, an external
service, or a template.

**Entry points.**
- Canvas "+ Add" top-right → opens the single-modal picker (preferred).
- `/projects/{p}/apps/new` direct URL (fallback for deep links).
- Right-click on empty canvas → "New App here" (positions the node).

**Screens.** Reference: `project-add.png`. **Single modal picker**
(Railway-style), not a multi-step page flow:

1. **Type picker modal** — "What would you like to create?" typeahead +
   option list. Mortise options (dropping Railway's Function and Bucket
   which we don't support):
   - **Git Repository** — expand to repo/branch search (same as current
     GitHub/GitLab pickers).
   - **Database** — expand to curated Postgres / Redis / MinIO cards
     (§12.11).
   - **Template** — same curated list, flat.
   - **Docker Image** — image input.
   - **External Service** — host + port facade (§12.6).
   - **Volume** — standalone PVC (for adopting existing data) — scope TBD.
   - **Empty App** — blank scaffold for advanced users.
2. **Configure pane** (slides in from right of modal or replaces panel):
   source-specific fields, name, environment picker (default `production`).
3. Submit → POST `/api/projects/{p}/apps` → modal closes → new App tile
   appears on canvas at click position (or top-right if opened via `+ Add`).

**States.** Errors inline in the configure pane (name taken, invalid
image ref, repo unreachable).

**Mortise divergence.** "External service" and the kind selector
(service vs cron) are Mortise-specific (§12.6, §12.17). **Shift from
tabbed page to single-modal picker** aligns with Railway's UX and keeps
the canvas visible behind — see §12.26.

### 3.7 App detail

**Goal.** Monitor, configure, deploy, and debug one App.

**Entry points.**
- Click an App tile on the canvas → opens drawer (canvas stays visible).
- `/projects/{p}/apps/{a}` deep link → opens drawer over canvas.

**Screens.** Reference: `project-app-select-deployments.png`,
`project-app-slect-settings.png`, `variables.png`.

**Drawer, not full page.** Right-side slide-over (≈45% width) that
preserves the canvas context. Close "X" top-right; click outside also
closes. Four top tabs: **Deployments / Variables / Metrics / Settings.**

- **Deployments** (default) — current deploy row: Active chip, author
  avatar, trigger ("{actor}, 29 min ago via {source}"), "View logs"
  button, kebab (Redeploy / Rollback / Abort). Region + replica badges
  top-right ("us-west2 · 1 Replica" equivalent → Mortise:
  `{storageClass?} · N replicas`). Expandable status row
  ("Deployment successful ✓"). Below: history list.
- **Variables** (§3.8).
- **Metrics** — CPU/memory charts with deploy markers.
- **Settings** — **Filter Settings** quick-filter input top of pane.
  Right-side anchor nav jumps between groups: Source, Networking, Scale
  (Replicas + Resources), Build, Deploy, Config-as-code (GitOps note —
  §12.10), Feature-flags (skip v1), Danger (delete App).

**Unexposed service badge** shown on Deployments row when
`network.public: false` (matches Railway's "Unexposed service" copy).

**States.** `status.phase ∈ {Pending, Building, Ready, Failed, Crashed}`
maps to chip color + tooltip.

**Mortise divergence.** (1) Drawer-over-canvas, not a page (§12.21).
(2) No separate "Logs" tab at app level — logs open from the "View logs"
button in the deploy row, streaming into a bottom panel (§3.12). (3)
Railway shows disk + network metrics; Mortise v1 shows only CPU + memory
(§12.13). (4) No "Agent" affordance (Railway AI assistant).

### 3.8 Variables editing

**Goal.** Add / edit / remove env vars at the environment level or at
`sharedVars` level.

**Entry points.** App detail drawer → Variables tab.

**Screens.** Reference: `variables.png`, `variable-reference.png`,
`change-details.png`, `settings-shared-variables.png`.

1. Environment tabs (Production, Staging, + Shared). `sharedVars`
   surfaces as a pseudo-env tab. (Railway scopes shared vars to the
   project — we scope per-App. §12.3.)
2. Table view: key, value (masked for secrets), source (literal /
   secretRef / fromBinding), kebab (Edit / Delete / Convert to secret).
3. **New variable row** (top of table). Columns:
   `[VARIABLE_NAME]` input | `[+ Add Reference]` button | `[VALUE or ${{REF}}]` input | `Add` | `Cancel`. The `{}` icon in the value input toggles a mini template helper.
4. **Add Reference picker** (§3.9). Opens as a dropdown from the Add
   Reference button. Filter input. Rows show `KEY | SourceApp`
   (`DATABASE_URL | postgres`). "Show N More" expand.
5. **Raw mode** — textarea, pastes `.env`-style content, preview diff.
6. **Import** — file picker, preview diff.
7. **Source-code suggestions** (Railway scans the connected repo for
   referenced env names and offers them as "Suggested Variables"). See
   §12.23 — **flag for decision**, probably out of scope v1.
8. **Staged state.** Adding/editing a var marks it as "edited" (purple
   chip) but doesn't persist until Deploy — see §12.2. Click **Details**
   on the staged-changes bar to see the change summary modal.

**Change summary modal** (from Deploy bar → Details). Reference:
`change-details.png`. Per-app accordion listing adds/edits/deletes
with Change / Current Value / New Value columns. Per-row Discard.
"{app} will redeploy" warning. Footer: Cancel / **Deploy Changes**.
(Railway shows a commit-message input here for their optional git-ops
commit of the spec. Mortise has no such flow and omits the field.)

**States.** Unresolved references (`${{bindings.my-db.host}}` with
missing binding) render with a red "reference broken" chip; Deploy
blocks. Secrets display masked.

**Mortise divergence.** Railway's `${{service.VAR}}` is free-text over
any var of any service; Mortise's scoped `${{bindings|secrets|shared}}`
is a contract picker over declared surfaces. §12.3.

### 3.9 Service bindings (connect Apps)

**Goal.** Let App A consume credentials/DNS from App B.

**Entry points.** App detail → Settings → Bindings, OR Variables tab
when a user types `@from:`.

**Screens.**
1. Bindings panel: list of existing bindings with
   `ref` + optional `project:`.
2. "Add binding" → picker of Apps in this project (default) or another
   project (expand). Only Apps with `credentials:` declared appear.
3. Preview: which env vars this will inject (matching the bound App's
   `credentials:` list). User can pick short-form (all) or long-form
   (single key under a renamed env var → `valueFrom.fromBinding`).

**Mortise divergence.** The picker is contract-driven — it surfaces the
backing App's declared credential keys, not a free-text expression. §12.3.

### 3.10 Domains

**Goal.** Expose an App publicly, add custom domains, verify TLS.

**Entry points.** App detail → Settings → Domains, per environment.

**Screens.**
1. **System domain** (always present when `network.public: true`) —
   autogenerated `${app}-${env}.${platformDomain}`. Copy button.
2. **Custom domains** — list with TLS status chip (Pending / Active /
   Failed). "Add custom domain" → dialog: domain name, verification
   snippet (CNAME target = platform domain), "Verify" button.
3. Remove domain → confirm.
4. Per-env TLS override (advanced) — `tls.clusterIssuer` OR
   `tls.secretName` (mutually exclusive, SPEC §5.6).

### 3.11 Storage (volumes)

**Goal.** Attach persistent storage to an App.

**Entry points.** App detail → Settings → Storage.

**Screens.**
1. Volumes list: name, mount path, size, storage class, access mode.
2. "Add volume" → dialog: name (DNS-1123), mount path, size (slider
   bounded by storage class), optional storage-class override, access
   mode selector (auto / RWO / RWX).
3. Edit size inline → confirmation modal explaining that expansion
   behavior depends on the storage class (not all support online
   resize). See §12.14.
4. Delete volume → hard warning.

**Mortise divergence.** No "Live Resize" button like Railway — Mortise
delegates to the CSI driver. No file browser, no backup UI in v1.
Backups are a docs-recipe path via Velero. §12.14.

### 3.12 Logs

**Goal.** See live and recent logs for an App.

**Entry points.** App detail → Logs tab.

**Screens.**
1. Top bar: environment selector (prod / staging / preview-XX), log
   category toggle (runtime / build / deploy), replica picker (if >1),
   search input, time-range selector, live-tail toggle.
2. Body: log stream (SSE). Line format: timestamp | replica | message.
3. Download/copy affordance.

**Mortise divergence.** Railway has a top-nav Observability → Log
Explorer spanning **all services in the workspace**. Mortise v1 is
per-App only, with a note that historical search requires a log agent
(Loki, etc.) as a docs-recipe. §12.12.

### 3.13 Deploy tokens (external CI)

**Goal.** Generate a token so an external CI can deploy a pre-built
image without humans.

**Entry points.** App detail → Settings → Deploy tokens.

**Screens.**
1. Tokens list: name, scope (app+env), created-at, last-used.
2. "Create token" → dialog: name, environment scope.
3. On create: token value shown **once**, with CI snippets
   (curl / GitHub Actions / GitLab CI) pre-filled.
4. Revoke → confirm.

**Mortise divergence.** Railway has no direct analog; external CI on
Railway uses their GitHub integration or the CLI. Mortise treats this
as a first-class flow. §12.5.

### 3.14 Preview environments

**Goal.** See the auto-created PR environments for the project (git-source
apps only).

**Scope.** PR Environments are a **project-level** toggle. When enabled,
every eligible App in the project gets a preview per PR; when disabled,
none do. No per-App opt-in/opt-out. See §12.16 for the rationale and the
personas this burns.

**Entry points.** Project settings → Environments → "PR Environments"
section (primary), OR top-bar env switcher shows active previews.

**Screens.**
1. Project settings → Environments → "PR Environments" section: Enable
   toggle, focused-preview (auto-derived from `watchPaths`), bot-preview
   opt-in, TTL default, domain template. §12.16.
2. List of active previews: PR number, branch, URL, TTL countdown,
   phase chip. Lives at `/projects/{p}/previews` (project-scoped, not
   per-App).
3. Clicking one: the App detail drawer scoped to that env, minus
   destructive actions (rollback disabled; delete available).

**Scope semantics (option a, locked).** When
`ProjectSpec.Preview.Enabled = true`:

- **Every App in the project reconciles into the preview namespace per
  PR.** That includes `kind: cron` Apps and `network.public: false`
  Apps. This is the cost side of the decision — accepted so bindings
  resolve coherently (a preview API can reach its preview DB and its
  preview worker's cache without manual stitching).
- Apps **without** an HTTP surface (`network.public: false` or
  `kind: cron`) **do not** get a preview URL, because there's nothing
  public to route to. They still participate in the preview namespace
  so binders find them.
- Users hit by the compute tax (see §12.16 persona burns) split heavy
  apps into a sibling project where previews are off.

**CRD impact.** `AppSpec.Preview` is removed; `ProjectSpec.Preview` is
added. Breaking change for any existing App YAML referencing
`spec.preview`. See §12.16 for the full migration note.

### 3.15 Environment annotations (advanced)

**Goal.** Set per-environment annotations for Linkerd injection, IRSA
roles, nginx rate limits, etc.

**Entry points.** App detail → Settings → Environment → Advanced → Annotations.

**Screens.**
1. Flat key/value editor. No validation, no reserved-prefix blocklist,
   no autocomplete (the point is total passthrough). "Danger: you
   must know what each annotation does" callout.

**Mortise divergence.** No Railway analog — this is an explicit k8s
escape hatch. §12.9.

### 3.16 Secret mounts (files, not env)

**Goal.** Mount an existing k8s Secret as files in the container (Java
keystores, mTLS, config files).

**Entry points.** App detail → Settings → Environment → Advanced → Secret mounts.

**Screens.**
1. List: volume name, k8s Secret name, mount path, items[], readOnly.
2. Add → dialog with those fields. Items section for key→file mapping.

**Mortise divergence.** No Railway analog. §12.8.

### 3.17 Platform settings

**Goal.** Admin manages platform-wide configuration.

**Entry points.** Top-bar Settings (admin-only).

**Screens.** Reference: `settings.png`, `settings-environment.png`,
`settings-shared-variables.png`, `settings-members.png`,
`settings-danger.png`.

Railway splits settings into **Project Settings** (per-project) and
**Workspace Settings** (billing, integrations). Mortise has no workspace
layer in v1, so we fold the two:

**Project Settings** (sub-nav inside `/projects/{p}/settings`):
- **General** — name, description, project ID, adopt/namespace-override
  advanced panel, Visibility (fixed to PRIVATE v1).
- **Environments** — list of environments with kebab (rename /
  delete); "+ New Environment" button; **"PR Environments" section** with
  Enable toggle (maps to `ProjectSpec.Preview.Enabled` — project-level,
  no per-App override in v1). See §3.14 and §12.16.
- **Shared Variables** — per-env accordion (SPEC §5.8b `sharedVars`).
- **Members** — see §12.24. Invite form: email + permission dropdown
  (Admin / Member) + Invite button. "Copy invite link" below. Members
  list.
- **Tokens** — project-scoped deploy tokens (alternative placement
  to App detail — see §12.5 open question).
- **Webhooks** — outgoing webhooks for deploy events. Scope TBD v2.
- **Integrations** — connected git providers (reuse §3.17 git provider
  list) + external secrets + DNS (cross-links).
- **Danger** — Manage Services (per-app Remove with 2-step confirm) +
  Delete Project (destructive).

**Platform Settings** (admin-only, `/admin/settings`):
- **General** — platform domain, description.
- **DNS** — provider + token.
- **Git providers** — list, create (implemented), delete, OAuth-connect.
- **Registry** — URL, namespace, credentials, pull secret.
- **Build** — BuildKit address, default platform, TLS.
- **TLS** — default cluster issuer.
- **Users & invites** — list, role, invite link generator.
- **Teams** — *(v2, see §12.1; v1 hides entirely)*.

**Filter Settings** (`/` hotkey) input top of each Settings pane —
match Railway's quick-filter affordance (`settings.png`, `project-app-slect-settings.png`).

### 3.18 Extensions page

**Goal.** Surface upstream projects users can install for SSO, monitoring,
backup, external secrets — not bundled.

**Entry points.** Top-bar Extensions.

**Screens.**
1. Categorized cards: Infrastructure, Security, Tenons.
2. Each card links to a docs recipe (`docs/recipes/*`) and, where
   applicable, the upstream chart.

**Mortise divergence.** No Railway analog. Already implemented. Keep.

---

## 12. Divergence catalog — where Mortise ≠ Railway

Every item here is a place where copying Railway's UI verbatim would
conflict with Mortise's architecture or scope. **Read this before
designing any flow.** Each entry poses the choice Mortise has to make
and flags the decision owner (you).

### 12.1 Workspaces / Teams

**Railway workspaces** are the org/billing/tenant layer above projects.
Each Railway account has a personal workspace; paid plans add more.
Workspaces scope billing, members (Admin/Member/Deployer), trusted
email domains for auto-invite, and a basket of projects. Switching
workspaces is how you context-switch between "my side projects" and
"acme-corp's infra." It's fundamentally a **multi-tenant SaaS construct.**

**Why they don't map to Mortise.** Mortise is self-hosted: one Helm
install = one instance = one org. No billing, no parallel tenants, no
account scoping. The **cluster is the implicit workspace.**

**Mortise's equivalent = Teams (SPEC §5.10, v2).** A lightweight
grouping *inside* one Mortise install: members + roles + assigned
projects. Narrower than Railway workspaces because there's no
tenant/billing boundary. Homelabber: one team (themselves). Company:
one team per internal group.

**Decision (v1).** **No workspace switcher, no team chrome.** For solo
/ single-team installs, the concept is invisible. Teams land in v2 as a
filter/assignment on the project list — not a top-level switcher.

**Persona check found 2/10 burned (2026-04-17).** A 10-persona sweep
flagged two target personas from SPEC §3 who need team-scoped RBAC
day one:
- **Regulated on-prem teams** with multiple internal tenants
  (risk/trading/retail) that can't share an admin role.
- **Internal platform teams** supporting many dev teams in one
  cluster who need to delegate namespace-scoped admin.

Both are explicit SPEC targets, not fringe users.

**Mitigation — implicit-team stub in v1.** Ship a single implicit
`Team` CRD at install time (name: `default-team`) and bind every
user/grant to it internally. UI stays chrome-free. v2 becomes
purely additive: split the implicit team into N teams; existing
users/grants rehome with zero migration. ~3 lines of Go, zero UI
change. **Strongly recommended as insurance.**

### 12.2 Staged changes — Deploy button

**Decision: adopt.** The Deploy button ships. Scope is ~95% UI, ~5% API,
0% controller for the MVP.

**What Railway's Deploy button buys.**
1. Edit N things → 1 reconcile (not N).
2. Preview diff before committing.
3. Atomic apply (all-or-nothing).
4. "Alt+Deploy" = commit config without restarting pods.

**Three implementation tiers.**

**Tier 1 — client-side staging (MVP, ship this).** UI holds the dirty
`App` spec in memory, diffs against the last-GET snapshot, shows the
purple diff + banner + count. On Deploy, one `PUT
/api/projects/{p}/apps/{a}`. On Discard, reset to snapshot.
- Backend changes: **zero.**
- UI changes: a dirty-state store (Svelte store), `beforeunload`
  handler, diff computation, banner component, per-field "edited" marker.
- Caveats: lost on tab close; can't be shared with a teammate.

**Tier 2 — server-side drafts (post-v1 if demand).** `POST
/apps/{a}/draft` persists a pending spec as a k8s Secret with TTL 24h.
Reload the tab or share a review link.
- Backend changes: **API handler + a k8s Secret schema.** No controller
  change.
- Effort: ~2–3 days.

**Tier 3 — "commit without restart." Skip.** Only relevant for env
vars, and k8s can't inject new env into running pods. The semantics —
"future pods get new env, current ones don't" — is confusing and rarely
what anyone actually wants.

**Scope.** The Deploy button governs everything on an App's Settings
surface: Variables, Resources, Replicas, Domains, Bindings, Storage,
Annotations, Secret Mounts. Source edits (repo, branch, image) are also
staged — a source change implies a rebuild on Deploy. Deleting the App
bypasses staging (it's a separate destructive flow).

### 12.3 Bindings vs reference variables

**The architectural difference.** Railway's `${{Svc.VAR}}` is free-text
string substitution over any variable of any service. Mortise's
bindings are a **contract**: the backing App declares
`credentials: [DATABASE_URL, host, port, user, password]` and binders
consume only declared keys. The contract is the reason Mortise can
render a type-safe picker and guarantee secrets project as
`secretKeyRef`, not plain env values.

**Three practical consequences.**

1. Railway supports **URL composition**: `URL=https://${{web.PUBLIC_DOMAIN}}/api`.
   Mortise short-form bindings inject whole values; `fromBinding` does
   one key-to-name; there's no string interpolation.
2. Railway refs are **type-unchecked at edit time**. Mortise can reject
   a bad ref at PUT time because the declared contracts are known.
3. `sharedVars` in Mortise is per-App-across-envs; Railway shared
   variables are per-project (visible to any service). Different
   scoping, both reasonable.

**Four options for Mortise.**

| # | Approach                                     | Composes URLs? | Type-safe? | Mortise-spec conflict? |
|---|----------------------------------------------|----------------|------------|------------------------|
| A | Strict contract picker (no templates)        | ❌             | ✅          | none                   |
| B | Open `${{app.var}}` substitution over all envs | ✅           | ❌          | kills the contract     |
| C | Free-form with `@from:` / `@secret:` tokens  | ⚠️ partial     | ✅          | none (§5.9a as-is)     |
| D | Scoped `${{}}` — Railway syntax, Mortise contract | ✅       | ✅          | tiny (new resolver engine) |

**D is the recommendation.** Values support exactly three template
namespaces and nothing else:

- `${{bindings.<app>.<key>}}` — resolves only if `<app>` is bound and
  `<key>` is in its declared `credentials:`.
- `${{secrets.<name>}}` — resolves from the App's secret store.
- `${{shared.<key>}}` — resolves from `sharedVars`.

**Resolver behavior.** At reconcile, if a value is exactly one token
(`${{bindings.db.host}}`), emit it as `valueFrom.fromBinding` or
`valueFrom.secretKeyRef` directly — no materialization. If a value is
composed (`postgres://user:${{secrets.db-pw}}@${{bindings.db.host}}:${{bindings.db.port}}/app`)
and **any** token resolves to secret material, the whole rendered string
is written to a new `{app}-env-resolved` Secret and projected via
`secretKeyRef`. If no secret tokens are involved, emit as literal
`value:`.

**Why D wins.** (1) Railway-native syntax, so muscle memory works.
(2) The picker teaches users the three namespaces — no template
syntax to free-form into. (3) Edit-time validation: every token
resolves against a known surface (declared credentials, declared
secrets, declared shared keys), so the UI rejects bad references
before PUT. (4) The contract stays load-bearing — you can only
reference what was explicitly declared.

**Migration from SPEC §5.9a.** The `@from:` / `@secret:` shortcuts
become secondary (`@from:db.host` is sugar for `${{bindings.db.host}}`).
Keep both in the CLI for terseness; the UI prefers `${{}}`.

**Edge cases worth spelling out.**
- **Typing a `${{` triggers autocomplete** with the three namespaces
  and then the resolvable keys inside each.
- **Composed values hide the resolved preview** when any token is a
  secret (show `${{secrets.db-pw}}` verbatim, not the secret value).
- **Breaking a binding** (user unbinds `my-db`) leaves
  `${{bindings.my-db.host}}` tokens unresolvable — UI marks env rows
  with a red "reference broken" chip and blocks Deploy.

**Short vs long form bindings remain.** `bindings: [{ ref: my-db }]`
(short form) is the "accept all declared credentials" default — still
the primary picker outcome. Power users can drop to long-form
`fromBinding` or compose via `${{}}`.

### 12.4 Project canvas (graph view)

**Decision: canvas is primary, list is the fallback toggle.**

Railway's signature UX is the zoomable canvas where services are tiles
and bindings are edges. We're building it. Notes:

- Node = App. Tile shows name, source badge, kind badge, status chip,
  current domain (if public), and replica count.
- Edge = binding. Draw an arrow from binder → bound App; hover shows
  which credential keys are injected.
- External-source Apps render with a distinct chrome (dashed border +
  cloud icon) to signal "not running in cluster."
- Cron Apps get a clock icon.
- Drag = reposition (persisted in App annotation
  `mortise.dev/ui-x`, `mortise.dev/ui-y`). Auto-layout fallback if no
  positions set (force-directed).
- Right-click on empty canvas = "New App here"; on a tile = quick
  actions (Deploy, Rollback, Open, Delete).
- List view is a toggle in the project workspace header for users who
  prefer it and for accessibility.

**Library choice TBD.** Svelte Flow (the Svelte port of React Flow) is
the obvious default. Alternatives: hand-rolled with SVG, or D3-force
for layout only.

### 12.5 Deploy tokens (external CI)

Railway doesn't have explicit per-App+env bearer tokens; their external
deploys go through their CLI/GitHub app. Mortise makes deploy tokens a
first-class concept (SPEC §5.4, already implemented). This is **a
feature**, not a gap — keep and highlight.

**Decision needed.** Placement — under App Settings or under a separate
"Integrations" tab? Currently under Settings.

### 12.6 `external` source type

Mortise supports an App that is a facade over an upstream service (RDS,
ElastiCache, an internal API). Railway has no equivalent. Needs its own
new-app flow tab and a distinct badge/chip in lists.

**Decision needed.** Do we split "External service" into a separate
flow or include it alongside image/git as a third source radio? SPEC
§5.5a is written as a source type, so a third radio fits.

### 12.7 Image source as peer to git

Railway's UX funnel pushes users to GitHub / templates; Docker image
is a less-emphasized path. Mortise treats image as a first-class peer
(self-hosted apps, backing services). Already reflected in the
Railway-style new-app page (`PROGRESS.md` Phase 7).

**Decision needed.** None — keep current weighting.

### 12.8 `secretMounts`

Mount an existing k8s Secret as files. No Railway equivalent.
Power-user feature — fold into an "Advanced" collapsed group. §3.16.

### 12.9 Environment annotations escape hatch

Per-env flat annotation map (SPEC §5.2a). No Railway analog. Pure power
user. §3.15. Hide under "Advanced"; add a "you must know what you're
doing" callout.

### 12.10 GitOps-managed Apps

Some users will manage App CRDs via Argo CD. Their UI/CLI env edits
and the CRD-as-source-of-truth are in tension — Mortise solves this via
`ignoreDifferences` on `env` / `sharedVars` (SPEC §5.9a). Not a UI
feature per se, but the Settings tab should have a docs link
("Managing via GitOps?") near the Variables section.

### 12.11 Templates — no marketplace

**Decision: curated cards inline on the new-app page. No gallery, no
search, no `/templates` route.**

Railway has a huge searchable marketplace. Mortise has a short curated
list of image-source App presets (Postgres, Redis, MinIO, etc.) — SPEC
§5.5, §5.13. Rendering rules:

- 4–8 cards at the bottom of the new-app page, under the primary
  source pickers (GitHub / Image / External).
- No search bar, no categories, no tags, no popularity counter. If we
  ever need more than ~12, revisit.
- Clicking a card pre-fills the image-source form with image, default
  storage, and `credentials:` block — user still reviews and submits.
- Community presets (SPEC §6.5) are **not** rendered here. If that
  ever ships, it's an "Import App from URL" affordance separate from
  this list.

### 12.12 Logs scope

Railway's Log Explorer spans all services. Mortise v1 logs are per-App
only; cross-service log search is explicitly deferred to the user's
log agent (Loki/Splunk/CloudWatch). UI should **not** imply platform
log aggregation.

### 12.13 Metrics coverage

Railway surfaces CPU, memory, disk, network with deploy markers.
Mortise v1 ships metrics-server (CPU + memory only). Disk/network chart
tiles should show "Install kube-prometheus-stack to enable" rather than
being absent (graceful degradation, SPEC §5.11).

### 12.14 Volumes — no file browser, no Mortise-managed backups

Railway supports Live Resize and backup schedules. Mortise delegates:
resize is whatever the StorageClass supports; backups route through
Velero as a docs-recipe.

**Backups — decision: docs-only with per-service guides. No Velero
coupling in the UI.**

This is the cleanest expression of SPEC §6.1 invariant #3 ("no
speculative upstream-tracking"). A first-party backup feature would
commit Mortise to Velero's API schema forever; docs let us pivot to
k8up or whatever replaces Velero without code churn.

**Guides to ship:**

- `docs/recipes/backup-velero.md` — primary path (cloud + on-prem).
- `docs/recipes/backup-k8up.md` — homelab-friendly Restic-based
  alternative; less moving parts than Velero.
- `docs/recipes/backup-enterprise-tools.md` — Commvault / NetBackup /
  Rubrik shape; "your existing backup tool already covers this, target
  `project-*` namespaces."
- `docs/recipes/backup-storage-snapshots.md` — Longhorn / Ceph / cloud
  CSI `VolumeSnapshot` for users whose storage driver handles it
  natively.

**UX nudge (still in scope).** A small banner on the Storage panel
when a project's first volume is created: "Not backed up yet — set up
backups before going live." Links to the guide set. Dismissible per
project. No CR reads, no ownership claims — just a heads-up.

**What we explicitly do NOT ship.**

- Read-only presence badge that queries Velero `Schedule` CRs.
- "Enable backups" button that writes Velero CRs.
- A Backup tab that lists/restores Velero `Backup` CRs.

Each of these would be modest UI code but commits us to Velero
upstream tracking. Not worth it.

### 12.15 Sealed variables = Secrets

Railway has "sealed variables" as a distinct state you apply. Mortise
secrets are sealed by default — value is never readable after save
(SPEC §5.9). Merge the two concepts in the UI: there's just "set as
secret" on a variable, which stores it as a k8s Secret and references
it via `secretKeyRef`.

### 12.16 Preview environment toggles

**Decision (2026-04-17): project-level only, scope option (a).** PR
Environments are a single toggle on `ProjectSpec.Preview.Enabled`. No
per-App `spec.preview.enabled`. When on, **every App in the project**
reconciles into each PR's preview namespace. Apps without an HTTP
surface (`kind: cron`, `network.public: false`) still reconcile — they
just don't get a preview URL. This keeps bindings coherent in preview
namespaces.

**Rationale.** Matches Railway's mental model (previews are a project
property, not a per-service one), collapses the UI surface, and keeps
bindings coherent — a preview API needs its preview DB, preview cache,
etc., which a per-App toggle would require the user to coordinate
manually.

**Personas this burns** (10-persona pass):

| Persona | Burn |
|---|---|
| Microservices SaaS (workers alongside services) | Medium — workers spin up per-PR, wasted compute. |
| ML platform (inference API + GPU training cron) | **Hard** — training cron in every PR blows GPU quota, may hit real model registry. |
| Data pipeline (dashboard + ETL crons) | **Hard** — crons re-run on every PR, duplicate external API calls, possible writes to real sinks. |
| E-commerce (services + email/image workers) | Medium — workers as compute tax. |

All other personas (solo dev, startup, backing services, consulting
shop, enterprise platform team) are neutral or benefit.

**Mitigations users can reach for today:** split heavy-cost apps into
a sibling project with PR Environments off; move cron work to
`external` image apps that reconcile but stay idle unless triggered;
keep worker replica count low in the preview environment definition.

**Future path (v2, not v1).** Re-introduce per-App override as a
*visible* UI toggle in the App detail Settings panel — not a hidden
YAML field. The project-level default stays authoritative; per-App is
an escape hatch. Deliberately deferred so v1 ships with one clear story.

**Railway parity details still kept:** "Focused PR environments" (build
only affected services) is auto-derived from `watchPaths` — no extra
toggle. "Bot PR" opt-in is a sub-setting under the project-level PR
Environments section.

**CRD migration.** `AppSpec.Preview` is removed; `ProjectSpec.Preview`
is added. Breaking change for any existing App YAML referencing
`spec.preview`. The App controller stops reading `spec.preview`; the
preview reconciler reads from the parent Project.

### 12.17 Cron kind

Railway: a regular service becomes a cron by pasting a cron expression
in its Settings. Mortise: cron is a separate **kind** (SPEC §5.8a) — a
CronJob reconciliation, not a Deployment. UX consequence: new-app flow
needs a kind picker (Service / Cron) because downstream fields differ
(no public domain, no replicas, schedule required).

### 12.18 First-run wizard

Unique to self-hosted. Railway doesn't need it. Already implemented.
Keep as-is.

### 12.19 Extensions page

Unique to Mortise. Already implemented. Keep.

### 12.20 Command palette

**Decision: v2.** Out of scope for v1. When it lands, scope to
navigation (fuzzy-find projects + apps) and top create actions
(project, app, token, secret rotate). No workflow commands
("deploy app X") in v2a either — keep it navigational.

### 12.21 App detail — drawer over canvas

**Decision: drawer.** Clicking an App on the canvas opens a right-side
slide-over (≈45% width); the canvas stays visible on the left,
click-outside closes. Deep-link `/projects/{p}/apps/{a}` still works —
the URL drives drawer-open state, the shell stays at the project
workspace.

**Migration cost.** Current UI renders App detail as a dedicated page.
Moving to a slide-over is a medium refactor: same sub-components
(Deployments / Variables / Metrics / Settings panels), new wrapping
layout, keep the `/apps/{a}` URL as the drawer-open deep link. Routing
logic stays identical; only the layout container swaps.

**Why it wins.** (1) Preserves context — users see which neighbor apps
are affected by a change (broken binding edges are immediately
visible). (2) Natural home for the staged-changes bar (pinned above the
canvas, drawer is non-blocking). (3) Muscle memory for Railway users.

### 12.22 Activity feed

**Decision (2026-04-17): adopt as a toggle-slide-out rail (Railway parity).**

**Behavior.** Project-scoped right-rail Activity panel, **closed by
default**. Opened by a pulse button in the top bar (§2.1 right rail).
When open, slides in from the right over the canvas/drawer without
displacing content. When closed, zero chrome cost. Esc and the pulse
button both dismiss it. Matches Railway (`activity.png`) — Railway's
rail is not always-open; the user toggles it via the pulse button.

**Contents.** Chronological feed of project events:
- Deploys ("Deployment successful", "Rolled back production to abc123")
- Structural changes ("Deploy PostgreSQL — 1 service and 1 volume
  updated", "Added binding from api → postgres")
- Variable edits ("Updated 3 variables in production")
- Membership events ("New environment staging")

Each row: actor avatar, one-line summary, relative timestamp. Filter
chips at the top (Deploys / Changes / Members / All).

**Data model.** Two sources fed into one view:
1. **Audit events** — actor-captured writes. Every POST/PATCH/DELETE
   handler in `internal/api/` threads the authenticated `Principal`
   through to a small append-only store. v1 store: a per-project
   ConfigMap (`activity-{project}`), capped at the last 500 events with
   a simple ring-buffer trim. No new CRD, no new DB.
2. **Synthesized deploy rows** — read from the existing App
   `status.deploys` history on demand, merged into the timeline by
   timestamp.

**Endpoint.** `GET /api/projects/{p}/activity?after=<cursor>&type=<filter>`.
Paged. Polls on rail open and on window focus — no SSE v1; refresh on
demand is fine for a closed-by-default panel.

**Cost.** ~5–8 days, mostly the actor-capture tax on every write
handler (non-optional; needed for audit no matter what UI surface
ships). The rail component itself is ~1 day of Svelte.

**Rejected alternatives.**
- *Tab-only (no rail)*: considered as a cheaper start, but the pulse
  button is so low-cost when the backend already exists that there's
  no reason to defer it.
- *Skip v1*: loses the "who edited this" answer that small teams hit
  fast.
- *k8s Events as the only source*: abandoned — k8s Events are
  controller-level ("Created Deployment"), not user-level ("jane@acme
  updated env var DATABASE_URL"). Audit needs the API handler to
  capture the Principal; k8s Events can't.

### 12.23 Variables picker — declared-only, no source-code scanning

**Decision: declared-only.** The Add Reference picker shows exactly
three populations, all driven by declarations on existing resources —
never inferred from source code or usage patterns:

1. **`${{bindings.<app>.<key>}}`** — every App in the project with a
   `credentials:` block contributes its declared keys. A Postgres App
   with `credentials: [DATABASE_URL, host, port, user, password]`
   contributes exactly those 5 keys.
2. **`${{secrets.<name>}}`** — every Secret the user has explicitly
   created in this App's secret store.
3. **`${{shared.<key>}}`** — every key in project `sharedVars`.

**Explicitly not offered:**
- Source-code scanning (Railway's "Suggested Variables" from
  `process.env.X` / `os.Getenv("X")` inference).
- Popular / recommended variable names (`DATABASE_URL` isn't suggested
  just because the App is connecting to a database).
- Inferred names from past deploy history.

**Why this matters.** The declared-only picker is why the resolver can
reject bad references at PUT time and why secret-valued references
always project as `secretKeyRef` instead of plain `value:`. Source
scanning would turn the picker into a suggestion engine whose
suggestions couldn't be validated — incompatible with the contract
model in §12.3.

### 12.24 Project Members — v1 link, v2 page

**Decision (v1): Project Settings → Members is a link, not a page.**
It navigates to Platform Settings → Users. Rationale: v1 has no
per-project scoping (no Teams, no project-scoped invites), so a
per-project Members page would just mirror the global user list —
pure duplication.

**v2: per-project Members page lands.** When Teams arrive (§12.1), the
Members tab becomes a real surface:
- Banner: "All members of {team} can access this project."
- Invite member form — email + permission (scoped to this project only,
  not the whole team).
- Project members list — direct invites separate from team inheritance.
- "Copy invite link" affordance.

The v1 link-only form preserves the Members menu slot so the v2
upgrade is a UI swap, not a navigation reshuffle.

### 12.25 Usage / cost page — skip

**Decision: skip.** Mortise does not ship a project-level Usage page.
Users who want resource dashboards run their own metrics stack
(kube-prometheus-stack, Datadog, Grafana Cloud, whatever the cluster
already has). Mortise points at the Extensions page (§3.18) for recipe
links.

**Rationale.** Resource-aggregation UIs are a job for the user's
metrics stack, not the deploy platform. Building a thin
metrics-server-backed version would duplicate kube-prom-stack with a
worse UX, and a billing-flavored version doesn't apply to self-hosted
at all. The one exception — **per-App Metrics tab** (§3.7) — stays,
because it's scoped to the App context where the user is already
looking.

### 12.26 Single-modal create picker

**Observed Railway behavior** (`project-add.png`): **one modal**,
typeahead at top, 8 stacked options (GitHub / Database / Template /
Docker Image / Function / Bucket / Volume / Empty Service).
Clicking an option expands its configure panel inline in the same
modal. User never navigates.

**Mortise reality.** The new-app flow today is a page (`/projects/{p}/apps/new`) with source-type tabs across the top. That works, but
it takes the user off the canvas, which breaks the staged-changes /
canvas-centric model.

**Decision: adopt the single-modal picker.** §3.6 is already rewritten
to match. Mortise option list drops `Function` and `Bucket`
(not supported), keeps everything else, adds `External Service`.

### 12.27 Canvas zoom + layer controls

**Observed Railway behavior** (`canvas.png`): bottom-left vertical
button stack:
- Grid snap toggle
- Zoom in (+)
- Zoom out (−)
- Fit to view
- Undo
- Redo
- Layers toggle (show/hide volumes, edges)

**Mortise adoption.** Mirror this stack 1:1. Undo/redo applies to
canvas node positioning only (not to spec edits — those go through
the staged-changes system). Volumes-as-layers toggle declutters
the canvas for users who don't care about storage edges.

**Decision needed.** None if Svelte Flow is chosen (it ships these
controls); flag if we hand-roll.

---

## 13. Open questions

Numbered for easy reference. Resolved questions move to §12 as
decisions; questions here still need answers.

**Resolved 2026-04-17.**
- Canvas — **primary view, list is fallback toggle** (§12.4).
- Staged changes — **adopt, Tier 1 client-side MVP** (§12.2).
- Workspace chrome — **hidden in v1; Teams in v2 as filter not switcher. Implicit-team stub recommended as insurance** (§12.1).
- Templates — **inline curated cards on new-app page** (§12.11).
- Command palette — **v2** (§12.20).
- Binding syntax — **scoped `${{}}` (Option D)** (§12.3).
- Backups — **docs-only, no Velero coupling in UI; per-service guides + UX nudge on first volume** (§12.14).
- Change-details modal commit-message field — **stripped; Mortise has no git-ops commit of the spec** (§3.8).
- PR Environments toggle — **project-level only, no per-App field. `AppSpec.Preview` moves to `ProjectSpec.Preview`** (§3.14, §12.16). Breaking CRD change accepted. Scope option (a): all apps in the project reconcile into the preview namespace; only HTTP-surface apps get preview URLs. Future v2 may re-introduce per-App override as a visible toggle.
- Activity surface — **toggle-slide-out rail** (Railway parity). Pulse button in top bar (project scope). Closed by default, opens over canvas/drawer. Backend captures actor on every write (§12.22).

**Still open.**
1. Svelte Flow vs alternatives for the canvas (§12.4).
2. Canvas node layout persistence — annotation on the App CRD, or a
   separate `UIHint` CRD, or pure client-side localStorage? (§12.4)
3. Deploy tokens tab placement — under App Settings or under a separate
   "Integrations" tab? (§12.5)
4. `external` source placement — third radio on new-app, or separate
   flow? Currently drafted as a third radio (§12.6).
5. For the staged-changes Deploy button: does **source** change (repo
   branch, image tag) also stage, or apply immediately? Staging implies
   the rebuild happens on Deploy click. §12.2 says yes, confirm.
6. `${{bindings.my-db.host}}` when the binding is broken — block
   Deploy, or allow Deploy with a warning? §12.3 says block; confirm.
7. **App detail drawer vs page** (§12.21). Recommend drawer.
8. **Activity feed** (§12.22). Recommend adopt cheaply via k8s Events.
9. **Source-code var suggestions** (§12.23). Recommend reject v1.
10. **Per-project Members page vs Platform Settings → Users link** (§12.24).
    Recommend v1 uses a global link, not a per-project page.
11. **Resource usage page** (§12.25). Recommend skip v1 — kube-prom-stack
    covers it.
12. **Drawer width on narrow viewports.** Railway collapses to full-
    width on < ~1024px. Confirm the breakpoint and whether the canvas
    hides or scrolls behind.

---

## 14. Flow status tracker

| Flow                              | §    | Screenshots | Spec'd | Implemented |
|-----------------------------------|------|-------------|--------|-------------|
| Onboarding — first-run wizard     | 3.1  | ☐           | scaffold | ✅ (Phase 7) |
| Login                             | 3.2  | ☐           | scaffold | ✅ |
| Project list                      | 3.3  | ✅ `dashboard.png`, `user-dropdown.png` | scaffold | ✅ |
| Create project                    | 3.4  | ☐           | scaffold | ✅ |
| Project workspace (canvas)        | 3.5  | ✅ `canvas.png` | updated | ⚠️ list only — canvas §12.4 not built |
| New app                           | 3.6  | ✅ `project-add.png` | updated (modal picker) | ⚠️ currently a page — §12.26 |
| App detail (drawer)               | 3.7  | ✅ `project-app-select-deployments.png`, `project-app-slect-settings.png` | updated | ⚠️ page today, §12.21 |
| Variables editing                 | 3.8  | ✅ `variables.png`, `change-details.png`, `settings-shared-variables.png` | updated | ✅ (table), ⚠️ scoped ref picker + staged changes TBD |
| Service bindings                  | 3.9  | ✅ `variable-reference.png` | updated | ⚠️ picker UX TBD |
| Domains                           | 3.10 | ☐           | scaffold | ✅ custom domains; TLS overrides TBD |
| Storage                           | 3.11 | ☐ (visible on canvas only) | scaffold | ⚠️ unknown |
| Logs                              | 3.12 | ☐           | scaffold | ✅ basic SSE |
| Deploy tokens                     | 3.13 | ☐           | scaffold | ✅ |
| Preview environments              | 3.14 | ✅ `settings-environment.png` | updated | ✅ backend; UI surface TBD |
| Environment annotations           | 3.15 | ☐           | scaffold | ⚠️ no UI yet |
| Secret mounts                     | 3.16 | ☐           | scaffold | ⚠️ no UI yet |
| Platform settings                 | 3.17 | ✅ `settings.png`, `settings-members.png`, `settings-danger.png`, `settings-usage.png` | updated | ✅ partial (git providers done) |
| Extensions                        | 3.18 | ☐           | scaffold | ✅ |
| Activity feed (toggle rail)       | 12.22 | ✅ `activity.png` | updated | ✗ — backend + UI TBD |
| Staged-changes bar                | 12.2 | ✅ `canvas.png`, `change-details.png` | updated | ✗ |

---

## 15. Action items discovered in this pass

Screenshots revealed decisions not previously on the board.

### A. Resolved 2026-04-17

| # | Decision | Anchor |
|---|----------|--------|
| A1 | **App detail: drawer over canvas.** Medium layout refactor; `/apps/{a}` URL still drives drawer state. | §12.21 |
| A3 | **Variables picker: declared-only.** Three populations only — `bindings.<app>.<key>`, `secrets.<name>`, `shared.<key>`. No source-code scanning, no inferred names. | §12.23 |
| A4 | **Project Members: v1 = link to Platform Settings → Users; v2 = real per-project page** tied to Teams. Menu slot preserved for the v2 swap. | §12.24 |
| A5 | **Resource Usage page: skip.** Users run their own metrics stack. Per-App Metrics tab stays. | §12.25 |
| A6 | **New-app shell: single-modal picker** (Railway pattern). Replaces the current `/projects/{p}/apps/new` page. | §12.26 |
| A7 | **Change-details commit-message field: strip.** Mortise has no git-ops commit of the k8s spec, so the field has no destination. Not repurposed — the Deploy-note alternative was rejected as scope creep. | §3.8 |
| A8 | **PR Environments: project-level only.** `AppSpec.Preview` is removed; `ProjectSpec.Preview` replaces it. Breaking CRD change accepted. 10-persona analysis shows this burns ML / data / microservices-with-workers projects (cron + worker compute tax, possible external side-effects). Scope **option (a)**: when enabled, every App in the project reconciles into the preview namespace (crons/non-public included, so bindings resolve); only apps with an HTTP surface get preview URLs. Mitigation v1: users split heavy-cost apps into a sibling project. v2 may add a per-App override toggle as a visible UI control (not a hidden YAML field). | §12.16 |
| A2 | **Activity surface: toggle-slide-out rail.** Pulse button in top bar (project scope) opens the rail over the canvas; closed by default. Backed by a per-project audit store (ConfigMap v1) plus synthesized deploy rows. Requires adding `Principal` capture to every write handler in `internal/api/`. Tab-only alternative rejected — pulse-button toggle is a cheap upgrade once the backend exists. | §12.22 |

### A-open. Still pending your call

*(none — all action items resolved 2026-04-17)*

### B. Build-order recommendation (if A1, A6 adopted)

The UI refactor hinges on two pieces: **the canvas** and **the drawer
layout**. Suggested order:

1. **Left-rail nav reshape** (Dashboard + Project scope) — cheap
   structural change, unblocks everything else.
2. **Canvas MVP with Svelte Flow** (Q1) — render existing Apps as
   nodes, bindings as edges, no drag-position persistence yet.
3. **App detail → drawer** (A1) — reuse existing sub-pages as drawer
   contents; URL stays stable.
4. **Staged-changes bar (Tier 1, §12.2)** — client-side only, one PUT
   on Deploy. Hardest piece UX-wise but no backend.
5. **Scoped `${{}}` reference picker** (§12.3, §3.9) — new widget,
   resolver already planned.
6. **Create-modal** (A6) — replaces new-app page.
7. **Activity rail** (A2) — once the drawer/canvas layout is stable.

### C. Out-of-screenshot items still pending on your desk

Unchanged from §13 "Still open":
- Q1 Svelte Flow vs alternatives.
- Q2 Canvas node position persistence storage.
- Q3 Deploy-tokens tab placement.
- Q4 External-source placement.
- Q5 Does source-field edit stage or apply immediately?
- Q6 Broken-binding Deploy behavior (block vs warn).
- Q13 Drawer breakpoint for narrow viewports.
