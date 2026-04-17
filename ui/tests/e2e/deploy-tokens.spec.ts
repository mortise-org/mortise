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
// Deploy tokens E2E tests
//
// Tests cover the Deploy Tokens section in the app Settings tab:
//   - Creating a deploy token for CI/CD
//   - Revoking a compromised token
//   - Verifying the token value is not shown after dismissal
// ---------------------------------------------------------------------------

test.describe('deploy tokens', () => {
	let adminToken: string;
	const projectName = `e2e-dtok-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		await createProjectViaAPI(request, adminToken, projectName, 'Deploy tokens E2E tests');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('CI engineer creates a deploy token with copy affordance', async ({ page, request }) => {
		const appName = `e2e-tok-create-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		const tokenId = 'tok-abc123';
		const tokenValue = 'dt_supersecret_token_value_abc123';

		// Mock listTokens (GET) → empty; createToken (POST) → new token with value.
		await page.route(
			`**/api/projects/${projectName}/apps/${appName}/tokens`,
			async (route) => {
				if (route.request().method() === 'GET') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify([])
					});
				}
				if (route.request().method() === 'POST') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify({
							id: tokenId,
							name: 'ci-deploy',
							app: appName,
							environment: 'production',
							createdAt: new Date().toISOString(),
							token: tokenValue
						})
					});
				}
				return route.continue();
			}
		);

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab → Deploy Tokens section.
		await page.getByRole('button', { name: 'Settings' }).click();
		await page.getByPlaceholder('Filter settings…').fill('deploy tokens');
		await expect(page.getByText('Deploy Tokens')).toBeVisible({ timeout: 5_000 });

		// Click "Create token".
		await page.getByRole('button', { name: 'Create token' }).click();

		// The token form should appear.
		await expect(page.locator('#tok-name')).toBeVisible({ timeout: 3_000 });
		await page.locator('#tok-name').fill('ci-deploy');

		// Select environment — 'production' is the only option.
		const tokEnvSelect = page.locator('#tok-env');
		await expect(tokEnvSelect).toBeVisible();
		await tokEnvSelect.selectOption('production');

		// Click Create.
		await page.getByRole('button', { name: 'Create' }).click();

		// The token value banner should appear with the secret value.
		await expect(page.getByText('Token created')).toBeVisible({ timeout: 5_000 });
		await expect(page.getByText(tokenValue)).toBeVisible();

		// Copy button (aria-label="Copy token") should be present.
		await expect(page.getByRole('button', { name: 'Copy token' })).toBeVisible();

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('operator revokes a compromised deploy token', async ({ page, request }) => {
		const appName = `e2e-tok-rev-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		const tokenId = 'tok-compromised-xyz';

		// Mock listTokens (GET) → existing token; revokeToken (DELETE) → 204.
		await page.route(
			`**/api/projects/${projectName}/apps/${appName}/tokens`,
			async (route) => {
				if (route.request().method() === 'GET') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify([
							{
								id: tokenId,
								name: 'ci-deploy',
								app: appName,
								environment: 'production',
								createdAt: new Date(Date.now() - 86400000).toISOString()
							}
						])
					});
				}
				return route.continue();
			}
		);
		await page.route(
			`**/api/projects/${projectName}/apps/${appName}/tokens/${tokenId}`,
			async (route) => {
				if (route.request().method() === 'DELETE') {
					return route.fulfill({ status: 204 });
				}
				return route.continue();
			}
		);

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Settings' }).click();
		await page.getByPlaceholder('Filter settings…').fill('deploy tokens');
		await expect(page.getByText('Deploy Tokens')).toBeVisible({ timeout: 5_000 });

		// The existing token 'ci-deploy' should be listed.
		await expect(page.getByText('ci-deploy')).toBeVisible({ timeout: 5_000 });

		// Click the Revoke button for this token.
		const tokenRow = page.locator('.rounded-md').filter({ hasText: 'ci-deploy' });
		await tokenRow.getByRole('button', { name: /Revoke/ }).click();

		// Token should be removed from the list.
		await expect(page.getByText('ci-deploy')).not.toBeVisible({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer cannot see token value after dismissing the one-time banner', async ({
		page,
		request
	}) => {
		const appName = `e2e-tok-dismiss-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		const tokenId = 'tok-dismiss-test';
		const tokenValue = 'dt_onetime_secret_abc987';

		// Mock: list returns empty; create returns the new token with value.
		await page.route(
			`**/api/projects/${projectName}/apps/${appName}/tokens`,
			async (route) => {
				if (route.request().method() === 'GET') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify([])
					});
				}
				if (route.request().method() === 'POST') {
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify({
							id: tokenId,
							name: 'temp-token',
							app: appName,
							environment: 'production',
							createdAt: new Date().toISOString(),
							token: tokenValue
						})
					});
				}
				return route.continue();
			}
		);

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Settings' }).click();
		await page.getByPlaceholder('Filter settings…').fill('deploy tokens');
		await expect(page.getByText('Deploy Tokens')).toBeVisible({ timeout: 5_000 });

		// Create a token.
		await page.getByRole('button', { name: 'Create token' }).click();
		await page.locator('#tok-name').fill('temp-token');
		await page.getByRole('button', { name: 'Create' }).click();

		// Token value is shown once.
		await expect(page.getByText(tokenValue)).toBeVisible({ timeout: 5_000 });

		// Click "Dismiss" to hide the banner.
		await page.getByRole('button', { name: 'Dismiss' }).click();

		// Token value should no longer be visible.
		await expect(page.getByText(tokenValue)).not.toBeVisible({ timeout: 3_000 });
		// The "Token created" success banner is gone.
		await expect(page.getByText('Token created')).not.toBeVisible();

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});
});
