# Mortise UI Spec

> Status: **draft scaffold**, generated 2026-04-17 from a docs.railway.com
> crawl cross-referenced against `SPEC.md`. Each flow is a stub — we fill
> in the **Screens** and **States** sections as screenshots arrive.
>
> This file is the source of truth for UI intent. Svelte route layout
> follows from this; divergences from this spec should update this file
> first, then the code.

---

## 0a-patch. Implementation corrections (2026-04-17)

Behavioral corrections captured from live UX review. These override any
contradictory language elsewhere in this document.

### Notifications bell
The bell icon (`<Bell>`) and its unread-count badge must appear in the
top-bar **on every authenticated page** (dashboard, project workspace,
admin, etc.), not only when `inProject` is true. Move it outside the
`{#if inProject}` guard in `+layout.svelte`.

### Dashboard left-rail nav
The dashboard nav (shown when NOT inside a project) must include a
**People** icon linking to `/admin/settings` (the Users & Invites section).
This makes the admin user-management flow discoverable from `/`.

### Platform settings — Storage section
`/admin/settings` must include a **Storage** section (below Build, above
TLS) that exposes:
- Default storage class name (maps to `PlatformConfig.spec.storage.defaultStorageClass`)
- Optional additional storage classes the operator may provision (free-text list, one per line)
This is required for users to set up storage before creating apps that use volumes.

### Project workspace toolbar
The toolbar row at the top of `/projects/{p}` (breadcrumb + staged-changes
bar + view-toggle + Add) must:
- **Remove the `Projects / {name}` breadcrumb.** The top navbar already
  shows the project name via the project-switcher dropdown. The second
  breadcrumb is redundant.
- **Float the view-toggle and Add button as an overlay** positioned in the
  top-right corner of the canvas/table content area (absolute or fixed,
  over the content, not in a dedicated toolbar row that pushes content down).
  Use `absolute top-4 right-4 z-10` inside the canvas/table container.
- The staged-changes bar stays in the header but should appear centered
  only when there are unsaved changes.

### App click — drawer in place, not navigation
Clicking an App node on the canvas OR a row in the list view must open the
`AppDrawer` as a slide-over **within the same page** (`/projects/{p}`) —
no URL change, no navigation. The drawer slides in from the right with a
translucent backdrop that closes it on click.

The URL `/projects/{p}/apps/{a}` still exists and renders the canvas with
the drawer already open (for deep-linking and back/forward navigation), but
clicking an app card from the project page must NOT trigger a full navigation.

### Environment switcher
The environment switcher in the top-bar must always include at least
`production` and `staging`. Additional environment names are collected
from all app specs in the project. Hard-coded default: `['production',
'staging']` is the floor; the API-derived set is unioned with it, never
replaces it.

### Git-provider "configure" link
In the new-app modal, when no git providers are connected, "Configure one"
must link to `/admin/settings` with the `#git-providers` anchor. If the
current user is not an admin, the link should be hidden and replaced with
"Ask your admin to connect a git provider."

### Canvas background
The SvelteFlow canvas must use `BackgroundVariant.Dots` with a visible
contrasting color. Use `patternColor="#252530"` (surface-600) on the
surface-900 page background for strong dot visibility. The current
implementation uses surface-700, which is too subtle.

### Variables tab — shared vars
The "Shared" section in the Variables tab calls
`GET /projects/:p/apps/:a/shared` which does **not exist** as a backend
endpoint. Fix: read `app.spec.sharedVars` directly from the already-loaded
`App` object (it is part of the CRD spec). Writes go via `api.updateApp`
patching `spec.sharedVars`. Remove the `getSharedVars` / `setSharedVars`
API calls.

### Variables tab — layout (stacked, not sub-tabs)
The production/shared env selector must NOT be sub-tabs. Instead, show env
vars as **stacked sections** within the tab:
1. Top section: env-specific vars (one section per environment, with a
   section heading for the env name). Show all envs the app has, each
   collapsible.
2. Bottom section: "Shared variables" — always visible below the env
   sections. These are `app.spec.sharedVars` and are available to every
   environment of this app.

### Settings tab — structuredClone error
All `api.updateApp` calls in `SettingsTab.svelte` that spread from
`app.spec` (a Svelte 5 reactive proxy) must convert it to a plain object
first. The pattern `{ ...app.spec, environments: envs }` leaves nested
proxies in the spread, causing `structuredClone` failures in some Svelte 5
contexts. Fix every save function:
```ts
// BEFORE (broken): { ...app.spec, environments: envs }
// AFTER: JSON.parse(JSON.stringify({ ...app.spec, environments: envs }))
// OR use: const plainSpec = $state.snapshot(app.spec)
```

### New-app modal — domain field
After selecting the app type and name, show a **Domain** field (optional)
that sets `spec.environments[0].domain`. Placeholder:
`app.yourdomain.com`. Helper text: "Leave blank to auto-assign a subdomain."

### New-app modal — watch paths picker
The "Watch paths" field (git source only) must change from a raw textarea
to an **interactive path picker**:
- Call `GET /api/repos/:owner/:repo/tree?provider=X&branch=Y` to fetch
  the top-level directory tree of the selected repository.
- Render a multi-select dropdown/tree showing directory entries returned.
- The user can check/uncheck directories; each checked entry becomes one
  watch-path.
- The user can also type a custom path and press Enter to add it.
- The backend endpoint (`repos.go`) must be added: it delegates to
  `GitAPI.ListTree(owner, repo, branch, path)` (new method on the
  interface), implemented for GitHub, GitLab, and Gitea.

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
| Staged changes         | Staged changes + Deploy bar  | Adopted (Tier 1 client-side MVP). See §12.2. |

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
- **Right rail:** **Activity pulse button (project scope only)** —
  toggles the Activity right-rail slide-out (§12.22). **Notifications
  bell (🔔)** — unread-count badge for deploy completions and build
  failures; dropdown shows recent items with mark-as-read. Separate
  from the Activity rail (which is a chronological project event log).
  User menu (email, theme, logout).
- **Far right:** user menu. On click, dropdown contains: Account
  Settings, Platform Settings (admin-only), Docs / Support, theme
  toggle, Log out.

### 2.1a Primary (left-rail) nav

Reference: `canvas.png`, `dashboard.png`.

**Dashboard scope** (`/`): Projects (current), Templates, Usage,
People, Settings. Mortise simplifies to **Projects, Extensions,
Platform Settings (admin-only)**. No Templates route (§12.11), no
Usage (§12.25), no People top-level (folded into Platform Settings).

