/**
 * VariablesTab E2E tests (real backend, no mocks).
 *
 * Tests cover the full VariablesTab surface: per-env sections, add/edit/delete
 * variables, raw/import mode, shared variables (project-scoped), and env
 * switching.
 *
 * All API calls hit the real backend. Auth is injected via injectToken().
 */
import { expect, test } from '@playwright/test';
import {
  randomSuffix,
  ensureAdmin,
  loginViaAPI,
  injectToken,
  createProjectViaAPI,
  deleteProjectViaAPI,
  getEnvViaAPI,
  getAppViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Helper: create a staging environment via API.
// ---------------------------------------------------------------------------
async function createEnvViaAPI(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  project: string,
  name: string
): Promise<void> {
  const res = await request.post(`/api/projects/${encodeURIComponent(project)}/environments`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, displayOrder: 1 }
  });
  if (!res.ok()) {
    const body = await res.text().catch(() => '');
    throw new Error(`create env failed: HTTP ${res.status()} ${body}`);
  }
}

// ---------------------------------------------------------------------------
// Helper: create an app with sharedVars and multi-env support.
// ---------------------------------------------------------------------------
async function getSharedVarsViaAPI(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  project: string
): Promise<Array<{ name: string; value: string }>> {
  const res = await request.get(
    `/api/projects/${encodeURIComponent(project)}/shared-vars`,
    { headers: { Authorization: `Bearer ${token}` } }
  );
  if (!res.ok()) return [];
  return (await res.json()) ?? [];
}

async function setSharedVarsViaAPI(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  project: string,
  vars: Array<{ name: string; value: string }>
): Promise<void> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const res = await request.put(
      `/api/projects/${encodeURIComponent(project)}/shared-vars`,
      { headers: { Authorization: `Bearer ${token}` }, data: vars }
    );
    if (res.ok()) return;
    const body = await res.text().catch(() => '');
    if (res.status() === 409 || (res.status() === 500 && body.includes('has been modified'))) {
      await new Promise(r => setTimeout(r, 500));
      continue;
    }
    throw new Error(`setSharedVars failed: HTTP ${res.status()} ${body}`);
  }
  throw new Error('setSharedVars: timed out retrying resource conflicts');
}

async function createAppWithSharedVars(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  project: string,
  appName: string,
  sharedVars: Array<{ name: string; value: string }> = []
): Promise<void> {
  const res = await request.post(`/api/projects/${encodeURIComponent(project)}/apps`, {
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
  if (!res.ok()) {
    const body = await res.text().catch(() => '');
    throw new Error(`create app with sharedVars failed: HTTP ${res.status()} ${body}`);
  }
  if (sharedVars.length > 0) {
    await setSharedVarsViaAPI(request, token, project, sharedVars);
  }
}

// ---------------------------------------------------------------------------
// Helper: seed env vars via PUT.
// ---------------------------------------------------------------------------
async function putEnvVars(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  project: string,
  appName: string,
  environment: string,
  vars: Array<{ name: string; value: string }>
): Promise<void> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const res = await request.put(
      `/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}/env?environment=${encodeURIComponent(environment)}`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: vars
      }
    );
    if (res.ok()) return;
    const body = await res.text().catch(() => '');
    if (res.status() === 409 || (res.status() === 500 && body.includes('has been modified'))) {
      await new Promise(r => setTimeout(r, 500));
      continue;
    }
    if (res.status() === 404 && body.includes('not found')) {
      await new Promise(r => setTimeout(r, 500));
      continue;
    }
    throw new Error(`putEnvVars failed: HTTP ${res.status()} ${body}`);
  }
  throw new Error('putEnvVars: timed out retrying resource conflicts');
}

// ---------------------------------------------------------------------------
// Helper: navigate to the Variables tab of an app.
// ---------------------------------------------------------------------------
async function goToVariablesTab(
  page: import('@playwright/test').Page,
  project: string,
  appName: string
): Promise<void> {
  await page.goto(`/projects/${project}/apps/${appName}`);
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 15_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();
}

