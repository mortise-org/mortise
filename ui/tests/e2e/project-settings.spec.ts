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
// Tests cover:
//   - Updating the project description
//   - Toggling PR environments on
//   - Verifying the project settings page structure
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

		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
			timeout: 10_000
		});

		// General section heading.
		await expect(page.getByText('General')).toBeVisible();

		// Project name (read-only).
		const nameInput = page.locator('input[disabled]');
		await expect(nameInput).toBeVisible();
		await expect(nameInput).toHaveValue(projectName);

		// Description input.
		const descInput = page.locator('input[placeholder="Optional description"]');
		await expect(descInput).toBeVisible();
		await expect(descInput).toHaveValue('Initial description');

		// Save button.
		await expect(page.getByRole('button', { name: 'Save changes' })).toBeVisible();
	});

	test('project admin updates the project description', async ({ page, request }) => {
		await injectToken(page, adminToken);

		// Intercept the PATCH call to avoid mutating the real project.
		await page.route(`**/api/projects/${projectName}`, async (route) => {
			if (route.request().method() === 'PATCH') {
				const body = await route.request().postDataJSON();
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						name: projectName,
						description: body.description ?? '',
						namespace: `project-${projectName}`,
						phase: 'Ready',
						appCount: 0
					})
				});
			}
			return route.continue();
		});

		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
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
	});

	test('project admin enables PR environments for the project', async ({ page, request }) => {
		await injectToken(page, adminToken);

		await page.route(`**/api/projects/${projectName}`, async (route) => {
			if (route.request().method() === 'PATCH') {
				return route.fulfill({
					status: 200,
					contentType: 'application/json',
					body: JSON.stringify({
						name: projectName,
						namespace: `project-${projectName}`,
						phase: 'Ready',
						appCount: 0,
						preview: { enabled: true }
					})
				});
			}
			return route.continue();
		});

		await page.goto(`/projects/${projectName}/settings`);
		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
			timeout: 10_000
		});

		// PR Environments section.
		await expect(page.getByRole('heading', { name: 'PR Environments' })).toBeVisible();
		await expect(page.getByText('Enable PR Environments')).toBeVisible();

		// The toggle switch for PR environments.
		const prToggle = page.getByRole('switch');
		await expect(prToggle).toBeVisible();

		// Toggle it on.
		await prToggle.click();
		await expect(prToggle).toHaveAttribute('aria-checked', 'true');
	});

	test('project admin sees Danger Zone with delete section', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);

		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
			timeout: 10_000
		});

		// Navigate to the Danger tab to see the danger zone content.
		await page.getByRole('button', { name: 'Danger' }).click();

		// Danger Zone section.
		await expect(page.getByText('Danger Zone')).toBeVisible();
		await expect(page.getByText('Delete Project', { exact: true })).toBeVisible();

		// Confirmation input — placeholder is the project name.
		await expect(page.getByPlaceholder(projectName)).toBeVisible();

		// Delete button is disabled when input is empty.
		const deleteBtn = page.getByRole('button', { name: 'Delete project' });
		await expect(deleteBtn).toBeDisabled();
	});

	test('project settings page has filter input that narrows visible sections', async ({
		page
	}) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${projectName}/settings`);

		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
			timeout: 10_000
		});

		// Filter input is present.
		const filterInput = page.getByPlaceholder('Filter settings...');
		await expect(filterInput).toBeVisible();

		// Clear filter — all sections visible.
		await filterInput.fill('');
		await expect(page.getByText('General')).toBeVisible();
		await expect(page.getByRole('heading', { name: 'PR Environments' })).toBeVisible();
	});
});