**Project scope** (`/projects/{p}`): **Canvas, Settings.** Two icons
only in v1. Metrics and Logs are per-App — accessed via the app detail
drawer tabs, not the left rail. v2 may add project-wide Metrics and
Logs icons to the rail once cross-app observability lands.

### 2.2 Command palette (`⌘K` / `Ctrl+K`)

**v2.** Direct-navigate to Projects, Apps, and top create actions. Out
of scope for v1 — see §12.20.

### 2.3 Breadcrumbs

`Projects / {project} / Apps / {app}` — the only location bar the user
needs. Derived from the URL, not stored state.

### 2.4 First-run wizard (pre-auth → admin bootstrap)

Unique to self-hosted. Three steps: domain → first git provider → admin
user. See §3 Onboarding.

### 2.5 Route table

Every URL, its screen, and its layout. Transient UI state (drawer tabs,
modals, rails) is **not URL-driven** — stored in the global Svelte store
and persisted to `sessionStorage`. See §2.7.

**Minimum viewport: 1280px.** Desktop-only for v1. No responsive or
mobile layout. No tablet breakpoints.

**Bare layout** (no header, no nav — auth flows only):

| URL | Screen |
|-----|--------|
| `/login` | Login form (§3.2). Redirects to `/` if authenticated. |
| `/setup` | Admin bootstrap (§3.1). Redirects to `/login` if setup complete. |
| `/setup/wizard` | 4-step platform wizard (§3.1). Redirects to `/` on finish. |

**Dashboard layout** (header + left-rail: Projects, Extensions, Platform Settings):

| URL | Screen |
|-----|--------|
| `/` | Project list (§3.3). |
| `/projects/new` | Create project form (§3.4). |
| `/extensions` | Extensions page (§3.18). |
| `/admin/settings` | Platform settings (§3.17). Admin-only; non-admin redirects to `/`. Anchor-scrolled sections: General, Git Providers, Registry, Build, TLS, Users & Invites. |

**Project layout** (header + left-rail icons: Canvas, Settings):

| URL | Screen | Overlay |
|-----|--------|---------|
| `/projects/{p}` | Project workspace — canvas primary, list toggle (§3.5). | — |
| `/projects/{p}/apps/{a}` | Same canvas, with app detail drawer open (§3.7). | Drawer ≈45% width right side. |
| `/projects/{p}/previews` | Preview environments list (§3.14). | — |
| `/projects/{p}/settings` | Project settings (§3.17). Anchor-scrolled sections: General, Environments, Shared Variables, Members, Tokens, Webhooks, Integrations, Danger. | — |

**Transient UI state** (session-scoped, not in the URL):

| State | Trigger | Persisted to |
|-------|---------|--------------|
| Active drawer tab | Tab click in app detail drawer | `sessionStorage` via store |
| Activity rail open/closed | Pulse button toggle | `sessionStorage` via store |
| New-app modal open/closed | "+ Add" button, right-click canvas | Component-local (no persistence) |
| Canvas zoom / pan | Scroll, pinch, zoom buttons | Svelte Flow internal state |
| Staged changes (dirty app specs) | Any settings edit in the drawer | Global store, in-memory only (lost on tab close — Tier 1, §12.2) |
| List vs canvas view preference | Toggle in workspace header | `sessionStorage` via store |

**Layout → left-rail mapping:**

| Layout | Left-rail content |
|--------|-------------------|
| Bare | None |
| Dashboard | Projects · Extensions · Platform Settings (admin-only) |
| Project | Canvas · Settings |

### 2.6 Design system

Dark-only. Dense. Railway-grade polish. No external component library —
all components built from Tailwind utilities referencing the design tokens
below. No raw hex/rgb values in component code — every color comes from
a token.

#### 2.6a Color tokens

Defined in `ui/src/app.css` via Tailwind v4 `@theme`. This is the
single source of truth for every color in the UI.

```css
@theme {
  /* Surface scale — backgrounds and borders */
  --color-surface-900: #0a0a0f;   /* page background */
  --color-surface-800: #12121a;   /* cards, panels, inputs */
  --color-surface-700: #1a1a25;   /* hover states, secondary bg, raised elements */
  --color-surface-600: #252530;   /* borders, dividers */
  --color-surface-500: #3a3a48;   /* active/focus borders, stronger dividers */

  /* Accent — primary interactive color (purple) */
  --color-accent: #8b5cf6;
  --color-accent-hover: #a78bfa;

  /* Status — semantic feedback */
  --color-success: #22c55e;
  --color-warning: #f59e0b;
  --color-danger: #ef4444;
  --color-info: #3b82f6;
}
```

Status colors are used at 10% opacity for backgrounds (`bg-success/10`)
and full opacity for text (`text-success`).

Text colors use Tailwind's built-in gray scale on the dark surface:
- `text-white` — headings, active nav items, primary emphasis
- `text-gray-200` — high-contrast body text
- `text-gray-300` — standard body text
- `text-gray-400` — labels, nav items, secondary text
- `text-gray-500` — helper text, placeholders, disabled content

#### 2.6b Typography

System font stack (Tailwind `font-sans`). Monospace (`font-mono`) for
code, env var keys, image refs, CLI snippets, and log output.

| Role | Classes | Usage |
|------|---------|-------|
| Page title | `text-xl font-semibold text-white` | One per page/view |
| Section heading | `text-sm font-medium text-gray-300` | Group labels, card headers |
| Body | `text-sm text-gray-300` | Default text |
| Label | `text-sm text-gray-400` | Form labels, table headers |
| Helper | `text-xs text-gray-500` | Below inputs, footnotes |
| Code / mono | `font-mono text-sm text-gray-300` | Env vars, image refs, domains |
| Badge text | `text-xs font-medium` | Inside status chips |

The UI is dense — `text-sm` is the workhorse. No `text-base` or
`text-lg` outside the page title.

#### 2.6c Component patterns

Every interactive element uses one of these patterns. Components MUST
use these class strings — do not improvise variants.

**Buttons:**

| Variant | Classes |
|---------|---------|
| Primary | `rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50 disabled:cursor-not-allowed` |
| Secondary | `rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 hover:bg-surface-700 hover:text-white` |
| Danger | `rounded-md bg-danger px-4 py-2 text-sm font-medium text-white hover:bg-danger/80` |
| Ghost | `rounded-md px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white` |
| Icon-only | `rounded-md p-2 text-gray-500 hover:bg-surface-700 hover:text-white` |

**Inputs:**

| Variant | Classes |
|---------|---------|
| Text | `w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent` |
| Textarea | `w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent` |
| Select | `rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent` |

**Cards:**

