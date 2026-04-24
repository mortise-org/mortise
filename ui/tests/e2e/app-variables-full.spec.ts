import { test, expect, type Page } from '@playwright/test';

// ---------------------------------------------------------------------------
// VariablesTab E2E tests (mocked backend — no live cluster required)
//
// All API calls are intercepted with page.route(). Auth is injected directly
// into localStorage. Tests cover the full VariablesTab surface: per-env
// section, project variables section, add/edit/delete, raw/import mode.
//
// Layout: per-env section (active env), then a "Project" variables section
// (project-scoped, shared across all apps and environments).
// ---------------------------------------------------------------------------

const mockProject = {
  name: 'my-project',
  namespace: 'project-my-project',
  phase: 'Ready' as const,
  appCount: 1,
  description: ''
};

const mockApp = {
  metadata: { name: 'web-app', namespace: 'project-my-project' },
  spec: {
    source: { type: 'image' as const, image: 'nginx:1.27' },
    network: { public: true, port: 8080 },
    environments: [
      { name: 'production', replicas: 1 },
      { name: 'staging', replicas: 1 }
    ],
    sharedVars: [
      { name: 'SHARED_KEY', value: 'shared-value' }
    ],
    storage: [],
    credentials: []
  },
  status: {
    phase: 'Ready' as const,
    environments: [
      { name: 'production', readyReplicas: 1, currentImage: 'nginx:1.27', deployHistory: [] }
    ]
  }
};

async function injectAuth(page: Page) {
  // Use addInitScript so localStorage is set BEFORE any page script runs.
  // Navigating to '/' first would hit the real /api/projects (unmocked at that
  // point), receive 401, auto-redirect to /login, and clear localStorage —
  // breaking any subsequent navigation.
  await page.addInitScript(() => {
    window.localStorage.setItem('mortise_token', 'test-token');
    window.localStorage.setItem(
      'mortise_user',
      JSON.stringify({ email: 'admin@example.com', role: 'admin' })
    );
  });
}

async function setupCommonMocks(page: Page, appOverride = mockApp) {
  await page.route('**/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
  await page.route('**/api/projects', (r) => r.fulfill({ json: [mockProject] }));
  await page.route('**/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
  await page.route('**/api/projects/my-project/apps', (r) => r.fulfill({ json: [appOverride] }));
  await page.route('**/api/projects/my-project/apps/web-app', (r) => r.fulfill({ json: appOverride }));
  await page.route('**/api/projects/my-project/activity', (r) => r.fulfill({ json: [] }));
  await page.route('**/api/projects/my-project/apps/web-app/domains*', (r) =>
    r.fulfill({ json: { primary: 'web-app.example.com', custom: [] } })
  );
  await page.route('**/api/projects/my-project/apps/web-app/tokens', (r) =>
    r.fulfill({ json: [] })
  );
  await page.route('**/api/projects/my-project/apps/web-app/secrets', (r) =>
    r.fulfill({ json: [] })
  );
}

async function goToVariablesTab(page: Page, appOverride = mockApp) {
  await injectAuth(page);
  await setupCommonMocks(page, appOverride);
  await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
    r.fulfill({ json: [] })
  );
  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();
}

// ---------------------------------------------------------------------------
// Test 1: Variables tab shows env section and project variables section
// ---------------------------------------------------------------------------
test('variables tab shows env section and project variables section', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
    r.fulfill({ json: [] })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // "Project" section header with scope label.
  await expect(page.getByText('Project')).toBeVisible({ timeout: 8_000 });
  await expect(page.getByText('all apps & environments')).toBeVisible();
});

// ---------------------------------------------------------------------------
// Test 2: Variables tab loads existing variables from GET /env/production
// ---------------------------------------------------------------------------
test('variables tab shows existing variables loaded from env/production', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env?environment=production', (r) =>
    r.fulfill({ json: [{ name: 'APP_ENV', value: 'production' }, { name: 'DEBUG', value: 'false' }] })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env?environment=staging', (r) =>
    r.fulfill({ json: [] })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Both variable keys must be visible in the production section (expanded by default).
  await expect(page.getByText('APP_ENV')).toBeVisible({ timeout: 8_000 });
  await expect(page.getByText('DEBUG')).toBeVisible();
});

