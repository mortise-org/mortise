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
// Project settings E2E tests  (/projects/{project}/settings)
//
// Real backend only. No page.route() mocks.
//
// Tests cover:
//   - Viewing the project settings page structure (General section)
//   - Updating the project description via Save changes
//   - PR Environments section visibility, toggle, and source env selector
//   - Danger Zone tab (delete button disabled state)
//   - Tab navigation switching visible content panes
// ---------------------------------------------------------------------------

test.describe('project settings', () => {
	let adminToken: string;
	const projectName = `e2e-psettings-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		await createProjectViaAPI(request, adminToken, projectName, 'Initial description');
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, projectName);
	});

	test('project admin sees General section with description field and Save button', async ({
		page
	}) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);

		// Wait for the tabbed settings page to load (General tab is the default).
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		// Project name (read-only).
		const nameInput = page.locator('#proj-name');
		await expect(nameInput).toBeVisible();
		await expect(nameInput).toHaveValue(projectName);

		// Backing namespace is shown in Project Settings instead of the dashboard card.
		const namespaceInput = page.locator('#proj-namespace');
		await expect(namespaceInput).toBeVisible();
		await expect(namespaceInput).toHaveValue(`pj-${projectName}`);

		// Description input.
		const descInput = page.locator('input[placeholder="Optional description"]');
		await expect(descInput).toBeVisible();
		await expect(descInput).toHaveValue('Initial description');

		// Save button.
		await expect(page.getByRole('button', { name: 'Save changes' })).toBeVisible();
	});

	test('project admin updates the project description', async ({ page, request }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		const descInput = page.locator('input[placeholder="Optional description"]');
		await descInput.clear();
		await descInput.fill('Updated description for E2E test');

		await page.getByRole('button', { name: 'Save changes' }).click();

		// The button should complete (no spinner remaining or back to normal text).
		await expect(page.getByRole('button', { name: 'Save changes' })).toBeVisible({
			timeout: 5_000
		});

		// Verify the description persisted by reloading.
		await page.reload();
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});
		await expect(descInput).toHaveValue('Updated description for E2E test');

		// Verify via API as well.
		const res = await request.get(`/api/projects/${projectName}`, {
			headers: { Authorization: `Bearer ${adminToken}` }
		});
		expect(res.ok()).toBeTruthy();
		const body = await res.json();
		expect(body.description).toBe('Updated description for E2E test');
	});

	test('project admin sees PR Environments section with toggle', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		// PR Environments section (on the General tab, which is the default).
		await expect(page.getByRole('heading', { name: 'PR Environments' })).toBeVisible();
		await expect(page.getByText('Enable PR Environments')).toBeVisible();

		// The toggle switch for PR environments.
		const prToggle = page.getByRole('switch', { name: 'Toggle PR environments' });
		await expect(prToggle).toBeVisible();
	});

	test('project admin can enable PR environments and configure source env', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		// Enable PR environments.
		const prToggle = page.getByRole('switch', { name: 'Toggle PR environments' });
		await prToggle.click();

		// Source environment select should be visible.
		const sourceEnvSelect = page.locator('#pr-source-env');
		await expect(sourceEnvSelect).toBeVisible();
		await expect(sourceEnvSelect).toHaveValue('');

		// Save PR config.
		await page.getByRole('button', { name: 'Save PR config', exact: true }).click();
		await expect(page.getByRole('button', { name: 'Save PR config', exact: true })).toBeVisible({
			timeout: 5_000
		});

		// Reload and verify toggle state persisted.
		await page.reload();
		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});
		const toggleAfterReload = page.getByRole('switch', { name: 'Toggle PR environments' });
		await expect(toggleAfterReload).toHaveAttribute('aria-checked', 'true');
	});

	test('project admin sees Danger Zone with delete section', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);

		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		// Navigate to the Danger tab to see the danger zone content.
		await page.getByRole('button', { name: 'Danger', exact: true }).click();

		// Danger Zone section.
		await expect(page.getByText('Danger Zone')).toBeVisible();
		await expect(page.getByText('Delete Project', { exact: true })).toBeVisible();

		// Confirmation input placeholder is the project name.
		await expect(page.getByPlaceholder(projectName)).toBeVisible();

		// Delete button is disabled when input is empty.
		const deleteBtn = page.getByRole('button', { name: 'Delete project' });
		await expect(deleteBtn).toBeDisabled();
	});

	test('tab navigation switches visible content panes', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);

		await expect(page.getByRole('button', { name: 'General' })).toBeVisible({
			timeout: 10_000
		});

		// Default tab (General) shows the description field.
		await expect(page.locator('input[placeholder="Optional description"]')).toBeVisible();

		// Switch to Members tab and verify members content appears.
		await page.getByRole('button', { name: 'Members', exact: true }).click();
		await expect(page.getByPlaceholder('username')).toBeVisible({ timeout: 5_000 });

		// Switch back to General and verify general content reappears.
		await page.getByRole('button', { name: 'General' }).click();
		await expect(page.locator('input[placeholder="Optional description"]')).toBeVisible();
	});
});