| Variant | Classes |
|---------|---------|
| Static | `rounded-lg border border-surface-600 bg-surface-800 p-5` |
| Clickable | `rounded-lg border border-surface-600 bg-surface-800 p-5 cursor-pointer transition-all duration-150 hover:-translate-y-0.5 hover:border-surface-500 hover:shadow-lg hover:shadow-black/20` |
| Selected | `rounded-lg border border-accent bg-surface-800 p-5` |

**Status badges** — map `App.status.phase` → badge variant:

| Phase / status | Classes |
|----------------|---------|
| Ready / Success | `inline-flex items-center rounded-full bg-success/10 px-2.5 py-0.5 text-xs font-medium text-success` |
| Building / Warning | `inline-flex items-center rounded-full bg-warning/10 px-2.5 py-0.5 text-xs font-medium text-warning` |
| Failed / Crashed | `inline-flex items-center rounded-full bg-danger/10 px-2.5 py-0.5 text-xs font-medium text-danger` |
| Pending / Info | `inline-flex items-center rounded-full bg-info/10 px-2.5 py-0.5 text-xs font-medium text-info` |
| Neutral | `inline-flex items-center rounded-full bg-surface-700 px-2.5 py-0.5 text-xs font-medium text-gray-400` |

**Tabs:**

| State | Classes |
|-------|---------|
| Active | `rounded px-2.5 py-1 text-xs bg-surface-600 text-white` |
| Inactive | `rounded px-2.5 py-1 text-xs text-gray-400 hover:text-white` |

**Dropdowns:**

| Part | Classes |
|------|---------|
| Container | `absolute z-20 mt-1 overflow-hidden rounded-md border border-surface-600 bg-surface-800 shadow-lg` |
| Item | `flex w-full items-center gap-2 px-3 py-2 text-sm text-gray-300 hover:bg-surface-700 hover:text-white cursor-pointer` |
| Active item | `bg-surface-600 text-white` |
| Divider | `border-t border-surface-600` |

**Modals:**

| Part | Classes |
|------|---------|
| Backdrop | `fixed inset-0 z-40 bg-black/60` |
| Wrapper | `fixed inset-0 z-50 flex items-center justify-center p-4` |
| Panel | `w-full max-w-lg rounded-lg border border-surface-600 bg-surface-800 p-6 shadow-xl` |
| Title | `text-lg font-semibold text-white` |
| Danger title | `text-lg font-semibold text-danger` |

Destructive modals (delete project, delete app): require the user to
type the resource name to confirm.

**Toast notifications:**

Bottom-right stack, auto-dismiss 5s, manual dismiss via ×.

| Part | Classes |
|------|---------|
| Stack container | `fixed bottom-4 right-4 z-50 flex flex-col gap-2` |
| Toast base | `rounded-lg border border-surface-600 bg-surface-800 px-4 py-3 text-sm shadow-lg` |
| Success variant | base + `border-l-4 border-l-success` |
| Error variant | base + `border-l-4 border-l-danger` |
| Info variant | base + `border-l-4 border-l-info` |

**Loading states:**

| Type | Classes | When to use |
|------|---------|-------------|
| Skeleton | `animate-pulse rounded bg-surface-700` | Cards, tables, text blocks during page load |
| Spinner | `inline-block h-4 w-4 animate-spin rounded-full border-2 border-gray-500 border-t-transparent` | Buttons, inline indicators |
| Full-page | Centered spinner + `text-sm text-gray-500` message | Initial route load |

**Empty states:**

Centered in the content area. Muted icon (48px, `text-gray-600`) +
`text-sm font-medium text-gray-400` heading + `text-xs text-gray-500`
subtext + optional primary CTA button.

#### 2.6d Spacing & layout

- Page padding: `p-8`.
- Card grid: `grid gap-4 grid-cols-1 sm:grid-cols-2 lg:grid-cols-3`.
- Section gaps: `space-y-6` between major sections.
- Form field gaps: `space-y-4` between fields.
- Inline element gaps: `gap-2` (button groups), `gap-3` (nav items).
- Max content width: `max-w-5xl mx-auto` for forms and settings.
  Canvas and workspace are full-width.
- Header height: `h-14`.
- Left rail width: `w-14` (icons only, no labels).
- Drawer width: `w-[45%]` (fixed — no responsive collapse in v1).
- Activity rail width: `w-80`.

#### 2.6e Transitions

Minimal and fast. No page transitions (SvelteKit client-side nav is
instant).

| Element | Classes / approach |
|---------|--------------------|
| Hover (buttons, cards) | `transition-all duration-150` |
| Drawer slide-in | `transition-transform duration-200 ease-out` (translate-x 100% → 0) |
| Activity rail | Same as drawer |
| Modal | Backdrop: `transition-opacity duration-150`. Panel: `transition-all duration-200 ease-out` (scale 95% → 100% + fade). |
| Toast enter | Slide-in from right, 200ms |

#### 2.6f Iconography

**Lucide** (`lucide-svelte`). No mixing icon libraries. Sizes:
`16px` (`w-4 h-4`) inline, `20px` (`w-5 h-5`) in buttons, `24px`
(`w-6 h-6`) in the left rail.

Canonical icon mappings:

| Concept | Lucide icon name |
|---------|------------------|
| App (service) | `Box` |
| App (cron) | `Clock` |
| App (external) | `Cloud` |
| Project | `Folder` |
| Git source | `GitBranch` |
| Image source | `Container` |
| Deploy | `Rocket` |
| Settings | `Settings` |
| Variables / Env | `Key` |
| Metrics | `BarChart3` |
| Logs | `Terminal` |
| Add / Create | `Plus` |
| Delete | `Trash2` |
| Close / Dismiss | `X` |
| Bindings / Link | `Link` |
| Domain / Globe | `Globe` |
| Storage / Volume | `HardDrive` |
| Activity | `Activity` |
| User | `User` |
| Danger / Warning | `AlertTriangle` |
| Canvas view | `LayoutDashboard` |
| List view | `List` |
| Copy | `Copy` |
| Search | `Search` |
| Expand / Chevron | `ChevronDown` |
| Notifications | `Bell` |

### 2.7 State management

All shared UI state lives in a **single global Svelte store** at
`ui/src/lib/store.svelte.ts`, built on Svelte 5 runes. Components read
from the store reactively; mutations go through store methods only.

This replaces the current `context.svelte.ts` single-value store. The
`api.ts` module remains separate — the store is a state container, not
a data-fetching layer.

#### 2.7a Store shape

