import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	loginViaUI,
	injectToken,
	randomSuffix,
	getEnvViaAPI,
	createProjectViaAPI,
	deleteProjectViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Full user journey — the "real user" happy path
//
// This test chains:
//   login → see projects → create project → deploy Docker app via modal →
//   view app in drawer → check Variables tab → close drawer → sign out
//
// API verification is done at key steps to confirm persistence.
// ---------------------------------------------------------------------------

test.describe('full user journey', () => {
	let adminToken: string;
	const projectName = `e2e-journey-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('login → project → deploy app → manage in drawer → sign out — full lifecycle', async ({
		page,
		request
	}) => {
		// Increase timeout for this long journey test.
		test.setTimeout(120_000);

		const appName = `journey-app-${randomSuffix()}`;

		// ── Step 1: Login via the UI ──────────────────────────────────
		await loginViaUI(page);
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();

		// Token stored as mortise_token.
		const token = await page.evaluate(() => localStorage.getItem('mortise_token'));
		expect(token).toBeTruthy();

		// ── Step 2: Create a project via the UI ──────────────────────
		// "New Project" link is admin-only in the dashboard header.
		await page.getByRole('link', { name: 'New Project' }).click();
		await expect(page).toHaveURL('/projects/new');
		await expect(page.getByRole('heading', { name: 'New Project' })).toBeVisible();

		await page.getByLabel('Project name').fill(projectName);
		await page.getByLabel('Description').fill('Journey test project');

		await page.getByRole('button', { name: 'Create project' }).click();

		// Should redirect to the project canvas page.
		await expect(page).toHaveURL(new RegExp(`/projects/${projectName}(\\?|$)`), { timeout: 10_000 });

		// Switch to list view to verify empty state.
		await page.getByTitle('List view').click();
		await expect(page.getByText('No apps in this project')).toBeVisible();

		// ── Step 3: Deploy a Docker image app via the new-app page ──────────────
		// Wait for the project namespace to be ready before creating an app.
		await expect(async () => {
			const res = await request.get(`/api/projects/${projectName}`, {
				headers: { Authorization: `Bearer ${adminToken}` }
			});
			expect(res.ok()).toBeTruthy();
			const body = await res.json();
			expect(body.phase).toBe('Ready');
		}).toPass({ timeout: 30_000 });

		await page.goto(`/projects/${projectName}/apps/new`);
		await expect(page).toHaveURL(new RegExp(`/projects/${projectName}/apps/new(\\?|$)`));

		// Select Docker Image type.
		await expect(page.getByText('Docker Image', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Docker Image', { exact: true }).click();

		// Fill image and app name.
		await page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest').fill('nginx:1.27');
		await page.getByPlaceholder('my-app').fill(appName);

		await page.getByRole('button', { name: 'Create app' }).click();

		// After creation, navigates to the app drawer URL.
		await expect(page).toHaveURL(new RegExp(`/projects/${projectName}/apps/${appName}(\\?|$)`), { timeout: 15_000 });

		// ── Step 4: App drawer is open with app name ─────────────────
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Phase badge is visible.
		const phaseBadge = page.locator('span', {
			hasText: /Ready|Pending|Deploying|Building|Failed|CrashLooping/
		});
		await expect(phaseBadge.first()).toBeVisible({ timeout: 15_000 });

		// ── Step 5: Check Variables tab ───────────────────────────────
		await page.getByRole('button', { name: 'Variables' }).click();
		// Actual empty state text in VariablesTab.
		await expect(page.getByText(/No variables set/).first()).toBeVisible({ timeout: 5_000 });

		// Add a variable inline. The "+" icon button is in the Runtime section
		// header. We scope to the bordered section container that holds
		// "Runtime - <env>" and click its last header button (the + icon).
		// In the empty state, the only buttons in the header are Table, Raw, +.
		const envVarSection = page
			.locator('div.rounded-lg.border')
			.filter({ hasText: /Runtime -/ })
			.first();
		await envVarSection.locator('button').last().click();
		await page.getByPlaceholder('VARIABLE_NAME').fill('APP_ENV');
		await page.getByPlaceholder('value').fill('production');
		await page.getByRole('button', { name: 'Add', exact: true }).click();

		// Variable should appear in the list.
		await expect(page.getByText('APP_ENV')).toBeVisible({ timeout: 10_000 });

		// Verify via API.
		await expect(async () => {
			const envVars = await getEnvViaAPI(request, adminToken, projectName, appName);
			expect(
				envVars.some((v) => v.name === 'APP_ENV' && v.value === 'production')
			).toBeTruthy();
		}).toPass({ timeout: 10_000 });

		// ── Step 6: Check Settings tab → Domains ─────────────────────
		await page.getByRole('button', { name: 'Settings', exact: true }).click();

		// Filter to domains section.
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByPlaceholder('custom.example.com')).toBeVisible({ timeout: 5_000 });
		await page.getByPlaceholder('Filter settings…').clear();

		// ── Step 7: Close drawer, return to canvas ────────────────────
		await page.getByRole('button', { name: 'Close drawer' }).click();

		await expect(page).toHaveURL(new RegExp(`/projects/${projectName}(\\?|$)`), { timeout: 5_000 });

		// Canvas (or list view) should be visible.
		await expect(page.getByTitle('Canvas view')).toBeVisible();

		// ── Step 8: Delete the project via project settings ───────────
		await page.getByTitle('Project Settings', { exact: true }).click();
		await expect(page).toHaveURL(new RegExp(`/projects/${projectName}/settings(\\?|$)`));

		// Navigate to the Danger tab.
		await page.getByRole('button', { name: 'Danger' }).click();

		// Type project name into confirmation input and delete.
		await page.getByPlaceholder(projectName).fill(projectName);
		await page.getByRole('button', { name: 'Delete project' }).click();

		// Should redirect to dashboard.
		await page.waitForURL((url) => url.pathname === '/', { timeout: 15_000 });

		// Project should eventually be gone from the list.
		await expect(async () => {
			await page.reload();
			await expect(page.locator('a').filter({ hasText: projectName })).toHaveCount(0);
		}).toPass({ timeout: 15_000, intervals: [2_000, 3_000, 5_000] });
	});
});

// ---------------------------------------------------------------------------
// Sign out journey
// ---------------------------------------------------------------------------

test.describe('sign out journey', () => {
	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
	});

	test('sign out clears mortise_token and redirects to login, cannot access dashboard', async ({
		page
	}) => {
		await loginViaUI(page);

		// Verify we're on the dashboard.
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();

		// Open user menu (click User icon button in the header right side).
		// The User icon is the last icon button in the header before sign out.
		const userMenuBtn = page.locator('header').locator('button').last();
		await userMenuBtn.click();

		// Click "Sign out" in the dropdown.
		await page.getByRole('button', { name: 'Sign out' }).click();

		// Should redirect to login.
		await expect(page).toHaveURL('/login', { timeout: 5_000 });

		// mortise_token should be cleared.
		const token = await page.evaluate(() => localStorage.getItem('mortise_token'));
		expect(token).toBeNull();

		// Trying to navigate to dashboard should redirect back to login.
		await page.goto('/');
		await page.waitForURL('**/login');
	});
});

// ---------------------------------------------------------------------------
// Platform settings journey (admin only)
// ---------------------------------------------------------------------------

test.describe('platform settings journey', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('navigate to platform settings via user menu', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible({ timeout: 10_000 });

		// Settings is in the left rail (admin only).
		await page.getByTitle('Settings').click();

		await expect(page).toHaveURL('/settings');
		await expect(
			page.getByRole('heading', { name: 'Settings' })
		).toBeVisible();
	});

	test('platform settings has General, Git Providers, Users sections', async ({
		page
	}) => {
		await injectToken(page, adminToken);
		await page.goto('/settings');

		await expect(
			page.getByRole('heading', { name: 'Settings' })
		).toBeVisible({ timeout: 10_000 });

		// Use heading role to avoid strict mode violations from descriptions.
		await expect(page.getByRole('heading', { name: 'Platform Domain' })).toBeVisible();
		await expect(page.getByRole('heading', { name: 'Git Providers' })).toBeVisible();
		await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
	});
});

// ---------------------------------------------------------------------------
// App creation journey — verifies the full modal flow from project canvas
// ---------------------------------------------------------------------------

test.describe('app creation via modal journey', () => {
	let adminToken: string;
	const projectName = `e2e-createjrn-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		await createProjectViaAPI(request, adminToken, projectName, 'App creation journey');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('create app via modal → view drawer → close', async ({ page }) => {
		test.setTimeout(60_000);

		const appName = `modal-app-${randomSuffix()}`;

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/apps/new`);

		// Select Docker Image.
		await expect(page.getByText('Docker Image', { exact: true })).toBeVisible({ timeout: 10_000 });
		await page.getByText('Docker Image', { exact: true }).click();

		// Fill image ref and app name.
		await page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest').fill('nginx:1.27');
		await page.getByPlaceholder('my-app').fill(appName);

		// Create the app.
		await page.getByRole('button', { name: 'Create app' }).click();

		// Should navigate to the app drawer URL.
		await expect(page).toHaveURL(new RegExp(`/projects/${projectName}/apps/${appName}(\\?|$)`), { timeout: 15_000 });

		// Drawer is open with app name.
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });

		// Switch tabs to verify they work.
		await page.getByRole('button', { name: 'Deployments' }).click();
		await page.getByRole('button', { name: 'Variables' }).click();
		await expect(page.getByText(/No variables set/).first()).toBeVisible({ timeout: 5_000 });

		// Close the drawer.
		await page.getByRole('button', { name: 'Close drawer' }).click();
		await expect(page).toHaveURL(new RegExp(`/projects/${projectName}(\\?|$)`), { timeout: 5_000 });

		// Switch to list view and verify the app appears.
		await page.getByTitle('List view').click();
		await expect(page.getByText(appName)).toBeVisible({ timeout: 10_000 });
	});
});
