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
// New-app page structure
// ---------------------------------------------------------------------------
test.describe('new app page structure', () => {
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

	test('shows all three deploy sections', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(
			page.getByRole('heading', { name: 'Deploy a new service' })
		).toBeVisible();

		// Section 1: Git repo
		await expect(
			page.getByRole('heading', { name: 'Deploy from a Git repo' })
		).toBeVisible();

		// Section 2: Docker image
		await expect(
			page.getByRole('heading', { name: 'Deploy a Docker image' })
		).toBeVisible();

		// Section 3: Templates
		await expect(
			page.getByRole('heading', { name: 'Deploy from a template' })
		).toBeVisible();
	});

	test('no git provider state shows connect prompt', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(
			page.getByText('Connect GitHub to deploy from a repository')
		).toBeVisible({ timeout: 10_000 });

		// The "Connect GitHub" element is a button that starts the device flow.
		const connectBtn = page.getByRole('button', { name: 'Connect GitHub' });
		await expect(connectBtn).toBeVisible();

		// A link to the manual git-providers settings page is also present.
		const manualLink = page.getByRole('link', { name: /connect GitLab \/ Gitea manually/ });
		await expect(manualLink).toBeVisible();
		await expect(manualLink).toHaveAttribute('href', '/settings/git-providers');
	});
});

// ---------------------------------------------------------------------------
// Deploy Docker image
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

	test('deploy button is disabled when image input is empty', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		await expect(
			page.getByRole('heading', { name: 'Deploy a Docker image' })
		).toBeVisible();

		// The Deploy button in the Docker section should be disabled initially.
		const deployBtn = page
			.locator('section', { has: page.getByText('Deploy a Docker image') })
			.getByRole('button', { name: 'Deploy' });
		await expect(deployBtn).toBeDisabled();
	});

	test('fill image and deploy redirects to project page', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		const section = page.locator('section', {
			has: page.getByText('Deploy a Docker image')
		});

		await section.getByRole('textbox').fill('nginx:1.27');

		const deployBtn = section.getByRole('button', { name: 'Deploy' });
		await expect(deployBtn).toBeEnabled();

		await deployBtn.click();

		// Should redirect to the project detail page.
		await page.waitForURL(`/projects/${project}`, { timeout: 15_000 });

		// The app card for "nginx" (derived from the image name) should appear.
		await expect(page.getByText('nginx')).toBeVisible({ timeout: 10_000 });
	});
});

// ---------------------------------------------------------------------------
// Deploy from Postgres template
// ---------------------------------------------------------------------------
test.describe('deploy postgres template', () => {
	let token: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-postgres-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test('select Postgres 16 template, verify prefill, and submit', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		// Click the Postgres 16 template button.
		await page.getByRole('button', { name: 'Postgres 16' }).click();

		// AppForm should be visible with the template name.
		await expect(page.getByRole('heading', { name: 'Postgres 16' })).toBeVisible();
		await expect(page.getByText('Back to templates')).toBeVisible();

		// Image reference should be prefilled.
		const imageInput = page.getByLabel('Image Reference');
		await expect(imageInput).toHaveValue('postgres:16');

		// Source should show Container Image (readonly).
		await expect(page.getByText('Container Image')).toBeVisible();

		// Storage should show pgdata volume (bind:value sets DOM property, not attribute).
		const pgStorageRow = page.locator('.flex.gap-2').filter({ has: page.getByPlaceholder('name') }).first();
		await expect(pgStorageRow.getByPlaceholder('name')).toHaveValue('pgdata');
		await expect(pgStorageRow.getByPlaceholder('/mount/path')).toHaveValue('/var/lib/postgresql/data');
		await expect(pgStorageRow.getByPlaceholder('10Gi')).toHaveValue('10Gi');

		// Credentials badges should be visible.
		await expect(page.getByText('DATABASE_URL')).toBeVisible();

		// Submit button should say "Deploy Postgres".
		const submitBtn = page.getByRole('button', { name: 'Deploy Postgres' });
		await expect(submitBtn).toBeVisible();

		// Fill a unique app name and submit.
		const appName = `pg-${randomSuffix()}`;
		await page.getByLabel('App Name').clear();
		await page.getByLabel('App Name').fill(appName);

		await submitBtn.click();

		// Should redirect to the project page.
		await page.waitForURL(`/projects/${project}`, { timeout: 15_000 });
		await expect(page.getByText(appName)).toBeVisible({ timeout: 10_000 });
	});
});

