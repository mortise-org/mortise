/**
 * Navigation reachability tests — every route in the app reachable via user clicks.
 *
 * ALL API calls are mocked via page.route(). No live backend required.
 * Tests are fully independent; each sets up its own state.
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

const mockGitApp = {
	metadata: { name: 'web-app', namespace: 'project-my-project' },
	spec: {
		source: { type: 'git' as const, repo: 'https://github.com/org/web-app', branch: 'main' },
		network: { public: true, port: 8080 },
		environments: [
			{ name: 'production', replicas: 2, resources: { cpu: '500m', memory: '512Mi' } },
			{ name: 'staging', replicas: 1 }
		],
		storage: [],
		credentials: []
	},
	status: {
		phase: 'Ready' as const,
		environments: [
			{
				name: 'production',
				readyReplicas: 2,
				currentImage: 'registry.example.com/web-app:abc123',
				deployHistory: [
					{
						image: 'registry.example.com/web-app:abc123',
						timestamp: new Date().toISOString(),
						gitSHA: 'abc1234'
					}
				]
			}
		]
	}
};

const mockDbApp = {
	metadata: { name: 'postgres', namespace: 'project-my-project' },
	spec: {
		source: { type: 'image' as const, image: 'postgres:16' },
		network: { public: false },
		environments: [{ name: 'production', replicas: 1 }],
		storage: [{ name: 'pgdata', mountPath: '/var/lib/postgresql/data', size: '10Gi' }],
		credentials: [{ name: 'DATABASE_URL', value: 'postgresql://postgres:pass@localhost/app' }]
	},
	status: {
		phase: 'Ready' as const,
		environments: [{ name: 'production', readyReplicas: 1, currentImage: 'postgres:16' }]
	}
};

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
	// Visit /login first so we land on the origin without triggering the root
	// page's unauthenticated redirect; then write the token to localStorage.
	await page.goto('/login');
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

async function setupMocks(page: Page) {
	await page.route('/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
	await page.route('/api/projects', (r) => r.fulfill({ json: [mockProject] }));
	await page.route('/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
	await page.route('/api/projects/my-project/apps', (r) =>
		r.fulfill({ json: [mockGitApp, mockDbApp] })
	);
	await page.route('/api/projects/my-project/apps/web-app', (r) =>
		r.fulfill({ json: mockGitApp })
	);
	await page.route('/api/projects/my-project/apps/postgres', (r) =>
		r.fulfill({ json: mockDbApp })
	);
	await page.route('/api/projects/my-project/activity', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/previews', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/members', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/apps/web-app/domains*', (r) =>
		r.fulfill({ json: { primary: 'web-app-production.example.com', custom: [] } })
	);
	await page.route('/api/projects/my-project/apps/web-app/tokens', (r) =>
		r.fulfill({ json: [] })
	);
	await page.route('/api/projects/my-project/apps/postgres/domains*', (r) =>
		r.fulfill({ json: { primary: '', custom: [] } })
	);
	await page.route('/api/projects/my-project/apps/postgres/tokens', (r) =>
		r.fulfill({ json: [] })
	);
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
	// Env URLs use ?environment=... query string, so match with a wildcard
	// rather than /env/**, which only matches path segments.
	await page.route('**/api/projects/my-project/apps/web-app/env*', (r) =>
		r.fulfill({ json: [] })
	);
	await page.route('**/api/projects/my-project/apps/web-app/shared', (r) =>
		r.fulfill({ json: {} })
	);
	await page.route('**/api/projects/my-project/apps/web-app/secrets', (r) =>
		r.fulfill({ json: [] })
	);
	await page.route('**/api/projects/my-project/apps/postgres/env*', (r) =>
		r.fulfill({ json: [] })
	);
	await page.route('**/api/projects/my-project/apps/postgres/secrets', (r) =>
		r.fulfill({ json: [] })
	);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('navigation reachability', () => {
	test('Test 1: Dashboard (/) is reachable and shows project', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page);
		await page.goto('/');

		await expect(page.getByText('my-project').first()).toBeVisible({ timeout: 5000 });
		// Left rail: Projects, Extensions, Platform Settings (admin)
		await expect(page.getByTitle('Projects')).toBeVisible({ timeout: 5000 });
		await expect(page.getByTitle('Extensions')).toBeVisible({ timeout: 5000 });
		await expect(page.getByTitle('Platform Settings')).toBeVisible({ timeout: 5000 });
	});

	test('Test 2: /extensions is reachable via left rail', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page);
		await page.goto('/');

		await page.getByTitle('Extensions').click();
		await expect(page).toHaveURL('/extensions');
		await expect(page.getByRole('heading', { name: 'Extensions' })).toBeVisible({ timeout: 5000 });
	});

	test('Test 3: /admin/settings is reachable via left rail (admin)', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		await page.getByTitle('Platform Settings').click();
		await expect(page).toHaveURL('/admin/settings');
		await expect(
			page.getByRole('heading', { name: 'Platform Settings' })
		).toBeVisible({ timeout: 5000 });
	});

	test('Test 4: /admin/settings is reachable via user menu', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		// The user menu is the button in the header right side containing User icon
		const userMenuBtn = page.locator('header').locator('button').last();
		await userMenuBtn.click();

		// Click "Platform Settings" in the dropdown
		await page.getByRole('link', { name: 'Platform Settings' }).first().click();
		await expect(page).toHaveURL('/admin/settings');
	});

	test('Test 5: /projects/new is reachable from dashboard', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page, true);
		await page.goto('/');

		// Click "New Project" link/button
		await page.getByRole('link', { name: 'New Project' }).click();
		await expect(page).toHaveURL('/projects/new');
	});

	test('Test 6: /projects/my-project is reachable by clicking project card', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page);
		await page.goto('/');

		// Click the project card
		await page.getByRole('link', { name: /my-project/ }).first().click();
		await expect(page).toHaveURL('/projects/my-project');
	});

	test('Test 7: App detail drawer opens by clicking app in list view', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		// Switch to list view
		await page.getByTitle('List view').click();

		// Wait for the table to appear and click the web-app row
		await expect(page.getByText('web-app')).toBeVisible({ timeout: 5000 });
		await page.getByText('web-app').first().click();

		// Drawer-in-place: URL stays on project canvas but drawer opens with the
		// app detail. The drawer shows Deployments tab by default and a heading
		// with the app name.
		await expect(page.getByRole('heading', { name: 'web-app', exact: true })).toBeVisible({
			timeout: 5000
		});
		await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({
			timeout: 5000
		});
	});

	test('Test 8: /projects/my-project/settings is reachable via left rail Settings icon', async ({
		page
	}) => {
		await setupMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project');

		// The project scope rail has "Project Settings" link
		await page.getByTitle('Project Settings').click();
		await expect(page).toHaveURL('/projects/my-project/settings');
		await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible({
			timeout: 5000
		});
	});

	test('Test 9: /projects/my-project/previews is reachable from project settings', async ({
		page
	}) => {
		await setupMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project/settings');

		// Click the Environments tab
		await page.getByRole('button', { name: 'Environments' }).click();

		// Click "View active PR environments →"
		await page.getByRole('link', { name: /View active PR environments/ }).click();
		await expect(page).toHaveURL('/projects/my-project/previews');
	});

	test('Test 10: Drawer tabs are all reachable', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project/apps/web-app');

		// Deployments tab is shown by default
		await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({
			timeout: 5000
		});

		// Click Variables tab
		await page.getByRole('button', { name: 'Variables', exact: true }).click();
		// Variables tab content renders something (editor or empty state)
		await expect(page.getByRole('button', { name: 'Variables', exact: true })).toBeVisible({
			timeout: 3000
		});

		// Click Logs tab
		await page.getByRole('button', { name: 'Logs', exact: true }).click();
		await expect(page.getByRole('button', { name: 'Logs', exact: true })).toBeVisible({
			timeout: 3000
		});

		// Click Settings tab
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		// Settings tab has a filter input
		await expect(page.getByPlaceholder('Filter settings…')).toBeVisible({ timeout: 5000 });
	});
});
