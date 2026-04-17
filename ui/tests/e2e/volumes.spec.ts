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
// Storage / volumes E2E tests
//
// Tests cover the Storage section in the app Settings tab:
//   - Adding a persistent volume
//   - Removing a volume
//   - Postgres app configured with a data volume
// ---------------------------------------------------------------------------

test.describe('storage volumes', () => {
	let adminToken: string;
	const projectName = `e2e-vols-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		await createProjectViaAPI(request, adminToken, projectName, 'Volume E2E tests');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('developer adds a persistent volume with /data mount and 5Gi size', async ({
		page,
		request
	}) => {
		const appName = `e2e-vol-add-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Settings tab.
		await page.getByRole('button', { name: 'Settings' }).click();

		// Scroll to Storage section — filter to it for speed.
		await page.getByPlaceholder('Filter settings…').fill('storage');
		await expect(page.getByText('Storage')).toBeVisible({ timeout: 5_000 });

		// Mock the updateApp call so it returns an app with the new volume.
		await page.route(`**/api/projects/${projectName}/apps/${appName}`, async (route) => {
			if (route.request().method() === 'PUT') {
				const body = await route.request().postDataJSON();
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						metadata: { name: appName, namespace: `project-${projectName}` },
						spec: { ...body, storage: [{ name: 'data', mountPath: '/data', size: '5Gi' }] },
						status: { phase: 'Ready' }
					})
				});
			}
			return route.continue();
		});

		// Click "Add volume".
		await page.getByRole('button', { name: 'Add volume' }).click();

		// Fill in the new volume form.
		await page.locator('#vol-name').fill('data');
		await page.locator('#vol-mount').fill('/data');
		await page.locator('#vol-size').fill('5Gi');

		// Submit.
		await page.getByRole('button', { name: 'Add' }).click();

		// Volume should appear in the list.
		await expect(page.getByText('data')).toBeVisible({ timeout: 5_000 });
		await expect(page.getByText('/data')).toBeVisible();
		await expect(page.getByText('5Gi')).toBeVisible();

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer removes a volume they no longer need', async ({ page, request }) => {
		const appName = `e2e-vol-del-${randomSuffix()}`;
		// Create app and immediately patch it with a volume via the API mock.
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);

		// Intercept GET to return the app pre-loaded with a volume.
		await page.route(`**/api/projects/${projectName}/apps/${appName}`, async (route) => {
			if (route.request().method() === 'GET') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						metadata: { name: appName, namespace: `project-${projectName}` },
						spec: {
							source: { type: 'image', image: 'nginx:1.27' },
							network: { public: true },
							environments: [{ name: 'production', replicas: 1 }],
							storage: [{ name: 'cache', mountPath: '/cache', size: '2Gi' }],
							credentials: []
						},
						status: { phase: 'Ready' }
					})
				});
			}
			if (route.request().method() === 'PUT') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						metadata: { name: appName, namespace: `project-${projectName}` },
						spec: {
							source: { type: 'image', image: 'nginx:1.27' },
							network: { public: true },
							environments: [{ name: 'production', replicas: 1 }],
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

		await page.getByRole('button', { name: 'Settings' }).click();

		await page.getByPlaceholder('Filter settings…').fill('storage');
		await expect(page.getByText('Storage')).toBeVisible({ timeout: 5_000 });

		// The pre-existing volume should be visible.
		await expect(page.getByText('cache')).toBeVisible({ timeout: 5_000 });
		await expect(page.getByText('/cache')).toBeVisible();

		// Click the trash icon on the volume row.
		// Trash button is the only Trash2 icon inside the volume list row.
		const volumeRow = page.locator('.rounded-md.border').filter({ hasText: 'cache' });
		await volumeRow.locator('button').click();

		// After deletion the volume should disappear.
		await expect(page.getByText('cache')).not.toBeVisible({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('database operator configures a Postgres app with a data volume', async ({
		page,
		request
	}) => {
		// Step 1: Create a Postgres app via the NewApp modal.
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/new`);

		await expect(page.getByText('Database')).toBeVisible({ timeout: 10_000 });
		await page.getByText('Database').click();

		// Postgres preset prefills the app name.
		await page.getByText('Postgres').click();
		const appNameInput = page.getByPlaceholder('my-app');
		const pgAppName = `postgres-${randomSuffix()}`;
		await appNameInput.clear();
		await appNameInput.fill(pgAppName);

		await page.getByRole('button', { name: 'Create app' }).click();

		// Should navigate to the app drawer.
		await expect(page).toHaveURL(`/projects/${projectName}/apps/${pgAppName}`, {
			timeout: 15_000
		});
		await expect(page.getByRole('heading', { name: pgAppName })).toBeVisible({ timeout: 10_000 });

		// Step 2: Open Settings and add a data volume.
		await page.getByRole('button', { name: 'Settings' }).click();
		await page.getByPlaceholder('Filter settings…').fill('storage');
		await expect(page.getByText('Storage')).toBeVisible({ timeout: 5_000 });

		// Mock the PUT to return an updated app with the volume.
		await page.route(`**/api/projects/${projectName}/apps/${pgAppName}`, async (route) => {
			if (route.request().method() === 'PUT') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						metadata: { name: pgAppName, namespace: `project-${projectName}` },
						spec: {
							source: { type: 'image', image: 'postgres:16' },
							network: { public: false },
							environments: [{ name: 'production', replicas: 1 }],
							storage: [{ name: 'pgdata', mountPath: '/var/lib/postgresql/data', size: '10Gi' }],
							credentials: [{ name: 'DATABASE_URL' }]
						},
						status: { phase: 'Ready' }
					})
				});
			}
			return route.continue();
		});

		await page.getByRole('button', { name: 'Add volume' }).click();
		await page.locator('#vol-name').fill('pgdata');
		await page.locator('#vol-mount').fill('/var/lib/postgresql/data');
		await page.locator('#vol-size').fill('10Gi');
		await page.getByRole('button', { name: 'Add' }).click();

		await expect(page.getByText('pgdata')).toBeVisible({ timeout: 5_000 });
		await expect(page.getByText('/var/lib/postgresql/data')).toBeVisible();
		await expect(page.getByText('10Gi')).toBeVisible();

		await deleteAppViaAPI(request, adminToken, projectName, pgAppName);
	});
});
