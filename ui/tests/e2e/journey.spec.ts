import { expect, test } from '@playwright/test';
import {
	ADMIN_EMAIL,
	ADMIN_PASSWORD,
	ensureAdmin,
	loginViaAPI,
	loginViaUI,
	injectToken,
	randomSuffix,
	getAppViaAPI,
	getEnvViaAPI,
	listSecretsViaAPI,
	listDomainsViaAPI,
	deleteProjectViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Full user journey — the "real user" happy path
//
// This test chains: login → create project → deploy Docker app → navigate to
// detail → deploy new image version → add env vars → add secret → add custom
// domain → delete app → delete project. Every step hits the real API.
// ---------------------------------------------------------------------------

test.describe('full user journey', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('login → project → deploy app → manage → delete — full lifecycle', async ({
		page,
		request
	}) => {
		// Increase timeout for this long journey test.
		test.setTimeout(120_000);

		const projectName = `e2e-journey-${randomSuffix()}`;
		const appImageName = 'nginx';

		// ── Step 1: Login via the UI ──────────────────────────────────
		await loginViaUI(page);
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();

		// ── Step 2: Create a project via the UI ──────────────────────
		await page.getByRole('link', { name: 'New project' }).click();
		await expect(page).toHaveURL('/projects/new');
		await expect(page.getByRole('heading', { name: 'New project' })).toBeVisible();

		await page.getByLabel('Name').fill(projectName);
		await page.getByLabel('Description').fill('Journey test project');

		// Namespace preview should update.
		await expect(page.getByText(`project-${projectName}`)).toBeVisible();

		await page.getByRole('button', { name: 'Create project' }).click();

		// Should redirect to the project detail page.
		await expect(page).toHaveURL(`/projects/${projectName}`, { timeout: 10_000 });
		await expect(page.getByRole('heading', { name: projectName })).toBeVisible();

		// Empty state should show.
		await expect(page.getByText('No apps in this project')).toBeVisible();

		// ── Step 3: Deploy a Docker image app ────────────────────────
		await page.getByRole('link', { name: 'Deploy app' }).click();
		await expect(page).toHaveURL(`/projects/${projectName}/apps/new`);

		const dockerSection = page.locator('section', {
			has: page.getByText('Deploy a Docker image')
		});
		await dockerSection.getByRole('textbox').fill('nginx:1.27');
		await dockerSection.getByRole('button', { name: 'Deploy' }).click();

		// Should redirect back to project page with the app card.
		await page.waitForURL(`/projects/${projectName}`, { timeout: 15_000 });
		await expect(page.getByText(appImageName)).toBeVisible({ timeout: 10_000 });

		// ── Step 4: Navigate to app detail ───────────────────────────
		await page.getByRole('link').filter({ hasText: appImageName }).click();

		await expect(page.getByRole('heading', { name: appImageName })).toBeVisible({
			timeout: 10_000
		});
		await expect(page.getByText('Container Image')).toBeVisible();
		await expect(page.getByText('nginx:1.27').first()).toBeVisible();

		// ── Step 5: Deploy a new image version ───────────────────────
		const imageInput = page.getByPlaceholder('registry.example.com/app:v2.0.0');
		await imageInput.fill('nginx:1.28');
		await page.getByRole('button', { name: 'Deploy' }).click();

		// Wait for deploy to complete, then verify the image was updated.
		// The page's loadApp() call might fetch before the operator updates, so poll.
		await expect(async () => {
			await page.reload();
			await expect(page.getByText('nginx:1.28').first()).toBeVisible({ timeout: 3_000 });
		}).toPass({ timeout: 20_000, intervals: [2_000, 3_000, 5_000] });
		await expect(imageInput).toHaveValue('');

		// Verify via API.
		const appAfterDeploy = await getAppViaAPI(
			request,
			adminToken,
			projectName,
			appImageName
		);
		const source = (appAfterDeploy.spec as Record<string, unknown>)
			.source as Record<string, unknown>;
		expect(source.image).toBe('nginx:1.28');

		// ── Step 6: Add environment variables ────────────────────────
		await page.getByRole('button', { name: '+ Add variable' }).click();
		await page.getByPlaceholder('KEY').fill('APP_ENV');
		await page.getByPlaceholder('value', { exact: true }).fill('production');

		await page.getByRole('button', { name: 'Save changes' }).click();

		// Wait for save to complete — ensure page has fully reloaded (the
		// save triggers loadApp() which briefly sets loading=true).
		await expect(page.getByRole('button', { name: 'Save changes' })).toHaveCount(0, {
			timeout: 10_000
		});
		await expect(page.getByRole('heading', { name: appImageName })).toBeVisible();

		// Verify persisted via API.
		const envVars = await getEnvViaAPI(request, adminToken, projectName, appImageName);
		expect(
			envVars.some((v) => v.name === 'APP_ENV' && v.value === 'production')
		).toBeTruthy();

		// ── Step 7: Add a secret ─────────────────────────────────────
		const secretName = `journey-secret-${randomSuffix()}`;
		await page.getByPlaceholder('SECRET_NAME').fill(secretName);
		await page.getByPlaceholder('value (write-only)').fill('super-secret');
		await page
			.getByPlaceholder('value (write-only)')
			.locator('..')
			.getByRole('button', { name: 'Add' })
			.click();

		// Should appear in the list.
		await expect(page.getByText(secretName).first()).toBeVisible({ timeout: 10_000 });

		// Verify via API.
		const secrets = await listSecretsViaAPI(
			request,
			adminToken,
			projectName,
			appImageName
		);
		expect(secrets.some((s) => s.name === secretName)).toBeTruthy();

		// ── Step 8: Add a custom domain ──────────────────────────────
		const customDomain = `journey-${randomSuffix()}.test.example.com`;
		const domainInput = page.getByPlaceholder('custom.example.com');
		await domainInput.fill(customDomain);
		await domainInput.locator('..').getByRole('button', { name: 'Add' }).click();

		// Should appear in the domains list.
		await expect(page.getByText(customDomain)).toBeVisible({ timeout: 10_000 });

		// Verify via API.
		const domains = await listDomainsViaAPI(
			request,
			adminToken,
			projectName,
			appImageName
		);
		expect(domains.custom).toContain(customDomain);

		// ── Step 9: Delete the app ───────────────────────────────────
		page.once('dialog', (dialog) => dialog.accept());
		await page.getByRole('button', { name: 'Delete App' }).click();

		// Should redirect to project page.
		await expect(page).toHaveURL(
			`/projects/${encodeURIComponent(projectName)}`,
			{ timeout: 10_000 }
		);

		// App should be gone.
		await expect(page.getByText(appImageName)).toHaveCount(0, { timeout: 5_000 });

		// ── Step 10: Delete the project ──────────────────────────────
		page.once('dialog', async (dialog) => {
			await dialog.accept(projectName);
		});
		await page.getByRole('button', { name: 'Delete project' }).click();

		// Should redirect to dashboard.
		await expect(page).toHaveURL('/', { timeout: 10_000 });

		// Project should be gone from the list.  The operator may take a few
		// seconds to finalise deletion, so poll with page reloads.
		await expect(async () => {
			await page.reload();
			await expect(page.getByRole('link').filter({ hasText: projectName })).toHaveCount(0);
		}).toPass({ timeout: 15_000, intervals: [2_000, 3_000, 5_000] });
	});
});

