import { expect, test } from '@playwright/test';
import {
	randomSuffix,
	ensureAdmin,
	loginViaAPI,
	injectToken,
	createProjectViaAPI,
	createAppViaAPI,
	deleteProjectViaAPI,
	deleteAppViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// New app modal structure (/projects/{p}/apps/new)
//
// The new UI shows a modal with a type picker. Navigating directly to
// /projects/{p}/apps/new renders the NewAppModal component.
// ---------------------------------------------------------------------------
test.describe('new app modal structure', () => {
	let token: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-newpage-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test('shows type picker with all app type options', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(
			page.getByRole('heading', { name: 'What would you like to create?' })
		).toBeVisible({ timeout: 10_000 });

		// All type options in the picker — use exact to avoid matching descriptions.
		await expect(page.getByText('Git Repository', { exact: true })).toBeVisible();
		await expect(page.getByText('Database', { exact: true })).toBeVisible();
		await expect(page.getByText('Template', { exact: true })).toBeVisible();
		await expect(page.getByText('Docker Image', { exact: true })).toBeVisible();
		await expect(page.getByText('External Service', { exact: true })).toBeVisible();
		await expect(page.getByText('Empty App', { exact: true })).toBeVisible();
	});

	test('selecting Docker Image shows image input and Create app button', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(page.getByText('Docker Image', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Docker Image', { exact: true }).click();

		// Configure pane shows image reference input.
		await expect(page.getByText('Image Reference')).toBeVisible();
		await expect(page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest')).toBeVisible();

		// App name input and Create button.
		await expect(page.getByText('App name')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Create app' })).toBeVisible();
	});

	test('selecting Database shows preset grid', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(page.getByText('Database', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Database', { exact: true }).click();

		// Database presets should be visible.
		await expect(page.getByText('Postgres', { exact: true })).toBeVisible();
		await expect(page.getByText('Redis', { exact: true })).toBeVisible();
		await expect(page.getByText('MinIO', { exact: true })).toBeVisible();
		await expect(page.getByText('MySQL', { exact: true })).toBeVisible();
	});

	test('Back button returns to type picker', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(page.getByText('Docker Image', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Docker Image', { exact: true }).click();

		// Configure pane is shown.
		await expect(page.getByRole('button', { name: 'Create app' })).toBeVisible();

		// Click "← Back" to return to type picker.
		await page.getByRole('button', { name: /Back/ }).click();

		await expect(
			page.getByRole('heading', { name: 'What would you like to create?' })
		).toBeVisible();
	});

	test('Cancel button navigates to project canvas', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(page.getByText('Docker Image', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Docker Image', { exact: true }).click();

		await page.getByRole('button', { name: 'Cancel' }).click();

		await expect(page).toHaveURL(`/projects/${project}`, { timeout: 10_000 });
	});
});

// ---------------------------------------------------------------------------
// Deploy Docker image via modal
// ---------------------------------------------------------------------------
test.describe('deploy docker image', () => {
	let token: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-docker-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test('Create app button is disabled when app name is empty', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(page.getByText('Docker Image', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Docker Image', { exact: true }).click();

		// Create button should be disabled when no app name is provided.
		const createBtn = page.getByRole('button', { name: 'Create app' });
		await expect(createBtn).toBeDisabled();
	});

	test('fill image and name then create navigates to app drawer', async ({ page }) => {
		const appName = `nginx-${randomSuffix()}`;

		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(page.getByText('Docker Image', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Docker Image', { exact: true }).click();

		await page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest').fill('nginx:1.27');
		await page.getByPlaceholder('my-app').fill(appName);

		const createBtn = page.getByRole('button', { name: 'Create app' });
		await expect(createBtn).toBeEnabled();
		await createBtn.click();

		// After creation, navigates to the app drawer URL.
		await expect(page).toHaveURL(`/projects/${project}/apps/${appName}`, { timeout: 15_000 });
	});
});

// ---------------------------------------------------------------------------
// Deploy from Database preset
// ---------------------------------------------------------------------------
test.describe('deploy database preset', () => {
	let token: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-dbpreset-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test('select Postgres preset, prefills app name and image', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(page.getByText('Database', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Database', { exact: true }).click();

		// Click Postgres in the grid.
		await page.getByText('Postgres', { exact: true }).click();

		// App name should be prefilled to 'postgres'.
		const appNameInput = page.getByPlaceholder('my-app');
		await expect(appNameInput).toHaveValue('postgres');

		// Create button should be enabled.
		await expect(page.getByRole('button', { name: 'Create app' })).toBeEnabled();
	});

	test('select Redis preset, prefills app name', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(page.getByText('Database', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Database', { exact: true }).click();

		await page.getByText('Redis', { exact: true }).click();

		const appNameInput = page.getByPlaceholder('my-app');
		await expect(appNameInput).toHaveValue('redis');
	});
});

// ---------------------------------------------------------------------------
// App list view
// ---------------------------------------------------------------------------
test.describe('app list view', () => {
	let token: string;
	let project: string;
	let appName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-listview-${randomSuffix()}`;
		appName = `list-app-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
		await createAppViaAPI(request, token, project, appName);
	});

	test.afterAll(async ({ request }) => {
		await deleteAppViaAPI(request, token, project, appName);
		await deleteProjectViaAPI(request, token, project);
	});

	test('switching to list view shows app table', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}`);

		// Switch to list view via the toolbar toggle.
		await page.getByTitle('List view').click();

		// App should appear in the table.
		await expect(page.getByText(appName)).toBeVisible({ timeout: 10_000 });
	});

	test('clicking app row in list view navigates to drawer URL', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}`);

		// Switch to list view.
		await page.getByTitle('List view').click();

		// Click the app row.
		await expect(page.getByText(appName)).toBeVisible({ timeout: 10_000 });
		await page.getByText(appName).click();

		await expect(page).toHaveURL(`/projects/${project}/apps/${appName}`, { timeout: 10_000 });
	});

	test('list view table has expected column headers', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}`);

		await page.getByTitle('List view').click();

		// Table headers.
		await expect(page.getByText('Name')).toBeVisible({ timeout: 10_000 });
		await expect(page.getByText('Source')).toBeVisible();
		await expect(page.getByText('Status')).toBeVisible();
	});
});

// ---------------------------------------------------------------------------
// App drawer (accessed via /projects/{p}/apps/{a})
// ---------------------------------------------------------------------------
test.describe('app drawer', () => {
	let token: string;
	let project: string;
	let appName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-drawer-${randomSuffix()}`;
		appName = `drawer-app-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
		await createAppViaAPI(request, token, project, appName, 'nginx:1.27');
	});

	test.afterAll(async ({ request }) => {
		await deleteAppViaAPI(request, token, project, appName);
		await deleteProjectViaAPI(request, token, project);
	});

	test('navigating to app URL shows drawer with app name', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		// Drawer shows app name as heading.
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });
	});

	test('drawer has five tabs', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// All five tab buttons.
		await expect(page.getByRole('button', { name: 'Deployments' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Variables' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Logs' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Metrics' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Settings' })).toBeVisible();
	});

	test('close button navigates back to project canvas', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Close button (X icon, aria-label="Close drawer").
		await page.getByRole('button', { name: 'Close drawer' }).click();

		await expect(page).toHaveURL(`/projects/${project}`, { timeout: 5_000 });
	});

	test('tab switching works — Variables tab shows content', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Variables' }).click();

		// Variables tab content should appear — "New variable" button is always visible.
		await expect(page.getByRole('button', { name: 'New variable' })).toBeVisible({ timeout: 5_000 });
	});

	test('tab switching works — Settings tab shows content', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		await page.getByRole('button', { name: 'Settings', exact: true }).click();

		// Settings tab renders the filter input.
		await expect(page.getByPlaceholder('Filter settings…')).toBeVisible({ timeout: 5_000 });
	});

	test('phase badge is visible in drawer header', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Phase badge (may be Pending, Ready, etc.).
		const phaseBadge = page.locator('span', {
			hasText: /Ready|Pending|Deploying|Building|Failed/
		});
		await expect(phaseBadge.first()).toBeVisible({ timeout: 10_000 });
	});
});