// ---------------------------------------------------------------------------
// Test 3: Add a new variable via form → PUT called with new key
// ---------------------------------------------------------------------------
test('add new variable via form calls PUT with the new key', async ({ page }) => {
  let capturedBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env*', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedBody = JSON.parse(route.request().postData() ?? '[]');
      return route.fulfill({ status: 204 });
    }
    // GET: return one existing var
    return route.fulfill({ json: [{ name: 'APP_ENV', value: 'production' }] });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Wait for production section to be expanded and show New variable button.
  await expect(page.getByText('APP_ENV')).toBeVisible({ timeout: 8_000 });

  // Click the New variable button in the production section (first one).
  await page.getByRole('button', { name: 'New variable', exact: true }).first().click();

  // Fill key and value.
  await page.getByPlaceholder('VARIABLE_NAME').first().fill('MY_NEW_VAR');
  await page.getByPlaceholder('value or binding ref').first().fill('hello-world');

  // Click Add to save.
  await page.getByRole('button', { name: 'Add' }).first().click();

  // Wait for the PUT to be captured.
  await expect(async () => {
    expect(capturedBody).toBeDefined();
    const body = capturedBody as Array<{ name: string; value: string }>;
    const keys = body.map(v => v.name);
    expect(keys).toContain('APP_ENV');
    expect(keys).toContain('MY_NEW_VAR');
    const myVar = body.find(v => v.name === 'MY_NEW_VAR');
    expect(myVar?.value).toBe('hello-world');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 4: Delete a variable (Trash button) → PUT called without that key
// ---------------------------------------------------------------------------
test('delete a variable calls PUT without the deleted key', async ({ page }) => {
  let capturedBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env*', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedBody = JSON.parse(route.request().postData() ?? '[]');
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ json: [{ name: 'KEEP_ME', value: 'yes' }, { name: 'DELETE_ME', value: 'bye' }] });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Wait for variables to appear.
  await expect(page.getByText('DELETE_ME')).toBeVisible({ timeout: 8_000 });

  // The trash button is only visible on hover. Hover the row then click.
  const row = page.locator('div.group').filter({ hasText: 'DELETE_ME' });
  await row.hover();
  await row.getByRole('button').click();

  await expect(async () => {
    expect(capturedBody).toBeDefined();
    const body = capturedBody as Array<{ name: string; value: string }>;
    const names = body.map(v => v.name);
    expect(names).toContain('KEEP_ME');
    expect(names).not.toContain('DELETE_ME');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 5: Inline edit a value → "Save 1 changes" button → PUT with updated value
// ---------------------------------------------------------------------------
test('inline edit calls PUT with updated value via Save changes button', async ({ page }) => {
  let capturedBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env*', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedBody = JSON.parse(route.request().postData() ?? '[]');
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ json: [{ name: 'APP_ENV', value: 'old-value' }] });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Wait for the value input to appear.
  await expect(page.getByText('APP_ENV')).toBeVisible({ timeout: 8_000 });

  // The value input is the text input within the var row.
  const valueInput = page.locator('input[placeholder="(empty)"]').first();
  await valueInput.fill('new-value');

  // The "Save 1 changes" button should now appear.
  const saveBtn = page.getByRole('button', { name: /Save \d+ change/ });
  await expect(saveBtn).toBeVisible({ timeout: 3_000 });
  await saveBtn.click();

  await expect(async () => {
    expect(capturedBody).toBeDefined();
    const body = capturedBody as Array<{ name: string; value: string }>;
    const envVar = body.find(v => v.name === 'APP_ENV');
    expect(envVar?.value).toBe('new-value');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 6: Switch to "Raw / Import" mode → textarea appears (per-section)
// ---------------------------------------------------------------------------
test('switching to Raw mode in a section shows the textarea', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
    r.fulfill({ json: [] })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Wait for production section to be visible.
  await expect(page.getByRole('button', { name: 'production', exact: true })).toBeVisible({ timeout: 8_000 });

  // Click the "Raw" mode button in the production section (first Raw button).
  await page.getByRole('button', { name: 'Raw', exact: true }).first().click();

  // Textarea with the dotenv placeholder should appear.
  const textarea = page.getByPlaceholder(/DATABASE_URL/);
  await expect(textarea).toBeVisible({ timeout: 5_000 });

  // The Import button should also appear.
  await expect(page.getByRole('button', { name: 'Import', exact: true })).toBeVisible();
});

// ---------------------------------------------------------------------------
// Test 7: Raw import calls PUT /env with merged body
// ---------------------------------------------------------------------------
test('raw import calls PUT env with correct body', async ({ page }) => {
  let capturedBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env*', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedBody = JSON.parse(route.request().postData() ?? '[]');
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ json: [] });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Switch to raw mode in production section.
  await expect(page.getByRole('button', { name: 'production', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Raw', exact: true }).first().click();

  const textarea = page.getByPlaceholder(/DATABASE_URL/);
  await expect(textarea).toBeVisible({ timeout: 5_000 });
  await textarea.fill('KEY=value\nFOO=bar');

  await page.getByRole('button', { name: 'Import', exact: true }).click();

  await expect(async () => {
    expect(capturedBody).toBeDefined();
    const body = capturedBody as Array<{ name: string; value: string }>;
    const names = body.map(v => v.name);
    expect(names).toContain('KEY');
    expect(names).toContain('FOO');
    const keyVar = body.find(v => v.name === 'KEY');
    expect(keyVar?.value).toBe('value');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 8: Shared variables section shows vars from app.spec.sharedVars
// ---------------------------------------------------------------------------
test('shared variables section renders vars from app.spec.sharedVars', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page); // mockApp has sharedVars: [{ name: 'SHARED_KEY', value: 'shared-value' }]

  await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
    r.fulfill({ json: [] })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Shared vars section header is always visible.
  await expect(page.getByText('Shared variables')).toBeVisible({ timeout: 8_000 });

  // SHARED_KEY from spec.sharedVars should be rendered without any API fetch.
  await expect(page.getByText('SHARED_KEY')).toBeVisible({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 9: Adding a shared variable calls updateApp (PUT /apps/:a) with sharedVars
// ---------------------------------------------------------------------------
test('adding a shared variable calls updateApp with sharedVars in spec', async ({ page }) => {
  let capturedAppBody: unknown;

  const appWithNoSharedVars = {
    ...mockApp,
    spec: { ...mockApp.spec, sharedVars: [] }
  };

  await injectAuth(page);
  await setupCommonMocks(page, appWithNoSharedVars);

  await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
    r.fulfill({ json: [] })
  );

  // updateApp is PUT /api/projects/:p/apps/:a (no trailing path).
  await page.route('**/api/projects/my-project/apps/web-app', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedAppBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ json: { ...appWithNoSharedVars, spec: JSON.parse(route.request().postData() ?? '{}') } });
    }
    return route.fulfill({ json: appWithNoSharedVars });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Wait for shared variables section to appear.
  await expect(page.getByText('Shared variables')).toBeVisible({ timeout: 8_000 });

  // Click the New shared variable button in the shared section.
  await page.getByRole('button', { name: 'New shared variable', exact: true }).click();

  // Fill in key and value in the shared section's input fields (last pair).
  await page.getByPlaceholder('VARIABLE_NAME').last().fill('GLOBAL_FLAG');
  await page.getByPlaceholder('value or binding ref').last().fill('true');
  await page.getByRole('button', { name: 'Add' }).last().click();

  await expect(async () => {
    expect(capturedAppBody).toBeDefined();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const body = capturedAppBody as any;
    expect(body.sharedVars).toBeDefined();
    const sharedVar = (body.sharedVars as Array<{ name: string; value: string }>).find(
      v => v.name === 'GLOBAL_FLAG'
    );
    expect(sharedVar?.value).toBe('true');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 10: Collapsing an env section hides its variables
// ---------------------------------------------------------------------------
test('collapsing an env section hides its variables', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env?environment=production', (r) =>
    r.fulfill({ json: [{ name: 'PROD_VAR', value: 'yes' }] })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env?environment=staging', (r) =>
    r.fulfill({ json: [] })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Production is expanded by default — variable should be visible.
  await expect(page.getByText('PROD_VAR')).toBeVisible({ timeout: 8_000 });

  // Click the production section header button to collapse it.
  await page.getByRole('button', { name: 'production', exact: true }).click();

  // Variable should no longer be visible.
  await expect(page.getByText('PROD_VAR')).not.toBeVisible({ timeout: 3_000 });
});

// ---------------------------------------------------------------------------
// Test 11: Staging section is collapsed by default, expands on click
// ---------------------------------------------------------------------------
test('staging section is collapsed by default and expands on click', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env?environment=production', (r) =>
    r.fulfill({ json: [] })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env?environment=staging', (r) =>
    r.fulfill({ json: [{ name: 'STAGE_VAR', value: 'maybe' }] })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // STAGE_VAR is in staging which is collapsed — should not be visible yet.
  await expect(page.getByRole('button', { name: 'staging', exact: true })).toBeVisible({ timeout: 8_000 });
  await expect(page.getByText('STAGE_VAR')).not.toBeVisible();

  // Click to expand staging section.
  await page.getByRole('button', { name: 'staging', exact: true }).click();

  // Staging vars should now be visible.
  await expect(page.getByText('STAGE_VAR')).toBeVisible({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 12: Edit an existing shared variable → Save → updateApp called with updated value
// ---------------------------------------------------------------------------
test('inline edit of shared variable calls updateApp with updated value', async ({ page }) => {
  let capturedAppBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page); // mockApp has sharedVars: [{ name: 'SHARED_KEY', value: 'shared-value' }]

  await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
    r.fulfill({ json: [] })
  );

  await page.route('**/api/projects/my-project/apps/web-app', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedAppBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ json: { ...mockApp, spec: JSON.parse(route.request().postData() ?? '{}') } });
    }
    return route.fulfill({ json: mockApp });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Wait for SHARED_KEY to appear in the shared section.
  await expect(page.getByText('SHARED_KEY')).toBeVisible({ timeout: 8_000 });

  // Find the value input for SHARED_KEY — it's the last input[placeholder="(empty)"] on the page
  // (shared section is rendered after all env sections).
  const valueInput = page.locator('input[placeholder="(empty)"]').last();
  await valueInput.fill('updated-value');

  // The "Save 1 changes" button should appear in the shared section.
  const saveBtn = page.getByRole('button', { name: /Save \d+ change/ });
  await expect(saveBtn).toBeVisible({ timeout: 3_000 });
  await saveBtn.click();

  await expect(async () => {
    expect(capturedAppBody).toBeDefined();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const body = capturedAppBody as any;
    expect(body.sharedVars).toBeDefined();
    const sharedVar = (body.sharedVars as Array<{ name: string; value: string }>).find(
      v => v.name === 'SHARED_KEY'
    );
    expect(sharedVar?.value).toBe('updated-value');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 13: Delete an existing shared variable → updateApp called without that key
// ---------------------------------------------------------------------------
test('deleting a shared variable calls updateApp without the deleted key', async ({ page }) => {
  let capturedAppBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page); // mockApp has sharedVars: [{ name: 'SHARED_KEY', value: 'shared-value' }]

  await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
    r.fulfill({ json: [] })
  );

  await page.route('**/api/projects/my-project/apps/web-app', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedAppBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ json: { ...mockApp, spec: JSON.parse(route.request().postData() ?? '{}') } });
    }
    return route.fulfill({ json: mockApp });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Wait for SHARED_KEY to appear.
  await expect(page.getByText('SHARED_KEY')).toBeVisible({ timeout: 8_000 });

  // Hover the shared var row and click the trash button.
  const row = page.locator('div.group').filter({ hasText: 'SHARED_KEY' });
  await row.hover();
  await row.getByRole('button').click();

  await expect(async () => {
    expect(capturedAppBody).toBeDefined();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const body = capturedAppBody as any;
    expect(body.sharedVars).toBeDefined();
    const names = (body.sharedVars as Array<{ name: string; value: string }>).map(v => v.name);
    expect(names).not.toContain('SHARED_KEY');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 14: Shared vars raw import → updateApp called with imported vars
// ---------------------------------------------------------------------------
test('raw import of shared variables calls updateApp with merged vars', async ({ page }) => {
  let capturedAppBody: unknown;

  const appWithNoSharedVars = {
    ...mockApp,
    spec: { ...mockApp.spec, sharedVars: [] }
  };

  await injectAuth(page);
  await setupCommonMocks(page, appWithNoSharedVars);

  await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
    r.fulfill({ json: [] })
  );

  await page.route('**/api/projects/my-project/apps/web-app', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedAppBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ json: { ...appWithNoSharedVars, spec: JSON.parse(route.request().postData() ?? '{}') } });
    }
    return route.fulfill({ json: appWithNoSharedVars });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({ timeout: 8_000 });
  await page.getByRole('button', { name: 'Variables', exact: true }).click();

  // Wait for shared variables section to appear.
  await expect(page.getByText('Shared variables')).toBeVisible({ timeout: 8_000 });

  // Click the Raw button in the shared section — shared section is last, so use .last().
  await page.getByRole('button', { name: 'Raw', exact: true }).last().click();

  // Fill the textarea with dotenv content.
  const textarea = page.getByPlaceholder(/DATABASE_URL/).last();
  await expect(textarea).toBeVisible({ timeout: 5_000 });
  await textarea.fill('SHARED_IMPORT=abc');

  // Click Import — shared section Import is last.
  await page.getByRole('button', { name: 'Import', exact: true }).last().click();

  await expect(async () => {
    expect(capturedAppBody).toBeDefined();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const body = capturedAppBody as any;
    expect(body.sharedVars).toBeDefined();
    const sharedVar = (body.sharedVars as Array<{ name: string; value: string }>).find(
      v => v.name === 'SHARED_IMPORT'
    );
    expect(sharedVar?.value).toBe('abc');
  }).toPass({ timeout: 5_000 });
});
