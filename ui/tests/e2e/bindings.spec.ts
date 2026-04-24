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
// Bindings E2E tests
//
// Tests cover the Bindings section in the app Settings tab:
//   - Connecting a web app to a Postgres database via bindings
//   - Using binding reference syntax in Variables tab
//   - Removing a binding
// ---------------------------------------------------------------------------

test.describe('bindings', () => {
	let adminToken: string;
	let projectName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		projectName = `e2e-bind-${randomSuffix()}`;
		await createProjectViaAPI(request, adminToken, projectName, 'Bindings E2E tests');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('developer connects web app to Postgres database via bindings', async ({
		page,
		request
	}) => {
		const webAppName = `web-${randomSuffix()}`;
		const pgAppName = `postgres-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, webAppName);
		await createAppViaAPI(request, adminToken, projectName, pgAppName);

		await injectToken(page, adminToken);

		// Mock listApps so the bindings picker can see the postgres app (with credentials).
		await page.route(`**/api/projects/${projectName}/apps`, async (route) => {
			if (route.request().method() === 'GET') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify([
						{
							metadata: { name: webAppName, namespace: `project-${projectName}` },
							spec: {
								source: { type: 'image', image: 'nginx:1.27' },
								network: { public: true },
								environments: [{ name: 'production', replicas: 1 }],
								storage: [],
								credentials: []
							},
							status: { phase: 'Ready' }
						},
						{
							metadata: { name: pgAppName, namespace: `project-${projectName}` },
							spec: {
								source: { type: 'image', image: 'postgres:16' },
								network: { public: false },
								environments: [{ name: 'production', replicas: 1 }],
								storage: [{ name: 'pgdata', mountPath: '/var/lib/postgresql/data', size: '10Gi' }],
								credentials: [{ name: 'DATABASE_URL' }, { name: 'PGHOST' }, { name: 'PGPORT' }]
							},
							status: { phase: 'Ready' }
						}
					])
				});
			}
			return route.continue();
		});

		// Mock PUT to return the web app with the binding added.
		await page.route(`**/api/projects/${projectName}/apps/${webAppName}`, async (route) => {
			if (route.request().method() === 'PUT') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						metadata: { name: webAppName, namespace: `project-${projectName}` },
						spec: {
							source: { type: 'image', image: 'nginx:1.27' },
							network: { public: true },
							environments: [
								{
									name: 'production',
									replicas: 1,
									bindings: [{ ref: pgAppName }]
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

		await page.goto(`/projects/${projectName}/apps/${webAppName}`);
		await expect(page.getByRole('heading', { name: webAppName })).toBeVisible({ timeout: 10_000 });

		// Open Variables tab → Bindings section.
		await page.getByRole('button', { name: 'Variables' }).click();
		await expect(page.getByText('Bindings')).toBeVisible({ timeout: 5_000 });

		// No bindings yet.
		await expect(page.getByText('No bindings')).toBeVisible();

		// Click the + button in the Bindings section header.
		const bindingsHeader = page.locator('div[role="button"]').filter({ hasText: 'Bindings' });
		await bindingsHeader.locator('button').click();

		// Select the postgres app from the dropdown.
		const bindingSelect = page.locator('#binding-ref');
		await expect(bindingSelect).toBeVisible({ timeout: 5_000 });
		await bindingSelect.selectOption(pgAppName);

		// The credentials preview should appear.
		await expect(page.getByText('DATABASE_URL')).toBeVisible({ timeout: 3_000 });

		// Click Add.
		await page.getByRole('button', { name: 'Add', exact: true }).click();

		// Binding should appear in the list.
		await expect(page.getByText(pgAppName).first()).toBeVisible({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, webAppName);
		await deleteAppViaAPI(request, adminToken, projectName, pgAppName);
	});

	test('developer uses binding reference variable syntax in Variables tab', async ({
		page,
		request
	}) => {
		const appName = `e2e-bindvar-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, appName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Open Variables tab.
		await page.getByRole('button', { name: 'Variables' }).click();

		// Click "New variable".
		await page.getByRole('button', { name: 'New variable', exact: true }).click();

		// Fill the key and the reference value.
		await page.getByPlaceholder('VARIABLE_NAME').fill('DATABASE_URL');
		await page.getByPlaceholder('value or binding ref').fill('${{bindings.postgres.DATABASE_URL}}');

		// The Add button should be visible.
		const addBtn = page.getByRole('button', { name: 'Add' }).first();
		await expect(addBtn).toBeVisible();
		await addBtn.click();

		// The variable with reference syntax should appear.
		await expect(page.getByText('DATABASE_URL')).toBeVisible({ timeout: 5_000 });

		await deleteAppViaAPI(request, adminToken, projectName, appName);
	});

	test('developer removes a binding they no longer need', async ({ page, request }) => {
		const webAppName = `web-rmb-${randomSuffix()}`;
		const pgAppName = `pg-rmb-${randomSuffix()}`;
		await createAppViaAPI(request, adminToken, projectName, webAppName);
		await createAppViaAPI(request, adminToken, projectName, pgAppName);

		await injectToken(page, adminToken);

		// Intercept GET to return the web app with a binding pre-populated.
		await page.route(`**/api/projects/${projectName}/apps/${webAppName}`, async (route) => {
			if (route.request().method() === 'GET') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						metadata: { name: webAppName, namespace: `project-${projectName}` },
						spec: {
							source: { type: 'image', image: 'nginx:1.27' },
							network: { public: true },
							environments: [
								{
									name: 'production',
									replicas: 1,
									bindings: [{ ref: pgAppName }]
								}
							],
							storage: [],
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
						metadata: { name: webAppName, namespace: `project-${projectName}` },
						spec: {
							source: { type: 'image', image: 'nginx:1.27' },
							network: { public: true },
							environments: [{ name: 'production', replicas: 1, bindings: [] }],
							storage: [],
							credentials: []
						},
						status: { phase: 'Ready' }
					})
				});
			}
			return route.continue();
		});

		await page.goto(`/projects/${projectName}/apps/${webAppName}`);
		await expect(page.getByRole('heading', { name: webAppName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Variables' }).click();
		await expect(page.getByText('Bindings')).toBeVisible({ timeout: 5_000 });

		// The existing binding should be visible.
		await expect(page.getByText(pgAppName).first()).toBeVisible({ timeout: 5_000 });

		// Hover and click the trash icon on the binding row.
		const bindingRow = page.locator('.group').filter({ hasText: pgAppName });
		await bindingRow.hover();
		await bindingRow.locator('button').click();

		// After removal the binding row should be gone.
		await expect(page.getByText(pgAppName)).not.toBeVisible({ timeout: 5_000 });
		await expect(page.getByText('No bindings')).toBeVisible();

		await deleteAppViaAPI(request, adminToken, projectName, webAppName);
		await deleteAppViaAPI(request, adminToken, projectName, pgAppName);
	});
});
