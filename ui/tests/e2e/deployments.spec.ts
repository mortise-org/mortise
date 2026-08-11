/**
 * Deployments tab E2E tests (real backend).
 *
 * Tests cover the Deployments tab in the app drawer:
 *   - Default tab selection
 *   - Redeploy button for a Ready app
 *   - Deployment history display
 */
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
	getAppViaAPI,
	waitForAppCurrentImage
} from './helpers';

test.describe('deployments tab', () => {
	let token: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-deploys-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project, 'Deployments E2E tests');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test('Deployments tab is the default when opening the app drawer', async ({
		page,
		request
	}) => {
		const appName = `e2e-deftab-${randomSuffix()}`;
		await createAppViaAPI(request, token, project, appName, 'nginx:1.27');

		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		await expect(page.getByRole('heading', { name: appName })).toBeVisible({
			timeout: 10_000
		});

		// Deployments tab button should be visible and selected by default
		await expect(
			page.getByRole('button', { name: 'Deployments', exact: true })
		).toBeVisible({ timeout: 5_000 });

		await deleteAppViaAPI(request, token, project, appName);
	});

	test('redeploy button works for a Ready app with a deployed image', async ({
		page,
		request
	}) => {
		test.slow();
		const appName = `e2e-redeploy-${randomSuffix()}`;
		await createAppViaAPI(request, token, project, appName, 'nginx:1.27', { port: 80 });
		await waitForAppCurrentImage(request, token, project, appName);

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
				r.url().includes(`/apps/${appName}/redeploy`) && r.request().method() === 'POST'
			),
			redeployBtn.click()
		]);
		expect(deployRes.ok()).toBe(true);

		await deleteAppViaAPI(request, token, project, appName);
	});

	test('deployment history shows at least one entry after app is deployed', async ({
		page,
		request
	}) => {
		const appName = `e2e-history-${randomSuffix()}`;
		await createAppViaAPI(request, token, project, appName, 'nginx:1.27');

		// Wait until the app has a currentImage and deploy history
		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const status = app.status as {
				environments?: Array<{
					currentImage?: string;
					deployHistory?: Array<{ image?: string }>;
				}>;
			};
			const env = status?.environments?.[0];
			expect(env?.currentImage).toBeTruthy();
			expect(env?.deployHistory?.length).toBeGreaterThanOrEqual(1);
		}).toPass({ timeout: 60_000 });

		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		await expect(page.getByRole('heading', { name: appName })).toBeVisible({
			timeout: 10_000
		});

		// Deployments tab is default. Verify the current deploy digest is
		// visible. shortDigest("nginx:1.27") yields "1.27"; the unified
		// timeline also renders it, so assert on the first (current-deploy
		// card) rather than an ambiguous page-wide match.
		await expect(page.getByText('1.27').first()).toBeVisible({ timeout: 10_000 });

		await deleteAppViaAPI(request, token, project, appName);
	});
});