```typescript
// ui/src/lib/store.svelte.ts
import { browser } from '$app/environment';

class MortiseStore {
  // ── Auth ──────────────────────────────────────────────
  token = $state<string | null>(null);
  user = $state<{ email: string; role: 'admin' | 'member' } | null>(null);

  get isAdmin(): boolean { return this.user?.role === 'admin'; }
  get isAuthenticated(): boolean { return this.token !== null; }

  // ── Navigation ────────────────────────────────────────
  currentProject = $state<string | null>(null);
  projects = $state<Project[]>([]);

  // ── Staged changes (§12.2, Tier 1 — in-memory only) ──
  stagedChanges = $state<Map<string, StagedChange>>(new Map());
  get stagedChangeCount(): number { return this.stagedChanges.size; }
  get hasUnsavedChanges(): boolean { return this.stagedChanges.size > 0; }

  // ── UI preferences (session-scoped) ───────────────────
  drawerTab = $state<'deployments' | 'variables' | 'logs' | 'metrics' | 'settings'>('deployments');
  activityRailOpen = $state(false);
  viewMode = $state<'canvas' | 'list'>('canvas');

  constructor() {
    if (browser) {
      this.token = localStorage.getItem('mortise_token');
      this.currentProject = localStorage.getItem('mortise_project');
      this.viewMode =
        (sessionStorage.getItem('mortise_view') as 'canvas' | 'list') ?? 'canvas';
      this.drawerTab =
        (sessionStorage.getItem('mortise_tab') as typeof this.drawerTab) ?? 'deployments';
      this.activityRailOpen =
        sessionStorage.getItem('mortise_activity') === 'true';
    }
  }

  // Auth
  login(token: string, user: { email: string; role: 'admin' | 'member' }) { ... }
  logout() { ... }

  // Navigation
  setProject(name: string | null) { ... }
  setProjects(list: Project[]) { ... }

  // Staged changes
  stageChange(appName: string, original: AppSpec, dirty: AppSpec) { ... }
  discardChange(appName: string) { ... }
  discardAll() { ... }

  // UI preferences (each setter persists to sessionStorage)
  setDrawerTab(tab: typeof this.drawerTab) { ... }
  toggleActivityRail() { ... }
  setViewMode(mode: typeof this.viewMode) { ... }
}

export const store = new MortiseStore();
```

```typescript
interface StagedChange {
  appName: string;
  original: AppSpec;   // snapshot from last GET
  dirty: AppSpec;      // user's current edits
}
```

A `beforeunload` handler warns when `store.hasUnsavedChanges` is true,
preventing accidental tab-close data loss (§12.2 Tier 1).

#### 2.7b State ownership rules

| State | Owner | Persistence | Survives |
|-------|-------|-------------|----------|
| Auth (token, user) | Global store | `localStorage` | Tab close, refresh |
| Current project | Global store | `localStorage` | Tab close, refresh |
| Projects list | Global store | In-memory (refetched on mount) | Refresh only |
| Staged changes | Global store | In-memory only | Refresh only (lost on tab close) |
| UI prefs (view mode, drawer tab, rail) | Global store | `sessionStorage` | Refresh (lost on tab close) |
| Form field values | Component-local `$state` | None | Unmount resets |
| Modal open/closed | Component-local `$state` | None | — |
| API response data | `$state` in `+page.svelte` load | None | Refetched on navigate |
| Canvas node positions | API (annotation on App CRD) | Server-side | Everything |

#### 2.7c API integration pattern

The store does **not** make API calls. `ui/src/lib/api.ts` handles all
HTTP communication. Components call `api.*`, then update the store:

```typescript
// In a component or +page.svelte:
const projects = await api.listProjects();
store.setProjects(projects);
```

`api.ts` responsibilities:
- Reads `store.token` for Bearer injection.
- On 401: calls `store.logout()` + redirects to `/login`.
- Throws typed errors for UI-layer consumption (toast display, inline
  error rendering).
- Does NOT write to the store — that is the caller's job.

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
3. `/setup/wizard` step 2 — connect a git provider (optional — skippable).
4. `/setup/wizard` step 3 — done; CTA to dashboard.

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

### 3.5 Project workspace (canvas + list)

**Goal.** See all apps in a project; pick one to work on; create a new one.

**Entry points.** `/projects/{p}`.

**Screens.** Reference: `canvas.png`.

**Canvas view (primary).** Full-spec in §12.4.
1. **Svelte Flow canvas** fills the viewport below the header. Apps
   render as custom nodes (240px wide, §12.4 node design), bindings
   render as animated smoothstep edges. Background: dot-grid on
   `surface-900`.
2. **Top controls:** staged-changes bar (center, when dirty — §12.2),
   canvas/list view toggle (right), "+ Add" button (right, opens
   new-app modal — §3.6).
3. **Bottom-left controls:** Svelte Flow `<Controls>` (zoom in/out, fit
   view, grid snap) + `<MiniMap>`.
4. **Interactions:** click node → drawer (§3.7), drag node → reposition
   (PATCH annotation), right-click empty → "New App here", right-click
   node → context menu. See §12.4 Interaction.
5. **Auto-layout:** dagre top-to-bottom when Apps have no `ui-x`/`ui-y`
   annotations. Manual positions take over after first drag.

**List view (fallback toggle).** Toggle icon in the top controls
switches to a table (persisted to `sessionStorage` via store):

| Column | Content |
|--------|---------|
| Name | App name (link opens drawer) |
| Source | Badge: git / image / external |
| Kind | Badge: service / cron |
| Status | Phase badge (§2.6c) |
| Domain | Primary domain or "Private" |
| Last deploy | Relative timestamp |

**States.** Empty (no Apps): centered empty state (§2.6c) with
"Create your first app" CTA → new-app modal. Loading: skeleton
node placeholders on canvas, skeleton rows in list view.

