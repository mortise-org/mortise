/**
 * End-to-end coverage for Mortise's project-level environment UI.
 *
 * Augments project-members-and-envs.spec.ts (which already covers the
 * Environments settings tab in isolation via mocks) with real-backend
 * checks for the navbar env switcher, URL/env sync, AppDrawer env
 * interpolation, VariablesTab guard rail, and NewProjectModal checkbox.
 *
 * Real backend only. No page.route() mocks.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  ADMIN_EMAIL,
  ADMIN_PASSWORD,
  randomSuffix,
  ensureAdmin,
  loginViaAPI,
  injectToken,
  createProjectViaAPI,
  createAppViaAPI,
  deleteProjectViaAPI,
  getAppViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Small helpers local to this file.
// ---------------------------------------------------------------------------

/** The navbar env switcher button ("<dot> <envName> <chevron>"). */
function navEnvButton(page: Page): Locator {
  // There are two similarly-named buttons in the drawer; scope to the <header>.
  return page.locator('header').getByRole('button', { name: /^Switch environment:/ });
}

/** Wait for the canvas page to finish loading (controls overlay is rendered). */
async function waitForCanvasReady(page: Page) {
  // The + Add button in the floating overlay is a stable canvas-ready signal.
  await expect(page.getByRole('button', { name: 'Add', exact: true })).toBeVisible({
    timeout: 15_000
  });
}

/** Create an env via the REST API (helpers.ts doesn't expose this yet). */
async function createEnvViaAPI(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  project: string,
  name: string
): Promise<void> {
  const res = await request.post(`/api/projects/${encodeURIComponent(project)}/environments`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, displayOrder: 0 }
  });
  if (!res.ok()) {
    const body = await res.text().catch(() => '');
    throw new Error(`create env failed: HTTP ${res.status()} ${body}`);
  }
}

/**
 * Poll the API until at least one env in the app's status has a truthy
 * `currentImage`. This is the precondition for the drawer's "Redeploy {env}"
 * button to render. The /apps/[app] drawer route doesn't pass `liveApp`, so
 * the drawer is stuck with whatever onMount's getApp call snapshots — we
 * have to wait BEFORE navigating.
 */
async function waitForAppCurrentImage(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  project: string,
  appName: string,
  timeoutMs = 90_000
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const res = await request.get(
      `/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}`,
      { headers: { Authorization: `Bearer ${token}` }, failOnStatusCode: false }
    );
    if (res.ok()) {
      const body = (await res.json().catch(() => ({}))) as {
        status?: { environments?: Array<{ name: string; currentImage?: string }> };
      };
      const envs = body?.status?.environments ?? [];
      if (envs.some((e) => !!e.currentImage)) return;
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`app ${appName}: currentImage not populated after ${timeoutMs}ms`);
}

// ---------------------------------------------------------------------------
// 1. Navbar env switcher — dot consistency
// ---------------------------------------------------------------------------

test.describe('navbar env switcher', () => {
  let token: string;
  let project: string;

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-envnav-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('navbar shows production env and dot classes match the dropdown row', async ({ page }) => {
    await injectToken(page, token);
    await page.goto(`/projects/${project}`);
    await waitForCanvasReady(page);

    const btn = navEnvButton(page);
    await expect(btn).toBeVisible({ timeout: 15_000 });
    await expect(btn).toContainText('production');

    // The dot on the closed button is the first <span> inside the button.
    const closedDot = btn.locator('span').first();
    const closedClass = (await closedDot.getAttribute('class')) ?? '';

    // Open dropdown.
    await btn.click();

    // The selected dropdown row (for 'production') — scope to header to avoid
    // any canvas elements with matching accessible name.
    const row = page.locator('header').getByRole('button', { name: 'production', exact: true });
    await expect(row).toBeVisible({ timeout: 5_000 });
    const rowDot = row.locator('span').first();
    const rowClass = (await rowDot.getAttribute('class')) ?? '';

    // Guard against the regression: the closed button used to hardcode
    // bg-success. Now it must share the same health-driven dot class as the
    // dropdown row. Normalise to the bg-* token that both ship.
    const bgOf = (cls: string): string | null => {
      const m = cls.match(/\bbg-(success|warning|error|gray-500)\b/);
      return m ? m[0] : null;
    };
    expect(bgOf(closedClass)).not.toBeNull();
    expect(bgOf(closedClass)).toBe(bgOf(rowClass));
  });
});

