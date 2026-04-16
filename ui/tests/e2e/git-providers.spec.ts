import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

// End-to-end CRUD flow for GitProvider via the Mortise UI.
//
// Assumes an operator is reachable at MORTISE_BASE_URL and admin credentials
// are supplied via MORTISE_ADMIN_EMAIL / MORTISE_ADMIN_PASSWORD.

const BASE_URL = process.env.MORTISE_BASE_URL ?? 'http://127.0.0.1:8080';
const ADMIN_EMAIL = process.env.MORTISE_ADMIN_EMAIL;
const ADMIN_PASSWORD = process.env.MORTISE_ADMIN_PASSWORD;

if (!ADMIN_EMAIL || !ADMIN_PASSWORD) {
	throw new Error(
		'MORTISE_ADMIN_EMAIL and MORTISE_ADMIN_PASSWORD must be set for the E2E suite.'
	);
}

function randomSuffix(): string {
	return Math.random().toString(36).slice(2, 8);
}

// Best-effort admin bootstrap. A 409 means setup has already run — we don't
// care, we only need SOME admin present. Any other failure is surfaced so the
// test fails fast rather than getting stuck at the login screen later.
async function ensureAdmin(request: APIRequestContext): Promise<void> {
	const res = await request.post('/api/auth/setup', {
		data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
		failOnStatusCode: false
	});
	if (res.status() === 409) {
		return;
	}
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`admin setup failed: HTTP ${res.status()} ${body}`);
	}
}

async function loginViaAPI(request: APIRequestContext): Promise<string> {
	const res = await request.post('/api/auth/login', {
		data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD }
	});
	if (!res.ok()) {
		throw new Error(`login failed: HTTP ${res.status()}`);
	}
	const body = await res.json();
	if (!body.token) {
		throw new Error('login response missing token');
	}
	return body.token as string;
}

async function loginViaUI(page: Page): Promise<void> {
	await page.goto('/login');
	await page.getByLabel('Email').fill(ADMIN_EMAIL!);
	await page.getByLabel('Password').fill(ADMIN_PASSWORD!);
	await Promise.all([
		page.waitForURL((url) => url.pathname === '/'),
		page.getByRole('button', { name: 'Sign in' }).click()
	]);
}

test.describe('git providers', () => {
	let providerName: string;
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test.afterEach(async ({ request }) => {
		if (!providerName) {
			return;
		}
		try {
			await request.delete(`/api/gitproviders/${providerName}`, {
				headers: { Authorization: `Bearer ${adminToken}` },
				failOnStatusCode: false
			});
		} catch {
			// swallow — the test-happy-path already deleted it, or the API is
			// unreachable. Either way, don't mask the real failure.
		}
	});

	test('create and delete a GitHub provider', async ({ page }) => {
		providerName = `e2e-github-${randomSuffix()}`;

		await loginViaUI(page);

		await page.goto('/settings/git-providers');
		await expect(page.getByRole('heading', { name: 'Git Providers' })).toBeVisible();
		// Empty-state entry button doubles as "open create form".
		const createFromEmpty = page.getByRole('button', { name: 'Create git provider' });
		await expect(createFromEmpty).toBeVisible();
		await expect(page.getByText('No git providers configured')).toBeVisible();
		await createFromEmpty.click();

		// Fill the form. getByLabel resolves against the <label for=...> pairs.
		await page.getByLabel('Name').fill(providerName);
		await page.getByLabel('Type').selectOption('github');

		const hostInput = page.getByLabel('Host');
		await expect(hostInput).toHaveValue('https://github.com');

		await page.getByLabel('OAuth Client ID').fill('e2e-test-client-id');
		await page.getByLabel('OAuth Client Secret').fill('e2e-test-client-secret');

		// "Generate" button is inline with the Webhook Secret label. The
		// secret input itself is type=password so we read its value directly.
		const webhookSecretInput = page.getByLabel('Webhook Secret');
		await page.getByRole('button', { name: 'Generate' }).click();
		await expect(webhookSecretInput).not.toHaveValue('');

		await page.getByRole('button', { name: 'Create provider' }).click();

		// The provider table should now render with our row. Phase may or may
		// not have populated yet — assert the row exists and that a Delete
		// action is wired up for it.
		const row = page.getByRole('row').filter({ hasText: providerName });
		await expect(row).toBeVisible({ timeout: 10_000 });
		await expect(row.getByRole('cell').nth(1)).toHaveText(/github/i);

		// Confirm the browser dialog, then click Delete.
		page.once('dialog', (dialog) => dialog.accept());
		await row.getByRole('button', { name: 'Delete' }).click();

		await expect(row).toHaveCount(0, { timeout: 5_000 });
		await expect(page.getByText('No git providers configured')).toBeVisible();

		// Test passed — skip afterEach's delete fallback.
		providerName = '';
	});
});
