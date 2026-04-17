import { test, expect, type Page } from '@playwright/test';

// ---------------------------------------------------------------------------
// VariablesTab E2E tests (mocked backend — no live cluster required)
//
// All API calls are intercepted with page.route(). Auth is injected directly
// into localStorage. Tests cover the full VariablesTab surface: form mode,
// raw/import mode, add/edit/delete, env tab switching, and shared vars.
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
  await page.goto('/');
  await page.evaluate(() => {
    localStorage.setItem('mortise_token', 'test-token');
    localStorage.setItem(
      'mortise_user',
      JSON.stringify({ email: 'admin@example.com', role: 'admin' })
    );
  });
}

async function setupCommonMocks(page: Page) {
  await page.route('**/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
  await page.route('**/api/projects', (r) => r.fulfill({ json: [mockProject] }));
  await page.route('**/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
  await page.route('**/api/projects/my-project/apps', (r) => r.fulfill({ json: [mockApp] }));
  await page.route('**/api/projects/my-project/apps/web-app', (r) => r.fulfill({ json: mockApp }));
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

async function goToVariablesTab(page: Page) {
  await injectAuth(page);
  await setupCommonMocks(page);
  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();
}

// ---------------------------------------------------------------------------
// Test 1: Variables tab loads existing variables from GET /env/production
// ---------------------------------------------------------------------------
test('variables tab shows existing variables loaded from env/production', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', (r) =>
    r.fulfill({ json: { APP_ENV: 'production', DEBUG: 'false' } })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: {} })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

  // Both variable keys must be visible in the table.
  await expect(page.getByText('APP_ENV')).toBeVisible({ timeout: 8_000 });
  await expect(page.getByText('DEBUG')).toBeVisible();
});

// ---------------------------------------------------------------------------
// Test 2: Add a new variable via form → PUT called with new key
// ---------------------------------------------------------------------------
test('add new variable via form calls PUT with the new key', async ({ page }) => {
  let capturedBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ json: { APP_ENV: 'production' } });
  });
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: {} })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

  // Expand the new variable row.
  await page.getByRole('button', { name: 'New variable' }).click();

  // Fill key and value.
  await page.getByPlaceholder('VARIABLE_NAME').fill('MY_NEW_VAR');
  await page.getByPlaceholder('value or binding ref').fill('hello-world');

  // Click Add to save.
  await page.getByRole('button', { name: 'Add' }).click();

  // Wait for the PUT to be captured.
  await expect(async () => {
    expect(capturedBody).toMatchObject({ APP_ENV: 'production', MY_NEW_VAR: 'hello-world' });
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 3: Delete a variable (Trash button) → PUT called without that key
// ---------------------------------------------------------------------------
test('delete a variable calls PUT without the deleted key', async ({ page }) => {
  let capturedBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ json: { KEEP_ME: 'yes', DELETE_ME: 'bye' } });
  });
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: {} })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

  // Wait for variables to appear.
  await expect(page.getByText('DELETE_ME')).toBeVisible({ timeout: 8_000 });

  // The trash button is only visible on hover. Hover the row then click.
  const row = page.locator('div.group').filter({ hasText: 'DELETE_ME' });
  await row.hover();
  await row.getByRole('button').click();

  await expect(async () => {
    expect(capturedBody).toBeDefined();
    const body = capturedBody as Record<string, string>;
    expect(body['KEEP_ME']).toBe('yes');
    expect(body['DELETE_ME']).toBeUndefined();
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 4: Inline edit a value → "Save 1 changes" button → PUT with updated value
// ---------------------------------------------------------------------------
test('inline edit calls PUT with updated value via Save changes button', async ({ page }) => {
  let capturedBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ json: { APP_ENV: 'old-value' } });
  });
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: {} })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

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
    expect(capturedBody).toMatchObject({ APP_ENV: 'new-value' });
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 5: Switch to "Raw / Import" mode → textarea appears
// ---------------------------------------------------------------------------
test('switching to Raw/Import mode shows the textarea', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', (r) =>
    r.fulfill({ json: {} })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: {} })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

  // Click the "Raw / Import" mode button.
  await page.getByRole('button', { name: 'Raw / Import' }).click();

  // Textarea with the dotenv placeholder should appear.
  const textarea = page.getByPlaceholder(/DATABASE_URL/);
  await expect(textarea).toBeVisible({ timeout: 5_000 });

  // The Import button should also appear.
  await expect(page.getByRole('button', { name: 'Import' })).toBeVisible();
});

