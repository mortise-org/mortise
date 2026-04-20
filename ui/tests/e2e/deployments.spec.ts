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
// Deployments tab E2E tests
//
// Tests cover the Deployments tab in the app drawer:
//   - Redeploying the current version
//   - Rolling back to a previous version
// ---------------------------------------------------------------------------

/** Build a realistic app fixture with deploy history. */
function buildAppFixture(
	appName: string,
	projectName: string,
	opts: { hasHistory?: boolean } = {}
) {
	const prodHistory = opts.hasHistory
		? [
				{
					image: `registry.example.com/${appName}:abc123`,
					timestamp: new Date().toISOString(),
					gitSHA: 'abc1234'
				},
				{
					image: `registry.example.com/${appName}:def456`,
					timestamp: new Date(Date.now() - 86400000).toISOString(),
					gitSHA: 'def4567'
				},
				{
					image: `registry.example.com/${appName}:ghi789`,
					timestamp: new Date(Date.now() - 172800000).toISOString(),
					gitSHA: 'ghi7890'
				}
			]
		: [
				{
					image: `registry.example.com/${appName}:abc123`,
					timestamp: new Date().toISOString(),
					gitSHA: 'abc1234'
				}
			];

	return {
		metadata: { name: appName, namespace: `pj-${projectName}` },
		spec: {
			source: { type: 'git', repo: 'https://github.com/org/my-app', branch: 'main' },
			network: { public: true, port: 8080 },
			environments: [
				{ name: 'production', replicas: 2, resources: { cpu: '500m', memory: '512Mi' } }
			],
			storage: [],
			credentials: []
		},
		status: {
			phase: 'Ready',
			environments: [
				{
					name: 'production',
					readyReplicas: 2,
					currentImage: `registry.example.com/${appName}:abc123`,
					deployHistory: prodHistory
				}
			]
		}
	};
}

test.describe('deployments tab', () => {
	let adminToken: string;
	let projectName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		projectName = `e2e-deploys-${randomSuffix()}`;
		await createProjectViaAPI(request, adminToken, projectName, 'Deployments E2E tests');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('developer redeoys the current version to fix a transient issue', async ({
		page,
		request
	}) => {
		const appName = `e2e-redeploy-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		// Return an app with a current image so Redeploy is enabled.
		await page.route(`**/api/projects/${projectName}/apps/${appName}`, async (route) => {
			if (route.request().method() === 'GET') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify(buildAppFixture(appName, projectName))
				});
			}
			return route.continue();
		});

		// Mock the deploy endpoint.
		let deployBody: unknown = null;
		await page.route(`**/api/projects/${projectName}/apps/${appName}/deploy`, async (route) => {
			if (route.request().method() === 'POST') {
				deployBody = await route.request().postDataJSON();
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						status: 'ok',
						app: appName,
						image: `registry.example.com/${appName}:abc123`
					})
				});
			}
			return route.continue();
		});

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Deployments tab is active by default.
		// Redeploy button should be enabled since we have a current image.
		const redeployBtn = page.getByRole('button', { name: 'Redeploy', exact: true });
		await expect(redeployBtn).toBeVisible({ timeout: 5_000 });
		await expect(redeployBtn).toBeEnabled();

		await redeployBtn.click();

		// The deploy API should have been called.
		await expect(async () => {
			expect(deployBody).not.toBeNull();
		}).toPass({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer rolls back to a previous version from deploy history', async ({
		page,
		request
	}) => {
		const appName = `e2e-rollback-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		// Return an app with multiple history entries.
		await page.route(`**/api/projects/${projectName}/apps/${appName}`, async (route) => {
			if (route.request().method() === 'GET') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify(buildAppFixture(appName, projectName, { hasHistory: true }))
				});
			}
			return route.continue();
		});

		// Mock rollback endpoint.
		let rollbackBody: unknown = null;
		await page.route(
			`**/api/projects/${projectName}/apps/${appName}/rollback`,
			async (route) => {
				if (route.request().method() === 'POST') {
					rollbackBody = await route.request().postDataJSON();
					return route.fulfill({
						status: 200,
						contentType: 'application/json',
						body: JSON.stringify({
							image: `registry.example.com/${appName}:def456`,
							timestamp: new Date(Date.now() - 86400000).toISOString(),
							gitSHA: 'def4567'
						})
					});
				}
				return route.continue();
			}
		);

		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// History section: second entry should have a Rollback button.
		const historyRollback = page.getByRole('button', { name: 'Rollback', exact: true }).first();
		await expect(historyRollback).toBeVisible({ timeout: 5_000 });
		await historyRollback.click();

		// The rollback API should have been called.
		await expect(async () => {
			expect(rollbackBody).not.toBeNull();
		}).toPass({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

});
