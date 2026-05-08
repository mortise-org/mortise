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

		// Create web app (standard image app)
		await createAppViaAPI(request, adminToken, projectName, webAppName);

		// Create postgres app with credentials in its spec
		const pgRes = await request.post(`/api/projects/${projectName}/apps`, {
			headers: { Authorization: `Bearer ${adminToken}` },
			data: {
				name: pgAppName,
				spec: {
					source: { type: 'image', image: 'postgres:16' },
					network: { public: false, port: 5432 },
					environments: [{ name: 'production', replicas: 1 }],
					credentials: [{ name: 'DATABASE_URL' }, { name: 'PGHOST' }, { name: 'PGPORT' }]
				}
			}
		});
		if (!pgRes.ok()) {
			const body = await pgRes.text().catch(() => '');
			throw new Error(`create postgres app failed: HTTP ${pgRes.status()} ${body}`);
		}

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${webAppName}`);
		await expect(page.getByRole('heading', { name: webAppName })).toBeVisible({ timeout: 10_000 });

		// Open Variables tab -> Bindings section.
		await page.getByRole('button', { name: 'Variables', exact: true }).click();

		// The Bindings section header is a div[role="button"].
		const bindingsHeader = page.locator('div[role="button"]').filter({ has: page.locator('span', { hasText: 'Bindings' }) }).first();
		await expect(bindingsHeader).toBeVisible({ timeout: 5_000 });

		// No bindings yet.
		await expect(page.getByText('No bindings')).toBeVisible();

		// Click the + button in the Bindings section header.
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
		await page.getByRole('button', { name: 'Variables', exact: true }).click();

		// Click the + button in the Runtime env section to add a new variable.
		const runtimeSection = page.locator('.rounded-lg.border').filter({ hasText: /^Runtime/ });
		await runtimeSection.locator('button').filter({ has: page.locator('svg') }).last().click();

		// Fill the key and the reference value.
		await page.getByPlaceholder('VARIABLE_NAME').fill('DATABASE_URL');
		await page.getByPlaceholder('value').first().fill('${{bindings.postgres.DATABASE_URL}}');

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

		// Create both apps
		await createAppViaAPI(request, adminToken, projectName, webAppName);
		const pgRes = await request.post(`/api/projects/${projectName}/apps`, {
			headers: { Authorization: `Bearer ${adminToken}` },
			data: {
				name: pgAppName,
				spec: {
					source: { type: 'image', image: 'postgres:16' },
					network: { public: false, port: 5432 },
					environments: [{ name: 'production', replicas: 1 }],
					credentials: [{ name: 'DATABASE_URL' }, { name: 'PGHOST' }, { name: 'PGPORT' }]
				}
			}
		});
		if (!pgRes.ok()) {
			const body = await pgRes.text().catch(() => '');
			throw new Error(`create postgres app failed: HTTP ${pgRes.status()} ${body}`);
		}

		// Add a binding to the web app via API so it starts pre-populated
		const app = await getAppViaAPI(request, adminToken, projectName, webAppName);
		const appSpec = app.spec as Record<string, unknown>;
		await request.put(`/api/projects/${projectName}/apps/${webAppName}`, {
			headers: { Authorization: `Bearer ${adminToken}` },
			data: {
				...appSpec,
				environments: [{ name: 'production', replicas: 1, bindings: [{ ref: pgAppName }] }]
			}
		});

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/${webAppName}`);
		await expect(page.getByRole('heading', { name: webAppName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Variables', exact: true }).click();
		const bindingsHeader2 = page.locator('div[role="button"]').filter({ has: page.locator('span', { hasText: 'Bindings' }) }).first();
		await expect(bindingsHeader2).toBeVisible({ timeout: 5_000 });

		// The existing binding should be visible.
		await expect(page.getByText(pgAppName).first()).toBeVisible({ timeout: 5_000 });

		// Hover and click the trash icon on the binding row.
		const bindingRow = page.locator('.group').filter({ hasText: pgAppName });
		await bindingRow.hover();
		await bindingRow.locator('button').click();

		// After removal the binding row should be gone from the Bindings section.
		// (The canvas AppNode still shows pgAppName, so we scope to the bindings panel.)
		const bindingsSection = page.locator('.rounded-lg.border').filter({ hasText: 'Bindings' }).first();
		await expect(bindingsSection.getByText(pgAppName)).not.toBeVisible({ timeout: 5_000 });
		await expect(page.getByText('No bindings')).toBeVisible();

		await deleteAppViaAPI(request, adminToken, projectName, webAppName);
		await deleteAppViaAPI(request, adminToken, projectName, pgAppName);
	});
});
