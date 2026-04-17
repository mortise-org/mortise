import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	createAppViaAPI,
	deleteProjectViaAPI,
	deleteAppViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Domains E2E tests
//
// Tests cover the Domains section in the app Settings tab:
//   - Adding a custom domain
//   - Removing a custom domain
//   - Copying the primary domain from the Networking section
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

		const customDomain = 'my-app.example.com';

		// Mock the listDomains and addDomain calls.
		await page.route(
			`**/api/projects/${projectName}/apps/${appName}/domains**`,
			async (route) => {
				if (route.request().method() === 'GET') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify({ primary: null, custom: [] })
					});
				}
				if (route.request().method() === 'POST') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify({ primary: null, custom: [customDomain] })
					});
				}
				return route.continue();
			}
		);

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab → Domains section.
		await page.getByRole('button', { name: 'Settings' }).click();
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByText('Domains')).toBeVisible({ timeout: 5_000 });

		// Type the custom domain into the input.
		const domainInput = page.getByPlaceholder('custom.example.com');
		await expect(domainInput).toBeVisible();
		await domainInput.fill(customDomain);

		// Click Add.
		await page.getByRole('button', { name: 'Add' }).click();

		// The domain should appear in the list.
		await expect(page.getByText(customDomain)).toBeVisible({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer removes a custom domain that is no longer in use', async ({ page, request }) => {
		const appName = `e2e-dom-rm-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		const existingDomain = 'old-domain.example.com';

		// Mock domains — GET returns one custom domain; DELETE returns it removed.
		await page.route(
			`**/api/projects/${projectName}/apps/${appName}/domains**`,
			async (route) => {
				if (route.request().method() === 'GET') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify({ primary: null, custom: [existingDomain] })
					});
				}
				if (route.request().method() === 'DELETE') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify({ primary: null, custom: [] })
					});
				}
				return route.continue();
			}
		);

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Settings' }).click();
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByText('Domains')).toBeVisible({ timeout: 5_000 });

		// The existing domain should be visible.
		await expect(page.getByText(existingDomain)).toBeVisible({ timeout: 5_000 });

		// Click "Remove" next to the domain.
		const domainRow = page.locator('.rounded-md').filter({ hasText: existingDomain });
		await domainRow.getByRole('button', { name: 'Remove' }).click();

		// Domain should disappear.
		await expect(page.getByText(existingDomain)).not.toBeVisible({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer sees the primary domain in Networking section with a copy button', async ({
		page,
		request
	}) => {
		const appName = `e2e-dom-primary-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		const primaryDomain = `${appName}.example.com`;

		await injectToken(page, adminToken);

		// Mock the app GET to include a primary domain on the first environment.
		await page.route(`**/api/projects/${projectName}/apps/${appName}`, async (route) => {
			if (route.request().method() === 'GET') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						metadata: { name: appName, namespace: `project-${projectName}` },
						spec: {
							source: { type: 'image', image: 'nginx:1.27' },
							network: { public: true, port: 8080 },
							environments: [
								{
									name: 'production',
									replicas: 1,
									domain: primaryDomain
								}
							],
							storage: [],
							credentials: []
						},
						status: { phase: 'Ready' }
					})
				});
			}
			return route.continue();
		});

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab → Networking section.
		await page.getByRole('button', { name: 'Settings' }).click();
		await page.getByPlaceholder('Filter settings…').fill('networking');
		await expect(page.getByText('Networking')).toBeVisible({ timeout: 5_000 });

		// Primary domain should be displayed.
		await expect(page.getByText(primaryDomain)).toBeVisible({ timeout: 5_000 });

		// Copy button (aria-label="Copy domain") should be visible.
		await expect(page.getByRole('button', { name: 'Copy domain' })).toBeVisible();

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});
});