**Mortise divergence.** Canvas is primary, list is fallback — matches
Railway. Full canvas spec in §12.4.

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
   option list. Mortise options (dropping Railway's Function, Bucket,
   and Volume which we don't offer as standalone — §12.28):
   - **Git Repository**
   - **Database** (Postgres / Redis / MinIO template cards — §12.11)
   - **Template** (flat curated list)
   - **Docker Image**
   - **External Service** (§12.6)
   - **Empty App** (blank scaffold for advanced users)
2. **Configure pane** (replaces the picker panel in the same modal
   on selection). Source-specific fields below. Common footer: app
   **name** (auto-derived where possible, editable; DNS-1123, ≤55 chars),
   **kind** selector (Service / Cron — appears for Git and Image only;
   External is always Service), **environment** picker (default
   `production`). Cron kind replaces replicas/domain fields with a
   **schedule** input (cron expression, e.g. `*/5 * * * *`) — §12.17.
3. Submit → POST `/api/projects/{p}/apps` → modal closes → new App tile
   appears on canvas at click position (or top-right if opened via `+ Add`).

**Configure pane — per source type:**

| Source | Fields |
|--------|--------|
| **Git Repository** | Repo picker (lists repos from connected GitProviders via `GitAPI`), branch (default `main`), root directory (optional), build config preview (read-only Nixpacks/Buildpacks/Dockerfile auto-detect — editable later in Settings). Name auto-derived from repo name. |
| **Docker Image** | Image ref input (`nginx:1.27`, `ghcr.io/org/app@sha256:...`), pull secret selector (optional — dropdown of existing k8s Secrets in the project namespace, or "+ New" inline). Name auto-derived from image basename. |
| **External Service** | Host input (FQDN or IP), port input, credentials editor (declare which `credentials[]` keys this facade exposes — the binding contract for other apps, §12.3). Name required. |
| **Database / Template card** | Pre-fills image ref, default storage entry, `credentials:` block, and `network.public: false` from the template definition. User reviews name, storage size, and (for Postgres) initial DB name. |
| **Empty App** | Name only. User configures source, storage, bindings via the drawer after creation. Useful when copying fields from another App's YAML, or for gitops-seeded shells. |

**Volumes on the canvas.** Attached volumes (`spec.storage[]`) render as
**pills inside the App tile** per §12.4 — not as separate canvas nodes
in v1. This is a deliberate rendering choice that matches Railway's
`canvas.png` reference.

**States.** Errors inline in the configure pane (name taken, invalid
image ref, repo unreachable).

**Mortise divergence.** (1) **Shift from tabbed page to single-modal
picker** — §12.26. (2) "External service" and the kind selector
(service vs cron) are Mortise-specific — §12.6, §12.17. (3) **No
standalone Volume option** in v1 — §12.28.

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
closes. Five top tabs: **Deployments / Variables / Logs / Metrics /
Settings.** Active tab is stored in the global Svelte store (session-
scoped, not URL-driven — see §2.7).

- **Deployments** (default) — current deploy row: Active chip, author
  avatar, trigger ("{actor}, 29 min ago via {source}"), kebab
  (Redeploy / Rollback / Abort). Region + replica badges top-right
  ("us-west2 · 1 Replica" equivalent → Mortise:
  `{storageClass?} · N replicas`). Expandable status row
  ("Deployment successful ✓"). Below: history list.
- **Variables** (§3.8).
- **Logs** (§3.12) — live + recent logs for this App, inline in the
  drawer. Environment selector, log category toggle, replica picker,
  search, live-tail toggle.
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
(2) Logs are a top-level drawer tab (5 tabs), not a separate bottom
panel — keeps all App context inside one slide-over. (3) Railway shows
disk + network metrics; Mortise v1 shows only CPU + memory (§12.13).
(4) No "Agent" affordance (Railway AI assistant).

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
missing binding) render with a red "reference broken" chip. Deploy
stays enabled but shows a confirmation modal: "N variables have broken
references — the app may fail to start. Deploy anyway?" (§12.3).
Secrets display masked.

**Mortise divergence.** Railway's `${{service.VAR}}` is free-text over
any var of any service; Mortise's scoped `${{bindings|secrets|shared}}`
is a contract picker over declared surfaces. §12.3.

### 3.9 Service bindings (connect Apps)

**Goal.** Let App A consume credentials/DNS from App B.

Bindings have two surfaces in the UI. Both are contract-driven — they
surface declared `credentials:` keys, never free-text (§12.3).

#### 3.9a Variables-tab dropdown (usage surface)

Reference: `variable-reference.png`.

**Entry points.**
- "Add Reference" button in the Variables tab new-variable row (§3.8).
- Typing `${{` in any value input (autocomplete mode, same dropdown).

**Widget.** Dropdown anchored to the trigger (button or cursor). Width
≈360px, max-height ≈400px with scroll.

1. **Filter input** at top — placeholder "Search bindings, secrets,
   shared variables". Live-filters the three sections below.
2. **Sections** (collapsible, all expanded by default):
   - **Bindings** — for each App in the project with a `credentials:`
     block, a parent row (app name + source icon) and child rows for
     each declared credential key. Selecting a child inserts
     `${{bindings.<app>.<key>}}` at the cursor.
   - **Secrets** — one row per Secret in this App's secret store.
     Selecting inserts `${{secrets.<name>}}`.
   - **Shared** — one row per key in project `sharedVars`. Selecting
     inserts `${{shared.<key>}}`.
3. **Row format:** `KEY` left, `SourceApp` right (monospace key, gray
   source label). Matches `variable-reference.png`.
4. **Empty state per section:** muted "No bindings available — add a
   binding in Settings → Bindings" (and analogous for secrets/shared).
5. **"Show N More"** expander per section when a section has >5 rows,
   per `variable-reference.png`.
6. **Keyboard:** `↑`/`↓` to move focus, `Enter` to insert, `Esc` to
   close. Arrow keys traverse across collapsed sections.

This dropdown is the **usage** surface — picking which declared
surface a variable consumes. It does not add or remove bindings on the
App spec; that is the structural surface below.

#### 3.9b Settings → Bindings panel (structural surface)

**Entry points.** App detail drawer → Settings tab → Bindings anchor
section.

**Goal.** Manage the `bindings[]` array on the App spec — which other
Apps this App is bound to. Adding a binding here makes the bound App's
`credentials[]` keys available in the Variables-tab dropdown above.

**Screens.**
1. **Bindings list** — one row per entry in `spec.bindings[]`:
   `{ref}` (app name + source icon), optional `{project}` badge for
   cross-project bindings, kebab (Edit / Remove). Empty state: "No
   bindings yet — bind another app to inject its credentials."
2. **"Add binding" button** → modal with an app picker: lists Apps in
   this project (default), "Bind from another project" expander that
   swaps to a project picker + app picker. **Only Apps with
   `credentials:` declared appear.**
3. **Preview panel** in the modal — once an App is selected, shows the
   credential keys that will be injected (matching the bound App's
   `credentials:` list). User picks:
   - **Short form** (default) — inject all declared keys as-is.
     Writes `{ ref: my-db }` to the spec.
   - **Long form** — advanced toggle. Injects one credential key under
     a renamed env var (`DATABASE_URL` → `PG_URL`). Writes
     `valueFrom.fromBinding` entries under `env[]`.
4. Submit → stages the change (§12.2). On Deploy, the bound App's
   credentials become resolvable in `${{bindings.<app>.<key>}}`
   references.

**Removing a binding** — kebab → Remove → stages. If any env vars in
the App still reference `${{bindings.<removed>.<key>}}`, they render
with a red "reference broken" chip (§12.3). Deploy shows the heavy
warning modal.

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

