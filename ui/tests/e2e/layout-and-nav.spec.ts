/**
 * Layout and navigation tests.
 *
 * All API calls mocked via page.route(). No live backend.
 * Auth injected via localStorage before navigation.
 *
 * Tests cover:
 *  - Project switcher dropdown (open, navigate, new project)
 *  - Environment switcher (open, select env)
 *  - Activity rail (open, events, filter chips, refresh, close)
 *  - Notifications dropdown (open, events, "View all activity" link)
 *  - User menu (email, Platform Settings admin-only, sign out)
 *  - Sign out clears token and redirects to /login
 */
import { test, expect, type Page } from '@playwright/test';

// ---------------------------------------------------------------------------
// Mock data
// ---------------------------------------------------------------------------

const mockProject = {
	name: 'my-project',
	namespace: 'project-my-project',
	phase: 'Ready' as const,
	appCount: 1,
	description: ''
};

const mockProject2 = {
	name: 'other-project',
	namespace: 'project-other-project',
	phase: 'Ready' as const,
	appCount: 0,
	description: ''
};

const mockApp = {
	metadata: { name: 'web-app', namespace: 'project-my-project' },
	spec: {
		source: { type: 'image' as const, image: 'nginx:1.27' },
		network: { public: true, port: 8080 },
		environments: [{ name: 'production', replicas: 1 }, { name: 'staging', replicas: 1 }],
		storage: [],
		credentials: []
	},
	status: {
		phase: 'Ready' as const,
		environments: [{
			name: 'production',
			readyReplicas: 1,
			currentImage: 'nginx:1.27',
			deployHistory: []
		}]
	}
};

const mockActivity = [
	{
		ts: new Date().toISOString(),
		actor: 'admin@example.com',
		action: 'deploy',
		kind: 'App',
		resource: 'web-app',
		project: 'my-project',
		msg: 'Deployed web-app to production'
	},
	{
		ts: new Date(Date.now() - 60000).toISOString(),
		actor: 'admin@example.com',
		action: 'update',
		kind: 'App',
		resource: 'web-app',
		project: 'my-project',
		msg: 'Updated web-app settings'
	}
];

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function injectAuth(page: Page, isAdmin = true) {
	await page.goto('/');
	await page.evaluate(({ isAdmin }) => {
		localStorage.setItem('mortise_token', 'test-token');
		localStorage.setItem(
			'mortise_user',
			JSON.stringify({ email: 'admin@example.com', role: isAdmin ? 'admin' : 'member' })
		);
	}, { isAdmin });
}

