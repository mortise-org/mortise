/**
 * Platform settings action tests — admin save actions at /admin/settings.
 *
 * ALL API calls are mocked via page.route(). No live backend required.
 * Tests are fully independent; each sets up its own state and mocks.
 */
import { test, expect, type Page } from '@playwright/test';

// ---------------------------------------------------------------------------
// Standard mock data
// ---------------------------------------------------------------------------

const mockProject = {
	name: 'my-project',
	namespace: 'project-my-project',
	phase: 'Ready' as const,
	appCount: 2,
	description: 'Test project'
};

const mockGitProvider = {
	name: 'github-main',
	type: 'github' as const,
	host: 'github.com',
	mode: 'oauth' as const,
	phase: 'Ready' as const,
	hasToken: true
};

const mockPlatform = {
	domain: 'example.com',
	dns: { provider: 'cloudflare' },
	tls: { certManagerClusterIssuer: 'letsencrypt-prod' }
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function injectAuth(page: Page, isAdmin = true) {
	await page.goto('/');
	await page.evaluate(({ isAdmin }) => {
		localStorage.setItem('mortise_token', 'test-token');
		localStorage.setItem(
			'mortise_user',
			JSON.stringify({
				email: 'admin@example.com',
				role: isAdmin ? 'admin' : 'member'
			})
		);
	}, { isAdmin });
}

async function setupBaseMocks(page: Page, providers = [mockGitProvider]) {
	await page.route('/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
	await page.route('/api/projects', (r) => r.fulfill({ json: [mockProject] }));
	await page.route('/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
	await page.route('/api/projects/my-project/activity', (r) => r.fulfill({ json: [] }));
	await page.route('/api/gitproviders', (r) => r.fulfill({ json: providers }));
	await page.route('/api/platform', (r) => r.fulfill({ json: mockPlatform }));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('platform settings actions', () => {
	test('Test 1: Admin can update platform domain → verifies PATCH body has { domain }', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupBaseMocks(page);
		await page.route('/api/platform', async (route) => {
			if (route.request().method() === 'PATCH') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({
					status: 200,
					json: { domain: 'newdomain.com', dns: { provider: 'cloudflare' }, tls: {} }
				});
			}
			return route.fulfill({ json: mockPlatform });
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		const domainInput = page.locator('#domain, input[placeholder="yourdomain.com"]');
		await domainInput.clear();
		await domainInput.fill('newdomain.com');

		// Save button in General section
		await page.locator('section#general button', { hasText: 'Save' }).click();

		await expect.poll(() => capturedBody).toMatchObject({ domain: 'newdomain.com' });
	});

	test('Test 2: Admin can save registry config → verifies PATCH called', async ({ page }) => {
		let patchCalled = false;

		await setupBaseMocks(page);
		await page.route('/api/platform', async (route) => {
			if (route.request().method() === 'PATCH') {
				patchCalled = true;
				return route.fulfill({ status: 200, json: mockPlatform });
			}
			return route.fulfill({ json: mockPlatform });
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		const regUrlInput = page.locator('#reg-url');
		await regUrlInput.scrollIntoViewIfNeeded();
		await regUrlInput.fill('registry.mycompany.com');

		const regNsInput = page.locator('#reg-ns');
		await regNsInput.fill('myorg');

		const regUserInput = page.locator('#reg-user');
		await regUserInput.fill('admin');

		await page.getByRole('button', { name: 'Save registry config' }).click();

		await expect.poll(() => patchCalled).toBe(true);
	});

	test('Test 3: Admin can save build config → verifies PATCH called', async ({ page }) => {
		let patchCalled = false;

		await setupBaseMocks(page);
		await page.route('/api/platform', async (route) => {
			if (route.request().method() === 'PATCH') {
				patchCalled = true;
				return route.fulfill({ status: 200, json: mockPlatform });
			}
			return route.fulfill({ json: mockPlatform });
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		const bkAddrInput = page.locator('#bk-addr');
		await bkAddrInput.scrollIntoViewIfNeeded();
		await bkAddrInput.fill('tcp://buildkitd.mortise-system:1234');

		const bkPlatformSelect = page.locator('#bk-platform');
		await bkPlatformSelect.selectOption('linux/arm64');

		await page.getByRole('button', { name: 'Save build config' }).click();

		await expect.poll(() => patchCalled).toBe(true);
	});

	test('Test 4: Admin can save TLS cluster issuer → verifies PATCH body has tls data', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupBaseMocks(page);
		await page.route('/api/platform', async (route) => {
			if (route.request().method() === 'PATCH') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockPlatform });
			}
			return route.fulfill({ json: mockPlatform });
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		const tlsIssuerInput = page.locator('#tls-issuer');
		await tlsIssuerInput.scrollIntoViewIfNeeded();
		await tlsIssuerInput.clear();
		await tlsIssuerInput.fill('letsencrypt-staging');

		await page.getByRole('button', { name: 'Save TLS config' }).click();

		await expect.poll(() => capturedBody).toMatchObject({
			tls: { certManagerClusterIssuer: 'letsencrypt-staging' }
		});
	});

	test('Test 5: Admin can add a git provider → verifies POST /api/gitproviders with correct body', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupBaseMocks(page, []);
		await page.route('/api/gitproviders', async (route) => {
			if (route.request().method() === 'POST') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 201, json: { name: 'new-github', type: 'github', host: 'github.com', phase: 'Pending', hasToken: false } });
			}
			// After create, return the new provider in the list
			if (capturedBody) {
				return route.fulfill({ json: [{ name: 'new-github', type: 'github', host: 'github.com', phase: 'Pending', hasToken: false }] });
			}
			return route.fulfill({ json: [] });
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		await page.getByRole('button', { name: 'Add Provider' }).click();
		await expect(page.getByText('New Git Provider')).toBeVisible({ timeout: 3000 });

		// Fill in the form
		await page.getByPlaceholder('github-main').fill('new-github');
		await page.getByPlaceholder('https://github.com').fill('https://github.com');

		// OAuth Client ID — first unlabelled text input inside the provider form grid
		const oauthClientIdInput = page.locator('input[type="text"]').nth(1);
		await oauthClientIdInput.fill('test-client-id');

		const oauthClientSecretInput = page.locator('input[type="password"]').first();
		await oauthClientSecretInput.fill('test-client-secret');

		// Webhook secret — last text input in the form
		const webhookInput = page.locator('input[type="text"]').last();
		await webhookInput.fill('my-webhook-secret');

		await page.getByRole('button', { name: 'Create' }).click();

		await expect.poll(() => capturedBody).toMatchObject({
			name: 'new-github',
			type: 'github',
			host: 'https://github.com',
			oauth: { clientID: 'test-client-id', clientSecret: 'test-client-secret' },
			webhookSecret: 'my-webhook-secret'
		});
	});

	test('Test 6: Admin can delete a git provider → verifies DELETE /api/gitproviders/github-main called', async ({ page }) => {
		let deleteWasCalled = false;

		await setupBaseMocks(page);
		await page.route('/api/gitproviders/github-main', async (route) => {
			if (route.request().method() === 'DELETE') {
				deleteWasCalled = true;
				return route.fulfill({ status: 200, json: {} });
			}
			return route.fulfill({ json: mockGitProvider });
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		// Provider row should be visible
		await expect(page.getByText('github-main')).toBeVisible({ timeout: 5000 });

		// Click the trash/delete icon button in the provider row
		const providerRow = page.locator('div').filter({ hasText: 'github-main' }).last();
		page.once('dialog', (dialog) => dialog.accept());
		await providerRow.getByRole('button').last().click();

		await expect.poll(() => deleteWasCalled).toBe(true);
	});

	test('Test 7: Git provider connect link points to correct OAuth URL (provider without token)', async ({ page }) => {
		const providerWithoutToken = {
			...mockGitProvider,
			name: 'github-unconnected',
			hasToken: false,
			phase: 'Pending' as const
		};

		await setupBaseMocks(page, [providerWithoutToken]);

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		// The "Connect" link should point to /api/oauth/{name}/authorize
		const connectLink = page.getByRole('link', { name: 'Connect' });
		await expect(connectLink).toBeVisible({ timeout: 5000 });
		await expect(connectLink).toHaveAttribute('href', '/api/oauth/github-unconnected/authorize');
	});

	test('Test 8: Filter input narrows visible sections (type "registry" → only registry section visible)', async ({ page }) => {
		await setupBaseMocks(page);

		await injectAuth(page, true);
		await page.goto('/admin/settings');
		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({ timeout: 5000 });

		// Verify General and Git Providers are visible before filtering
		await expect(page.getByText('General')).toBeVisible();
		await expect(page.getByText('Git Providers')).toBeVisible();

		// Type "registry" in the filter — note the actual placeholder in the HTML is "Filter settings..."
		const filterInput = page.getByPlaceholder('Filter settings...');
		await filterInput.fill('registry');

		// Registry section should still be visible
		await expect(page.getByText('Registry')).toBeVisible({ timeout: 3000 });

		// General section heading should not be visible (filtered out)
		// The section id="general" has h2 "General" — it should be hidden
		await expect(page.locator('section#general h2')).not.toBeVisible();
	});

	test('Test 9: Platform Settings link hidden for non-admin users', async ({ page }) => {
		await setupBaseMocks(page);

		// Inject as non-admin (member)
		await injectAuth(page, false);
		await page.goto('/');

		// Left rail Platform Settings icon should not be present for members
		await expect(page.getByTitle('Platform Settings')).not.toBeVisible({ timeout: 5000 });
	});
});