**Goal.** See live and recent logs for an App, including crash diagnostics
and build history.

**Entry points.** App detail drawer → **Logs** tab (3rd tab). Also
reachable from the Deployments tab deploy row kebab → "View logs"
(switches to Logs tab with that deploy's time range pre-selected).

**Screens.** Rendered inline in the drawer — same ≈45% width slide-over
as all other drawer tabs, not a separate panel.

1. **Tab bar (top):** `Live` | `Build` | `History` (History only shown
   when log adapter is configured in PlatformConfig).
2. **Live tab** — SSE stream of running pod stdout/stderr.
   - Header: environment selector, time-range chips (15 min / 1 h /
     6 h / 24 h), live-tail toggle, Copy, Clear.
   - "Previous" toggle: shown only when the selected pod has restart
     count > 0. Switches to the previous container's logs. Invaluable
     for crash diagnosis.
   - Time-range chips switch from SSE to one-shot fetch (`sinceTime`
     param); live-tail toggle re-enables streaming.
3. **Log line rendering:**
   - Each line prefixed with a fixed-width timestamp gutter
     (`14:32:01 · 2m ago`), derived from `?timestamps=true`.
   - If a line parses as JSON: render as an expandable key/value row.
     `level` field drives left-border color: `error`=red, `warn`=amber,
     `info`=gray, `debug`=dimmed. Expand arrow reveals all fields.
   - Plain text lines render as-is in monospace.
4. **Build tab** — persisted build log lines from the most recent build
   (stored in `buildlogs-{app}` ConfigMap, capped at 1 000 lines).
   Static scrollable list, no streaming. Shows build timestamp + commit
   SHA in the header.
5. **History tab** (when log adapter configured) — time-range query
   against the adapter endpoint. Header: start/end date pickers,
   freetext filter, pod filter. Body: same log line renderer as Live.
   Footer: "Showing N lines" + "Load more" if `hasMore=true`.
6. Footer (all tabs): download/copy affordance.

**States.** Empty (no logs yet — "Deploy this app to see logs" +
deploy CTA), loading (spinner), streaming (live-tail dot pulses),
disconnected (reconnect banner), error.

**Mortise divergence.** Railway has a top-nav Observability → Log
Explorer spanning **all services in the workspace**. Mortise v1 is
per-App only, inside the drawer. Cross-app historical search is a
docs-recipe (install a log agent). §12.12.

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
  list) + external secrets (cross-links).
- **Danger** — Manage Services (per-app Remove with 2-step confirm) +
  Delete Project (destructive).

**Platform Settings** (admin-only, `/admin/settings`):
- **General** — platform domain, description.
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
  with a red "reference broken" chip. Deploy stays enabled but shows
  a confirmation modal: "N variables have broken references — the app
  may fail to start. Deploy anyway?"

**Short vs long form bindings remain.** `bindings: [{ ref: my-db }]`
(short form) is the "accept all declared credentials" default — still
the primary picker outcome. Power users can drop to long-form
`fromBinding` or compose via `${{}}`.

### 12.4 Project canvas (graph view)

**Decision: canvas is primary, list is the fallback toggle.**

**Library: Svelte Flow** (`@xyflow/svelte`). The Svelte port of React
Flow — mature, actively maintained, handles zoom/pan/drag/minimap/
controls out of the box. No hand-rolling.

Railway's signature UX is the zoomable canvas where services are tiles
and bindings are edges. We're building it.

#### Node design (App tiles)

Each App renders as a custom Svelte Flow node. All nodes are the same
base width; height varies by content.

**Dimensions:**
- Width: `240px` fixed.
- Min height: `120px` (name + source badge + status only).
- Max height: grows with content (volumes, domain, replica badge).
- Border radius: `rounded-lg` (8px).
- Background: `surface-800`. Border: `surface-600` (1px).
- Selected: border changes to `accent`.
- Hover: `border-surface-500`, subtle `shadow-lg shadow-black/20`.

**Tile content (top to bottom):**
1. **Header row:** App name (`text-sm font-medium text-white`),
   source-type icon (Git: `GitBranch`, Image: `Container`, External:
   `Cloud`), kind badge for cron (`Clock` icon, `text-xs`).
2. **Status chip:** phase badge per §2.6c status badges table.
3. **Domain** (if `network.public: true`): `text-xs font-mono
   text-gray-500 truncate` — shows the primary domain, truncated.
4. **Replica badge:** `text-xs text-gray-500` — "N replicas".
5. **Volumes** (if any): rendered as small pills inside the tile
   (`text-xs bg-surface-700 rounded px-1.5 py-0.5`), each showing
   volume name + size.

**Variant chrome:**
- `source.type: external` — dashed border (`border-dashed`) + `Cloud`
  icon in header. Signals "not running in cluster."
- `kind: cron` — `Clock` icon badge next to name.
- `network.public: false` — no domain row; "Private" label in
  `text-xs text-gray-500`.

#### Edge design (bindings)

Each binding (`bindings[].ref`) renders as a Svelte Flow edge from the
binder App to the bound App.

- **Line style:** `stroke: surface-500`, `strokeWidth: 1.5`,
  animated dash (`animated: true` in Svelte Flow).
- **Arrow:** marker-end arrowhead pointing at the bound (target) App.
- **Hover:** stroke brightens to `accent`, tooltip shows injected
  credential keys (`DATABASE_URL, host, port, ...`).
- **Selected:** stroke `accent`, `strokeWidth: 2`.
- **Edge type:** `smoothstep` (Svelte Flow built-in — avoids node
  overlap better than `bezier` for horizontal layouts).

Cross-project bindings (if visible) render with a dotted line and a
project badge on the edge label.

#### Interaction

- **Click node** → opens App detail drawer (URL changes to
  `/projects/{p}/apps/{a}`).
- **Drag node** → reposition. On drag-end, PATCH the App's
  `mortise.dev/ui-x` and `mortise.dev/ui-y` annotations via the API.
  Positions persist server-side so all users see the same layout.
- **Right-click empty canvas** → context menu: "New App here"
  (opens the create modal, pre-sets position).
- **Right-click node** → context menu: Deploy, Rollback, Open drawer,
  Delete.
- **Canvas background:** `surface-900`. Dot grid pattern
  (`bg-[radial-gradient(circle,_var(--color-surface-700)_1px,_transparent_1px)]
  bg-[size:20px_20px]`).