async function setupCommonMocks(page: Page) {
	await page.route('/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
	await page.route('/api/projects', (r) => r.fulfill({ json: [mockProject, mockProject2] }));
	await page.route('/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
	await page.route('/api/projects/my-project/apps', (r) => r.fulfill({ json: [mockApp] }));
	await page.route('/api/projects/my-project/apps/web-app', (r) => r.fulfill({ json: mockApp }));
	await page.route('/api/projects/my-project/activity', (r) => r.fulfill({ json: mockActivity }));
	await page.route('/api/projects/my-project/apps/web-app/domains*', (r) =>
		r.fulfill({ json: { primary: 'web-app.example.com', custom: [] } })
	);
	await page.route('/api/projects/my-project/apps/web-app/tokens', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/apps/web-app/env/**', (r) => r.fulfill({ json: {} }));
	await page.route('/api/projects/my-project/apps/web-app/shared', (r) => r.fulfill({ json: {} }));
	await page.route('/api/projects/my-project/apps/web-app/secrets', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/other-project', (r) => r.fulfill({ json: mockProject2 }));
	await page.route('/api/projects/other-project/apps', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/other-project/activity', (r) => r.fulfill({ json: [] }));
	// Mock the "new" project routes so navigating to /projects/new doesn't
	// trigger a real API call with the fake test-token (which returns 401 and
	// redirects to /login).
	await page.route('/api/projects/new', (r) => r.fulfill({ status: 404, json: { error: 'not found' } }));
	await page.route('/api/projects/new/apps', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/new/activity', (r) => r.fulfill({ json: [] }));
	await page.route('/api/platform', (r) =>
		r.fulfill({ json: { domain: 'example.com', tls: {} } })
	);
	await page.route('/api/gitproviders', (r) => r.fulfill({ json: [] }));
}

// ---------------------------------------------------------------------------
// Project switcher
// ---------------------------------------------------------------------------

test.describe('project switcher', () => {
	test('dropdown opens and shows both projects', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		// Wait for the page to load — breadcrumb text confirms it
		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		// The project switcher button in the header shows the current project name.
		// It sits inside the header and contains the project name + chevron.
		const switcher = page.locator('header').getByRole('button').filter({ hasText: 'my-project' });
		await expect(switcher).toBeVisible({ timeout: 5_000 });
		await switcher.click();

		// Dropdown appears with both project names
		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible({ timeout: 3_000 });
		await expect(dropdown.getByText('my-project')).toBeVisible();
		await expect(dropdown.getByText('other-project')).toBeVisible();
	});

	test('selecting a project from the dropdown navigates to that project', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		const switcher = page.locator('header').getByRole('button').filter({ hasText: 'my-project' });
		await switcher.click();

		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible({ timeout: 3_000 });

		// Click the other project — it renders inside the dropdown as a button
		await dropdown.getByText('other-project').click();

		await expect(page).toHaveURL('/projects/other-project', { timeout: 5_000 });
	});

	test('+ New project in switcher navigates to /projects/new', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		const switcher = page.locator('header').getByRole('button').filter({ hasText: 'my-project' });
		await switcher.click();

		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible({ timeout: 3_000 });

		await dropdown.getByText('+ New project').click();

		await expect(page).toHaveURL('/projects/new', { timeout: 5_000 });
	});
});

// ---------------------------------------------------------------------------
// Environment switcher
// ---------------------------------------------------------------------------

test.describe('environment switcher', () => {
	test('env switcher shows production and staging environments from app envs', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		// The env switcher sits next to the project switcher in the header.
		// It shows the current env name with a green dot.
		// Click it to open the dropdown.
		const envButton = page.locator('header').getByRole('button').filter({ hasText: 'production' });
		await expect(envButton).toBeVisible({ timeout: 5_000 });
		await envButton.click();

		// Dropdown lists both envs from the app spec
		await expect(page.getByRole('button', { name: 'production' }).last()).toBeVisible({ timeout: 3_000 });
		await expect(page.getByRole('button', { name: 'staging' })).toBeVisible();
	});

	test('selecting an env from the switcher updates the displayed env name', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		// Open env switcher
		const envButton = page.locator('header').getByRole('button').filter({ hasText: 'production' });
		await expect(envButton).toBeVisible({ timeout: 5_000 });
		await envButton.click();

		// Click staging
		await page.getByRole('button', { name: 'staging' }).click();

		// The env switcher button should now show 'staging'
		await expect(
			page.locator('header').getByRole('button').filter({ hasText: 'staging' })
		).toBeVisible({ timeout: 3_000 });
	});
});

// ---------------------------------------------------------------------------
// Activity rail
// ---------------------------------------------------------------------------

test.describe('activity rail', () => {
	test('activity rail opens when Activity button clicked', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		// Activity button has title="Activity" in the header
		await page.getByTitle('Activity', { exact: true }).click();

		// Rail panel appears with "Activity" heading
		await expect(page.getByRole('heading', { name: 'Activity' })).toBeVisible({ timeout: 5_000 });
	});

	test('activity rail shows event messages from the API', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		await page.getByTitle('Activity', { exact: true }).click();

		// Both activity messages from mockActivity should appear
		await expect(page.getByText('Deployed web-app to production')).toBeVisible({ timeout: 5_000 });
		await expect(page.getByText('Updated web-app settings')).toBeVisible();
	});

	test('activity rail filter chips are present: all, deploys, changes, members', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });
		await page.getByTitle('Activity', { exact: true }).click();

		await expect(page.getByRole('heading', { name: 'Activity' })).toBeVisible({ timeout: 5_000 });

		// Filter chip buttons
		await expect(page.getByRole('button', { name: 'all' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'deploys' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'changes' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'members' })).toBeVisible();
	});

	test('deploys filter chip hides non-deploy events', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });
		await page.getByTitle('Activity', { exact: true }).click();

		await expect(page.getByRole('heading', { name: 'Activity' })).toBeVisible({ timeout: 5_000 });

		// With 'all' both events visible
		await expect(page.getByText('Deployed web-app to production')).toBeVisible();
		await expect(page.getByText('Updated web-app settings')).toBeVisible();

		// Click 'deploys' — only deploy-action events should remain
		await page.getByRole('button', { name: 'deploys' }).click();

		await expect(page.getByText('Deployed web-app to production')).toBeVisible({ timeout: 3_000 });
		// 'update' action is filtered out under 'deploys'
		await expect(page.getByText('Updated web-app settings')).toHaveCount(0);
	});

	test('activity rail refresh button re-fetches events', async ({ page }) => {
		let callCount = 0;
		await setupCommonMocks(page);
		// Override activity route to count calls
		await page.route('/api/projects/my-project/activity', (r) => {
			callCount++;
			return r.fulfill({ json: mockActivity });
		});

		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });
		await page.getByTitle('Activity', { exact: true }).click();

		await expect(page.getByRole('heading', { name: 'Activity' })).toBeVisible({ timeout: 5_000 });

		const initialCount = callCount;

		// The refresh button has title="Refresh" and contains the ↻ character
		await page.getByTitle('Refresh').click();

		// At least one more call to the activity endpoint
		await expect(async () => {
			expect(callCount).toBeGreaterThan(initialCount);
		}).toPass({ timeout: 5_000 });
	});

	test('activity rail close button hides the rail', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });
		await page.getByTitle('Activity', { exact: true }).click();

		await expect(page.getByRole('heading', { name: 'Activity' })).toBeVisible({ timeout: 5_000 });

		// Close button has aria-label="Close activity rail"
		await page.getByRole('button', { name: 'Close activity rail' }).click();

		await expect(page.getByRole('heading', { name: 'Activity' })).toHaveCount(0, { timeout: 3_000 });
	});
});

// ---------------------------------------------------------------------------
// Notifications dropdown
// ---------------------------------------------------------------------------

test.describe('notifications dropdown', () => {
	test('notifications dropdown opens when bell icon clicked', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		// Bell button has title="Notifications"
		await page.getByTitle('Notifications').click();

		// Dropdown renders with "Notifications" heading
		await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible({ timeout: 5_000 });
	});

	test('notifications dropdown shows recent deploy events', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });
		await page.getByTitle('Notifications').click();

		await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible({ timeout: 5_000 });

		// The NotificationDropdown filters to deploy/build/rollback/promote actions.
		// mockActivity has one 'deploy' event — its msg should appear.
		await expect(page.getByText('Deployed web-app to production')).toBeVisible({ timeout: 5_000 });
	});

	test('"View all activity" button opens the activity rail', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });
		await page.getByTitle('Notifications').click();

		await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible({ timeout: 5_000 });

		// "View all activity →" button at the bottom of the dropdown
		await page.getByRole('button', { name: 'View all activity →' }).click();

		// Activity rail opens
		await expect(page.getByRole('heading', { name: 'Activity' })).toBeVisible({ timeout: 5_000 });
	});
});

// ---------------------------------------------------------------------------
// User menu
// ---------------------------------------------------------------------------

test.describe('user menu', () => {
	test('user menu opens showing email and sign-out for all users', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		// The user icon button is in the header right side (no title attribute).
		// It is the last icon-only button in the header right section.
		const userBtn = page.locator('header').locator('button').filter({ has: page.locator('svg') }).last();
		await userBtn.click();

		// Dropdown shows user email
		await expect(page.getByText('admin@example.com')).toBeVisible({ timeout: 5_000 });
		// Sign out button always present
		await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
	});

	test('user menu shows Platform Settings link for admin users', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page, true); // isAdmin = true
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		const userBtn = page.locator('header').locator('button').filter({ has: page.locator('svg') }).last();
		await userBtn.click();

		await expect(page.getByRole('link', { name: 'Platform Settings' })).toBeVisible({ timeout: 5_000 });
	});

	test('user menu does not show Platform Settings for non-admin users', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page, false); // isAdmin = false (member)
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		const userBtn = page.locator('header').locator('button').filter({ has: page.locator('svg') }).last();
		await userBtn.click();

		// Platform Settings should NOT appear for members
		await expect(page.getByRole('link', { name: 'Platform Settings' })).toHaveCount(0, { timeout: 3_000 });
		// Sign out still present
		await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
	});

	test('Platform Settings link navigates to /admin/settings', async ({ page }) => {
		await setupCommonMocks(page);
		await page.route('/api/platform', (r) =>
			r.fulfill({ json: { domain: 'example.com', tls: {} } })
		);

		await injectAuth(page, true);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		const userBtn = page.locator('header').locator('button').filter({ has: page.locator('svg') }).last();
		await userBtn.click();

		await page.getByRole('link', { name: 'Platform Settings' }).click();

		await expect(page).toHaveURL('/admin/settings', { timeout: 5_000 });
	});

	test('sign out clears mortise_token from localStorage and redirects to /login', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		// Verify token is set before sign out
		const tokenBefore = await page.evaluate(() => localStorage.getItem('mortise_token'));
		expect(tokenBefore).toBe('test-token');

		// Open user menu
		const userBtn = page.locator('header').locator('button').filter({ has: page.locator('svg') }).last();
		await userBtn.click();

		await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible({ timeout: 5_000 });
		await page.getByRole('button', { name: 'Sign out' }).click();

		// Should redirect to /login
		await expect(page).toHaveURL('/login', { timeout: 5_000 });

		// Token removed from localStorage
		const tokenAfter = await page.evaluate(() => localStorage.getItem('mortise_token'));
		expect(tokenAfter).toBeNull();
	});

	test('sign out also clears mortise_user from localStorage', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		await expect(page.getByText('my-project', { exact: false }).first()).toBeVisible({ timeout: 10_000 });

		const userBtn = page.locator('header').locator('button').filter({ has: page.locator('svg') }).last();
		await userBtn.click();

		await page.getByRole('button', { name: 'Sign out' }).click();

		await expect(page).toHaveURL('/login', { timeout: 5_000 });

		const userAfter = await page.evaluate(() => localStorage.getItem('mortise_user'));
		expect(userAfter).toBeNull();
	});
});
