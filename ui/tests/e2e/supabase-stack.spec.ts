/**
 * Supabase stack creation — NewAppModal loading-state + filtered-services tests.
 *
 * All API calls mocked via page.route(). No live backend.
 * Auth injected via localStorage before navigation.
 *
 * Covers:
 *  - "Create Supabase Stack" button is disabled while /api/templates loads
 *    and becomes enabled once the fetch resolves.
 *  - Unchecking an optional service sends the correctly filtered `services`
 *    array to POST /api/projects/{p}/stacks.
 */
import { test, expect, type Page } from '@playwright/test';

const mockProject = {
	name: 'my-project',
	namespace: 'project-my-project',
	phase: 'Ready' as const,
	appCount: 0,
	description: ''
};

const mockTemplates = [
	{
		name: 'supabase',
		description: 'Self-hosted Supabase',
		services: [
			{ name: 'postgres', description: 'PostgreSQL database', image: 'supabase/postgres', required: true },
			{ name: 'auth', description: 'GoTrue authentication', image: 'supabase/gotrue', required: false },
			{ name: 'rest', description: 'PostgREST API', image: 'postgrest/postgrest', required: false }
		]
	}
];

async function injectAuth(page: Page, isAdmin = true) {
	await page.goto('/');
	await page.evaluate(({ isAdmin }) => {
		localStorage.setItem('mortise_token', 'test-token');
		localStorage.setItem(
			'mortise_user',
			JSON.stringify({ email: 'admin@example.com', role: isAdmin ? 'admin' : 'member' })
		);
	}, { isAdmin });
}

async function setupCommonMocks(page: Page) {
	await page.route('/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
	await page.route('/api/projects', (r) => r.fulfill({ json: [mockProject] }));
	await page.route('/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
	await page.route('/api/projects/my-project/apps', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/activity', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/previews', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/members', (r) => r.fulfill({ json: [] }));
	await page.route('/api/gitproviders', (r) => r.fulfill({ json: [] }));
	await page.route('/api/platform', (r) =>
		r.fulfill({ json: { domain: 'example.com', dns: { provider: 'cloudflare' }, tls: {} } })
	);
	await page.route('/api/auth/git/github/status', (r) =>
		r.fulfill({ json: { connected: false } })
	);
}

/** Open the NewAppModal and pick the Supabase source type. */
async function openSupabase(page: Page) {
	await page.goto('/projects/my-project');
	await page.getByRole('button', { name: 'Add', exact: true }).click();
	await expect(
		page.getByRole('heading', { name: 'What would you like to create?' })
	).toBeVisible({ timeout: 5000 });
	await page.getByText('Supabase', { exact: false }).first().click();
}

test.describe('NewAppModal — Supabase stack', () => {
	test('Create Supabase Stack is disabled while templates are loading', async ({ page }) => {
		await setupCommonMocks(page);

		// Delay the /api/templates fetch so we can observe the loading state.
		let releaseTemplates: (() => void) | null = null;
		const templatesReady = new Promise<void>((resolve) => {
			releaseTemplates = resolve;
		});
		await page.route('/api/templates', async (route) => {
			await templatesReady;
			await route.fulfill({ json: mockTemplates });
		});

		await injectAuth(page);
		await openSupabase(page);

		// While the templates request is pending, the button should be disabled
		// and show the loading label.
		const createBtn = page.getByRole('button', { name: 'Loading services...', exact: true });
		await expect(createBtn).toBeVisible({ timeout: 3000 });
		await expect(createBtn).toBeDisabled();

		// Release the /api/templates response.
		releaseTemplates!();

		// The button flips to "Create Supabase Stack" and becomes enabled.
		const readyBtn = page.getByRole('button', { name: 'Create Supabase Stack', exact: true });
		await expect(readyBtn).toBeVisible({ timeout: 5000 });
		await expect(readyBtn).toBeEnabled();
	});

	test('Create Supabase Stack POSTs only the selected services', async ({ page }) => {
		await setupCommonMocks(page);
		await page.route('/api/templates', (r) => r.fulfill({ json: mockTemplates }));

		let postBody: { template?: string; services?: string[] } | null = null;
		await page.route('/api/projects/my-project/stacks', async (route) => {
			if (route.request().method() === 'POST') {
				postBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ json: { apps: ['supabase-postgres', 'supabase-auth'] } });
			}
			return route.fulfill({ status: 405, json: { error: 'method not allowed' } });
		});

		await injectAuth(page);
		await openSupabase(page);

		// Wait for services to render.
		await expect(page.getByText('GoTrue authentication')).toBeVisible({ timeout: 5000 });
		await expect(page.getByText('PostgREST API')).toBeVisible();

		// Uncheck "rest". The label wraps the checkbox, so scope to the row
		// containing the "PostgREST API" description and toggle its checkbox.
		const restRow = page
			.locator('label')
			.filter({ hasText: 'PostgREST API' });
		await restRow.locator('input[type="checkbox"]').uncheck();

		// Click the now-ready Create button.
		await page.getByRole('button', { name: 'Create Supabase Stack', exact: true }).click();

		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		expect(postBody!.template).toBe('supabase');
		// services is only sent when a subset is selected; it must include
		// postgres + auth and must NOT include rest.
		expect(postBody!.services).toEqual(expect.arrayContaining(['postgres', 'auth']));
		expect(postBody!.services).not.toContain('rest');
		expect(postBody!.services).toHaveLength(2);
	});
});
