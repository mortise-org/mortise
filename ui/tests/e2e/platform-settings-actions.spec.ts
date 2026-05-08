/**
 * Platform settings action tests: admin save actions at /settings.
 *
 * Real backend only. No page.route() mocks.
 * Tests are fully independent; each navigates fresh and verifies via API.
 */
import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix
} from './helpers';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Read the current platform config via API. */
async function getPlatformViaAPI(
	request: import('@playwright/test').APIRequestContext,
	token: string
): Promise<Record<string, unknown>> {
	const res = await request.get('/api/platform', {
		headers: { Authorization: `Bearer ${token}` }
	});
	if (!res.ok()) {
		throw new Error(`getPlatform failed: HTTP ${res.status()}`);
	}
	return (await res.json()) as Record<string, unknown>;
}

/** Save a platform config field via API (for setup/restore). */
async function patchPlatformViaAPI(
	request: import('@playwright/test').APIRequestContext,
	token: string,
	data: Record<string, unknown>
): Promise<void> {
	const res = await request.patch('/api/platform', {
		headers: { Authorization: `Bearer ${token}` },
		data
	});
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`patchPlatform failed: HTTP ${res.status()} ${body}`);
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('platform settings actions', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('admin can update platform domain and verify via API', async ({ page, request }) => {
		// Read original domain to restore later.
		const original = await getPlatformViaAPI(request, adminToken);
		const originalDomain = (original.domain as string) ?? '';

		const testDomain = `e2e-${randomSuffix()}.example.com`;

		await injectToken(page, adminToken);
		await page.goto('/settings');
		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const domainInput = page.locator('#platform-domain');
		await domainInput.scrollIntoViewIfNeeded();
		await domainInput.clear();
		await domainInput.fill(testDomain);

		// Save button in General section.
		await page.locator('section#general').getByRole('button', { name: 'Save', exact: true }).click();

		// Wait for save to complete (button text returns to "Save").
		await expect(
			page.locator('section#general').getByRole('button', { name: 'Save', exact: true })
		).toBeVisible({ timeout: 5_000 });

		// Verify via API (retry in case of concurrent platform config reconciliation).
		await expect(async () => {
			const updated = await getPlatformViaAPI(request, adminToken);
			expect(updated.domain).toBe(testDomain);
		}).toPass({ timeout: 10_000 });

		// Restore original domain with retry for conflict.
		await expect(async () => {
			await patchPlatformViaAPI(request, adminToken, { domain: originalDomain });
		}).toPass({ timeout: 10_000 });
	});

	test('admin can save registry config and verify via API', async ({ page, request }) => {
		const testUrl = `registry-${randomSuffix()}.example.com`;

		await injectToken(page, adminToken);
		await page.goto('/settings');
		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const regUrlInput = page.locator('#reg-url');
		await regUrlInput.scrollIntoViewIfNeeded();
		await regUrlInput.clear();
		await regUrlInput.fill(testUrl);

		await page.getByRole('button', { name: 'Save registry config', exact: true }).click();

		// Wait for save to complete.
		await expect(
			page.getByRole('button', { name: 'Save registry config', exact: true })
		).toBeVisible({ timeout: 5_000 });

		// Verify via API (retry in case of concurrent platform config reconciliation).
		await expect(async () => {
			const updated = await getPlatformViaAPI(request, adminToken);
			const registry = updated.registry as Record<string, unknown> | undefined;
			expect(registry?.url).toBe(testUrl);
		}).toPass({ timeout: 10_000 });

		// Restore by clearing the registry URL with retry for conflict.
		await expect(async () => {
			await patchPlatformViaAPI(request, adminToken, { registry: { url: '' } });
		}).toPass({ timeout: 10_000 });
	});

	test('admin can save build config and verify via API', async ({ page, request }) => {
		const testAddr = `tcp://buildkitd-${randomSuffix()}.mortise-system:1234`;

		await injectToken(page, adminToken);
		await page.goto('/settings');
		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const bkAddrInput = page.locator('#bk-addr');
		await bkAddrInput.scrollIntoViewIfNeeded();
		await bkAddrInput.clear();
		await bkAddrInput.fill(testAddr);

		await page.getByRole('button', { name: 'Save build config', exact: true }).click();

		// Wait for save to complete.
		await expect(
			page.getByRole('button', { name: 'Save build config', exact: true })
		).toBeVisible({ timeout: 5_000 });

		// Verify via API (retry in case of concurrent platform config reconciliation).
		await expect(async () => {
			const updated = await getPlatformViaAPI(request, adminToken);
			const build = updated.build as Record<string, unknown> | undefined;
			expect(build?.buildkitAddr).toBe(testAddr);
		}).toPass({ timeout: 10_000 });

		// Restore by clearing with retry for conflict.
		await expect(async () => {
			await patchPlatformViaAPI(request, adminToken, { build: { buildkitAddr: '' } });
		}).toPass({ timeout: 10_000 });
	});

	test('admin can save TLS cluster issuer and verify via API', async ({ page, request }) => {
		const original = await getPlatformViaAPI(request, adminToken);
		const originalTls = original.tls as Record<string, unknown> | undefined;
		const originalIssuer = (originalTls?.certManagerClusterIssuer as string) ?? '';

		const testIssuer = `e2e-issuer-${randomSuffix()}`;

		// The save may fail silently due to resource version conflicts.
		// Retry the entire fill+save+verify flow.
		await expect(async () => {
			await injectToken(page, adminToken);
			await page.goto('/settings');
			await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

			const tlsIssuerInput = page.locator('#tls-issuer');
			await tlsIssuerInput.scrollIntoViewIfNeeded();
			await tlsIssuerInput.clear();
			await tlsIssuerInput.fill(testIssuer);

			await page.getByRole('button', { name: 'Save TLS config', exact: true }).click();
			await expect(
				page.getByRole('button', { name: 'Save TLS config', exact: true })
			).toBeVisible({ timeout: 5_000 });

			await new Promise(r => setTimeout(r, 1_000));
			const updated = await getPlatformViaAPI(request, adminToken);
			const tls = updated.tls as Record<string, unknown>;
			expect(tls.certManagerClusterIssuer).toBe(testIssuer);
		}).toPass({ timeout: 20_000, intervals: [2_000, 3_000, 5_000] });

		// Restore original issuer with retry for conflict.
		await expect(async () => {
			await patchPlatformViaAPI(request, adminToken, {
				tls: { certManagerClusterIssuer: originalIssuer }
			});
		}).toPass({ timeout: 10_000 });
	});

	test('admin can save storage config and verify via API', async ({ page, request }) => {
		const original = await getPlatformViaAPI(request, adminToken);
		const originalStorage = original.storage as Record<string, unknown> | undefined;
		const originalClass = (originalStorage?.defaultStorageClass as string) ?? '';

		const testClass = `e2e-sc-${randomSuffix()}`;

		await injectToken(page, adminToken);
		await page.goto('/settings');
		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		const storageInput = page.locator('#storage-class');
		await storageInput.scrollIntoViewIfNeeded();
		await storageInput.clear();
		await storageInput.fill(testClass);

		await page.getByRole('button', { name: 'Save storage config', exact: true }).click();

		// Wait for save to complete.
		await expect(
			page.getByRole('button', { name: 'Save storage config', exact: true })
		).toBeVisible({ timeout: 5_000 });

		// Verify via API (retry in case of concurrent platform config reconciliation).
		await expect(async () => {
			const updated = await getPlatformViaAPI(request, adminToken);
			const storage = updated.storage as Record<string, unknown>;
			expect(storage.defaultStorageClass).toBe(testClass);
		}).toPass({ timeout: 10_000 });

		// Restore with retry for conflict.
		await expect(async () => {
			await patchPlatformViaAPI(request, adminToken, {
				storage: { defaultStorageClass: originalClass }
			});
		}).toPass({ timeout: 10_000 });
	});

	test('filter input narrows visible sections (type "registry")', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/settings');
		await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible({ timeout: 10_000 });

		// Verify General section is visible before filtering.
		await expect(page.locator('section#general')).toBeVisible();

		const filterInput = page.getByPlaceholder('Filter settings...');
		await filterInput.fill('registry');

		// Registry section should still be visible.
		await expect(page.locator('section#registry')).toBeVisible({ timeout: 3_000 });

		// General section should be hidden (filtered out).
		await expect(page.locator('section#general h2')).not.toBeVisible();
	});

	test('settings link hidden for non-admin users', async ({ page, request }) => {
		// Create a non-admin user.
		const memberEmail = `e2e-nonadmin-${randomSuffix()}@test.local`;
		const createRes = await request.post('/api/admin/users', {
			headers: { Authorization: `Bearer ${adminToken}` },
			data: { email: memberEmail, password: 'testpassword123', role: 'member' }
		});
		if (!createRes.ok() && createRes.status() !== 409) {
			throw new Error(`create user failed: HTTP ${createRes.status()}`);
		}

		// Login as the non-admin user.
		const memberRes = await request.post('/api/auth/login', {
			data: { email: memberEmail, password: 'testpassword123' }
		});
		expect(memberRes.ok()).toBeTruthy();
		const memberBody = await memberRes.json();
		const memberToken = memberBody.token as string;

		await injectToken(page, memberToken);
		await page.goto('/');

		// Wait for the page to load with user context.
		await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible({ timeout: 15_000 });

		// Left rail Settings icon should not be visible for non-admin members.
		await expect(page.getByTitle('Settings', { exact: true })).not.toBeVisible({ timeout: 3_000 });

		// Cleanup: delete the member user.
		await request
			.delete(`/api/admin/users/${encodeURIComponent(memberEmail)}`, {
				headers: { Authorization: `Bearer ${adminToken}` },
				failOnStatusCode: false
			})
			.catch(() => {});
	});
});
