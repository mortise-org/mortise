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

		// Click "Add Provider" to show the form.
		await page.getByRole('button', { name: 'Add Provider' }).click();

		// Form appears with provider fields.
		await expect(page.getByText('New Git Provider')).toBeVisible();

		// Fill in the form. Scope inputs to the form section to avoid matching
		// other inputs on the page.
		const formSection = page.locator('section#git-providers, div').filter({ hasText: 'New Git Provider' }).last();
		await formSection.getByPlaceholder('github-main').fill(providerName);
		await formSection.getByPlaceholder('https://github.com').fill('https://github.com');

		// Fill required OAuth fields (backend validates these).
		// The form has: Name, Host URL, OAuth Client ID, OAuth Client Secret, Webhook Secret
		// Use label text to locate the inputs.
		await formSection.locator('label').filter({ hasText: 'OAuth Client ID' }).locator('~ input').fill('test-client-id');
		await formSection.locator('label').filter({ hasText: 'OAuth Client Secret' }).locator('~ input').fill('test-client-secret');
		await formSection.locator('label').filter({ hasText: 'Webhook Secret' }).locator('~ input').fill('test-webhook-secret');

		// Submit the form.
		await page.getByRole('button', { name: 'Create', exact: true }).click();

		// The form should close and the provider list should show our provider.
		await expect(page.getByText(providerName)).toBeVisible({ timeout: 10_000 });

		// Delete the provider via the trash icon button next to the provider row.
		const providerRow = page.locator('div').filter({ hasText: providerName }).last();
		await providerRow.getByRole('button').last().click();

		// Dialog confirmation.
		page.once('dialog', (dialog) => dialog.accept());

		// Provider should be gone.
		await expect(page.getByText(providerName)).toHaveCount(0, { timeout: 5_000 });

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
