/**
 * Admin settings page tests: /admin/settings (redirects to /settings)
 *
 * Real backend only. No page.route() mocks.
 *
 * Tests cover:
 *   - Storage section visibility, ordering, and filter behavior
 *   - General section visibility with domain field
 *   - Navigation elements (settings icon, notifications bell)
 */
import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken
} from './helpers';

// ---------------------------------------------------------------------------
// Storage section
// ---------------------------------------------------------------------------

test.describe('admin settings: storage section', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('storage section is visible on settings page', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });
		const storageSection = page.locator('section#storage');
		await storageSection.scrollIntoViewIfNeeded();
		await expect(storageSection.locator('h2')).toBeVisible({ timeout: 5_000 });
	});

	test('storage section has default storage class input and save button', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const storageInput = page.locator('#storage-class');
		await storageInput.scrollIntoViewIfNeeded();
		await expect(storageInput).toBeVisible();
		await expect(page.getByRole('button', { name: 'Save storage config', exact: true })).toBeVisible();
	});

	test('storage section appears between build and TLS sections', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const buildSection = page.locator('section#build');
		const storageSection = page.locator('section#storage');
		const tlsSection = page.locator('section#tls');

		await expect(buildSection).toBeVisible();
		await storageSection.scrollIntoViewIfNeeded();
		await expect(storageSection).toBeVisible();
		await tlsSection.scrollIntoViewIfNeeded();
		await expect(tlsSection).toBeVisible();

		const buildBox = await buildSection.boundingBox();
		const storageBox = await storageSection.boundingBox();
		const tlsBox = await tlsSection.boundingBox();

		expect(buildBox!.y).toBeLessThan(storageBox!.y);
		expect(storageBox!.y).toBeLessThan(tlsBox!.y);
	});

	test('storage section is hidden when filter does not match', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const filterInput = page.getByPlaceholder('Filter settings...');
		await filterInput.fill('registry');

		await expect(page.locator('section#storage h2')).not.toBeVisible({ timeout: 3_000 });
	});

	test('storage section is shown when filter matches "storage"', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const filterInput = page.getByPlaceholder('Filter settings...');
		await filterInput.fill('storage');

		await expect(page.locator('section#storage h2')).toBeVisible({ timeout: 3_000 });
		// Other sections filtered out.
		await expect(page.locator('section#general h2')).not.toBeVisible();
	});
});

// ---------------------------------------------------------------------------
// General section and navigation elements
// ---------------------------------------------------------------------------

test.describe('admin settings: general and nav', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('settings page renders General section with domain field', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const generalSection = page.locator('section#general');
		await expect(generalSection).toBeVisible();
		await expect(page.locator('#platform-domain')).toBeVisible();
	});

	test('settings icon is visible in left rail for admin users', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/');

		await expect(page.getByTitle('Settings')).toBeVisible({ timeout: 5_000 });
	});

	test('notifications bell is visible on settings page', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });
		await expect(page.getByTitle('Notifications')).toBeVisible({ timeout: 3_000 });
	});

	test('notifications dropdown opens from settings page', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		await page.getByTitle('Notifications').click();
		await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible({ timeout: 5_000 });
	});
});
