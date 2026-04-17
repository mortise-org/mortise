/**
 * Git provider CRUD and OAuth setup tests.
 *
 * All API calls mocked via page.route(). No live backend.
 */
import { test, expect, type Page } from '@playwright/test';

// ---------------------------------------------------------------------------
// Standard mock data
// ---------------------------------------------------------------------------

const mockGitProvider = {
	name: 'github-main',
	type: 'github' as const,
	host: 'github.com',
	mode: 'oauth' as const,
	phase: 'Ready' as const,
	hasToken: true
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

async function setupBaseRoutes(page: Page) {
	await page.route('/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
	await page.route('/api/projects', (r) => r.fulfill({ json: [] }));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('git providers oauth', () => {
	test('Test 1: Admin can view git providers list', async ({ page }) => {
		await setupBaseRoutes(page);
		await page.route('/api/gitproviders', (r) => r.fulfill({ json: [mockGitProvider] }));
		await page.route('/api/platform', (r) =>
			r.fulfill({
				json: {
					domain: 'example.com',
					dns: { provider: 'cloudflare' },
					tls: { certManagerClusterIssuer: 'letsencrypt-prod' }
				}
			})
		);
		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({
			timeout: 5000
		});
		// Provider name appears in the list
		await expect(page.getByText('github-main')).toBeVisible({ timeout: 5000 });
		// Type and host shown
		await expect(page.getByText(/github · github\.com/)).toBeVisible({ timeout: 5000 });
		// Provider is connected (has token)
		await expect(page.getByText('Connected')).toBeVisible({ timeout: 5000 });
	});

	test('Test 2: Admin can add a GitHub OAuth provider', async ({ page }) => {
		await setupBaseRoutes(page);

		// Start with empty providers list; single route handler captures POST body
		let providersPayload: object[] = [];
		let capturedBody: Record<string, unknown> | null = null;
		await page.route('/api/gitproviders', async (r) => {
			if (r.request().method() === 'GET') {
				await r.fulfill({ json: providersPayload });
			} else if (r.request().method() === 'POST') {
				capturedBody = JSON.parse(r.request().postData() ?? '{}');
				providersPayload = [mockGitProvider];
				await r.fulfill({ status: 201, json: mockGitProvider });
			}
		});
		await page.route('/api/platform', (r) =>
			r.fulfill({
				json: {
					domain: 'example.com',
					dns: { provider: 'cloudflare' },
					tls: { certManagerClusterIssuer: 'letsencrypt-prod' }
				}
			})
		);

		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({
			timeout: 5000
		});

		// Click "Add Provider" button
		await page.getByRole('button', { name: 'Add Provider' }).click();

		// Fill in the form
		await page.getByPlaceholder('github-main').fill('github-main');
		// Type is already 'github' by default — verify the select
		const typeSelect = page.locator('select').filter({ hasText: 'GitHub' });
		await expect(typeSelect).toBeVisible({ timeout: 3000 });

		// Fill host URL
		await page.getByPlaceholder('https://github.com').fill('https://github.com');

		// Fill OAuth credentials — labels lack `for` attributes so use adjacent sibling CSS
		const clientIdInput = page.locator('label:has-text("OAuth Client ID") + input');
		await clientIdInput.fill('test-id');
		const clientSecretInput = page.locator('label:has-text("OAuth Client Secret") + input');
		await clientSecretInput.fill('test-secret');

		// Click "Create" to submit
		await page.getByRole('button', { name: 'Create' }).click();

		// Provider should appear in the list after creation
		await expect(page.getByText('github-main')).toBeVisible({ timeout: 5000 });
	});

	test('Test 3: Admin can delete a git provider', async ({ page }) => {
		await setupBaseRoutes(page);

		let providersPayload = [mockGitProvider];
		await page.route('/api/gitproviders', async (r) => {
			await r.fulfill({ json: providersPayload });
		});
		await page.route('/api/platform', (r) =>
			r.fulfill({
				json: {
					domain: 'example.com',
					dns: { provider: 'cloudflare' },
					tls: { certManagerClusterIssuer: 'letsencrypt-prod' }
				}
			})
		);

		let deleteCalled = false;
		await page.route('/api/gitproviders/github-main', async (r) => {
			if (r.request().method() === 'DELETE') {
				deleteCalled = true;
				providersPayload = [];
				await r.fulfill({ status: 204, body: '' });
			}
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByText('github-main')).toBeVisible({ timeout: 5000 });

		// Override dialog confirmation (window.confirm) to return true
		page.on('dialog', (dialog) => dialog.accept());

		// Click the delete (Trash2) button — section#git-providers has "Add Provider" (nth 0)
		// then the trash icon button for the first provider (nth 1)
		await page.locator('section#git-providers').getByRole('button').nth(1).click();

		// Provider should be removed from the list
		await expect(page.getByText('github-main')).toHaveCount(0, { timeout: 5000 });
		expect(deleteCalled).toBe(true);
	});

	test('Test 4: Platform domain and TLS config can be updated', async ({ page }) => {
		await setupBaseRoutes(page);
		await page.route('/api/gitproviders', (r) => r.fulfill({ json: [] }));

		let patchCalled = false;
		let patchBody: Record<string, unknown> | null = null;

		await page.route('/api/platform', async (r) => {
			if (r.request().method() === 'GET') {
				await r.fulfill({
					json: {
						domain: 'example.com',
						dns: { provider: 'cloudflare' },
						tls: { certManagerClusterIssuer: 'letsencrypt-prod' }
					}
				});
			} else if (r.request().method() === 'PATCH') {
				patchCalled = true;
				patchBody = JSON.parse(r.request().postData() ?? '{}');
				await r.fulfill({
					json: {
						domain: 'newdomain.com',
						dns: { provider: 'cloudflare' },
						tls: { certManagerClusterIssuer: 'letsencrypt-prod' }
					}
				});
			}
		});

		await injectAuth(page, true);
		await page.goto('/admin/settings');

		await expect(page.getByRole('heading', { name: 'Platform Settings' })).toBeVisible({
			timeout: 5000
		});

		// Change the domain field
		const domainInput = page.getByPlaceholder('yourdomain.com');
		await domainInput.fill('newdomain.com');

		// Click Save (in the General section)
		await page.getByRole('button', { name: 'Save' }).first().click();

		// Verify PATCH was called
		await expect(async () => {
			expect(patchCalled).toBe(true);
		}).toPass({ timeout: 5000 });
	});
});
