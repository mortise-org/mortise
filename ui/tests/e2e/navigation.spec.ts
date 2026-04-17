import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	loginViaUI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	deleteProjectViaAPI
} from './helpers';

// End-to-end tests for the new left-rail navigation, project switcher, user
// menu, and extensions page.
//
// New layout:
//   - No flat header nav links (Extensions, Settings, Sign out are gone from header)
//   - Left rail (<nav>) has icon-only links with title= attributes
//   - "Sign out" is in a user menu dropdown (click User icon → Sign out)
//   - "Platform Settings" is in the user menu (admin only) → /admin/settings
//   - Project switcher is a button showing the current project name (inside project)

test.describe('left rail navigation', () => {
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

	test('left rail visible when authenticated (dashboard scope)', async ({ page }) => {
		await loginViaUI(page);

		// Dashboard scope nav: Projects, Extensions, Platform Settings
		await expect(page.getByTitle('Projects')).toBeVisible();
		await expect(page.getByTitle('Extensions')).toBeVisible();
		// Platform Settings link is admin-only
		await expect(page.getByTitle('Platform Settings')).toBeVisible();
	});

	test('left rail not visible on login page', async ({ page }) => {
		await page.goto('/login');

		// The rail nav should not exist on the bare login layout.
		await expect(page.locator('nav')).toHaveCount(0);
	});

	test('Mortise logo links to dashboard', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/extensions');
		await expect(page.getByRole('heading', { name: 'Extensions' })).toBeVisible();

		await page.getByRole('link', { name: 'Mortise' }).click();

		await expect(page).toHaveURL('/');
	});

	test('Extensions nav link navigates to /extensions', async ({ page }) => {
		await loginViaUI(page);

		await page.getByTitle('Extensions').click();

		await expect(page).toHaveURL('/extensions');
		await expect(page.getByRole('heading', { name: 'Extensions' })).toBeVisible();
	});

	test('Platform Settings nav link navigates to /admin/settings', async ({ page }) => {
		await loginViaUI(page);

		await page.getByTitle('Platform Settings').click();

		await expect(page).toHaveURL('/admin/settings');
	});

	test('Projects nav link navigates to /', async ({ page }) => {
		await loginViaUI(page);
		await page.goto('/extensions');

		await page.getByTitle('Projects').click();

		await expect(page).toHaveURL('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();
	});

	test('sign out clears mortise_token and redirects to login', async ({ page }) => {
		await loginViaUI(page);

		// Open the user menu via the User icon button in the header.
		await page.locator('header button[title="User"], header button').filter({ has: page.locator('svg') }).last().click();

		// Click "Sign out" in the dropdown.
		await page.getByRole('button', { name: 'Sign out' }).click();

		await expect(page).toHaveURL('/login', { timeout: 5_000 });

		const token = await page.evaluate(() => localStorage.getItem('mortise_token'));
		expect(token).toBeNull();
	});
});

test.describe('project scope left rail', () => {
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

	test('inside a project, rail shows Canvas and Project Settings links', async ({
		page,
		request
	}) => {
		const name = `e2e-rail-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);

		// Wait for page to load.
		await page.waitForLoadState('networkidle');

		// Project scope: Canvas + Project Settings (no Projects/Extensions/Platform Settings)
		await expect(page.getByTitle('Canvas')).toBeVisible({ timeout: 10_000 });
		await expect(page.getByTitle('Project Settings')).toBeVisible();

		// Dashboard links should NOT appear in project scope.
		await expect(page.getByTitle('Extensions')).toHaveCount(0);
	});

	test('Canvas link is active on the canvas page', async ({ page, request }) => {
		const name = `e2e-railact-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);
		await page.waitForLoadState('networkidle');

		// The Canvas link should be present.
		const canvasLink = page.getByTitle('Canvas');
		await expect(canvasLink).toBeVisible({ timeout: 10_000 });
		await expect(canvasLink).toHaveAttribute('href', `/projects/${name}`);
	});

	test('Project Settings link navigates to project settings page', async ({ page, request }) => {
		const name = `e2e-railset-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);

		await page.getByTitle('Project Settings').click();

		await expect(page).toHaveURL(`/projects/${name}/settings`);
		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible();
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

	test('switcher shows current project name inside a project', async ({ page, request }) => {
		const name = `e2e-sw-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);
		await page.waitForLoadState('networkidle');

		// The project switcher button shows the project name.
		const switcher = page.locator('header').getByRole('button').filter({ hasText: name });
		await expect(switcher).toBeVisible({ timeout: 10_000 });
	});

	test('switcher opens on click and shows project list', async ({ page, request }) => {
		const name = `e2e-swopen-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);
		await page.waitForLoadState('networkidle');

		const switcher = page.locator('header').getByRole('button').filter({ hasText: name });
		await expect(switcher).toBeVisible({ timeout: 10_000 });
		await switcher.click();

		// Dropdown should appear.
		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible();
		await expect(dropdown.getByText(name, { exact: false })).toBeVisible();
	});

	test('switcher navigates to project on click', async ({ page, request }) => {
		const name1 = `e2e-swnav1-${randomSuffix()}`;
		const name2 = `e2e-swnav2-${randomSuffix()}`;
		projectsToCleanup.push(name1, name2);
		await createProjectViaAPI(request, adminToken, name1);
		await createProjectViaAPI(request, adminToken, name2);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name1}`);
		await page.waitForLoadState('networkidle');

		const switcher = page.locator('header').getByRole('button').filter({ hasText: name1 });
		await expect(switcher).toBeVisible({ timeout: 10_000 });
		await switcher.click();

		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible();
		await dropdown.getByText(name2, { exact: false }).click();

		await expect(page).toHaveURL(`/projects/${name2}`, { timeout: 5_000 });
	});

	test('switcher "+ New project" navigates to /projects/new', async ({ page, request }) => {
		const name = `e2e-swnew-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);
		await page.waitForLoadState('networkidle');

		const switcher = page.locator('header').getByRole('button').filter({ hasText: name });
		await expect(switcher).toBeVisible({ timeout: 10_000 });
		await switcher.click();

		const dropdown = page.locator('header .absolute');
		await expect(dropdown).toBeVisible();
		await dropdown.getByText('+ New project').click();

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

		await expect(page.getByRole('heading', { name: 'cert-manager' })).toBeVisible();
		await expect(page.getByRole('heading', { name: 'ExternalDNS' })).toBeVisible();
		await expect(page.getByRole('heading', { name: 'Traefik' })).toBeVisible();
	});

	test('extension card has action link', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/extensions');

		// cert-manager card should have a "Docs" link pointing to the cert-manager site.
		const certManagerCard = page.locator('.rounded-lg').filter({ has: page.getByRole('heading', { name: 'cert-manager' }) });
		const docsLink = certManagerCard.getByRole('link', { name: 'Docs' });
		await expect(docsLink).toBeVisible();
		await expect(docsLink).toHaveAttribute('href', 'https://cert-manager.io/docs/');
	});
});
