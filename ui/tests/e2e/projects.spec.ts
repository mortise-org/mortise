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
// Assumes an operator is reachable at MORTISE_BASE_URL and admin credentials
// are supplied via MORTISE_ADMIN_EMAIL / MORTISE_ADMIN_PASSWORD.

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

	test('dashboard renders projects heading and default project', async ({ page }) => {
		await loginViaUI(page);

		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();

		// The "default" project is auto-created during first setup.
		const defaultCard = page.getByRole('link').filter({ hasText: 'default' });
		await expect(defaultCard).toBeVisible({ timeout: 10_000 });
	});

	test('create project via UI', async ({ page, request }) => {
		const name = `e2e-proj-${randomSuffix()}`;
		projectsToCleanup.push(name);

		await loginViaUI(page);
		await page.getByRole('link', { name: 'New project' }).click();
		await expect(page).toHaveURL('/projects/new');

		await expect(page.getByRole('heading', { name: 'New project' })).toBeVisible();

		await page.getByLabel('Name').fill(name);
		await page.getByLabel('Description').fill('E2E test project');

		// Verify namespace preview updates.
		await expect(page.getByText(`project-${name}`)).toBeVisible();

		await page.getByRole('button', { name: 'Create project' }).click();

		// Should redirect to the new project's detail page.
		await expect(page).toHaveURL(`/projects/${name}`, { timeout: 10_000 });
		await expect(page.getByRole('heading', { name })).toBeVisible();
	});

	test('project name validation rejects invalid names', async ({ page }) => {
		await loginViaUI(page);
		await page.goto('/projects/new');

		const nameInput = page.getByLabel('Name');
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

	test('project detail page loads with breadcrumbs and deploy button', async ({
		page,
		request
	}) => {
		const name = `e2e-detail-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name, 'Detail test');

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);

		// Breadcrumbs: "Projects" link and namespace.
		await expect(page.getByRole('link', { name: 'Projects' })).toBeVisible();
		await expect(page.getByText(`project-${name}`)).toBeVisible();

		// Project name heading.
		await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

		// "Deploy app" link.
		await expect(page.getByRole('link', { name: 'Deploy app' })).toBeVisible();

		// "Delete project" button.
		await expect(page.getByRole('button', { name: 'Delete project' })).toBeVisible();

		// Empty state for apps.
		await expect(page.getByText('No apps in this project')).toBeVisible();
		await expect(page.getByRole('link', { name: 'Deploy an app' })).toBeVisible();
	});

	test('delete project via UI', async ({ page, request }) => {
		const name = `e2e-del-${randomSuffix()}`;
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto(`/projects/${name}`);

		// Wait for the page to load.
		await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

		// The delete button triggers a prompt() dialog where the user must type
		// the project name to confirm.
		page.once('dialog', async (dialog) => {
			expect(dialog.type()).toBe('prompt');
			await dialog.accept(name);
		});

		await page.getByRole('button', { name: 'Delete project' }).click();

		// Should redirect back to the dashboard.
		await expect(page).toHaveURL('/', { timeout: 10_000 });

		// The deleted project should no longer appear.
		await expect(page.getByRole('link').filter({ hasText: name })).toHaveCount(0, {
			timeout: 5_000
		});

		// No cleanup needed -- project is already deleted.
	});

	test('project card links to detail page', async ({ page, request }) => {
		const name = `e2e-link-${randomSuffix()}`;
		projectsToCleanup.push(name);
		await createProjectViaAPI(request, adminToken, name);

		await injectToken(page, adminToken);
		await page.goto('/');

		// Find the card for our project and click it.
		const card = page.getByRole('link').filter({ hasText: name });
		await expect(card).toBeVisible({ timeout: 10_000 });
		await card.click();

		await expect(page).toHaveURL(`/projects/${name}`);
		await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });
	});

	test('new project cancel navigates back to dashboard', async ({ page }) => {
		await loginViaUI(page);
		await page.goto('/projects/new');

		await expect(page.getByRole('heading', { name: 'New project' })).toBeVisible();

		// Click the "Cancel" link.
		await page.getByRole('link', { name: 'Cancel' }).click();

		await expect(page).toHaveURL('/');
		await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();
	});
});
