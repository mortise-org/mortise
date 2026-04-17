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
		await expect(page.getByText('Create your first admin account')).toBeVisible();

		await expect(page.getByLabel('Email')).toBeVisible();
		await expect(page.getByLabel('Password', { exact: true })).toBeVisible();
		await expect(page.getByLabel('Confirm password')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Create admin account' })).toBeVisible();
	});

	test('shows validation error for mismatched passwords', async ({ page }) => {
		await page.route('**/api/auth/status', (route) =>
			route.fulfill({
				status: 200,
				contentType: 'application/json',
				body: JSON.stringify({ setupRequired: true })
			})
		);

		await page.goto('/setup');

		await page.getByLabel('Email').fill('valid@example.com');
		await page.getByLabel('Password', { exact: true }).fill('password123');
		await page.getByLabel('Confirm password').fill('differentpassword');
		await page.getByRole('button', { name: 'Create admin account' }).click();

		await expect(page.getByText('Passwords do not match')).toBeVisible();
	});

	test('shows validation error for short password', async ({ page }) => {
		await page.route('**/api/auth/status', (route) =>
			route.fulfill({
				status: 200,
				contentType: 'application/json',
				body: JSON.stringify({ setupRequired: true })
			})
		);

		await page.goto('/setup');

		await page.getByLabel('Email').fill('valid@example.com');
		await page.getByLabel('Password', { exact: true }).fill('short');
		await page.getByLabel('Confirm password').fill('short');

		// Remove native HTML5 validation so the custom JS validation fires.
		await page.evaluate(() => {
			document.querySelector('form')?.setAttribute('novalidate', '');
		});
		await page.getByRole('button', { name: 'Create admin account' }).click();

		await expect(page.getByText('Password must be at least 8 characters')).toBeVisible();
	});

	test('shows validation error for invalid email', async ({ page }) => {
		await page.route('**/api/auth/status', (route) =>
			route.fulfill({
				status: 200,
				contentType: 'application/json',
				body: JSON.stringify({ setupRequired: true })
			})
		);

		await page.goto('/setup');

		await page.getByLabel('Email').fill('not-an-email');
		await page.getByLabel('Password', { exact: true }).fill('password123');
		await page.getByLabel('Confirm password').fill('password123');

		// Remove native HTML5 validation so the custom JS validation fires.
		await page.evaluate(() => {
			document.querySelector('form')?.setAttribute('novalidate', '');
		});
		await page.getByRole('button', { name: 'Create admin account' }).click();

		await expect(page.getByText('Enter a valid email address')).toBeVisible();
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

		await page.getByLabel('Email').fill('admin@example.com');
		await page.getByLabel('Password', { exact: true }).fill('password12345');
		await page.getByLabel('Confirm password').fill('password12345');
		await page.getByRole('button', { name: 'Create admin account' }).click();

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
		await expect(page.getByText('Sign in to your account')).toBeVisible();
		await expect(page.getByLabel('Email')).toBeVisible();
		await expect(page.getByLabel('Password')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
	});

	test('successful login redirects to / and stores token', async ({ page }) => {
		await page.goto('/login');

		await page.getByLabel('Email').fill(ADMIN_EMAIL);
		await page.getByLabel('Password').fill(ADMIN_PASSWORD);

		await Promise.all([
			page.waitForURL((url) => url.pathname === '/'),
			page.getByRole('button', { name: 'Sign in' }).click()
		]);

		const token = await page.evaluate(() => localStorage.getItem('token'));
		expect(token).toBeTruthy();
	});

	test('invalid credentials show error message', async ({ page }) => {
		await page.goto('/login');

		await page.getByLabel('Email').fill('wrong@example.com');
		await page.getByLabel('Password').fill('wrongpassword');
		await page.getByRole('button', { name: 'Sign in' }).click();

		// The server returns an error; the page renders it in the error div.
		const errorEl = page.locator('.bg-danger\\/10');
		await expect(errorEl).toBeVisible({ timeout: 5_000 });
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
		await page.evaluate(() => localStorage.removeItem('token'));

		await page.goto('/');
		await page.waitForURL('**/login');
	});

	test('visiting /projects/default without a token redirects to /login', async ({ page }) => {
		await page.goto('/login');
		await page.evaluate(() => localStorage.removeItem('token'));

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
// Setup wizard (/setup/wizard)
// ---------------------------------------------------------------------------

test.describe('setup wizard', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('shows progress bar and step 1 by default', async ({ page }) => {
		// Inject token so the wizard doesn't redirect to /login.
		await injectToken(page, adminToken);

		// Intercept the PlatformConfig GET so the wizard doesn't redirect to /
		// (it redirects if a domain already exists).
		await page.route('**/api/platform', (route) => {
			if (route.request().method() === 'GET') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({ domain: '' })
				});
			}
			return route.continue();
		});

		await page.goto('/setup/wizard');

		// Progress bar: 4 step indicators.
		const progressDots = page.locator('.h-1\\.5.w-12');
		await expect(progressDots).toHaveCount(4);

		// Step 1 content.
		await expect(page.getByRole('heading', { name: 'Platform Domain' })).toBeVisible();
		await expect(page.getByPlaceholder('yourdomain.com')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Next' })).toBeVisible();
		// Step 1's "Skip for now" is a link to /.
		await expect(page.getByRole('link', { name: 'Skip for now' })).toBeVisible();
	});

	test('can navigate through all wizard steps', async ({ page }) => {
		await injectToken(page, adminToken);

		// Mock PlatformConfig so wizard doesn't redirect, and PATCH succeeds.
		await page.route('**/api/platform', (route) => {
			if (route.request().method() === 'GET') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({ domain: '' })
				});
			}
			return route.fulfill({
				status: 200,
				contentType: 'application/json',
				body: JSON.stringify({})
			});
		});

		// Mock git provider creation so step 3 doesn't hit the real API.
		await page.route('**/api/gitproviders', (route) => {
			if (route.request().method() === 'POST') {
				return route.fulfill({
					status: 201,
					contentType: 'application/json',
					body: JSON.stringify({})
				});
			}
			return route.continue();
		});

		await page.goto('/setup/wizard');

		// -- Step 1: Platform Domain --
		await expect(page.getByRole('heading', { name: 'Platform Domain' })).toBeVisible();

		// Enter a domain and click Next to advance to step 2.
		// (Step 1's "Skip for now" is an <a href="/"> that leaves the wizard.)
		await page.getByPlaceholder('yourdomain.com').fill('test.example.com');
		await page.getByRole('button', { name: 'Next' }).click();

		// -- Step 2: DNS Provider --
		await expect(page.getByRole('heading', { name: 'DNS Provider' })).toBeVisible();

		// Verify the DNS dropdown has expected options.
		const dnsSelect = page.locator('select').first();
		await expect(dnsSelect).toBeVisible();
		const options = dnsSelect.locator('option');
		await expect(options).toHaveCount(3);
		await expect(options.nth(0)).toHaveText(/None/);
		await expect(options.nth(1)).toHaveText(/Cloudflare/);
		await expect(options.nth(2)).toHaveText(/Route 53/);

		// Selecting Cloudflare shows the API token input.
		await dnsSelect.selectOption('cloudflare');
		await expect(page.getByPlaceholder('API token')).toBeVisible();

		// Selecting None hides the API token input.
		await dnsSelect.selectOption('none');
		await expect(page.getByPlaceholder('API token')).not.toBeVisible();

		// Go back to step 1.
		await page.getByRole('button', { name: 'Back' }).click();
		await expect(page.getByRole('heading', { name: 'Platform Domain' })).toBeVisible();

		// Advance again: fill domain and click Next.
		await page.getByPlaceholder('yourdomain.com').fill('test.example.com');
		await page.getByRole('button', { name: 'Next' }).click();
		await expect(page.getByRole('heading', { name: 'DNS Provider' })).toBeVisible();

		// Skip step 2 to step 3.
		await page.getByRole('button', { name: 'Skip for now' }).click();

		// -- Step 3: Git Provider --
		await expect(page.getByRole('heading', { name: 'Git Provider' })).toBeVisible();

		// Verify git provider form fields.
		const gitTypeSelect = page.locator('select').first();
		await expect(gitTypeSelect).toBeVisible();
		await expect(page.getByPlaceholder(/Provider name/)).toBeVisible();
		await expect(page.getByPlaceholder(/Host URL/)).toBeVisible();
		await expect(page.getByPlaceholder('OAuth Client ID')).toBeVisible();
		await expect(page.getByPlaceholder('OAuth Client Secret')).toBeVisible();
		await expect(page.getByPlaceholder('Webhook Secret')).toBeVisible();

		// Go back to step 2.
		await page.getByRole('button', { name: 'Back' }).click();
		await expect(page.getByRole('heading', { name: 'DNS Provider' })).toBeVisible();

		// Skip step 2, then skip step 3.
		await page.getByRole('button', { name: 'Skip for now' }).click();
		await expect(page.getByRole('heading', { name: 'Git Provider' })).toBeVisible();
		await page.getByRole('button', { name: 'Skip for now' }).click();

		// -- Step 4: Completion --
		await expect(page.getByRole('heading', { name: 'Your platform is ready!' })).toBeVisible();
		await expect(
			page.getByRole('link', { name: 'Deploy your first app' })
		).toHaveAttribute('href', '/projects/default/apps/new');
		await expect(page.getByRole('link', { name: 'Go to dashboard' })).toHaveAttribute(
			'href',
			'/'
		);
	});
});
