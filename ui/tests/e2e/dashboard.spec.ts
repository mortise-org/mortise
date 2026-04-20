/**
 * Dashboard page tests — covers the main / route and layout features that
 * apply on every authenticated page (notifications bell, People nav item).
 *
 * ALL API calls are mocked via page.route(). No live backend required.
 * Tests are fully independent; each sets up its own state.
 */
import { test, expect, type Page } from '@playwright/test';

// ---------------------------------------------------------------------------
// Mock data
// ---------------------------------------------------------------------------

const mockProject = {
	name: 'my-project',
	namespace: 'project-my-project',
	phase: 'Ready' as const,
	appCount: 2,
	description: 'Test project'
};

const mockPlatform = {
	domain: 'example.com',
	tls: { certManagerClusterIssuer: 'letsencrypt-prod' },
	storage: { defaultStorageClass: 'local-path' }
};

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

async function setupMocks(page: Page) {
	await page.route('/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
	await page.route('/api/projects', (r) => r.fulfill({ json: [mockProject] }));
	await page.route('/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
	await page.route('/api/projects/my-project/activity', (r) => r.fulfill({ json: [] }));
	await page.route('/api/platform', (r) => r.fulfill({ json: mockPlatform }));
	await page.route('/api/gitproviders', (r) => r.fulfill({ json: [] }));
}

// ---------------------------------------------------------------------------
// Dashboard layout
// ---------------------------------------------------------------------------

test.describe('dashboard', () => {
	test('Test 1: Dashboard (/) renders project list and left-rail icons', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		await expect(page.getByRole('heading', { name: 'my-project', exact: true })).toBeVisible({ timeout: 5000 });
		await expect(page.getByTitle('Projects')).toBeVisible();
		await expect(page.getByTitle('Extensions')).toBeVisible();
		await expect(page.getByTitle('Platform Settings')).toBeVisible();
	});

	test('Test 2: Notifications bell is visible on the dashboard (/) page', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		await expect(page.getByTitle('Notifications')).toBeVisible({ timeout: 5000 });
	});

	test('Test 3: Notifications dropdown opens from the dashboard (/) page', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		await expect(page.getByTitle('Notifications')).toBeVisible({ timeout: 5000 });
		await page.getByTitle('Notifications').click();

		await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible({ timeout: 5000 });
	});

	test('Test 4: Activity button is NOT visible on the dashboard (/) page', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		// Activity button only shows in project context
		await expect(page.getByTitle('Activity', { exact: true })).toHaveCount(0, { timeout: 3000 });
	});

	test('Test 5: People (Users) icon is visible in left rail for all authenticated users', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, false); // non-admin member
		await page.goto('/');

		await expect(page.getByTitle('People')).toBeVisible({ timeout: 5000 });
	});

	test('Test 6: People icon is visible in left rail for admin users too', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		await expect(page.getByTitle('People')).toBeVisible({ timeout: 5000 });
	});

	test('Test 7: People icon navigates to /admin/settings', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		await page.getByTitle('People').click();

		await expect(page).toHaveURL('/admin/settings', { timeout: 5000 });
	});

	test('Test 8: People icon is not visible when inside a project context', async ({ page }) => {
		await setupMocks(page);
		await page.route('/api/projects/my-project/apps', (r) => r.fulfill({ json: [] }));
		await injectAuth(page, true);
		await page.goto('/projects/my-project');

		// In project context, left rail shows Canvas + Settings, not People
		await expect(page.getByTitle('People')).toHaveCount(0, { timeout: 3000 });
	});

	test('Test 9: Notifications bell is visible inside a project context too', async ({ page }) => {
		await setupMocks(page);
		await page.route('/api/projects/my-project/apps', (r) => r.fulfill({ json: [] }));
		await injectAuth(page, true);
		await page.goto('/projects/my-project');

		// Bell must be visible in project context as well (it was already there before)
		await expect(page.getByTitle('Notifications')).toBeVisible({ timeout: 5000 });
	});

	test('Test 10: New Project link navigates to /projects/new', async ({ page }) => {
		await setupMocks(page);
		await page.route('/api/projects/new', (r) => r.fulfill({ status: 404, json: { error: 'not found' } }));
		await page.route('/api/projects/new/apps', (r) => r.fulfill({ json: [] }));
		await page.route('/api/projects/new/activity', (r) => r.fulfill({ json: [] }));
		await injectAuth(page, true);
		await page.goto('/');

		await page.getByRole('link', { name: 'New Project' }).click();
		await expect(page).toHaveURL('/projects/new', { timeout: 5000 });
	});
});
