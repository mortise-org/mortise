import { expect, test } from '@playwright/test';
import {
	ADMIN_EMAIL,
	ADMIN_PASSWORD,
	ensureAdmin,
	loginViaAPI,
	loginViaUI,
	injectToken
} from './helpers';

// ---------------------------------------------------------------------------
// Setup page (/setup)
// ---------------------------------------------------------------------------

test.describe('setup page', () => {
	test.beforeAll(async ({ request }) => {
		// Ensure admin exists so the setup endpoint will return 409.
		await ensureAdmin(request);
	});

	test('renders the welcome heading and form fields', async ({ page }) => {
		// Intercept the setup-status check so the layout doesn't redirect us
		// away from /setup before we can inspect the page.
		await page.route('**/api/auth/status', (route) =>
			route.fulfill({
				status: 200,
				contentType: 'application/json',
				body: JSON.stringify({ setupRequired: true })
			})
		);

		await page.goto('/setup');

		await expect(page.getByRole('heading', { name: 'Welcome to Mortise' })).toBeVisible();
		// Actual subtitle text in setup page
		await expect(page.getByText(/Create your admin account/)).toBeVisible();

		await expect(page.getByLabel('Admin Username')).toBeVisible();
		await expect(page.getByLabel('Password')).toBeVisible();
		await expect(page.getByRole('button', { name: /Create account/ })).toBeVisible();
	});

	test('redirects to /login with flash when setup already done (409)', async ({ page }) => {
		await page.route('**/api/auth/status', (route) =>
			route.fulfill({
				status: 200,
				contentType: 'application/json',
				body: JSON.stringify({ setupRequired: true })
			})
		);

		// Let the actual /api/auth/setup endpoint return a real 409.
		await page.goto('/setup');

		await page.getByLabel('Admin Username').fill('admin@example.com');
		await page.getByLabel('Password').fill('password12345');
		await page.getByRole('button', { name: /Create account/ }).click();

		// The setup endpoint returns 409 (admin already exists), which the
		// page handles by redirecting to /login with a flash message.
		await page.waitForURL('**/login');
		await expect(page.getByText('Setup already complete. Please sign in.')).toBeVisible();
	});
});

// ---------------------------------------------------------------------------
// Login page (/login)
// ---------------------------------------------------------------------------

test.describe('login page', () => {
	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
	});

	test('renders the sign-in heading and form fields', async ({ page }) => {
		await page.goto('/login');

		await expect(page.getByRole('heading', { name: 'Mortise' })).toBeVisible();
		await expect(page.getByText('Sign in to your platform')).toBeVisible();
		await expect(page.getByLabel('Username')).toBeVisible();
		await expect(page.getByLabel('Password')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
	});

	test('successful login redirects to / and stores mortise_token', async ({ page }) => {
		await page.goto('/login');

		await page.getByLabel('Username').fill(ADMIN_EMAIL);
		await page.getByLabel('Password').fill(ADMIN_PASSWORD);

		await Promise.all([
			page.waitForURL((url) => url.pathname === '/'),
			page.getByRole('button', { name: 'Sign in' }).click()
		]);

		const token = await page.evaluate(() => localStorage.getItem('mortise_token'));
		expect(token).toBeTruthy();
	});

	test('successful login stores admin role and shows admin nav', async ({ page }) => {
		await page.goto('/login');

		await page.getByLabel('Username').fill(ADMIN_EMAIL);
		await page.getByLabel('Password').fill(ADMIN_PASSWORD);

		await Promise.all([
			page.waitForURL((url) => url.pathname === '/'),
			page.getByRole('button', { name: 'Sign in' }).click()
		]);

		// After login, the Platform Settings link should be visible for admin.
		await expect(page.getByTitle('Platform Settings')).toBeVisible({ timeout: 5_000 });
	});

	test('invalid credentials show error message', async ({ page }) => {
		await page.goto('/login');

		await page.getByLabel('Username').fill('wrong@example.com');
		await page.getByLabel('Password').fill('wrongpassword');
		await page.getByRole('button', { name: 'Sign in' }).click();

		// The server returns an error; the page renders it as a text-danger paragraph.
		await expect(page.locator('p.text-danger')).toBeVisible({ timeout: 5_000 });
	});
});

// ---------------------------------------------------------------------------
// Auth redirects
// ---------------------------------------------------------------------------

test.describe('auth redirects', () => {
	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
	});

	test('visiting / without a token redirects to /login', async ({ page }) => {
		// Clear any existing token.
		await page.goto('/login');
		await page.evaluate(() => localStorage.removeItem('mortise_token'));

		await page.goto('/');
		await page.waitForURL('**/login');
	});

	test('visiting /projects/default without a token redirects to /login', async ({ page }) => {
		await page.goto('/login');
		await page.evaluate(() => localStorage.removeItem('mortise_token'));

		await page.goto('/projects/default');
		await page.waitForURL('**/login');
	});

	test('after login, navigating to / shows the Projects heading', async ({ page }) => {
		await loginViaUI(page);

		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();
	});
});

// ---------------------------------------------------------------------------
// Getting started page (/setup/wizard)
// ---------------------------------------------------------------------------

test.describe('getting started page', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('shows getting started checklist with configuration guidance', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/setup/wizard');

		await expect(page.getByRole('heading', { name: "You're in" })).toBeVisible({ timeout: 10_000 });

		await expect(page.getByText('Platform domain')).toBeVisible();
		await expect(page.getByText('Git provider')).toBeVisible();
		await expect(page.getByText('HTTPS, storage, registry')).toBeVisible();

		await expect(page.getByRole('button', { name: /Go to Dashboard/, exact: false })).toBeVisible();
		await expect(page.getByRole('link', { name: 'Settings' })).toBeVisible();
	});
});
