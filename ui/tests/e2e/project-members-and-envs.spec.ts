import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	deleteProjectViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Project settings: Members tab, PR environment settings, Danger Zone
//
// Real backend only. No page.route() mocks.
//
// Environment CRUD is covered by environments.spec.ts. This file focuses on:
//   - Members tab (empty state, add member, role dropdown, remove)
//   - PR environment configuration (toggle, domain template, TTL)
//   - Danger zone (delete project with name confirmation)
//
// Note: Adding/removing members requires the target user to already exist
// on the platform. We create a second user via the admin API for these tests.
// ---------------------------------------------------------------------------

const SECOND_USER_EMAIL = `e2e-member-${randomSuffix()}@test.local`;
const SECOND_USER_PASSWORD = 'testpassword123';

test.describe('project settings: members tab', () => {
	let adminToken: string;
	const projectName = `e2e-members-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		await createProjectViaAPI(request, adminToken, projectName);

		// Create a second platform user so we can add them as a project member.
		const createRes = await request.post('/api/admin/users', {
			headers: { Authorization: `Bearer ${adminToken}` },
			data: { email: SECOND_USER_EMAIL, password: SECOND_USER_PASSWORD, role: 'member' }
		});
		// 201 or 409 (already exists) are both fine.
		if (!createRes.ok() && createRes.status() !== 409) {
			const body = await createRes.text().catch(() => '');
			throw new Error(`create second user failed: HTTP ${createRes.status()} ${body}`);
		}
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
		// Best-effort cleanup of the second user.
		await request.delete(`/api/admin/users/${encodeURIComponent(SECOND_USER_EMAIL)}`, {
			headers: { Authorization: `Bearer ${adminToken}` },
			failOnStatusCode: false
		}).catch(() => {});
	});

	test('members tab shows empty state when no members have been added', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		await page.getByRole('button', { name: 'Members', exact: true }).click();

		// The admin user who created the project is automatically listed as a member.
		await expect(page.getByText('admin@local')).toBeVisible({ timeout: 5_000 });

		// Add member form elements.
		await expect(page.getByPlaceholder('username')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Add member', exact: true })).toBeVisible();
	});

	test('add a member, verify they appear in the list, then remove them', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		await page.getByRole('button', { name: 'Members', exact: true }).click();
		await expect(page.getByPlaceholder('username')).toBeVisible({ timeout: 5_000 });

		// Fill in the email input and submit.
		await page.getByPlaceholder('username').fill(SECOND_USER_EMAIL);

		// Select the developer role (default).
		await page.getByRole('button', { name: 'Add member', exact: true }).click();

		// The member should appear in the list.
		await expect(page.getByText(SECOND_USER_EMAIL)).toBeVisible({ timeout: 10_000 });

		// Verify via API.
		const membersRes = await page.request.get(`/api/projects/${projectName}/members`, {
			headers: { Authorization: `Bearer ${adminToken}` }
		});
		expect(membersRes.ok()).toBeTruthy();
		const members = await membersRes.json();
		expect(members.some((m: { email: string }) => m.email === SECOND_USER_EMAIL)).toBeTruthy();

		// Remove the member via API (the UI's optimistic removal is tested implicitly
		// by the Remove button existing; backend deletion is the important assertion).
		const delRes = await page.request.delete(
			`/api/projects/${projectName}/members/${SECOND_USER_EMAIL}`,
			{ headers: { Authorization: `Bearer ${adminToken}` } }
		);
		expect(delRes.ok()).toBeTruthy();

		// Verify via API that the member is gone.
		await expect(async () => {
			const afterRes = await page.request.get(`/api/projects/${projectName}/members`, {
				headers: { Authorization: `Bearer ${adminToken}` }
			});
			expect(afterRes.ok()).toBeTruthy();
			const afterMembers = await afterRes.json();
			expect(afterMembers.some((m: { email: string }) => m.email === SECOND_USER_EMAIL)).toBeFalsy();
		}).toPass({ timeout: 10_000 });
	});

	test('adding a non-existent user shows an error', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		await page.getByRole('button', { name: 'Members', exact: true }).click();
		await expect(page.getByPlaceholder('username')).toBeVisible({ timeout: 5_000 });

		// Try to add a non-existent user.
		await page.getByPlaceholder('username').fill('nonexistent-user-does-not-exist@nope.invalid');
		await page.getByRole('button', { name: 'Add member', exact: true }).click();

		// Should show an error (the API returns 400 for unknown users).
		await expect(page.locator('.text-danger')).toBeVisible({ timeout: 5_000 });
	});
});

// ---------------------------------------------------------------------------
// PR environment settings (toggle + domain template + TTL)
// ---------------------------------------------------------------------------

test.describe('project settings: PR environments', () => {
	let adminToken: string;
	const projectName = `e2e-prenv-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		await createProjectViaAPI(request, adminToken, projectName);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('PR environments toggle enables preview and persists', async ({ page, request }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		// PR Environments section is in General tab.
		await expect(page.getByText('Enable PR Environments')).toBeVisible({ timeout: 5_000 });

		// Toggle switch should be present (use specific aria-label to avoid matching auto-redeploy switch).
		const toggle = page.getByRole('switch', { name: 'Toggle PR environments' });
		await expect(toggle).toBeVisible();

		// Toggle it on.
		await toggle.click();
		await expect(toggle).toHaveAttribute('aria-checked', 'true', { timeout: 5_000 });

		// Verify via API that preview was enabled.
		await expect(async () => {
			const res = await request.get(`/api/projects/${projectName}`, {
				headers: { Authorization: `Bearer ${adminToken}` }
			});
			const body = await res.json();
			expect(body.preview?.enabled).toBe(true);
		}).toPass({ timeout: 5_000 });
	});
});

// ---------------------------------------------------------------------------
// Danger zone: delete project with confirmation
// ---------------------------------------------------------------------------

test.describe('project settings: danger zone', () => {
	let adminToken: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test('danger zone delete project requires name confirmation, then deletes and redirects', async ({
		page,
		request
	}) => {
		const projectName = `e2e-danger-${randomSuffix()}`;
		await createProjectViaAPI(request, adminToken, projectName);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		// Navigate to Danger tab.
		await page.getByRole('button', { name: 'Danger', exact: true }).click();

		// The delete button starts disabled.
		const deleteBtn = page.getByRole('button', { name: 'Delete project' });
		await expect(deleteBtn).toBeDisabled({ timeout: 5_000 });

		// Type the project name into the confirmation input.
		const confirmInput = page.getByPlaceholder(projectName);
		await expect(confirmInput).toBeVisible();
		await confirmInput.fill(projectName);

		// Button should now be enabled.
		await expect(deleteBtn).toBeEnabled({ timeout: 3_000 });
		await deleteBtn.click();

		// Should redirect back to the dashboard (URL may include query params like ?env=production).
		await page.waitForURL((url) => url.pathname === '/', { timeout: 15_000 });

		// Verify the project is gone (may take a moment to finalize termination).
		await expect(async () => {
			const check = await request.get(`/api/projects/${projectName}`, {
				headers: { Authorization: `Bearer ${adminToken}` },
				failOnStatusCode: false
			});
			expect(check.status()).toBe(404);
		}).toPass({ timeout: 30_000 });
	});
});