- **Auto-layout:** when Apps have no `ui-x`/`ui-y` annotations (new
  project, or first canvas load), apply Svelte Flow's `dagre` layout
  algorithm (top-to-bottom, binding edges as hierarchy). Once a user
  drags any node, auto-layout stops — manual positions take over.

#### Controls (bottom-left stack, §12.27)

Svelte Flow ships `<Controls>` and `<MiniMap>` components. Use them
directly:

- Zoom in (+) / Zoom out (−) / Fit view — Svelte Flow `<Controls>`.
- Minimap — `<MiniMap>` component, `surface-800` background, node
  color derived from status (success green, danger red, default gray).
- Grid snap toggle — Svelte Flow `snapToGrid` prop.

Undo/redo (for node positioning only) is deferred — Svelte Flow
doesn't ship it natively, and the staged-changes system handles spec
edits. Not worth the complexity for v1.

#### List view fallback

Toggle in the project workspace header (canvas icon / list icon).
Stored in the global store (`viewMode`, session-scoped — §2.7). List
view renders a table:

| Column | Content |
|--------|---------|
| Name | App name (link opens drawer) |
| Source | Badge: git / image / external |
| Kind | Badge: service / cron |
| Status | Phase badge (§2.6c) |
| Domain | Primary domain or "Private" |
| Last deploy | Relative timestamp |

### 12.5 Deploy tokens (external CI)

Railway doesn't have explicit per-App+env bearer tokens; their external
deploys go through their CLI/GitHub app. Mortise makes deploy tokens a
first-class concept (SPEC §5.4, already implemented). This is **a
feature**, not a gap — keep and highlight.

**Decision: keep under App Settings.** Deploy tokens are a simple
list + create/revoke — doesn't warrant a separate tab.

### 12.6 `external` source type

Mortise supports an App that is a facade over an upstream service (RDS,
ElastiCache, an internal API). Railway has no equivalent. Needs its own
new-app flow tab and a distinct badge/chip in lists.

**Decision: third radio** alongside git/image in the new-app modal.
SPEC §5.5a is written as a source type, so a third radio fits
naturally.

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

**Mortise adoption.** Use Svelte Flow's built-in `<Controls>` and
`<MiniMap>` components (§12.4). Undo/redo for node positioning deferred
— not worth the complexity for v1. Volumes-as-layers toggle declutters
the canvas for users who don't care about storage edges.

### 12.28 Standalone Volume — dropped from v1

**Observed Railway behavior.** The create-picker offers `Volume` as a
standalone option (see `project-add.png`). Creating a Volume produces a
PVC with no attached service; users later attach the volume to services
via each service's Settings.

**Decision: drop from the v1 picker.** Mortise volumes attach via App
Settings → Storage only. No "create a volume first" flow. Dropping it
has three knock-on impacts:

#### A. SPEC / CRD impact

- **No new CRD.** Keeping the "everything is an App" rule intact
  (CLAUDE.md architecture rules). A `Volume` CRD would be the first
  crack in that invariant.
- **No orphan-PVC reconciler.** Today the App controller creates one
  PVC per `spec.storage[]` entry, owns it via
  `controllerutil.SetControllerReference`, and garbage-collects on App
  delete. An orphan-volume flow would need a parallel reconciler with
  its own ownership/cleanup story, plus rules for what happens when
  the last App unbinds.
- **Avoided: adoption semantics.** A standalone volume + "attach to
  App" would require Mortise to decide who owns the PVC after
  attachment. That's two-way ownership (the Volume CR on one side, the
  App's `storage[]` on the other) and complicates the "Mortise owns
  only what it creates" invariant.

#### B. API impact (deferred)

Not building in v1:
- `POST /api/projects/{p}/volumes` — create standalone PVC.
- `GET /api/projects/{p}/volumes` — list orphan + attached volumes.
- `DELETE /api/projects/{p}/volumes/{v}` — with detach-first safety.
- `POST /api/projects/{p}/apps/{a}/volumes/attach` — attach an
  existing volume to an App.

That's ~5 new endpoints, a new `VolumeRef` resolver on the App
controller, and a detach path that has to refuse-when-mounted. All
deferred.

#### C. Canvas impact (avoided)

- Volumes stay rendered **inside the App tile as pills** per §12.4
  node spec — matching Railway's `canvas.png`.
- No separate volume node type. No volume-attach edge type. No
  drag-volume-onto-app gesture. No "orphan volumes" cluster on the
  canvas.

If we added standalone volumes later, the canvas would need: (1) a new
node kind with distinct chrome, (2) a new edge type for mount
relationships separate from binding edges, (3) a "floating" visual
zone for unattached volumes, (4) drag-to-attach and drag-to-detach
gestures. Non-trivial UX work.

#### D. Persona burn analysis

**Who wants a standalone Volume in the picker?** Three personas:

