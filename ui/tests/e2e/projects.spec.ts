import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	loginViaUI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	deleteProjectViaAPI
} from './helpers';

// End-to-end tests for the Projects CRUD UI.
//
// New UI: project list is the dashboard (/), projects are cards linking to
// /projects/{name} (canvas view). Project creation is at /projects/new.
// Project deletion is via the project settings page (/projects/{name}/settings).

test.describe('projects', () => {
	let adminToken: string;
	const projectsToCleanup: string[] = [];

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
	});

	test.afterEach(async ({ request }) => {
		// Best-effort cleanup of any projects created during the test.
		while (projectsToCleanup.length > 0) {
			const name = projectsToCleanup.pop()!;
			await deleteProjectViaAPI(request, adminToken, name);
		}
	});

	test('dashboard renders Projects heading and default project', async ({ page }) => {
		await loginViaUI(page);

		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();

		// The "default" project is auto-created during first setup.
		const defaultCard = page.locator('a').filter({ hasText: 'default' });
		await expect(defaultCard).toBeVisible({ timeout: 10_000 });
	});

	test('create project via UI', async ({ page, request }) => {
		const name = `e2e-proj-${randomSuffix()}`;
		projectsToCleanup.push(name);

		await loginViaUI(page);

		// "+ New Project" button is in the top-right of the dashboard.
		await page.getByRole('link', { name: 'New Project' }).click();
		await expect(page).toHaveURL('/projects/new');

		await expect(page.getByRole('heading', { name: 'New Project' })).toBeVisible();

		await page.getByLabel('Project name').fill(name);
		await page.getByLabel('Description').fill('E2E test project');

		// Namespace preview updates in the helper text.
		await expect(page.getByText(`project-${name}`)).toBeVisible();

		await page.getByRole('button', { name: 'Create project' }).click();

		// Should redirect to the new project's canvas page.
		await expect(page).toHaveURL(`/projects/${name}`, { timeout: 10_000 });
	});

	test('project name validation rejects invalid names', async ({ page }) => {
		await loginViaUI(page);
		await page.goto('/projects/new');

		const nameInput = page.getByLabel('Project name');
		const submitButton = page.getByRole('button', { name: 'Create project' });
		const validationError = page.getByText(
			'Project name must be 1-63 lowercase letters, digits, or hyphens, starting and ending with alphanumeric.'
		);

		// Uppercase letters
		await nameInput.fill('MyProject');
		await submitButton.click();
		await expect(validationError).toBeVisible();

		// Starts with hyphen
		await nameInput.fill('-bad-name');
		await submitButton.click();
		await expect(validationError).toBeVisible();

		// Special characters
		await nameInput.fill('no_underscores');
		await submitButton.click();
		await expect(validationError).toBeVisible();
	});

	test('project canvas page loads with toolbar and breadcrumb', async ({
		page,
		request
	}) => {
		const name = `e2e-detail-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name, 'Detail test');

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);

		// Toolbar breadcrumb: "Projects" link and project name.
		await expect(page.getByRole('link', { name: 'Projects' })).toBeVisible({ timeout: 10_000 });
		await expect(page.getByText(name, { exact: false }).first()).toBeVisible();

		// "+ Add" button in the toolbar (it's a button, not a link).
		await expect(page.getByRole('button', { name: 'Add', exact: false }).first()).toBeVisible();

		// View toggle buttons.
		await expect(page.getByTitle('Canvas view')).toBeVisible();
		await expect(page.getByTitle('List view')).toBeVisible();
	});

	test('list view shows empty state with Deploy an app link', async ({ page, request }) => {
		const name = `e2e-empty-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);

		// Switch to list view to see the empty state message.
		await page.getByTitle('List view').click();

		await expect(page.getByText('No apps in this project')).toBeVisible({ timeout: 10_000 });
		// "Deploy an app" is a button (not a link) that opens the new app modal.
		await expect(page.getByRole('button', { name: 'Deploy an app' })).toBeVisible();
	});

	test('project settings page renders with delete section', async ({ page, request }) => {
		const name = `e2e-settdet-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name, 'Settings test');

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}/settings`);

		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
			timeout: 10_000
		});

		// Navigate to the Danger tab to see the delete section.
		await page.getByRole('button', { name: 'Danger' }).click();
		await expect(page.getByText('Delete Project', { exact: true })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Delete project' })).toBeVisible();
	});

	test('delete project via project settings UI', async ({ page, request }) => {
		const name = `e2e-del-${randomSuffix()}`;
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}/settings`);

		// Wait for the page to load.
		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
			timeout: 10_000
		});

		// Navigate to the Danger tab.
		await page.getByRole('button', { name: 'Danger' }).click();

		// The delete button is disabled until the user types the project name.
		const deleteBtn = page.getByRole('button', { name: 'Delete project' });
		await expect(deleteBtn).toBeDisabled();

		// Type the project name into the confirmation input.
		await page.getByPlaceholder(name).fill(name);
		await expect(deleteBtn).toBeEnabled();
		await deleteBtn.click();

		// Should redirect back to the dashboard.
		await expect(page).toHaveURL('/', { timeout: 10_000 });

		// The deleted project should no longer appear. Project deletion in
		// Kubernetes may take several seconds to propagate, so allow a longer
		// timeout and reload the list.
		await expect(async () => {
			await page.reload();
			await expect(page.locator('a').filter({ hasText: name })).toHaveCount(0);
		}).toPass({ timeout: 15_000 });

		// No cleanup needed — project is already deleted.
	});

	test('project card links to canvas page', async ({ page, request }) => {
		const name = `e2e-link-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto('/');

		// Find the card for our project and click it.
		const card = page.locator('a').filter({ hasText: name });
		await expect(card).toBeVisible({ timeout: 10_000 });
		await card.click();

		await expect(page).toHaveURL(`/projects/${name}`);
	});

	test('new project cancel navigates back to dashboard', async ({ page }) => {
		await loginViaUI(page);
		await page.goto('/projects/new');

		await expect(page.getByRole('heading', { name: 'New Project' })).toBeVisible();

		// Click the "Cancel" link.
		await page.getByRole('link', { name: 'Cancel' }).click();

		await expect(page).toHaveURL('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();
	});

	test('back to projects link on new project page', async ({ page }) => {
		await loginViaUI(page);
		await page.goto('/projects/new');

		await expect(page.getByRole('heading', { name: 'New Project' })).toBeVisible();

		// "← Back to Projects" link.
		await page.getByRole('link', { name: /Back to Projects/ }).click();

		await expect(page).toHaveURL('/');
	});
});