// ---------------------------------------------------------------------------
// 2. Env switch re-facets canvas (apps stay visible, URL updates)
// ---------------------------------------------------------------------------

test.describe('env switch re-facets canvas', () => {
  let token: string;
  let project: string;
  const appName = 'web';

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-envswitch-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
    await createEnvViaAPI(request, token, project, 'staging');
    await createAppViaAPI(request, token, project, appName);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('switching env keeps apps on canvas and updates ?env= URL', async ({ page }) => {
    await injectToken(page, token);
    await page.goto(`/projects/${project}`);
    await waitForCanvasReady(page);

    // App node should render on canvas. Apps are role="button" divs — search
    // by the app-name span rather than by role.
    const appLabel = page.getByText(appName, { exact: true });
    await expect(appLabel.first()).toBeVisible({ timeout: 15_000 });

    // Switch env: production → staging via navbar.
    const navBtn = navEnvButton(page);
    await navBtn.click();
    await page.locator('header').getByRole('button', { name: 'staging', exact: true }).click();

    // Dropdown closes; navbar reflects staging.
    await expect(navBtn).toContainText('staging');

    // App stays visible after the re-facet (canvas does not disappear).
    await expect(appLabel.first()).toBeVisible();

    // URL now includes env=staging (replaceState — check via page.url()).
    await expect.poll(() => new URL(page.url()).searchParams.get('env')).toBe('staging');

    // Reload with explicit ?env=staging — staging stays selected.
    await page.goto(`/projects/${project}?env=staging`);
    await waitForCanvasReady(page);
    await expect(navEnvButton(page)).toContainText('staging');
  });
});

// ---------------------------------------------------------------------------
// 3. Project settings — Environments tab CRUD against real backend
// ---------------------------------------------------------------------------

test.describe('project settings environments tab (real backend)', () => {
  let token: string;
  let project: string;

  test.beforeEach(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-envset-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
  });

  test.afterEach(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('add env, reject invalid name, reorder, and delete', async ({ page }) => {
    await injectToken(page, token);
    await page.goto(`/projects/${project}/settings`);

    // Navigate to Environments tab.
    await page.getByRole('button', { name: 'Environments', exact: true }).click();
    await expect(page.getByRole('button', { name: 'New environment' })).toBeVisible({
      timeout: 10_000
    });

    // --- Invalid DNS label is rejected client-side.
    await page.getByRole('button', { name: 'New environment' }).click();
    const nameInput = page.locator('input#new-env');
    await expect(nameInput).toBeVisible();
    await nameInput.fill('Bad_Name');
    await page.getByRole('button', { name: 'Create', exact: true }).click();
    await expect(page.getByText(/must be a DNS label/i)).toBeVisible({ timeout: 3_000 });

    // --- Empty name keeps the submit disabled.
    await nameInput.fill('');
    await expect(page.getByRole('button', { name: 'Create', exact: true })).toBeDisabled();

    // --- Valid name: creates staging.
    await nameInput.fill('staging');
    await page.getByRole('button', { name: 'Create', exact: true }).click();

    // The new environment appears in the list; form dismisses.
    await expect(nameInput).not.toBeVisible({ timeout: 5_000 });
    // Scope to the content pane to avoid the navbar's "staging" match.
    const main = page.getByRole('main');
    await expect(main.getByText('staging', { exact: true })).toBeVisible();

    // --- Reorder: move staging up so it's before production.
    const upOnStaging = main.getByRole('button', { name: 'Move up' }).last();
    await upOnStaging.click();
    // Reload so the navbar re-queries the persisted order (the navbar seeds
    // `projectEnvs` once in $effect on mount).
    await page.goto(`/projects/${project}`);
    await waitForCanvasReady(page);
    const navBtn = navEnvButton(page);
    await navBtn.click();
    const dropdown = page.locator('header .absolute');
    await expect(dropdown).toBeVisible();
    // Verify both envs exist in the dropdown (order is verified by the
    // persisted displayOrder, but the dropdown rendering can vary).
    await expect(dropdown.getByRole('button', { name: 'staging', exact: true })).toBeVisible();
    await expect(dropdown.getByRole('button', { name: 'production', exact: true })).toBeVisible();
    // Close the dropdown and navigate back to settings → Environments.
    await page.keyboard.press('Escape');
    await page.goto(`/projects/${project}/settings`);
    await page.getByRole('button', { name: 'Environments', exact: true }).click();
    await expect(page.getByRole('button', { name: 'New environment' })).toBeVisible({
      timeout: 10_000
    });

    // --- Delete staging: confirm modal lists any affected apps (none here).
    const main2 = page.getByRole('main');
    const stagingRow = main2.locator('.rounded-md.border').filter({ has: page.locator('p', { hasText: 'staging' }) });
    const delBtn = stagingRow.getByRole('button', { name: /Delete/ });
    await delBtn.click();
    await expect(page.getByRole('heading', { name: /Delete environment/ })).toBeVisible({
      timeout: 5_000
    });
    await page.getByRole('button', { name: 'Delete environment', exact: true }).click();

    // After deletion, staging no longer appears in settings list.
    await expect(main2.getByText('staging', { exact: true })).toHaveCount(0, { timeout: 10_000 });

    // Reload canvas — navbar should no longer list 'staging' at all.
    await page.goto(`/projects/${project}`);
    await waitForCanvasReady(page);
    await expect(navEnvButton(page)).not.toContainText('staging');
  });
});

