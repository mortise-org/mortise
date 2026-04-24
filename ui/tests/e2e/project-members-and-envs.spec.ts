import { test, expect, type Page } from '@playwright/test';

// ---------------------------------------------------------------------------
// Project settings — Members, Environments, PR environments, Danger Zone
// (mocked backend — no live cluster required)
//
// All API calls are intercepted with page.route(). Auth is injected directly
// into localStorage.
// ---------------------------------------------------------------------------

const mockProject = {
  name: 'my-project',
  namespace: 'project-my-project',
  phase: 'Ready' as const,
  appCount: 1,
  description: 'Test project'
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

async function setupCommonMocks(page: Page, members: unknown[] = []) {
  await page.route('**/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
  await page.route('**/api/projects', (r) => r.fulfill({ json: [mockProject] }));
  await page.route('**/api/projects/my-project/apps', (r) => r.fulfill({ json: [mockApp] }));
  await page.route('**/api/projects/my-project/activity', (r) => r.fulfill({ json: [] }));
  await page.route('**/api/projects/my-project/members', (r) =>
    r.fulfill({ json: members })
  );
}

async function goToSettingsTab(page: Page, tab: string, members: unknown[] = []) {
  await injectAuth(page);

  // Set up project route before navigation.
  await page.route('**/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
  await setupCommonMocks(page, members);

  await page.goto('/projects/my-project/settings');
  await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
    timeout: 10_000
  });

  await page.getByRole('button', { name: tab }).click();
}

// ---------------------------------------------------------------------------
// Test 1: Members tab shows empty state + invite form when no members exist
// ---------------------------------------------------------------------------
test('members tab shows empty state and invite form when there are no members', async ({
  page
}) => {
  await goToSettingsTab(page, 'Members', []);

  // Empty state message.
  await expect(page.getByText('No project members yet.')).toBeVisible({ timeout: 5_000 });

  // Invite form elements.
  await expect(page.getByPlaceholder('username')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Invite' })).toBeVisible();
});

// ---------------------------------------------------------------------------
// Test 2: Invite a member → POST /members, invite link appears
// ---------------------------------------------------------------------------
test('inviting a member posts to /members and shows the invite link', async ({ page }) => {
  let capturedInviteBody: unknown;

  await injectAuth(page);
  await page.route('**/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
  await setupCommonMocks(page, []);

  // POST /members returns an invite link; after invite, GET /members returns the new member.
  let membersData: unknown[] = [];
  await page.route('**/api/projects/my-project/members', async (route) => {
    if (route.request().method() === 'POST') {
      capturedInviteBody = JSON.parse(route.request().postData() ?? '{}');
      membersData = [{ email: 'newuser@example.com', role: 'member' }];
      return route.fulfill({
        json: { token: 'invite-abc', link: 'http://localhost/invite/abc' }
      });
    }
    // GET — return current list (updated after invite).
    return route.fulfill({ json: membersData });
  });

  await page.goto('/projects/my-project/settings');
  await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
    timeout: 10_000
  });
  await page.getByRole('button', { name: 'Members' }).click();

  // Fill in the email input and submit.
  await page.getByPlaceholder('username').fill('newuser@example.com');
  await page.getByRole('button', { name: 'Invite' }).click();

  // The invite link box should appear with the returned URL.
  await expect(page.getByText('Invite link created')).toBeVisible({ timeout: 5_000 });
  await expect(page.getByText('http://localhost/invite/abc')).toBeVisible();

  // Verify the POST body.
  expect(capturedInviteBody).toMatchObject({ email: 'newuser@example.com', role: 'member' });
});

// ---------------------------------------------------------------------------
// Test 3: Copy invite link button exists (with clipboard mock)
// ---------------------------------------------------------------------------
test('copy invite link button is visible after invite and triggers clipboard write', async ({
  page
}) => {
  let clipboardText = '';

  await injectAuth(page);
  await page.route('**/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
  await setupCommonMocks(page, []);

  let membersData: unknown[] = [];
  await page.route('**/api/projects/my-project/members', async (route) => {
    if (route.request().method() === 'POST') {
      membersData = [{ email: 'copy@example.com', role: 'member' }];
      return route.fulfill({
        json: { token: 'invite-xyz', link: 'http://localhost/invite/xyz' }
      });
    }
    return route.fulfill({ json: membersData });
  });

  // Grant clipboard permissions.
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);

  // Mock navigator.clipboard.writeText so we can capture it.
  await page.addInitScript(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__clipboardWritten = '';
    Object.defineProperty(navigator, 'clipboard', {
      value: {
        writeText: async (text: string) => {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (window as any).__clipboardWritten = text;
        }
      },
      writable: true
    });
  });

  await page.goto('/projects/my-project/settings');
  await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
    timeout: 10_000
  });
  await page.getByRole('button', { name: 'Members' }).click();

  await page.getByPlaceholder('username').fill('copy@example.com');
  await page.getByRole('button', { name: 'Invite' }).click();

  // Wait for invite link box.
  await expect(page.getByText('Invite link created')).toBeVisible({ timeout: 5_000 });

  // The copy button has aria-label "Copy invite link".
  const copyBtn = page.getByRole('button', { name: 'Copy invite link' });
  await expect(copyBtn).toBeVisible();
  await copyBtn.click();

  // Verify clipboard was written.
  clipboardText = await page.evaluate(() => (window as unknown as Record<string, string>)['__clipboardWritten']);
  expect(clipboardText).toBe('http://localhost/invite/xyz');
});

