import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix,
	deleteGitProviderViaAPI
} from './helpers';

// End-to-end CRUD flow for GitProvider via the Mortise UI.
//
// Platform settings (including Git Providers) are now at /admin/settings.
// The old /settings/git-providers redirects to /admin/settings via a
// client-side redirect.

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

	test('/settings/git-providers redirects to /admin/settings', async ({ page }) => {
		await injectToken(page, adminToken);

		await page.goto('/settings/git-providers');

		// Should redirect to /admin/settings.
		await expect(page).toHaveURL('/admin/settings', { timeout: 5_000 });
	});

	test('platform settings page renders with Git Providers section', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(
			page.getByRole('heading', { name: 'Platform Settings' })
		).toBeVisible({ timeout: 10_000 });

		await expect(page.getByRole('heading', { name: 'Git Providers' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Add Provider' })).toBeVisible();
	});

	test('create and delete a GitHub provider', async ({ page }) => {
		providerName = `e2e-github-${randomSuffix()}`;

		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(
			page.getByRole('heading', { name: 'Platform Settings' })
		).toBeVisible({ timeout: 10_000 });

		// Scope form + list interaction to the git-providers section.
		const section = page.locator('section#git-providers');

		// Click "Add Provider" to show the form.
		await section.getByRole('button', { name: 'Add Provider' }).click();

		// Form appears with provider fields.
		await expect(section.getByRole('heading', { name: 'New Git Provider' })).toBeVisible();

		await section.getByPlaceholder('github-main').fill(providerName);
		await section.getByPlaceholder('https://github.com').fill('https://github.com');

		// OAuth fields have labels with matching `for` attrs linking to input ids,
		// so getByLabel works directly.
		await section.getByLabel('OAuth Client ID').fill('test-client-id');
		await section.getByLabel('OAuth Client Secret').fill('test-client-secret');
		await section.getByLabel('Webhook Secret').fill('test-webhook-secret');

		// Submit the form.
		await section.getByRole('button', { name: 'Create', exact: true }).click();

		// The form should close and the provider list should show our provider.
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

		// Test passed — skip afterEach's delete fallback.
		providerName = '';
	});

	test('platform settings shows General and DNS sections', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(
			page.getByRole('heading', { name: 'Platform Settings' })
		).toBeVisible({ timeout: 10_000 });

		// General section.
		await expect(page.getByRole('heading', { name: 'General' })).toBeVisible();
		await expect(page.getByPlaceholder('yourdomain.com')).toBeVisible();

		// DNS section.
		await expect(page.getByRole('heading', { name: 'DNS' })).toBeVisible();

		// Users section.
		await expect(page.getByText('Users & Invites')).toBeVisible();
	});

	test('filter input narrows visible sections', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/admin/settings');

		await expect(
			page.getByRole('heading', { name: 'Platform Settings' })
		).toBeVisible({ timeout: 10_000 });

		const filterInput = page.getByPlaceholder('Filter settings...');
		await expect(filterInput).toBeVisible();

		// Typing 'git' should keep the git providers section visible.
		await filterInput.fill('git');
		await expect(page.getByRole('heading', { name: 'Git Providers' })).toBeVisible();
	});
});
