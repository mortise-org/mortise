/**
 * Build and deploy flow E2E tests (real backend).
 *
 * Tests cover the Docker Image deploy flow and redeploy trigger.
 * Git-source tests are skipped because E2E has no git provider configured.
 */
import { expect, test } from '@playwright/test';
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

test.describe('build and deploy', () => {
	let token: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-bnd-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test('create a Docker Image app via the new-app modal', async ({ page }) => {
		const appName = `img-create-${randomSuffix()}`;

		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		// Type picker
		await expect(
			page.getByRole('heading', { name: 'What would you like to create?' })
		).toBeVisible({ timeout: 10_000 });

		await page.getByText('Docker Image', { exact: true }).click();

		// Fill image reference and app name
		await page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest').fill('nginx:1.27');
		await page.getByPlaceholder('my-app').fill(appName);

		// Create the app
		const createBtn = page.getByRole('button', { name: 'Create app' });
		await expect(createBtn).toBeEnabled();
		await createBtn.click();

		// After creation, URL navigates to the app drawer (may include ?env= query)
		await expect(page).toHaveURL(new RegExp(`/projects/${project}/apps/${appName}(\\?|$)`), {
			timeout: 15_000
		});

		// Drawer shows the app heading
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({
			timeout: 10_000
		});

		// Deployments tab button is visible (default tab)
		await expect(
			page.getByRole('button', { name: 'Deployments', exact: true })
		).toBeVisible({ timeout: 5_000 });

		// Clean up
		await deleteAppViaAPI(page.request, token, project, appName);
	});

	test('app creation navigates to the app drawer on success', async ({ page }) => {
		const appName = `img-nav-${randomSuffix()}`;

		await injectToken(page, token);
		await page.goto(`/projects/${project}`);

		// Open the new-app modal via the Add button on the project canvas
		await page.getByRole('button', { name: 'Add', exact: true }).click();
		await expect(
			page.getByRole('heading', { name: 'What would you like to create?' })
		).toBeVisible({ timeout: 10_000 });

		// Select Docker Image
		await page.getByText('Docker Image', { exact: true }).click();

		// Fill image and app name
		await page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest').fill('nginx:1.27');
		await page.getByPlaceholder('my-app').fill(appName);

		await page.getByRole('button', { name: 'Create app' }).click();

		// Drawer opens showing the new app
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({
			timeout: 15_000
		});
		await expect(
			page.getByRole('button', { name: 'Deployments', exact: true })
		).toBeVisible({ timeout: 5_000 });

		// Clean up
		await deleteAppViaAPI(page.request, token, project, appName);
	});

	test('redeploy triggers POST /deploy for a Ready app', async ({ page, request }) => {
		test.slow();
		const appName = `img-redep-${randomSuffix()}`;
		await createAppViaAPI(request, token, project, appName, 'nginx:1.27', { port: 80 });

		// Poll until the app phase is Ready.
		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const status = app.status as {
				phase?: string;
				environments?: Array<{ phase?: string; currentImage?: string }>;
			};
			expect(status?.phase === 'Ready' || status?.environments?.[0]?.phase === 'Ready').toBeTruthy();
		}).toPass({ timeout: 90_000 });

		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		await expect(page.getByRole('heading', { name: appName })).toBeVisible({
			timeout: 10_000
		});

		// Wait for the Redeploy button to be enabled, then click it once.
		const redeployBtn = page.getByRole('button', { name: 'Redeploy', exact: true }).first();
		await expect(async () => {
			await page.reload();
			await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 5_000 });
			await expect(redeployBtn).toBeVisible({ timeout: 5_000 });
			await expect(redeployBtn).toBeEnabled({ timeout: 5_000 });
		}).toPass({ timeout: 90_000, intervals: [3_000, 5_000, 5_000] });

		const [deployRes] = await Promise.all([
			page.waitForResponse((r) =>
				r.url().includes(`/apps/${appName}/deploy`) && r.request().method() === 'POST'
			),
			redeployBtn.click()
		]);
		expect(deployRes.ok()).toBe(true);

		// Clean up
		await deleteAppViaAPI(request, token, project, appName);
	});
});