// ---------------------------------------------------------------------------
// Test 4: Remove a member → DELETE /members/{email} called
// ---------------------------------------------------------------------------
test('removing a member calls DELETE /members/{email}', async ({ page }) => {
  let deleteUrlCalled = '';

  await injectAuth(page);
  await page.route('**/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
  await setupCommonMocks(page, [{ email: 'user@example.com', role: 'member' }]);

  await page.route('**/api/projects/my-project/members/user%40example.com', async (route) => {
    if (route.request().method() === 'DELETE') {
      deleteUrlCalled = route.request().url();
      return route.fulfill({ status: 204 });
    }
    return route.continue();
  });

  await page.goto('/projects/my-project/settings');
  await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
    timeout: 10_000
  });
  await page.getByRole('button', { name: 'Members' }).click();

  // Member should appear.
  await expect(page.getByText('user@example.com')).toBeVisible({ timeout: 5_000 });

  // Click the Remove button next to that member.
  // Find the row that contains both the email text and a Remove button.
  const memberRow = page.locator('div').filter({ hasText: /user@example\.com/ }).filter({ has: page.getByRole('button', { name: 'Remove' }) }).first();
  await memberRow.getByRole('button', { name: 'Remove' }).click();

  await expect(async () => {
    expect(deleteUrlCalled).toContain('members');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 5: Environments tab shows the "New environment" button
// ---------------------------------------------------------------------------
test('environments tab shows New environment button', async ({ page }) => {
  await goToSettingsTab(page, 'Environments');

  await expect(page.getByRole('button', { name: 'New environment' })).toBeVisible({
    timeout: 5_000
  });
});

// ---------------------------------------------------------------------------
// Test 6: Add a new environment via inline form (local state, no API call)
// ---------------------------------------------------------------------------
test('adding an environment via the inline form shows it in the list', async ({ page }) => {
  await goToSettingsTab(page, 'Environments');

  // Click "New environment".
  await page.getByRole('button', { name: 'New environment' }).click();

  // Inline form should appear with an env name input.
  const envInput = page.locator('input#new-env');
  await expect(envInput).toBeVisible({ timeout: 5_000 });

  // Fill in the name and create.
  await envInput.fill('canary');
  await page.getByRole('button', { name: 'Create' }).click();

  // The new environment should appear in the list.
  await expect(page.getByText('canary')).toBeVisible({ timeout: 5_000 });

  // The form should be dismissed.
  await expect(envInput).not.toBeVisible();
});

// ---------------------------------------------------------------------------
// Test 7: PR environments toggle auto-saves → PATCH called with preview.enabled
// ---------------------------------------------------------------------------
test('PR environments toggle auto-saves and calls PATCH with preview.enabled', async ({
  page
}) => {
  let capturedPatchBody: unknown;

  await injectAuth(page);

  await page.route('**/api/projects/my-project', async (route) => {
    if (route.request().method() === 'PATCH') {
      capturedPatchBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({
        json: { ...mockProject, preview: { enabled: true } }
      });
    }
    return route.fulfill({ json: mockProject });
  });
  await setupCommonMocks(page);

  await page.goto('/projects/my-project/settings');
  await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
    timeout: 10_000
  });

  // The General tab is active by default; PR Environments section is in General.
  await expect(page.getByText('Enable PR Environments')).toBeVisible({ timeout: 5_000 });

  // Click the switch toggle.
  const toggle = page.getByRole('switch');
  await toggle.click();

  // PATCH should be called automatically (no separate save button needed for toggle).
  await expect(async () => {
    expect(capturedPatchBody).toBeDefined();
    const body = capturedPatchBody as Record<string, unknown>;
    const preview = body['preview'] as Record<string, unknown>;
    expect(preview['enabled']).toBe(true);
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 8: Save PR config (domain template + TTL) → PATCH /api/projects/my-project
// ---------------------------------------------------------------------------
test('saving PR config calls PATCH with domain template and TTL', async ({ page }) => {
  let capturedPatchBody: unknown;

  await injectAuth(page);

  await page.route('**/api/projects/my-project', async (route) => {
    if (route.request().method() === 'PATCH') {
      capturedPatchBody = JSON.parse(route.request().postData() ?? '{}');
      return route.fulfill({ json: mockProject });
    }
    return route.fulfill({ json: mockProject });
  });
  await setupCommonMocks(page);

  await page.goto('/projects/my-project/settings');
  await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
    timeout: 10_000
  });

  // Fill in domain template.
  const domainInput = page.locator('input#pr-domain');
  await expect(domainInput).toBeVisible({ timeout: 5_000 });
  await domainInput.fill('pr-{number}.{app}.example.com');

  // Set TTL to 1 week.
  const ttlSelect = page.locator('select#pr-ttl');
  await ttlSelect.selectOption('168h');

  // Click "Save PR config".
  await page.getByRole('button', { name: 'Save PR config' }).click();

  await expect(async () => {
    expect(capturedPatchBody).toBeDefined();
    const body = capturedPatchBody as Record<string, unknown>;
    const preview = body['preview'] as Record<string, unknown>;
    expect(preview['domainTemplate']).toBe('pr-{number}.{app}.example.com');
    expect(preview['ttl']).toBe('168h');
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 9: Danger zone: type project name + click delete → DELETE /api/projects/my-project
// ---------------------------------------------------------------------------
test('danger zone delete project calls DELETE after name confirmation', async ({ page }) => {
  let deleteCalled = false;

  await injectAuth(page);
  await page.route('**/api/projects/my-project', async (route) => {
    if (route.request().method() === 'DELETE') {
      deleteCalled = true;
      return route.fulfill({ json: { status: 'ok', project: 'my-project' } });
    }
    return route.fulfill({ json: mockProject });
  });
  await setupCommonMocks(page);

  // Mock the redirect target so goto('/') doesn't 404.
  await page.route('**/api/projects', (r) => r.fulfill({ json: [] }));

  await page.goto('/projects/my-project/settings');
  await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
    timeout: 10_000
  });

  // Navigate to Danger tab.
  await page.getByRole('button', { name: 'Danger' }).click();

  // The delete button starts disabled.
  const deleteBtn = page.getByRole('button', { name: 'Delete project' });
  await expect(deleteBtn).toBeDisabled({ timeout: 5_000 });

  // Type the project name into the confirmation input.
  const confirmInput = page.locator('input#del-confirm');
  await expect(confirmInput).toBeVisible();
  await confirmInput.fill('my-project');

  // Button should now be enabled.
  await expect(deleteBtn).toBeEnabled({ timeout: 3_000 });
  await deleteBtn.click();

  await expect(async () => {
    expect(deleteCalled).toBe(true);
  }).toPass({ timeout: 5_000 });
});

// ---------------------------------------------------------------------------
// Test 10: Environments tab has a link to active PR environments
// ---------------------------------------------------------------------------
test('environments tab shows a link to active PR environments', async ({ page }) => {
  await goToSettingsTab(page, 'Environments');

  // The link text is "View active PR environments →".
  const prLink = page.getByRole('link', { name: 'View active PR environments →' });
  await expect(prLink).toBeVisible({ timeout: 5_000 });
  await expect(prLink).toHaveAttribute('href', '/projects/my-project/previews');
});

// ---------------------------------------------------------------------------
// Test 11: Environments tab Cancel button closes the inline form
// ---------------------------------------------------------------------------
test('environments tab Cancel button dismisses the new environment form', async ({ page }) => {
  await goToSettingsTab(page, 'Environments');

  await page.getByRole('button', { name: 'New environment' }).click();

  const envInput = page.locator('input#new-env');
  await expect(envInput).toBeVisible({ timeout: 5_000 });

  await page.getByRole('button', { name: 'Cancel' }).click();

  // Form should be gone.
  await expect(envInput).not.toBeVisible();
});