| Persona | Use case | Burned by the drop? | v1 workaround |
|---------|----------|---------------------|---------------|
| **Homelabber adopting existing data** | Has an existing PVC (e.g. from a manually-deployed Jellyfin) and wants Mortise to manage the new App that mounts it. | **Partial burn.** | Cover with an **"Adopt existing PVC" affordance under App Settings → Storage** — select from PVCs already in the project namespace. Low-cost addition, covers the highest-value case. |
| **Data migration / volume swap** | Creates volume, fills via App A, detaches, attaches to App B. | **Hard burn.** | Create App B with the same PVC name in `storage[]`; coordinate with App A's deletion. Clunky but works for one-offs. This is a k8s-admin workflow; rare in the Railway-style user base Mortise targets. |
| **RWX shared storage between Apps** | Two Apps reading/writing the same volume concurrently (media transcoding, build caches). | **Hard burn.** | No clean v1 story. Both Apps declare the same PVC name with matching storage class + RWX access mode — but ownership fights (whichever App reconciles second overwrites the first's PVC spec). Genuine gap. |

**Aggregate burn: 1 partial + 2 hard out of ~10 personas from SPEC §3.**
The hard burns (migration, RWX) are advanced k8s workflows that
Railway itself doesn't handle well either — they're not core to the
Railway-parity mission.

#### E. v1 mitigation — "Adopt existing PVC"

To cover the homelabber persona with minimum scope:

- App Settings → Storage → "Add volume" dialog gains a second tab:
  **"Adopt existing PVC"**.
- Lists PVCs in the project namespace **not currently owned by a
  Mortise App**.
- On selection, the existing PVC gets an `mortise.dev/owned-by: <app>`
  annotation added; the App's `spec.storage[]` gains an entry
  referencing the PVC by name; mount path is user-supplied.
- Detaching (removing from `storage[]`) removes the annotation but
  does **not** delete the PVC. This is the one exception to the "owned
  == garbage-collected" rule, flagged explicitly in the UI ("This PVC
  was adopted and will be left in place when removed").

~1 day of work. Covers the highest-value standalone-Volume use case
without introducing any new CRDs, reconcilers, or canvas node types.

#### F. v2 door left open

If RWX/migration demand materializes, v2 can add a real `Volume` CRD
with clear ownership rules (volume-owns-PVC; apps reference volumes by
name). The v1 decision doesn't preclude this — it just avoids
speculative complexity.

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

**Resolved 2026-04-17 (pass 2).**
- Q1: **Svelte Flow** (`@xyflow/svelte`) for the canvas. Node/edge design spec added to §12.4.
- Q2: **App CRD annotations** (`mortise.dev/ui-x`, `mortise.dev/ui-y`) for canvas position persistence. PATCH on drag-end. Auto-layout (dagre) for new projects with no positions set.
- Q7: **Drawer confirmed.** App detail is a right-side slide-over (§12.21).
- Q12: **Desktop-only for v1.** Minimum viewport 1280px. No responsive/mobile layout. Drawer is ≈45% width; no collapse-to-full-width behavior in v1.
- Logs tab: **Logs is a 5th tab** in the app detail drawer (Deployments / Variables / Logs / Metrics / Settings). Not a bottom panel. §3.7, §3.12 updated.
- Left-rail project scope: **Canvas + Settings only** in v1. Metrics/Logs icons deferred to v2 when project-wide observability lands. §2.1a updated.
- Font: **System font stack** (Tailwind `font-sans`). No explicit font loading.
- Notifications bell: **Keep.** Unread-count badge for deploy completions + build failures. Separate from Activity rail. §2.1 updated.
- Icon library: **Lucide** (`lucide-svelte`). Canonical mappings in §2.6f.

**Resolved 2026-04-17 (pass 3).**
- Q3: **Deploy tokens stay under App Settings.** No separate Integrations tab. Deploy tokens are a simple list + create/revoke — doesn't warrant a new tab. §12.5.
- Q4: **External source is a third radio** alongside git/image in the new-app modal. §12.6.
- Q5: **Source changes stage.** Changing repo branch, image tag, or any source field in Settings is staged like everything else. Rebuild happens on Deploy click, not on field change. §12.2 scope confirmed.
- Q6: **Broken bindings allow Deploy with heavy warning.** `${{bindings.my-db.host}}` with a missing binding renders a red "reference broken" chip on the affected env rows. The Deploy button stays enabled but shows a confirmation modal: "N variables have broken references — the app may fail to start. Deploy anyway?" §12.3 updated.

**Resolved 2026-04-17 (pass 4).**
- §3.6 configure pane fields per source type — written into §3.6. Kind selector (Service/Cron) appears for Git and Image only; External is always Service; Cron swaps replicas/domain for a schedule input.
- §3.9 bindings picker widget — split into usage surface (dropdown in Variables tab, §3.9a) and structural surface (App Settings panel, §3.9b). Dropdown covers `variable-reference.png`.
- Standalone Volume — **dropped from v1 create picker.** Mitigated by "Adopt existing PVC" affordance under App Settings → Storage. Full impact analysis in §12.28 (no new CRD, no orphan-PVC reconciler, no new canvas node types; persona burn: 1 partial + 2 hard out of 10 for an advanced k8s workflow).

**All questions resolved.** No open items.

---

## 14. Flow status tracker

| Flow                              | §    | Screenshots | Spec'd | Implemented |
|-----------------------------------|------|-------------|--------|-------------|
| Onboarding — first-run wizard     | 3.1  | ☐           | scaffold | ✅ (Phase 7) |
| Login                             | 3.2  | ☐           | scaffold | ✅ |
| Project list                      | 3.3  | ✅ `dashboard.png`, `user-dropdown.png` | scaffold | ✅ |
| Create project                    | 3.4  | ☐           | scaffold | ✅ |
| Project workspace (canvas)        | 3.5  | ✅ `canvas.png` | updated (Svelte Flow spec'd §12.4) | ⚠️ list only — canvas not built |
| New app                           | 3.6  | ✅ `project-add.png` | updated (modal picker) | ⚠️ currently a page — §12.26 |
| App detail (drawer)               | 3.7  | ✅ `project-app-select-deployments.png`, `project-app-slect-settings.png` | updated | ⚠️ page today, §12.21 |
| Variables editing                 | 3.8  | ✅ `variables.png`, `change-details.png`, `settings-shared-variables.png` | updated | ✅ (table), ⚠️ scoped ref picker + staged changes TBD |
| Service bindings                  | 3.9  | ✅ `variable-reference.png` | updated (dropdown + structural panel) | ⚠️ neither surface built |
| Domains                           | 3.10 | ☐           | scaffold | ✅ custom domains; TLS overrides TBD |
| Storage                           | 3.11 | ☐ (visible on canvas only) | scaffold | ⚠️ unknown |
| Logs (drawer tab)                 | 3.12 | ☐           | updated (drawer tab) | ✅ basic SSE; drawer integration TBD |
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

### B. Build-order recommendation

The UI overhaul hinges on three pieces: **the global store**, **the
canvas**, and **the drawer layout**. Suggested order:

1. **Global store + design system foundation** — create
   `store.svelte.ts` (§2.7), install `lucide-svelte`, add
   `--color-info` token to `app.css`. Migrate existing
   `context.svelte.ts` consumers. Unblocks everything else.
2. **Left-rail nav reshape** (Dashboard + Project scope per §2.5) —
   structural layout change from top-bar nav to icon rail.
3. **Canvas MVP with Svelte Flow** (§12.4) — render existing Apps as
   nodes, bindings as edges. Drag-position persistence via API
   annotation PATCH. Auto-layout fallback.
4. **App detail → drawer** — reuse existing sub-pages as drawer
   contents; add Logs as 5th tab (§3.7, §3.12). URL stays stable.
5. **Staged-changes bar (Tier 1, §12.2)** — client-side dirty-state
   store, diff computation, deploy banner. Hardest UX piece, no backend.
6. **Scoped `${{}}` reference picker** (§12.3, §3.9) — new widget,
   resolver already planned.
7. **Create-modal** (§12.26, §3.6) — replaces new-app page, modal over
   canvas. Configure pane fields per source type (§3.6 table). No
   standalone Volume option (§12.28).
8. **Activity rail** (§12.22) — once the drawer/canvas layout is stable.
   Backend activity-store work required in parallel.
9. **Notifications bell** — unread-count badge + dropdown. Depends on
   activity/event backend.

### C. All items resolved

All open questions from §13 are resolved as of 2026-04-17 pass 3.
No items pending.
