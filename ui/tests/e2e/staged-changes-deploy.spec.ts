/**
 * Staged-changes deploy flow tests.
 *
 * Tests verify that settings changes made through the UI are persisted
 * to the backend via the real API. No page.route() mocking.
 */
import { test, expect } from '@playwright/test';
import {
	randomSuffix,
	ensureAdmin,
	loginViaAPI,
	injectToken,
	createProjectViaAPI,
	createAppViaAPI,
	deleteProjectViaAPI,
	deleteAppViaAPI,
	getAppViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('staged changes deploy flow', () => {
	let adminToken: string;
	let projectName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		projectName = `e2e-staged-${randomSuffix()}`;
		await createProjectViaAPI(request, adminToken, projectName, 'Staged changes E2E');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('settings tab renders all sections for an image app', async ({ page, request }) => {
		const appName = `img-settings-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await expect(page.getByPlaceholder('Filter settings…')).toBeVisible({ timeout: 5_000 });

		// Verify key settings sections are rendered
		await expect(page.getByRole('heading', { name: 'Source' })).toBeVisible({ timeout: 5_000 });
		await expect(page.getByRole('heading', { name: 'Scale' })).toBeVisible({ timeout: 5_000 });
		await expect(page.getByRole('heading', { name: 'Networking' })).toBeVisible({ timeout: 5_000 });
		await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('updating replicas via Settings persists the change', async ({ page, request }) => {
		const appName = `scale-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await expect(page.getByPlaceholder('Filter settings…')).toBeVisible({ timeout: 5_000 });

		// Change replica count from 1 to 3
		const replicasInput = page.getByLabel('Replicas');
		await replicasInput.clear();
		await replicasInput.fill('3');

		// Click the Scale "Update" button
		const updateBtns = page.getByRole('button', { name: 'Update', exact: true });
		await updateBtns.last().click();

		// Verify the change persisted via API
		await expect(async () => {
			const app = await getAppViaAPI(request, adminToken, projectName, appName);
			const spec = app.spec as Record<string, unknown>;
			const envs = spec.environments as Array<{ name: string; replicas: number }>;
			const production = envs.find((env) => env.name === 'production');
			expect(production?.replicas).toBe(3);
		}).toPass({ timeout: 10_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('no staged-changes bar visible when no changes have been made', async ({
		page,
		request
	}) => {
		const appName = `nobar-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}`);
		await expect(page.getByRole('button', { name: 'Add', exact: true })).toBeVisible({ timeout: 10_000 });

		// When no staged changes exist, the bar and Discard button should not be visible
		await expect(page.getByRole('button', { name: 'Discard', exact: true })).toHaveCount(0);
		await expect(
			page.getByText(/^\d+ changes? to apply$/i)
		).toHaveCount(0);

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('updating image source via Settings persists the change', async ({ page, request }) => {
		const appName = `src-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await expect(page.getByPlaceholder('Filter settings…')).toBeVisible({ timeout: 5_000 });

		// Change the image
		const imageInput = page.getByLabel('Image');
		await imageInput.clear();
		await imageInput.fill('nginx:1.28');

		// Click Update in Source section (first Update button)
		await page.getByRole('button', { name: 'Update', exact: true }).first().click();

		// Verify the change persisted via API
		await expect(async () => {
			const app = await getAppViaAPI(request, adminToken, projectName, appName);
			const spec = app.spec as Record<string, unknown>;
			const source = spec.source as { type: string; image: string };
			expect(source.image).toBe('nginx:1.28');
		}).toPass({ timeout: 10_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});
});
