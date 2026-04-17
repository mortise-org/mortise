/**
 * Tests for the preview environments page at /projects/my-project/previews.
 *
 * All API calls mocked via page.route(). No live backend required.
 *
 * NOTE: The previews page's onMount currently sets loading=false without
 * calling the API (stub implementation). The page always renders the empty
 * state. Tests are written against actual rendered behaviour and will
 * become more complete once the API fetch is wired up.
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
	description: ''
};

const mockApp = {
	metadata: { name: 'web-app', namespace: 'project-my-project' },
	spec: {
		source: { type: 'image' as const, image: 'nginx:1.27' },
		network: { public: true, port: 8080 },
		environments: [{ name: 'production', replicas: 1 }],
		storage: [],
		credentials: []
	},
	status: { phase: 'Ready' as const, environments: [] }
};

const mockGitProvider = {
	name: 'github-main',
	type: 'github' as const,
	host: 'github.com',
	mode: 'oauth' as const,
	phase: 'Ready' as const,
	hasToken: true
};

const mockPreviews = [
	{
		name: 'web-app-pr-42',
		appRef: 'web-app',
		pr: { number: 42, branch: 'feature/new-ui', sha: 'abc1234' },
		phase: 'Ready' as const,
		url: 'https://web-app-pr-42.example.com',
		expiresAt: new Date(Date.now() + 86400000).toISOString()
	},
	{
		name: 'web-app-pr-41',
		appRef: 'web-app',
		pr: { number: 41, branch: 'fix/login-bug', sha: 'def5678' },
		phase: 'Building' as const,
		expiresAt: new Date(Date.now() + 86400000).toISOString()
	},
	{
		name: 'web-app-pr-40',
		appRef: 'web-app',
		pr: { number: 40, branch: 'old-feature', sha: 'ghi9012' },
		phase: 'Expired' as const,
		expiresAt: undefined
	}
];

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
	await page.route('/api/projects/my-project/apps', (r) => r.fulfill({ json: [mockApp] }));
	await page.route('/api/projects/my-project/activity', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/previews', (r) =>
		r.fulfill({ json: mockPreviews })
	);
	await page.route('/api/projects/my-project/members', (r) => r.fulfill({ json: [] }));
	await page.route('/api/gitproviders', (r) => r.fulfill({ json: [mockGitProvider] }));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('Previews page', () => {
	// 1. Page heading
	test('Test 1: previews page shows correct heading', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project/previews');

		await expect(page.getByRole('heading', { name: 'PR Environments' })).toBeVisible({
			timeout: 5000
		});
	});

	// 2. Sub-heading / descriptive text
	test('Test 2: previews page shows descriptive subtitle text', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project/previews');

		await expect(
			page.getByText('Active preview environments for open pull requests')
		).toBeVisible({ timeout: 5000 });
	});

	// 3. "Configure in Project Settings →" link at top goes to settings page
	test('Test 3: "Configure in Project Settings →" link goes to project settings', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await page.goto('/projects/my-project/previews');

		const configLink = page.getByRole('link', { name: /Configure in Project Settings/ });
		await expect(configLink).toBeVisible({ timeout: 5000 });

		await configLink.click();
		await expect(page).toHaveURL('/projects/my-project/settings', { timeout: 5000 });
	});

	// 4. Empty state: when no previews, shows "No active PR environments" heading
	test('Test 4: empty state shows "No active PR environments" heading', async ({ page }) => {
		await setupCommonMocks(page);
		// Override with empty list
		await page.route('/api/projects/my-project/previews', (r) => r.fulfill({ json: [] }));
		await injectAuth(page);
		await page.goto('/projects/my-project/previews');

		await expect(page.getByText('No active PR environments')).toBeVisible({ timeout: 5000 });
	});

	// 5. Empty state: "Enable PR Environments" button links to project settings
	test('Test 5: empty state "Enable PR Environments" button links to project settings', async ({ page }) => {
		await setupCommonMocks(page);
		await page.route('/api/projects/my-project/previews', (r) => r.fulfill({ json: [] }));
		await injectAuth(page);
		await page.goto('/projects/my-project/previews');

		await expect(page.getByText('No active PR environments')).toBeVisible({ timeout: 5000 });

		const enableLink = page.getByRole('link', { name: 'Enable PR Environments' });
		await expect(enableLink).toBeVisible();

		// Verify the link href points to project settings
		await expect(enableLink).toHaveAttribute('href', '/projects/my-project/settings');
	});

	// 6. Empty state description text
	test('Test 6: empty state shows descriptive message about PR environments', async ({ page }) => {
		await setupCommonMocks(page);
		await page.route('/api/projects/my-project/previews', (r) => r.fulfill({ json: [] }));
		await injectAuth(page);
		await page.goto('/projects/my-project/previews');

		// The description paragraph under "No active PR environments"
		await expect(
			page.getByText(/When PR Environments are enabled/)
		).toBeVisible({ timeout: 5000 });
	});

	// 7. Previews page is reachable from project settings "Environments" tab
	test('Test 7: previews page is reachable from project settings environments tab', async ({ page }) => {
		await setupCommonMocks(page);
		await page.route('/api/projects/my-project/previews', (r) => r.fulfill({ json: [] }));
		await injectAuth(page);
		await page.goto('/projects/my-project/settings');

		// The Environments tab in project settings links to the previews page
		await page.getByRole('button', { name: 'Environments' }).click();
		await page.getByRole('link', { name: /View active PR environments/ }).click();

		await expect(page).toHaveURL('/projects/my-project/previews', { timeout: 5000 });
		await expect(page.getByRole('heading', { name: 'PR Environments' })).toBeVisible({
			timeout: 5000
		});
	});
});