// ---------------------------------------------------------------------------
// Helper: click the + (add) button in a section identified by its heading text.
// The Runtime section contains "Runtime - {env}", the Project section contains
// "all apps & environments". We find the section's rounded-lg container and
// click the last button that contains an SVG (the Plus icon button).
// ---------------------------------------------------------------------------
async function clickPlusButton(
  page: import('@playwright/test').Page,
  sectionText: string
): Promise<void> {
  const section = page.locator('.rounded-lg.border').filter({ hasText: sectionText }).first();
  // The + button is the last button in the header row that contains an SVG icon
  const header = section.locator('.flex.items-center.justify-between').first();
  await header.locator('button:has(svg)').last().click();
}

// ---------------------------------------------------------------------------
// Tests 1-7, 10: single-env app (production only)
// ---------------------------------------------------------------------------
test.describe('variables tab - env vars (production)', () => {
  let token: string;
  let project: string;
  let appName: string;

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-vars-${randomSuffix()}`;
    appName = `vars-app-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
    await createAppWithSharedVars(request, token, project, appName);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  // -------------------------------------------------------------------------
  // Test 1: Variables tab shows env section and project variables section
  // -------------------------------------------------------------------------
  test('variables tab shows env section and project variables section', async ({ page }) => {
    await injectToken(page, token);
    await goToVariablesTab(page, project, appName);

    // The Runtime section heading shows "Runtime - production"
    await expect(page.getByText('Runtime - production')).toBeVisible({ timeout: 8_000 });
    // The Project section has the subtitle "all apps & environments"
    await expect(page.getByText('all apps & environments')).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Test 2: Variables tab loads existing variables
  // -------------------------------------------------------------------------
  test('variables tab shows existing variables loaded from env/production', async ({ page, request }) => {
    // Seed vars via API first.
    await putEnvVars(request, token, project, appName, 'production', [
      { name: 'APP_ENV', value: 'production' },
      { name: 'DEBUG', value: 'false' }
    ]);

    await injectToken(page, token);
    await goToVariablesTab(page, project, appName);

    await expect(page.getByText('APP_ENV')).toBeVisible({ timeout: 8_000 });
    await expect(page.getByText('DEBUG')).toBeVisible();

    // Cleanup: remove seeded vars so other tests start clean.
    await putEnvVars(request, token, project, appName, 'production', []);
  });

  // -------------------------------------------------------------------------
  // Test 3: Add a new variable via form, verify via API
  // -------------------------------------------------------------------------
  test('add new variable via form persists to backend', async ({ page, request }) => {
    // Seed one existing var so the section is expanded.
    await putEnvVars(request, token, project, appName, 'production', [
      { name: 'APP_ENV', value: 'production' }
    ]);

    await injectToken(page, token);
    await goToVariablesTab(page, project, appName);

    await expect(page.getByText('APP_ENV')).toBeVisible({ timeout: 8_000 });

    // Click the + button in the Runtime section to show the new variable row.
    await clickPlusButton(page, 'Runtime - production');

    await page.getByPlaceholder('VARIABLE_NAME').first().fill('MY_NEW_VAR');
    await page.getByPlaceholder('value', { exact: true }).first().fill('hello-world');

    await page.getByRole('button', { name: 'Add', exact: true }).first().click();

    // Verify via API.
    await expect(async () => {
      const envVars = await getEnvViaAPI(request, token, project, appName, 'production');
      const names = envVars.map(v => v.name);
      expect(names).toContain('APP_ENV');
      expect(names).toContain('MY_NEW_VAR');
      const myVar = envVars.find(v => v.name === 'MY_NEW_VAR');
      expect(myVar?.value).toBe('hello-world');
    }).toPass({ timeout: 10_000 });

    // Cleanup.
    await putEnvVars(request, token, project, appName, 'production', []);
  });

  // -------------------------------------------------------------------------
  // Test 4: Delete a variable, verify via API
  // -------------------------------------------------------------------------
  test('delete a variable removes it from backend', async ({ page, request }) => {
    await putEnvVars(request, token, project, appName, 'production', [
      { name: 'KEEP_ME', value: 'yes' },
      { name: 'DELETE_ME', value: 'bye' }
    ]);

    await injectToken(page, token);
    await goToVariablesTab(page, project, appName);

    await expect(page.getByText('DELETE_ME')).toBeVisible({ timeout: 8_000 });

    // Hover the row and click the trash button (last button in the row).
    const row = page.locator('div.group').filter({ hasText: 'DELETE_ME' });
    await row.hover();
    await row.getByRole('button').last().click();

    // Verify via API.
    await expect(async () => {
      const envVars = await getEnvViaAPI(request, token, project, appName, 'production');
      const names = envVars.map(v => v.name);
      expect(names).toContain('KEEP_ME');
      expect(names).not.toContain('DELETE_ME');
    }).toPass({ timeout: 10_000 });

    // Cleanup.
    await putEnvVars(request, token, project, appName, 'production', []);
  });

  // -------------------------------------------------------------------------
  // Test 5: Inline edit a value, verify via API
  // -------------------------------------------------------------------------
  test('inline edit calls PUT with updated value via Save changes button', async ({ page, request }) => {
    await putEnvVars(request, token, project, appName, 'production', [
      { name: 'APP_ENV', value: 'old-value' }
    ]);

    await injectToken(page, token);
    await goToVariablesTab(page, project, appName);

    await expect(page.getByText('APP_ENV')).toBeVisible({ timeout: 8_000 });

    // Values are hidden by default. Click the reveal (eye) button to show the
    // value input, then edit it. The row is div.group containing "APP_ENV".
    const row = page.locator('div.group').filter({ hasText: 'APP_ENV' });
    // Click the eye button (titled "Reveal") to reveal the value input.
    await row.getByTitle('Reveal').click();

    // Now the value input is visible. Fill with the new value.
    const valueInput = row.locator('input[type="text"]');
    await valueInput.fill('new-value');

    const saveBtn = page.getByRole('button', { name: /Save \d+ change/ });
    await expect(saveBtn).toBeVisible({ timeout: 3_000 });
    await saveBtn.click();

    // Verify via API.
    await expect(async () => {
      const envVars = await getEnvViaAPI(request, token, project, appName, 'production');
      const envVar = envVars.find(v => v.name === 'APP_ENV');
      expect(envVar?.value).toBe('new-value');
    }).toPass({ timeout: 10_000 });

    // Cleanup.
    await putEnvVars(request, token, project, appName, 'production', []);
  });

  // -------------------------------------------------------------------------
  // Test 6: Switch to Raw mode shows textarea
  // -------------------------------------------------------------------------
  test('switching to Raw mode in a section shows the textarea', async ({ page }) => {
    await injectToken(page, token);
    await goToVariablesTab(page, project, appName);

    // Wait for the Runtime section to be visible.
    await expect(page.getByText('Runtime - production')).toBeVisible({ timeout: 8_000 });

    // Click the "Raw" toggle button in the Runtime section.
    const runtimeSection = page.locator('.rounded-lg.border').filter({ hasText: 'Runtime - production' }).first();
    await runtimeSection.getByText('Raw', { exact: true }).click();

    const textarea = page.getByPlaceholder(/DATABASE_URL/);
    await expect(textarea).toBeVisible({ timeout: 5_000 });

    // The save button in raw mode is labeled "Save" (not "Import").
    await expect(runtimeSection.getByRole('button', { name: 'Save', exact: true })).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Test 7: Raw import persists (verify via API)
  // -------------------------------------------------------------------------
  test('raw import persists variables to backend', async ({ page, request }) => {
    // Start clean.
    await putEnvVars(request, token, project, appName, 'production', []);

    await injectToken(page, token);
    await goToVariablesTab(page, project, appName);

    // Wait for the Runtime section to be visible.
    await expect(page.getByText('Runtime - production')).toBeVisible({ timeout: 8_000 });

    // Click the "Raw" toggle button in the Runtime section.
    const runtimeSection = page.locator('.rounded-lg.border').filter({ hasText: 'Runtime - production' }).first();
    await runtimeSection.getByText('Raw', { exact: true }).click();

    const textarea = page.getByPlaceholder(/DATABASE_URL/);
    await expect(textarea).toBeVisible({ timeout: 5_000 });
    await textarea.fill('KEY=value\nFOO=bar');

    // The save button in raw mode is labeled "Save" (not "Import").
    await runtimeSection.getByRole('button', { name: 'Save', exact: true }).click();

    // Verify via API.
    await expect(async () => {
      const envVars = await getEnvViaAPI(request, token, project, appName, 'production');
      const names = envVars.map(v => v.name);
      expect(names).toContain('KEY');
      expect(names).toContain('FOO');
      const keyVar = envVars.find(v => v.name === 'KEY');
      expect(keyVar?.value).toBe('value');
    }).toPass({ timeout: 10_000 });

    // Cleanup.
    await putEnvVars(request, token, project, appName, 'production', []);
  });
});

// ---------------------------------------------------------------------------
// Test 11: Staging env via env switcher (multi-env)
// ---------------------------------------------------------------------------
test.describe('variables tab - staging env via switcher', () => {
  let token: string;
  let project: string;
  let appName: string;

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-vstage-${randomSuffix()}`;
    appName = `vstg-app-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
    await createEnvViaAPI(request, token, project, 'staging');
    await createAppWithSharedVars(request, token, project, appName);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('switching to staging env via navbar shows staging variables', async ({ page, request }) => {
    // Seed a staging var.
    await putEnvVars(request, token, project, appName, 'staging', [
      { name: 'STAGE_VAR', value: 'maybe' }
    ]);

    await injectToken(page, token);
    await goToVariablesTab(page, project, appName);

    // Default env is production; staging vars should not be visible.
    await expect(page.getByText('Runtime - production')).toBeVisible({ timeout: 8_000 });
    await expect(page.getByText('STAGE_VAR')).not.toBeVisible();

    // Switch to staging via the env switcher in the navbar.
    // The env switcher button has aria-label "Switch environment: production".
    const envSwitcher = page.getByLabel(/Switch environment/);
    await envSwitcher.click();

    // Click the "staging" option in the dropdown.
    await page.getByRole('button', { name: 'staging' }).click();

    // Now the Runtime section should show "Runtime - staging" and the var.
    await expect(page.getByText('Runtime - staging')).toBeVisible({ timeout: 8_000 });
    await expect(page.getByText('STAGE_VAR')).toBeVisible({ timeout: 5_000 });

    // Cleanup.
    await putEnvVars(request, token, project, appName, 'staging', []);
  });
});

// ---------------------------------------------------------------------------
// Tests 8, 9, 12, 13, 14: shared variables (project-scoped)
// ---------------------------------------------------------------------------
test.describe('variables tab - project variables', () => {
  let token: string;
  let project: string;
  let appName: string;

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-vshare-${randomSuffix()}`;
    appName = `vsh-app-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  // -------------------------------------------------------------------------
  // Test 8: Project variables section shows vars from app spec
  // -------------------------------------------------------------------------
  test('project variables section renders vars from shared vars', async ({ page, request }) => {
    const app = `vsh-render-${randomSuffix()}`;
    await createAppWithSharedVars(request, token, project, app, [
      { name: 'SHARED_KEY', value: 'shared-value' }
    ]);

    await injectToken(page, token);
    await goToVariablesTab(page, project, app);

    // The Project section has the subtitle "all apps & environments".
    await expect(page.getByText('all apps & environments')).toBeVisible({ timeout: 8_000 });
    await expect(page.getByText('SHARED_KEY')).toBeVisible({ timeout: 5_000 });
  });

  // -------------------------------------------------------------------------
  // Test 9: Adding a project variable updates app spec
  // -------------------------------------------------------------------------
  test('adding a project variable persists to app spec', async ({ page, request }) => {
    const app = `vsh-add-${randomSuffix()}`;
    await createAppWithSharedVars(request, token, project, app);

    await injectToken(page, token);
    await goToVariablesTab(page, project, app);

    // Wait for the Project section to be visible.
    await expect(page.getByText('all apps & environments')).toBeVisible({ timeout: 8_000 });

    // Click the + button in the Project section.
    await clickPlusButton(page, 'all apps & environments');

    await page.getByPlaceholder('VARIABLE_NAME').last().fill('GLOBAL_FLAG');
    await page.getByPlaceholder('value', { exact: true }).last().fill('true');
    await page.getByRole('button', { name: 'Add', exact: true }).last().click();

    // Verify via project shared vars API.
    await expect(async () => {
      const vars = await getSharedVarsViaAPI(request, token, project);
      const sharedVar = vars.find(v => v.name === 'GLOBAL_FLAG');
      expect(sharedVar).toBeDefined();
      expect(sharedVar?.value).toBe('true');
    }).toPass({ timeout: 10_000 });

    await setSharedVarsViaAPI(request, token, project, []);
  });

  // -------------------------------------------------------------------------
  // Test 12: Edit project variable
  // -------------------------------------------------------------------------
  test('inline edit of project variable persists updated value', async ({ page, request }) => {
    const app = `vsh-edit-${randomSuffix()}`;
    await createAppWithSharedVars(request, token, project, app, [
      { name: 'SHARED_KEY', value: 'shared-value' }
    ]);

    await injectToken(page, token);
    await goToVariablesTab(page, project, app);

    await expect(page.getByText('SHARED_KEY')).toBeVisible({ timeout: 8_000 });

    // Values are hidden by default. Click the masked value to reveal it.
    // The SHARED_KEY row is in the Project section (last div.group with that text).
    const row = page.locator('div.group').filter({ hasText: 'SHARED_KEY' }).last();
    // Click the asterisk-masked value button to reveal the input.
    await row.locator('button', { hasText: /^\*+$/ }).click();

    // Now edit the revealed value input.
    const valueInput = row.locator('input[type="text"]');
    await valueInput.fill('updated-value');

    const saveBtn = page.getByRole('button', { name: /Save \d+/ });
    await expect(saveBtn).toBeVisible({ timeout: 3_000 });
    await saveBtn.click();

    // Verify via project shared vars API.
    await expect(async () => {
      const vars = await getSharedVarsViaAPI(request, token, project);
      const sharedVar = vars.find(v => v.name === 'SHARED_KEY');
      expect(sharedVar).toBeDefined();
      expect(sharedVar?.value).toBe('updated-value');
    }).toPass({ timeout: 10_000 });
  });

  // -------------------------------------------------------------------------
  // Test 13: Delete project variable
  // -------------------------------------------------------------------------
  test('deleting a project variable removes it from app spec', async ({ page, request }) => {
    const app = `vsh-del-${randomSuffix()}`;
    await createAppWithSharedVars(request, token, project, app, [
      { name: 'SHARED_KEY', value: 'shared-value' }
    ]);

    await injectToken(page, token);
    await goToVariablesTab(page, project, app);

    await expect(page.getByText('SHARED_KEY')).toBeVisible({ timeout: 8_000 });

    // Hover the shared var row and click the trash button (last button in the row).
    const row = page.locator('div.group').filter({ hasText: 'SHARED_KEY' });
    await row.hover();
    await row.getByRole('button').last().click();

    // Verify via project shared vars API.
    await expect(async () => {
      const vars = await getSharedVarsViaAPI(request, token, project);
      const names = vars.map(v => v.name);
      expect(names).not.toContain('SHARED_KEY');
    }).toPass({ timeout: 10_000 });
  });

  // -------------------------------------------------------------------------
  // Test 14: Project vars raw import
  // -------------------------------------------------------------------------
  test('raw import of project variables persists to app spec', async ({ page, request }) => {
    const app = `vsh-raw-${randomSuffix()}`;
    await createAppWithSharedVars(request, token, project, app);

    await injectToken(page, token);
    await goToVariablesTab(page, project, app);

    // Wait for the Project section to be visible.
    await expect(page.getByText('all apps & environments')).toBeVisible({ timeout: 8_000 });

    // Click the Raw button in the Project section (last Raw button on the page).
    const projectSection = page.locator('.rounded-lg.border').filter({ hasText: 'all apps & environments' }).first();
    await projectSection.getByText('Raw', { exact: true }).click();

    // The shared/project raw textarea has placeholder containing "JWT_SECRET".
    const textarea = page.getByPlaceholder(/JWT_SECRET/).last();
    await expect(textarea).toBeVisible({ timeout: 5_000 });
    await textarea.fill('SHARED_IMPORT=abc');

    // The save button in raw mode is labeled "Save" (not "Import").
    await projectSection.getByRole('button', { name: 'Save', exact: true }).click();

    // Verify via project shared vars API.
    await expect(async () => {
      const vars = await getSharedVarsViaAPI(request, token, project);
      const sharedVar = vars.find(v => v.name === 'SHARED_IMPORT');
      expect(sharedVar).toBeDefined();
      expect(sharedVar?.value).toBe('abc');
    }).toPass({ timeout: 10_000 });

    await setSharedVarsViaAPI(request, token, project, []);
  });
});
