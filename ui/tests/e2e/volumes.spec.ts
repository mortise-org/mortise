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
	getAppViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Storage / volumes E2E tests (real backend)
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
		await page.getByRole('button', { name: 'Settings', exact: true }).click();

		// Scroll to Storage section via filter.
		await page.getByPlaceholder('Filter settings…').fill('storage');
		await expect(page.getByRole('heading', { name: 'Storage' })).toBeVisible({ timeout: 5_000 });

		// Click "Add volume".
		await page.getByRole('button', { name: 'Add volume', exact: true }).click();

		// Fill in the new volume form.
		await page.locator('#vol-name').fill('data');
		await page.locator('#vol-mount').fill('/data');
		await page.locator('#vol-size').fill('5Gi');

		// Submit.
		const volForm = page.locator('#vol-name').locator('xpath=ancestor::div[3]');
		const addBtn = volForm.getByRole('button', { name: 'Add', exact: true });
		await expect(addBtn).toBeEnabled({ timeout: 5_000 });
		const addResponsePromise = page.waitForResponse((r) =>
			r.url().includes(`/apps/${appName}`) && r.request().method() === 'PUT'
		);
		await addBtn.click();
		await addResponsePromise;

		// Verify via API that spec.storage was updated.
		await expect(async () => {
			const app = await getAppViaAPI(request, adminToken, projectName, appName);
			const spec = app.spec as { storage?: Array<{ name: string; mountPath: string; size: string }> };
			expect(spec.storage).toEqual(
				expect.arrayContaining([
					expect.objectContaining({ name: 'data', mountPath: '/data', size: '5Gi' })
				])
			);
		}).toPass({ timeout: 10_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer removes a volume they no longer need', async ({ page, request }) => {
		const appName = `e2e-vol-del-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		// Pre-add a volume via API (PUT the app spec with storage).
		const app = await getAppViaAPI(request, adminToken, projectName, appName);
		const spec = app.spec as Record<string, unknown>;
		await request.put(
			`/api/projects/${encodeURIComponent(projectName)}/apps/${encodeURIComponent(appName)}`,
			{
				headers: { Authorization: `Bearer ${adminToken}` },
				data: { ...spec, storage: [{ name: 'cache', mountPath: '/cache', size: '2Gi' }] }
			}
		);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Settings', exact: true }).click();

		await page.getByPlaceholder('Filter settings…').fill('storage');
		await expect(page.getByRole('heading', { name: 'Storage' })).toBeVisible({ timeout: 5_000 });

		// The pre-existing volume should be visible (use .first() to avoid matching the canvas AppNode badge).
		await expect(page.getByText('cache', { exact: true }).first()).toBeVisible({ timeout: 5_000 });
		await expect(page.getByText('/cache')).toBeVisible();

		// Click the trash icon on the volume row.
		const volumeRow = page.locator('.rounded-md.border').filter({ hasText: 'cache' });
		const removeResponsePromise = page.waitForResponse((r) =>
			r.url().includes(`/apps/${appName}`) && r.request().method() === 'PUT'
		);
		await volumeRow.locator('button').click();
		await removeResponsePromise;

		// Verify via API that storage is now empty. The save may fail silently
		// due to resource version conflicts; retry the check.
		await expect(async () => {
			const updated = await getAppViaAPI(request, adminToken, projectName, appName);
			const updatedSpec = updated.spec as { storage?: unknown[] };
			expect(updatedSpec.storage ?? []).toHaveLength(0);
		}).toPass({ timeout: 10_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('database operator configures a Postgres app with a data volume', async ({
		page,
		request
	}) => {
		// Create a Postgres app via the NewApp modal.
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/new`);

		await expect(page.getByText('Database', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Database', { exact: true }).click();

		// Postgres preset prefills the app name.
		await page.getByText('Postgres', { exact: true }).click();
		const appNameInput = page.getByPlaceholder('my-app');
		const pgAppName = `postgres-${randomSuffix()}`;
		await appNameInput.clear();
		await appNameInput.fill(pgAppName);

		await page.getByRole('button', { name: 'Create app', exact: true }).click();

		// Should navigate to the app drawer.
		await expect(page).toHaveURL(new RegExp(`/projects/${projectName}/apps/${pgAppName}(\\?|$)`), {
			timeout: 15_000
		});
		await expect(page.getByRole('heading', { name: pgAppName })).toBeVisible({ timeout: 10_000 });

		// Open Settings and add a data volume.
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await page.getByPlaceholder('Filter settings…').fill('storage');
		await expect(page.getByRole('heading', { name: 'Storage' })).toBeVisible({ timeout: 5_000 });

		await page.getByRole('button', { name: 'Add volume', exact: true }).click();
		await page.locator('#vol-name').fill('pgdata');
		await page.locator('#vol-mount').fill('/var/lib/postgresql/data');
		await page.locator('#vol-size').fill('10Gi');
		const pgdataResponsePromise = page.waitForResponse((r) =>
			r.url().includes(`/apps/${pgAppName}`) && r.request().method() === 'PUT'
		);
		await page.getByRole('button', { name: 'Add', exact: true }).click();
		await pgdataResponsePromise;

		// The drawer doesn't re-fetch after save; reload to get updated spec.
		await page.goto(`/projects/${projectName}/apps/${pgAppName}`);
		await expect(page.getByRole('heading', { name: pgAppName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await page.getByPlaceholder('Filter settings…').fill('storage');

		const storageSection = page.locator('.rounded-lg.border').filter({ hasText: 'Storage' }).first();
		await expect(storageSection.getByText('pgdata')).toBeVisible({ timeout: 5_000 });
		await expect(storageSection.getByText('/var/lib/postgresql/data')).toBeVisible();
		await expect(storageSection.getByText('10Gi')).toBeVisible();

		// Verify via API that spec.storage was updated.
		const app = await getAppViaAPI(request, adminToken, projectName, pgAppName);
		const spec = app.spec as { storage?: Array<{ name: string; mountPath: string; size: string }> };
		expect(spec.storage).toEqual(
			expect.arrayContaining([
				expect.objectContaining({
					name: 'pgdata',
					mountPath: '/var/lib/postgresql/data',
					size: '10Gi'
				})
			])
		);

		await deleteAppViaAPI(request, adminToken, projectName, pgAppName);
	});
});