// ---------------------------------------------------------------------------
// Deploy from Redis template
// ---------------------------------------------------------------------------
test.describe('deploy redis template', () => {
	let token: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-redis-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test('select Redis 7 template, verify prefill, and submit', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		// Click the Redis 7 template button.
		await page.getByRole('button', { name: 'Redis 7' }).click();

		// AppForm should show Redis heading.
		await expect(page.getByRole('heading', { name: 'Redis 7' })).toBeVisible();

		// Image should be prefilled.
		const imageInput = page.getByLabel('Image Reference');
		await expect(imageInput).toHaveValue('redis:7-alpine');

		// Storage should show redis-data (bind:value sets DOM property, not attribute).
		const redisStorageRow = page.locator('.flex.gap-2').filter({ has: page.getByPlaceholder('name') }).first();
		await expect(redisStorageRow.getByPlaceholder('name')).toHaveValue('redis-data');
		await expect(redisStorageRow.getByPlaceholder('/mount/path')).toHaveValue('/data');

		// Credentials badges.
		await expect(page.getByText('REDIS_URL')).toBeVisible();

		// Submit.
		const submitBtn = page.getByRole('button', { name: 'Deploy Redis' });
		await expect(submitBtn).toBeVisible();

		const appName = `redis-${randomSuffix()}`;
		await page.getByLabel('App Name').clear();
		await page.getByLabel('App Name').fill(appName);

		await submitBtn.click();

		await page.waitForURL(`/projects/${project}`, { timeout: 15_000 });
		await expect(page.getByText(appName)).toBeVisible({ timeout: 10_000 });
	});
});

// ---------------------------------------------------------------------------
// Template form navigation (cancel / back)
// ---------------------------------------------------------------------------
test.describe('template form navigation', () => {
	let token: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-tmpnav-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test('cancel link navigates back to project page', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		// Select a template to show the form.
		await page.getByRole('button', { name: 'Postgres 16' }).click();
		await expect(page.getByRole('heading', { name: 'Postgres 16' })).toBeVisible();

		// Click Cancel link.
		const cancelLink = page.getByRole('link', { name: 'Cancel' });
		await expect(cancelLink).toBeVisible();
		await cancelLink.click();

		// Should navigate to the project page.
		await page.waitForURL(`/projects/${project}`, { timeout: 10_000 });
	});

	test('back to templates button returns to template list', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/new`);

		// Select a template.
		await page.getByRole('button', { name: 'Vaultwarden' }).click();
		await expect(page.getByRole('heading', { name: 'Vaultwarden' })).toBeVisible();

		// Click "Back to templates".
		await page.getByText('Back to templates').click();

		// Template list should reappear (we should see the heading and template buttons).
		await expect(
			page.getByRole('heading', { name: 'Deploy from a template' })
		).toBeVisible();
		await expect(page.getByRole('button', { name: 'Postgres 16' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Redis 7' })).toBeVisible();
	});
});

// ---------------------------------------------------------------------------
// App detail page
// ---------------------------------------------------------------------------
test.describe('app detail page', () => {
	let token: string;
	let project: string;
	let appName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-detail-${randomSuffix()}`;
		appName = `detail-app-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
		await createAppViaAPI(request, token, project, appName, 'nginx:1.27');
	});

	test.afterAll(async ({ request }) => {
		await deleteAppViaAPI(request, token, project, appName);
		await deleteProjectViaAPI(request, token, project);
	});

	test('shows overview cards with correct source info', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);

		// App heading.
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({
			timeout: 10_000
		});

		// Phase badge should appear (any phase is fine).
		const phaseBadge = page.locator('span', {
			hasText: /Ready|Pending|Deploying|Building|Failed/
		});
		await expect(phaseBadge.first()).toBeVisible();

		// Breadcrumbs.
		await expect(page.getByText('Projects')).toBeVisible();
		await expect(page.getByRole('link', { name: project })).toBeVisible();
		await expect(page.getByText('apps')).toBeVisible();

		// Source card.
		await expect(page.getByText('Source')).toBeVisible();
		await expect(page.getByText('Container Image')).toBeVisible();

		// Replicas card.
		await expect(page.getByText('Replicas')).toBeVisible();
		await expect(page.getByText('ready / desired')).toBeVisible();

		// Domain card.
		await expect(page.getByText('Domain', { exact: true })).toBeVisible();
	});

	test('delete app redirects to project page', async ({ page }) => {
		// Create a throwaway app for deletion.
		const deleteAppName = `del-app-${randomSuffix()}`;
		const res = await page.request.post(`/api/projects/${project}/apps`, {
			headers: { Authorization: `Bearer ${token}` },
			data: {
				name: deleteAppName,
				spec: {
					source: { type: 'image', image: 'nginx:1.27' },
					network: { public: true },
					environments: [{ name: 'production', replicas: 1 }]
				}
			}
		});
		expect(res.ok()).toBeTruthy();

		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${deleteAppName}`);

		// Wait for the app to load.
		await expect(page.getByRole('heading', { name: deleteAppName })).toBeVisible({
			timeout: 10_000
		});

		// Click Delete App. The handler uses confirm(), so accept the dialog.
		page.once('dialog', (dialog) => dialog.accept());
		await page.getByRole('button', { name: 'Delete App' }).click();

		// Should redirect to the project page.
		await page.waitForURL(`/projects/${project}`, { timeout: 15_000 });

		// The deleted app should no longer appear.
		await expect(page.getByText(deleteAppName)).toHaveCount(0, { timeout: 10_000 });
	});
});
