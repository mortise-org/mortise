/**
 * Admin settings page tests — covers /admin/settings including the Storage
 * section and People nav item added in this iteration.
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
	appCount: 1,
	description: ''
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
	await page.route('/api/gitproviders', (r) => r.fulfill({ json: [] }));
	await page.route('/api/platform', (r) => r.fulfill({ json: mockPlatform }));
}

// ---------------------------------------------------------------------------
// Storage section
// ---------------------------------------------------------------------------

test.describe('admin settings — storage section', () => {
	test('Test 1: Storage section is visible on platform settings page', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });
		await expect(page.locator('section#storage h2')).toBeVisible({ timeout: 3000 });
	});

	test('Test 2: Storage section loads default storage class from platform config', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		const storageInput = page.locator('#storage-class');
		await storageInput.scrollIntoViewIfNeeded();
		await expect(storageInput).toHaveValue('local-path', { timeout: 3000 });
	});

	test('Test 3: Admin can save storage config → verifies PATCH body has storage data', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/platform', async (route) => {
			if (route.request().method() === 'PATCH') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockPlatform });
			}
			return route.fulfill({ json: mockPlatform });
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		const storageInput = page.locator('#storage-class');
		await storageInput.scrollIntoViewIfNeeded();
		await storageInput.clear();
		await storageInput.fill('longhorn');

		await page.getByRole('button', { name: 'Save storage config' }).click();

		await expect.poll(() => capturedBody).toMatchObject({
			storage: { defaultStorageClass: 'longhorn' }
		});
	});

	test('Test 4: Storage section appears between Build and TLS sections', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		// Verify order: Build section appears before Storage, Storage before TLS
		const buildSection = page.locator('section#build');
		const storageSection = page.locator('section#storage');
		const tlsSection = page.locator('section#tls');

		await expect(buildSection).toBeVisible();
		await storageSection.scrollIntoViewIfNeeded();
		await expect(storageSection).toBeVisible();
		await tlsSection.scrollIntoViewIfNeeded();
		await expect(tlsSection).toBeVisible();

		// Build should come before storage in DOM
		const buildBox = await buildSection.boundingBox();
		const storageBox = await storageSection.boundingBox();
		const tlsBox = await tlsSection.boundingBox();

		expect(buildBox!.y).toBeLessThan(storageBox!.y);
		expect(storageBox!.y).toBeLessThan(tlsBox!.y);
	});

	test('Test 5: Storage section is hidden when filter does not match', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		const filterInput = page.getByPlaceholder('Filter settings...');
		await filterInput.fill('registry');

		await expect(page.locator('section#storage h2')).not.toBeVisible({ timeout: 3000 });
	});

	test('Test 6: Storage section is shown when filter matches "storage"', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		const filterInput = page.getByPlaceholder('Filter settings...');
		await filterInput.fill('storage');

		await expect(page.locator('section#storage h2')).toBeVisible({ timeout: 3000 });
		// Other sections filtered out
		await expect(page.locator('section#general h2')).not.toBeVisible();
	});
});

// ---------------------------------------------------------------------------
// People nav item
// ---------------------------------------------------------------------------

test.describe('admin settings — People nav item', () => {
	test('Test 7: People icon is present in left rail for non-admin on dashboard', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, false);
		await page.goto('/');

		await expect(page.getByTitle('People')).toBeVisible({ timeout: 5000 });
	});

	test('Test 8: People icon links to /admin/settings for non-admin', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, false);
		await page.goto('/');

		const peopleLink = page.getByTitle('People');
		await expect(peopleLink).toHaveAttribute('href', '/admin/settings');
	});

	test('Test 9: People icon is present alongside Platform Settings for admin users', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		// Both People and Platform Settings are shown for admin
		await expect(page.getByTitle('People')).toBeVisible({ timeout: 5000 });
		await expect(page.getByTitle('Platform Settings')).toBeVisible({ timeout: 5000 });
	});

	test('Test 10: Notifications bell is visible on /admin/settings (not just in project context)', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });
		await expect(page.getByTitle('Notifications')).toBeVisible({ timeout: 3000 });
	});

	test('Test 11: Notifications dropdown opens from /admin/settings page', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		await page.getByTitle('Notifications').click();
		await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible({ timeout: 5000 });
	});
});