// ---------------------------------------------------------------------------
// Test 6: Raw import calls POST /env/import with { env, content }
// ---------------------------------------------------------------------------
test('raw import calls POST env/import with correct body', async ({ page }) => {
  let capturedImportBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', (r) =>
    r.fulfill({ json: {} })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: {} })
  );

  // Mock the import endpoint.
  await page.route('**/api/projects/my-project/apps/web-app/env/import', async (route) => {
    capturedImportBody = JSON.parse(route.request().postData() ?? '{}');
    return route.fulfill({ status: 204 });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

  // Switch to raw mode.
  await page.getByRole('button', { name: 'Raw / Import' }).click();

  const textarea = page.getByPlaceholder(/DATABASE_URL/);
  await expect(textarea).toBeVisible({ timeout: 5_000 });
  await textarea.fill('KEY=value\nFOO=bar');

  await page.getByRole('button', { name: 'Import' }).click();

  await expect(async () => {
    expect(capturedImportBody).toMatchObject({ env: 'production', content: 'KEY=value\nFOO=bar' });
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 7: Switch to Shared tab → GET /shared called, shared var form appears
// ---------------------------------------------------------------------------
test('switching to Shared tab loads shared vars endpoint', async ({ page }) => {
  let sharedFetched = false;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', (r) =>
    r.fulfill({ json: {} })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: {} })
  );
  await page.route('**/api/projects/my-project/apps/web-app/shared', async (route) => {
    sharedFetched = true;
    return route.fulfill({ json: { SHARED_KEY: 'shared-value' } });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

  // Click the "Shared" tab button.
  await page.getByRole('button', { name: 'Shared' }).click();

  // Wait until shared vars load.
  await expect(async () => {
    expect(sharedFetched).toBe(true);
  }).toPass({ timeout: 5_000 });

  // The shared variable key should be visible.
  await expect(page.getByText('SHARED_KEY')).toBeVisible({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 8: Add shared variable → PUT /shared called with correct body
// ---------------------------------------------------------------------------
test('adding a shared variable calls PUT /shared with correct body', async ({ page }) => {
  let capturedSharedBody: unknown;

  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', (r) =>
    r.fulfill({ json: {} })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: {} })
  );
  await page.route('**/api/projects/my-project/apps/web-app/shared', async (route) => {
    if (route.request().method() === 'PUT') {
      capturedSharedBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ json: {} });
  });

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

  // Switch to Shared tab.
  await page.getByRole('button', { name: 'Shared' }).click();

  // Wait for shared tab to load.
  await expect(page.getByRole('button', { name: 'New variable' })).toBeVisible({
    timeout: 5_000
  });

  // Click New variable and fill in a shared var.
  await page.getByRole('button', { name: 'New variable' }).click();
  await page.getByPlaceholder('VARIABLE_NAME').fill('GLOBAL_FLAG');
  await page.getByPlaceholder('value or binding ref').fill('true');
  await page.getByRole('button', { name: 'Add' }).click();

  await expect(async () => {
    expect(capturedSharedBody).toMatchObject({ GLOBAL_FLAG: 'true' });
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 9: Switching between env tabs updates selected state
// ---------------------------------------------------------------------------
test('switching environment tab changes the active tab highlight', async ({ page }) => {
  await injectAuth(page);
  await setupCommonMocks(page);

  await page.route('**/api/projects/my-project/apps/web-app/env/production', (r) =>
    r.fulfill({ json: { PROD_VAR: 'yes' } })
  );
  await page.route('**/api/projects/my-project/apps/web-app/env/staging', (r) =>
    r.fulfill({ json: { STAGE_VAR: 'maybe' } })
  );

  await page.goto('/projects/my-project/apps/web-app');
  await page.getByRole('button', { name: 'Variables' }).click();

  // Production is selected by default.
  await expect(page.getByText('PROD_VAR')).toBeVisible({ timeout: 8_000 });

  // Click staging tab.
  await page.getByRole('button', { name: 'staging' }).click();

  // Staging vars should now load.
  await expect(page.getByText('STAGE_VAR')).toBeVisible({ timeout: 5_000 });

  // Production var should no longer be visible.
  await expect(page.getByText('PROD_VAR')).not.toBeVisible();
});