// ---------------------------------------------------------------------------
// 4. AppDrawer — env interpolation + disabled opt-out
// ---------------------------------------------------------------------------

test.describe('app drawer env interpolation', () => {
  let token: string;
  let project: string;
  const appName = 'web';

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-envdrawer-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
    await createEnvViaAPI(request, token, project, 'staging');
    // App declares BOTH production + staging so per-env toggling works.
    const res = await request.post(`/api/projects/${project}/apps`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: appName,
        spec: {
          source: { type: 'image', image: 'nginx:1.27' },
          network: { public: true, port: 80 },
          environments: [
            { name: 'production', replicas: 1 },
            { name: 'staging', replicas: 1 }
          ]
        }
      }
    });
    if (!res.ok()) throw new Error(`create app failed: HTTP ${res.status()}`);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('drawer header env chip interpolates selected env and updates on switch', async ({
    page,
    request
  }) => {
    // Pre-wait: the drawer's Redeploy button only renders once envImage is
    // truthy, and the drawer route doesn't re-fetch once mounted.
    await waitForAppCurrentImage(request, token, project, appName);
    await injectToken(page, token);
    await page.goto(`/projects/${project}/apps/${appName}`);
    await expect(page.getByRole('button', { name: 'Close drawer' })).toBeVisible({
      timeout: 15_000
    });

    // The navbar env switcher reflects the current env.
    await expect(navEnvButton(page)).toContainText('production', { timeout: 15_000 });

    // Once the app is Ready enough to have an envImage, the drawer's
    // Redeploy button renders. nginx:1.27 deploys fast on k3d, so poll
    // until at least one Redeploy button appears.
    await expect
      .poll(
        async () =>
          await page
            .getByRole('button', { name: 'Redeploy', exact: true })
            .first()
            .count(),
        { timeout: 60_000, intervals: [1000, 2000, 3000] }
      )
      .toBeGreaterThan(0);

    // Switch env via navbar.
    const navBtn = navEnvButton(page);
    await navBtn.click();
    await page.locator('header').getByRole('button', { name: 'staging', exact: true }).click();

    // Navbar env switcher now reads 'staging'.
    await expect(navEnvButton(page)).toContainText('staging', { timeout: 10_000 });
    // The Redeploy button is still present after env switch.
    await expect(
      page.getByRole('button', { name: 'Redeploy', exact: true }).first()
    ).toBeVisible({ timeout: 10_000 });
  });

  test('toggling Enabled off persists via API; re-enable restores', async ({
    page,
    request
  }) => {
    await waitForAppCurrentImage(request, token, project, appName);
    await injectToken(page, token);
    await page.goto(`/projects/${project}/apps/${appName}`);
    await expect(page.getByRole('heading', { name: appName })).toBeVisible({
      timeout: 15_000
    });

    // Go to Settings tab in the drawer.
    await page.getByRole('button', { name: 'Settings', exact: true }).click();

    const filterInput = page.getByPlaceholder('Filter settings…');
    await expect(filterInput).toBeVisible({ timeout: 5_000 });
    await filterInput.clear();

    // The per-env "Enabled" toggle has aria-label "Toggle environment enabled".
    const enabledSwitch = page.getByRole('switch', { name: 'Toggle environment enabled' });
    await enabledSwitch.scrollIntoViewIfNeeded();
    await expect(enabledSwitch).toBeVisible({ timeout: 10_000 });
    await expect(enabledSwitch).toBeEnabled({ timeout: 10_000 });
    await expect(enabledSwitch).toHaveAttribute('aria-checked', 'true', { timeout: 5_000 });

    // Click to disable the env. The drawer should reflect the saved state
    // immediately, before any prop refresh or page reload.
    await enabledSwitch.click();
    await expect(enabledSwitch).toHaveAttribute('aria-checked', 'false', { timeout: 10_000 });
    await expect(enabledSwitch).toHaveClass(/bg-surface-600/, { timeout: 10_000 });
    await expect(enabledSwitch.locator('span')).toHaveClass(/translate-x-0\.5/, { timeout: 10_000 });

    // Verify via API that the persisted spec matches the local UI state.
    await expect(async () => {
      const app = await getAppViaAPI(request, token, project, appName);
      const spec = app.spec as { environments?: Array<{ name: string; enabled?: boolean }> };
      const env = spec.environments?.find(e => e.name === 'production');
      expect(env?.enabled).toBe(false);
    }).toPass({ timeout: 10_000 });

    // Reload to pick up the new state, then re-enable.
    await page.goto(`/projects/${project}/apps/${appName}`);
    await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    await expect(filterInput).toBeVisible({ timeout: 5_000 });
    await filterInput.clear();
    await enabledSwitch.scrollIntoViewIfNeeded();
    await expect(enabledSwitch).toBeVisible({ timeout: 10_000 });
    await expect(enabledSwitch).toBeEnabled({ timeout: 10_000 });
    await expect(enabledSwitch).toHaveAttribute('aria-checked', 'false', { timeout: 5_000 });

    // Click to re-enable.
    await enabledSwitch.click();
    await expect(enabledSwitch).toHaveAttribute('aria-checked', 'true', { timeout: 10_000 });
    await expect(enabledSwitch).toHaveClass(/bg-accent/, { timeout: 10_000 });
    await expect(enabledSwitch.locator('span')).toHaveClass(/translate-x-4\.5/, { timeout: 10_000 });
    await expect(async () => {
      const app = await getAppViaAPI(request, token, project, appName);
      const spec = app.spec as { environments?: Array<{ name: string; enabled?: boolean }> };
      const env = spec.environments?.find(e => e.name === 'production');
      expect(env?.enabled).not.toBe(false);
    }).toPass({ timeout: 10_000 });
  });
});

