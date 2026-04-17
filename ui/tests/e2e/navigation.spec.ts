import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	loginViaUI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	createAppViaAPI,
	deleteProjectViaAPI,
	deleteAppViaAPI
} from './helpers';

// End-to-end tests for header navigation, project switcher, extensions page,
// and breadcrumbs.
//
// Assumes an operator is reachable at MORTISE_BASE_URL and admin credentials
// are supplied via MORTISE_ADMIN_EMAIL / MORTISE_ADMIN_PASSWORD.

test.describe('header & navigation', () => {
	let adminToken: string;
	const projectsToCleanup: string[] = [];

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test.afterEach(async ({ request }) => {
		while (projectsToCleanup.length > 0) {
			const name = projectsToCleanup.pop()!;
			await deleteProjectViaAPI(request, adminToken, name);
		}
	});

	test('header visible when authenticated', async ({ page }) => {
		await loginViaUI(page);

		await expect(page.getByRole('link', { name: 'Mortise' })).toBeVisible();
		await expect(page.getByRole('link', { name: 'Extensions' })).toBeVisible();
		await expect(page.getByRole('link', { name: 'Settings' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
	});

	test('header not visible on login page', async ({ page }) => {
		await page.goto('/login');

		// The header elements should not exist on the bare login layout.
		await expect(page.getByRole('link', { name: 'Mortise' })).toHaveCount(0);
		await expect(page.getByRole('link', { name: 'Extensions' })).toHaveCount(0);
	});

	test('Mortise logo links to dashboard', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/extensions');
		await expect(page.getByRole('heading', { name: 'Extensions' })).toBeVisible();

		await page.getByRole('link', { name: 'Mortise' }).click();

		await expect(page).toHaveURL('/');
	});

	test('Extensions link navigates to /extensions', async ({ page }) => {
		await loginViaUI(page);

		await page.getByRole('link', { name: 'Extensions' }).click();

		await expect(page).toHaveURL('/extensions');
		await expect(page.getByRole('heading', { name: 'Extensions' })).toBeVisible();
	});

	test('Settings link navigates to /settings/git-providers', async ({ page }) => {
		await loginViaUI(page);

		await page.getByRole('link', { name: 'Settings' }).click();

		await expect(page).toHaveURL('/settings/git-providers');
	});

	test('Sign out clears token and redirects to login', async ({ page }) => {
		await loginViaUI(page);

		await page.getByRole('button', { name: 'Sign out' }).click();

		await expect(page).toHaveURL('/login', { timeout: 5_000 });

		const token = await page.evaluate(() => localStorage.getItem('token'));
		expect(token).toBeNull();
	});
});

test.describe('project switcher', () => {
	let adminToken: string;
	const projectsToCleanup: string[] = [];

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test.afterEach(async ({ request }) => {
		while (projectsToCleanup.length > 0) {
			const name = projectsToCleanup.pop()!;
			await deleteProjectViaAPI(request, adminToken, name);
		}
	});

	test('switcher shows active project on project page', async ({ page, request }) => {
		const name = `e2e-sw-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);
		await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

		// The switcher button should show the project name.
		const switcher = page.locator('header button').filter({ hasText: 'Project:' });
		await expect(switcher).toContainText(name);
	});

	test('switcher shows "All projects" when not on a project page', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10_000 });

		const switcher = page.locator('header button').filter({ hasText: 'Project:' });
		await expect(switcher).toContainText('All projects');
	});

	test('switcher opens on click and shows project list', async ({ page, request }) => {
		const name = `e2e-swopen-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10_000 });

		const switcher = page.locator('header button').filter({ hasText: 'Project:' });
		await switcher.click();

		// The dropdown should appear and contain our project.
		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible();
		await expect(dropdown.getByText(name)).toBeVisible();
	});

	test('switcher closes on outside click', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10_000 });

		const switcher = page.locator('header button').filter({ hasText: 'Project:' });
		await switcher.click();

		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible();

		// Click outside the switcher (on the main content area).
		await page.locator('main').click();
		await expect(dropdown).toBeHidden();
	});

	test('switcher navigates to project on click', async ({ page, request }) => {
		const name = `e2e-swnav-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10_000 });

		const switcher = page.locator('header button').filter({ hasText: 'Project:' });
		await switcher.click();

		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible();
		await dropdown.getByText(name, { exact: true }).click();

		await expect(page).toHaveURL(`/projects/${name}`, { timeout: 5_000 });
	});

	test('Create new project from switcher', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10_000 });

		const switcher = page.locator('header button').filter({ hasText: 'Project:' });
		await switcher.click();

		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible();
		await dropdown.getByText('Create new project...').click();

		await expect(page).toHaveURL('/projects/new', { timeout: 5_000 });
	});
});

test.describe('extensions page', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('extensions page renders with heading', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/extensions');

		await expect(page.getByRole('heading', { name: 'Extensions' })).toBeVisible();
	});

	test('all category headings present', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/extensions');

		await expect(page.getByRole('heading', { name: 'Infrastructure' })).toBeVisible();
		await expect(page.getByRole('heading', { name: 'Security' })).toBeVisible();
		await expect(page.getByRole('heading', { name: 'Tenons' })).toBeVisible();
	});

	test('extension cards render for infrastructure', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/extensions');

		await expect(page.getByText('cert-manager')).toBeVisible();
		await expect(page.getByText('ExternalDNS')).toBeVisible();
		await expect(page.getByText('Traefik')).toBeVisible();
	});

	test('extension card has action link', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/extensions');

		// cert-manager card should have a "Docs" link pointing to the cert-manager site.
		const certManagerCard = page.locator('div').filter({ hasText: /^cert-manager/ }).first();
		const docsLink = certManagerCard.getByRole('link', { name: 'Docs' });
		await expect(docsLink).toBeVisible();
		await expect(docsLink).toHaveAttribute('href', 'https://cert-manager.io/docs/');
	});
});

test.describe('breadcrumbs', () => {
	let adminToken: string;
	const projectsToCleanup: string[] = [];
	const appsToCleanup: { project: string; app: string }[] = [];

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test.afterEach(async ({ request }) => {
		while (appsToCleanup.length > 0) {
			const { project, app } = appsToCleanup.pop()!;
			await deleteAppViaAPI(request, adminToken, project, app);
		}
		while (projectsToCleanup.length > 0) {
			const name = projectsToCleanup.pop()!;
			await deleteProjectViaAPI(request, adminToken, name);
		}
	});

	test('project detail shows breadcrumbs with Projects link', async ({ page, request }) => {
		const name = `e2e-bc-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);
		await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

		// "Projects" breadcrumb should link to /.
		const projectsLink = page.getByRole('link', { name: 'Projects' });
		await expect(projectsLink).toBeVisible();
		await expect(projectsLink).toHaveAttribute('href', '/');

		// Namespace breadcrumb text.
		await expect(page.getByText(`project-${name}`)).toBeVisible();
	});

	test('app detail shows project breadcrumb linking back', async ({ page, request }) => {
		const projName = `e2e-bcapp-${randomSuffix()}`;
		const appName = `e2e-app-${randomSuffix()}`;
		projectsToCleanup.push(projName);
		appsToCleanup.push({ project: projName, app: appName });
		await createProjectViaAPI(request, adminToken, projName);
		await createAppViaAPI(request, adminToken, projName, appName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projName}/apps/${appName}`);

		// Wait for the app page to load.
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 15_000 });

		// "Projects" breadcrumb links to /.
		const projectsLink = page.getByRole('link', { name: 'Projects' });
		await expect(projectsLink).toBeVisible();
		await expect(projectsLink).toHaveAttribute('href', '/');

		// Project name breadcrumb links to the project page.
		const projLink = page.getByRole('link', { name: projName });
		await expect(projLink).toBeVisible();
		await expect(projLink).toHaveAttribute(
			'href',
			`/projects/${encodeURIComponent(projName)}`
		);
	});
});
