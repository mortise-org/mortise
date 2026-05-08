import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	createAppViaAPI,
	deleteProjectViaAPI,
	deleteAppViaAPI,
	listDomainsViaAPI,
	addDomainViaAPI,
	removeDomainViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Domains E2E tests (real backend)
//
// Tests cover the Domains section in the app Settings tab:
//   - Adding a custom domain
//   - Removing a custom domain
//   - Verifying domains via the API
// ---------------------------------------------------------------------------

test.describe('domains', () => {
	let adminToken: string;
	const projectName = `e2e-doms-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		await createProjectViaAPI(request, adminToken, projectName, 'Domains E2E tests');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('developer adds a custom domain to their production app', async ({ page, request }) => {
		const appName = `e2e-dom-add-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		const customDomain = `${appName}.example.com`;

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab, filter to Domains section.
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible({ timeout: 5_000 });

		// Type the custom domain into the input.
		const domainInput = page.getByPlaceholder('custom.example.com');
		await expect(domainInput).toBeVisible();
		await domainInput.fill(customDomain);

		// Click Add.
		await page.getByRole('button', { name: 'Add', exact: true }).click();

		// The domain should appear in the list (optimistic UI may need a moment).
		await expect(async () => {
			await expect(page.getByText(customDomain)).toBeVisible({ timeout: 3_000 });
		}).toPass({ timeout: 10_000 });

		// Verify via API that the domain was persisted.
		await expect(async () => {
			const domains = await listDomainsViaAPI(request, adminToken, projectName, appName);
			expect(domains.custom ?? []).toContain(customDomain);
		}).toPass({ timeout: 10_000 });

		// Clean up the domain via API before deleting the app.
		await removeDomainViaAPI(request, adminToken, projectName, appName, customDomain);
		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer removes a custom domain that is no longer in use', async ({ page, request }) => {
		const appName = `e2e-dom-rm-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		const existingDomain = `old-${appName}.example.com`;

		// Pre-add a domain via the API so there's something to remove.
		await addDomainViaAPI(request, adminToken, projectName, appName, existingDomain);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible({ timeout: 5_000 });

		// The existing domain should be visible.
		await expect(page.getByText(existingDomain)).toBeVisible({ timeout: 5_000 });

		// Click "Remove" next to the domain.
		const domainRow = page.locator('.rounded-md').filter({ hasText: existingDomain });
		await domainRow.getByRole('button', { name: 'Remove' }).click();

		// Domain should disappear from the UI.
		await expect(page.getByText(existingDomain)).not.toBeVisible({ timeout: 5_000 });

		// Verify via API that the domain was removed.
		await expect(async () => {
			const domains = await listDomainsViaAPI(request, adminToken, projectName, appName);
			expect(domains.custom ?? []).not.toContain(existingDomain);
		}).toPass({ timeout: 10_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer adds and verifies a domain end-to-end via API', async ({ page, request }) => {
		const appName = `e2e-dom-verify-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		const customDomain = `verify-${appName}.example.com`;

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab, filter to Domains.
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible({ timeout: 5_000 });

		// Add the domain via the UI.
		const domainInput = page.getByPlaceholder('custom.example.com');
		await domainInput.fill(customDomain);
		await page.getByRole('button', { name: 'Add', exact: true }).click();

		// Wait for it to appear in the UI.
		await expect(page.getByText(customDomain)).toBeVisible({ timeout: 5_000 });

		// Cross-verify: API should show the same domain.
		await expect(async () => {
			const domains = await listDomainsViaAPI(request, adminToken, projectName, appName);
			expect(domains.custom ?? []).toContain(customDomain);
		}).toPass({ timeout: 10_000 });

		await removeDomainViaAPI(request, adminToken, projectName, appName, customDomain);
		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});
});