// ---------------------------------------------------------------------------
// Template journey — deploy Postgres, verify config, manage, delete
// ---------------------------------------------------------------------------

test.describe('template deploy journey', () => {
	let adminToken: string;
	const projectName = `e2e-tmpljrn-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		await request.post('/api/projects', {
			headers: { Authorization: `Bearer ${adminToken}` },
			data: { name: projectName, description: 'Template journey' }
		});
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('deploy postgres template → navigate to detail → verify config → add env var → delete', async ({
		page,
		request
	}) => {
		test.setTimeout(90_000);

		const appName = `pg-jrn-${randomSuffix()}`;

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/new`);

		// Select Postgres template.
		await page.getByRole('button', { name: 'Postgres 16' }).click();
		await expect(page.getByRole('heading', { name: 'Postgres 16' })).toBeVisible();

		// Verify prefilled values.
		await expect(page.getByLabel('Image Reference')).toHaveValue('postgres:16');
		const storageNameInput = page.locator('.flex.gap-2').filter({ has: page.getByPlaceholder('name') }).first().getByPlaceholder('name');
		await expect(storageNameInput).toHaveValue('pgdata');
		await expect(page.getByText('DATABASE_URL')).toBeVisible();

		// Set custom app name.
		await page.getByLabel('App Name').clear();
		await page.getByLabel('App Name').fill(appName);

		// Submit.
		await page.getByRole('button', { name: 'Deploy Postgres' }).click();

		// Should redirect to project page.
		await page.waitForURL(`/projects/${projectName}`, { timeout: 15_000 });
		await expect(page.getByText(appName)).toBeVisible({ timeout: 10_000 });

		// Navigate to app detail.
		await page.getByRole('link').filter({ hasText: appName }).click();
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({
			timeout: 10_000
		});

		// Verify source shows Container Image with postgres:16.
		await expect(page.getByText('Container Image')).toBeVisible();
		await expect(page.getByText('postgres:16').first()).toBeVisible();

		// Verify via API that storage was configured.
		const app = await getAppViaAPI(request, adminToken, projectName, appName);
		const spec = app.spec as Record<string, unknown>;
		const storage = spec.storage as Record<string, unknown>[];
		expect(storage).toBeDefined();
		expect(storage.some((s) => s.name === 'pgdata')).toBeTruthy();

		// Add an env var and save.  Use .last() because the template's existing
		// POSTGRES_PASSWORD row also has a placeholder="KEY" input.
		await page.getByRole('button', { name: '+ Add variable' }).click();
		await page.getByPlaceholder('KEY').last().fill('POSTGRES_DB');
		await page.waitForTimeout(200);
		await page.getByPlaceholder('value', { exact: true }).last().fill('mydb');
		await page.getByRole('button', { name: 'Save changes' }).click();

		await expect(page.getByRole('button', { name: 'Save changes' })).toHaveCount(0, {
			timeout: 10_000
		});

		// Verify env var saved.
		const envVars = await getEnvViaAPI(request, adminToken, projectName, appName);
		expect(
			envVars.some((v) => v.name === 'POSTGRES_DB' && v.value === 'mydb')
		).toBeTruthy();

		// Delete the app.
		page.once('dialog', (dialog) => dialog.accept());
		await page.getByRole('button', { name: 'Delete App' }).click();

		await expect(page).toHaveURL(
			`/projects/${encodeURIComponent(projectName)}`,
			{ timeout: 10_000 }
		);
	});
});

// ---------------------------------------------------------------------------
// Sign out journey
// ---------------------------------------------------------------------------

test.describe('sign out journey', () => {
	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
	});

	test('sign out clears token and redirects to login, cannot access dashboard', async ({
		page
	}) => {
		await loginViaUI(page);

		// Verify we're on the dashboard.
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();

		// Sign out.
		await page.getByRole('button', { name: 'Sign out' }).click();

		// Should redirect to login.
		await expect(page).toHaveURL('/login', { timeout: 5_000 });

		// Token should be cleared.
		const token = await page.evaluate(() => localStorage.getItem('token'));
		expect(token).toBeNull();

		// Trying to navigate to dashboard should redirect back to login.
		await page.goto('/');
		await page.waitForURL('**/login');
	});
});
