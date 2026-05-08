import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix,
	createGitProviderViaAPI,
	deleteGitProviderViaAPI
} from './helpers';

// End-to-end CRUD flow for GitProvider via the Mortise UI.
//
// Platform settings (including Git Providers) are now at /settings.
// The old /settings/git-providers and /admin/settings both redirect to
// /settings via client-side redirects.

test.describe('git providers', () => {
	let providerName: string;
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test.afterEach(async ({ request }) => {
		if (!providerName) {
			return;
		}
		try {
			await deleteGitProviderViaAPI(request, adminToken, providerName);
		} catch {
			// swallow — the test may have already deleted it
		}
	});

	test('/settings/git-providers redirects to /settings', async ({ page }) => {
		await injectToken(page, adminToken);

		await page.goto('/settings/git-providers');

		// Should redirect to /settings.
		await expect(page).toHaveURL('/settings', { timeout: 5_000 });
	});

	test('platform settings page renders with Git Providers section', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/settings');

		await expect(
			page.getByRole('heading', { name: 'Settings' })
		).toBeVisible({ timeout: 10_000 });

		await expect(page.getByRole('heading', { name: 'Git Providers' })).toBeVisible();
		await expect(page.getByRole('button', { name: /Add Connection/ })).toBeVisible();
	});

	test('view and delete a GitHub provider created via API', async ({ page, request }) => {
		providerName = `e2e-github-${randomSuffix()}`;

		// Create the provider via the API so we can test the UI list + delete.
		await createGitProviderViaAPI(request, adminToken, providerName);

		await injectToken(page, adminToken);
		await page.goto('/settings');

		await expect(
			page.getByRole('heading', { name: 'Settings' })
		).toBeVisible({ timeout: 10_000 });

		// Scope interaction to the git-providers section.
		const section = page.locator('section#git-providers');

		// The provider list should show our provider.
		await expect(section.getByText(providerName)).toBeVisible({ timeout: 10_000 });

		// Delete the provider. Accept the confirm() dialog before clicking the
		// trash button in the provider's row.
		page.once('dialog', (dialog) => dialog.accept());
		const providerRow = section
			.locator('div')
			.filter({ hasText: providerName })
			.filter({ has: page.getByRole('button') })
			.first();
		await providerRow.getByRole('button').last().click();

		// Provider should be gone from the list.
		await expect(section.getByText(providerName)).toHaveCount(0, { timeout: 5_000 });

		// Test passed -- skip afterEach's delete fallback.
		providerName = '';
	});

	test('platform settings shows Platform Domain section', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/settings');

		await expect(
			page.getByRole('heading', { name: 'Settings' })
		).toBeVisible({ timeout: 10_000 });

		// Platform Domain section (previously "General").
		await expect(page.getByRole('heading', { name: 'Platform Domain' })).toBeVisible();
		await expect(page.getByPlaceholder('apps.example.com')).toBeVisible();

		// Users section.
		await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
	});

	test('filter input narrows visible sections', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/settings');

		await expect(
			page.getByRole('heading', { name: 'Settings' })
		).toBeVisible({ timeout: 10_000 });

		const filterInput = page.getByPlaceholder('Filter settings...');
		await expect(filterInput).toBeVisible();

		// Typing 'git' should keep the git providers section visible.
		await filterInput.fill('git');
		await expect(page.getByRole('heading', { name: 'Git Providers' })).toBeVisible();
	});
});
