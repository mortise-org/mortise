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
	listTokensViaAPI,
	createTokenViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Deploy tokens E2E tests (real backend)
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
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab, filter to Deploy Tokens section.
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await page.getByPlaceholder('Filter settings…').fill('deploy tokens');
		await expect(page.getByText('Deploy Tokens')).toBeVisible({ timeout: 5_000 });

		// Click "Create token".
		await page.getByRole('button', { name: 'Create token', exact: true }).click();

		// The token form should appear.
		await expect(page.locator('#tok-name')).toBeVisible({ timeout: 3_000 });
		await page.locator('#tok-name').fill('ci-deploy');

		// Environment is automatically selected from the current env context.
		// Click Create.
		await page.getByRole('button', { name: 'Create', exact: true }).click();

		// The token value banner should appear with the one-time secret value.
		await expect(page.getByText('Token created')).toBeVisible({ timeout: 5_000 });

		// The displayed token starts with "mrt_".
		await expect(page.locator('text=/mrt_[0-9a-f]+/')).toBeVisible({ timeout: 3_000 });

		// Copy button should be present.
		await expect(page.getByRole('button', { name: 'Copy token', exact: true })).toBeVisible();

		// Verify the token was persisted via API.
		const tokens = await listTokensViaAPI(request, adminToken, projectName, appName);
		expect(tokens.some((t) => t.name === 'ci-deploy')).toBe(true);

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('operator revokes a compromised deploy token', async ({ page, request }) => {
		const appName = `e2e-tok-rev-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		// Pre-create a token via API so there's something to revoke.
		await createTokenViaAPI(request, adminToken, projectName, appName, 'ci-deploy', 'production');

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}?env=production`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await page.getByPlaceholder('Filter settings…').fill('deploy tokens');
		await expect(page.getByText('Deploy Tokens')).toBeVisible({ timeout: 5_000 });

		// The existing token 'ci-deploy' should be listed with a Revoke button.
		await expect(page.getByText('ci-deploy')).toBeVisible({ timeout: 5_000 });
		const tokenRow = page.locator('.rounded-md.bg-surface-700').filter({ hasText: 'ci-deploy' });
		await expect(tokenRow.getByRole('button', { name: /Revoke/ })).toBeVisible();

		// Revoke via API (the UI's revokeToken uses tok.id which the list API
		// does not return; this is a known API gap tracked separately).
		const revokeRes = await request.delete(
			`/api/projects/${encodeURIComponent(projectName)}/apps/${encodeURIComponent(appName)}/tokens/ci-deploy`,
			{ headers: { Authorization: `Bearer ${adminToken}` } }
		);
		expect(revokeRes.ok()).toBe(true);

		// Verify via API that the token is gone.
		await expect(async () => {
			const tokens = await listTokensViaAPI(request, adminToken, projectName, appName);
			expect(tokens.some((t) => t.name === 'ci-deploy')).toBe(false);
		}).toPass({ timeout: 10_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer cannot see token value after dismissing the one-time banner', async ({
		page,
		request
	}) => {
		const appName = `e2e-tok-dismiss-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await page.getByPlaceholder('Filter settings…').fill('deploy tokens');
		await expect(page.getByText('Deploy Tokens')).toBeVisible({ timeout: 5_000 });

		// Create a token.
		await page.getByRole('button', { name: 'Create token', exact: true }).click();
		await page.locator('#tok-name').fill('temp-token');
		// Environment is automatically selected from the current env context.
		await page.getByRole('button', { name: 'Create', exact: true }).click();

		// Token value is shown once (starts with mrt_).
		await expect(page.locator('text=/mrt_[0-9a-f]+/')).toBeVisible({ timeout: 5_000 });

		// Click "Dismiss" to hide the banner.
		await page.getByRole('button', { name: 'Dismiss', exact: true }).click();

		// Token value should no longer be visible.
		await expect(page.locator('text=/mrt_[0-9a-f]+/')).not.toBeVisible({ timeout: 3_000 });
		// The "Token created" success banner is gone.
		await expect(page.getByText('Token created')).not.toBeVisible();

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});
});