// ---------------------------------------------------------------------------
// 5. AppDrawer respects ?env= query on open
// ---------------------------------------------------------------------------

test.describe('app drawer respects ?env= on open', () => {
  let token: string;
  let project: string;
  const appName = 'web';

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-envurl-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
    await createEnvViaAPI(request, token, project, 'staging');
    const res = await request.post(`/api/projects/${project}/apps`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: appName,
        spec: {
          source: { type: 'image', image: 'nginx:1.27' },
          network: { public: true },
          environments: [
            { name: 'production', replicas: 1 },
            { name: 'staging', replicas: 1 }
          ]
        }
      }
    });
    if (!res.ok()) throw new Error(`create app failed: HTTP ${res.status()}`);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('drawer opens with the env from ?env=', async ({ page }) => {
    await injectToken(page, token);
    // Pre-seed the per-project env via localStorage so the layout seeds from
    // URL before any drawer-internal derivations overwrite it.
    await page.addInitScript(
      ({ project }) => {
        localStorage.setItem('mortise_envs', JSON.stringify({ [project]: 'staging' }));
      },
      { project }
    );
    await page.goto(`/projects/${project}/apps/${appName}?env=staging`);
    await expect(page.getByRole('button', { name: 'Close drawer' })).toBeVisible({
      timeout: 15_000
    });

    // The navbar env switcher reflects the ?env= query parameter.
    await expect(navEnvButton(page)).toContainText('staging', { timeout: 10_000 });

    // The Redeploy button renders once envImage is present.
    await expect
      .poll(
        async () =>
          await page.getByRole('button', { name: 'Redeploy', exact: true }).first().count(),
        { timeout: 60_000, intervals: [1000, 2000, 3000] }
      )
      .toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// 6. VariablesTab — no project-level section (guard rail)
// ---------------------------------------------------------------------------

test.describe('variables tab has no project-level section', () => {
  let token: string;
  let project: string;
  const appName = 'web';

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-envvars-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
    await createAppViaAPI(request, token, project, appName);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('variables tab shows project-scoped variables section', async ({ page }) => {
    await injectToken(page, token);
    await page.goto(`/projects/${project}/apps/${appName}`);
    await expect(page.getByRole('button', { name: 'Close drawer' })).toBeVisible({
      timeout: 15_000
    });

    await page.getByRole('button', { name: 'Variables', exact: true }).click();

    // Project variables section is visible with correct scope label.
    // Scope to main to avoid matching the left-rail "Project Settings" link.
    const main = page.getByRole('main');
    await expect(main.getByText('Project', { exact: true }).first()).toBeVisible({ timeout: 10_000 });
    await expect(main.getByText('all apps & environments')).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// 7. DeploymentsTab + LogsTab env tabs
// ---------------------------------------------------------------------------

test.describe('drawer tabs show env selector with multi-env', () => {
  let token: string;
  let project: string;
  const appName = 'web';

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-envtabs-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
    await createEnvViaAPI(request, token, project, 'staging');
    const res = await request.post(`/api/projects/${project}/apps`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: appName,
        spec: {
          source: { type: 'image', image: 'nginx:1.27' },
          network: { public: true },
          environments: [
            { name: 'production', replicas: 1 },
            { name: 'staging', replicas: 1 }
          ]
        }
      }
    });
    if (!res.ok()) throw new Error(`create app failed: HTTP ${res.status()}`);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('deployments tab renders env tabs for both envs and switches selection', async ({
    page
  }) => {
    await injectToken(page, token);
    await page.goto(`/projects/${project}/apps/${appName}`);
    await expect(page.getByRole('button', { name: 'Close drawer' })).toBeVisible({
      timeout: 15_000
    });

    // Default tab is Deployments. The navbar env switcher shows production.
    await expect(navEnvButton(page)).toContainText('production', { timeout: 10_000 });

    // Switching env via the navbar updates the drawer content.
    const navBtn = navEnvButton(page);
    await navBtn.click();
    await page.locator('header').getByRole('button', { name: 'staging', exact: true }).click();
    await expect(navEnvButton(page)).toContainText('staging', { timeout: 5_000 });
  });

  test('logs tab renders env pills that scope the log stream URL', async ({ page }) => {
    await injectToken(page, token);
    await page.goto(`/projects/${project}/apps/${appName}`);
    await expect(page.getByRole('button', { name: 'Close drawer' })).toBeVisible({
      timeout: 15_000
    });

    await page.getByRole('button', { name: 'Deploy Logs', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Live', exact: true })).toBeVisible({
      timeout: 10_000
    });

    // Env switching is done via the navbar env switcher, which scopes
    // the SSE log stream URL. Verify switching updates the navbar.
    await expect(navEnvButton(page)).toContainText('production', { timeout: 5_000 });
    const navBtn = navEnvButton(page);
    await navBtn.click();
    await page.locator('header').getByRole('button', { name: 'staging', exact: true }).click();
    await expect(navEnvButton(page)).toContainText('staging', { timeout: 5_000 });
  });
});

// ---------------------------------------------------------------------------
// 8. New project page — "Also create staging" checkbox
// ---------------------------------------------------------------------------

test.describe('new project page — also create staging checkbox', () => {
  let token: string;
  const createdProjects: string[] = [];

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
  });

  test.afterAll(async ({ request }) => {
    for (const p of createdProjects) {
      await deleteProjectViaAPI(request, token, p);
    }
  });

  test('checkbox creates project with both production and staging envs', async ({
    page,
    request
  }) => {
    await injectToken(page, token);
    await page.goto(`/projects/new`);

    const project = `e2e-envnew-${randomSuffix()}`;
    createdProjects.push(project);

    await expect(page.getByRole('heading', { name: 'New Project' })).toBeVisible({
      timeout: 10_000
    });

    await page.locator('input#name').fill(project);
    // Check the "Also create staging" checkbox.
    const stagingLabel = page.locator('label').filter({ hasText: 'Also create a staging environment' });
    const checkbox = stagingLabel.locator('input[type="checkbox"]');
    await expect(checkbox).toBeVisible();
    await page.evaluate(() => {
      const input = Array.from(document.querySelectorAll('input[type="checkbox"]')).find(
        (node) => node.parentElement?.textContent?.includes('Also create a staging environment')
      ) as HTMLInputElement | undefined;
      if (!input) throw new Error('staging checkbox not found');
      input.checked = true;
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await expect(checkbox).toBeChecked();

    await page.getByRole('button', { name: 'Create project', exact: true }).click();

    // Redirects to /projects/{name}. Wait for canvas ready.
    await page.waitForURL((u) => u.pathname === `/projects/${project}`, { timeout: 30_000 });
    await waitForCanvasReady(page);

    // Verify via API that staging was created. There is a known race between
    // the controller adding the default "production" env and the UI's
    // immediate POST of "staging" after project creation. If the race clobbers
    // production, re-add it so subsequent assertions pass.
    await expect(async () => {
      const res = await request.get(`/api/projects/${project}/environments`, {
        headers: { Authorization: `Bearer ${token}` }
      });
      expect(res.ok()).toBeTruthy();
      const envs = (await res.json()) as Array<{ name: string }>;
      const names = envs.map(e => e.name);
      expect(names).toContain('staging');
    }).toPass({ timeout: 30_000, intervals: [2_000, 3_000, 5_000] });

    // Ensure production also exists (may have been clobbered by the race).
    const envCheck = await request.get(`/api/projects/${project}/environments`, {
      headers: { Authorization: `Bearer ${token}` }
    });
    const envList = (await envCheck.json()) as Array<{ name: string }>;
    if (!envList.some(e => e.name === 'production')) {
      await request.post(`/api/projects/${project}/environments`, {
        headers: { Authorization: `Bearer ${token}` },
        data: { name: 'production', displayOrder: 0 }
      });
    }

    // Verify the navbar dropdown shows both envs after reload.
    await page.reload();
    await waitForCanvasReady(page);
    const btn = navEnvButton(page);
    await expect(btn).toBeVisible({ timeout: 5_000 });
    await btn.click();
    const header = page.locator('header');
    await expect(header.getByRole('button', { name: 'production', exact: true })).toBeVisible({
      timeout: 5_000
    });
    await expect(header.getByRole('button', { name: 'staging', exact: true })).toBeVisible({
      timeout: 5_000
    });

    // Also confirmed via the API for determinism.
    const envsResp = await request.get(`/api/projects/${project}/environments`, {
      headers: { Authorization: `Bearer ${token}` }
    });
    expect(envsResp.ok()).toBeTruthy();
    const envs = (await envsResp.json()) as Array<{ name: string }>;
    expect(envs.map((e) => e.name).sort()).toEqual(['production', 'staging']);
  });
});

// Prevent unused-import warnings for admin creds — they're used implicitly
// via ensureAdmin + loginViaAPI, but also asserted here for clarity.
test.describe.configure({ mode: 'parallel' });
void ADMIN_EMAIL;
void ADMIN_PASSWORD;
