/**
 * Git source app creation and build+deploy flow tests.
 *
 * All API calls mocked via page.route(). No live backend.
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

const mockBuildingApp = {
	metadata: { name: 'web-app', namespace: 'project-my-project' },
	spec: {
		source: { type: 'git' as const, repo: 'https://github.com/org/web-app', branch: 'main' },
		network: { public: true },
		environments: [{ name: 'production', replicas: 1 }],
		storage: [],
		credentials: []
	},
	status: {
		phase: 'Building' as const,
		environments: [{ name: 'production', readyReplicas: 0 }]
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

const mockRepos = [
	{
		fullName: 'org/web-app',
		name: 'web-app',
		description: 'Web application',
		defaultBranch: 'main',
		cloneURL: 'https://github.com/org/web-app.git',
		updatedAt: new Date().toISOString(),
		language: 'TypeScript',
		private: false
	}
];

const mockBranches = [
	{ name: 'main', default: true },
	{ name: 'develop', default: false }
];

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function injectAuth(page: Page, isAdmin = true) {
	// Visit the login page first so we land on the origin without triggering the
	// root page's unauthenticated redirect; then write the token to localStorage.
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

async function setupBaseRoutes(page: Page) {
	await page.route('**/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
	await page.route('**/api/projects', (r) => r.fulfill({ json: [mockProject] }));
	await page.route('**/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
	await page.route('**/api/projects/my-project/apps', (r) => r.fulfill({ json: [mockGitApp] }));
	await page.route('**/api/gitproviders', (r) => r.fulfill({ json: [mockGitProvider] }));
	await page.route('**/api/repos?**', (r) => r.fulfill({ json: mockRepos }));
	await page.route('**/api/repos/org/web-app/branches?**', (r) =>
		r.fulfill({ json: mockBranches })
	);
	// loadRepoTree is a best-effort nice-to-have; return empty to satisfy it.
	await page.route('**/api/repos/org/web-app/tree?**', (r) =>
		r.fulfill({ json: [] })
	);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('build and deploy', () => {
	test('Test 1: Create a git-source app from the modal', async ({ page }) => {
		await setupBaseRoutes(page);

		let postBody: Record<string, unknown> | null = null;
		let postCalled = false;

		// Mock POST to capture the request body
		await page.route('/api/projects/my-project/apps', async (r) => {
			if (r.request().method() === 'POST') {
				postCalled = true;
				postBody = JSON.parse(r.request().postData() ?? '{}');
				await r.fulfill({ status: 201, json: mockGitApp });
			} else {
				await r.fulfill({ json: [mockGitApp] });
			}
		});

		await injectAuth(page);
		await page.goto('/projects/my-project');

		// Click the "+ Add" button to open the modal
		await page.getByRole('button', { name: 'Add' }).click();

		// Modal appears — select "Git Repository"
		await expect(page.getByText('What would you like to create?')).toBeVisible({
			timeout: 5000
		});
		await page.getByText('Git Repository').click();

		// Git source config appears
		await expect(page.getByText('Git Provider')).toBeVisible({ timeout: 5000 });

		// Provider is already selected (github-main, the only one)
		const providerSelect = page.locator('select').first();
		await expect(providerSelect).toBeVisible({ timeout: 3000 });

		// Repos should load automatically
		await expect(page.getByText('org/web-app')).toBeVisible({ timeout: 5000 });

		// Select the repo
		await page.getByText('org/web-app').click();

		// Branch "main" should be auto-selected (it's the default)
		const branchSelect = page.locator('select').filter({ hasText: /main/ }).first();
		await expect(branchSelect).toBeVisible({ timeout: 3000 });

		// App name should be auto-populated from repo name
		const appNameInput = page.getByPlaceholder('my-app');
		await expect(appNameInput).toHaveValue('web-app', { timeout: 3000 });

		// Click "Create app"
		await page.getByRole('button', { name: 'Create app' }).click();

		// Verify POST was called with git source
		await expect(async () => {
			expect(postCalled).toBe(true);
		}).toPass({ timeout: 5000 });

		expect(postBody).toBeTruthy();
		const spec = (postBody as { spec?: { source?: { type?: string } } })?.spec;
		expect(spec?.source?.type).toBe('git');
	});

	test('Test 2: App shows Building phase while image is being built', async ({ page }) => {
		await setupBaseRoutes(page);

		// Override the app GET to return a Building phase app
		await page.route('/api/projects/my-project/apps/web-app', (r) =>
			r.fulfill({ json: mockBuildingApp })
		);
		await page.route('/api/projects/my-project/apps/web-app/domains*', (r) =>
			r.fulfill({ json: { primary: '', custom: [] } })
		);
		await page.route('/api/projects/my-project/apps/web-app/tokens', (r) =>
			r.fulfill({ json: [] })
		);

		await injectAuth(page);
		await page.goto('/projects/my-project/apps/web-app');

		// The drawer header shows the phase badge
		// Phase chip renders as a <span> with the phase text
		await expect(
			page.locator('span').filter({ hasText: 'Building' }).first()
		).toBeVisible({ timeout: 5000 });
	});

	test('Test 3: Redeploy triggers POST /api/projects/{p}/apps/{a}/deploy', async ({ page }) => {
		await setupBaseRoutes(page);

		let deployCalled = false;
		let deployBody: Record<string, unknown> | null = null;

		await page.route('/api/projects/my-project/apps/web-app', (r) =>
			r.fulfill({ json: mockGitApp })
		);
		await page.route('/api/projects/my-project/apps/web-app/domains*', (r) =>
			r.fulfill({ json: { primary: 'web-app-production.example.com', custom: [] } })
		);
		await page.route('/api/projects/my-project/apps/web-app/tokens', (r) =>
			r.fulfill({ json: [] })
		);
		await page.route('/api/projects/my-project/apps/web-app/deploy', async (r) => {
			deployCalled = true;
			deployBody = JSON.parse(r.request().postData() ?? '{}');
			await r.fulfill({
				json: {
					status: 'ok',
					app: 'web-app',
					image: 'registry.example.com/web-app:abc123'
				}
			});
		});

		await injectAuth(page);
		await page.goto('/projects/my-project/apps/web-app');

		// Deployments tab is the default — verify the Redeploy button is visible
		await expect(page.getByRole('button', { name: 'Redeploy' })).toBeVisible({ timeout: 5000 });

		// Click Redeploy
		await page.getByRole('button', { name: 'Redeploy' }).click();

		// Verify POST /deploy was called
		await expect(async () => {
			expect(deployCalled).toBe(true);
		}).toPass({ timeout: 5000 });

		expect(deployBody).toBeTruthy();
		const body = deployBody as { environment?: string; image?: string };
		expect(body.environment).toBe('production');
		expect(body.image).toBe('registry.example.com/web-app:abc123');
	});

	test('Test 4: Creating a cron app includes cron schedule in spec', async ({ page }) => {
		await setupBaseRoutes(page);

		let postBody: Record<string, unknown> | null = null;

		await page.route('/api/projects/my-project/apps', async (r) => {
			if (r.request().method() === 'POST') {
				postBody = JSON.parse(r.request().postData() ?? '{}');
				const cronApp = {
					...mockGitApp,
					metadata: { name: 'cron-job', namespace: 'project-my-project' },
					spec: {
						...mockGitApp.spec,
						kind: 'cron' as const,
						environments: [
							{
								name: 'production',
								replicas: 0,
								annotations: { 'mortise.dev/schedule': '0 * * * *' }
							}
						]
					}
				};
				await r.fulfill({ status: 201, json: cronApp });
			} else {
				await r.fulfill({ json: [mockGitApp] });
			}
		});

		await injectAuth(page);
		await page.goto('/projects/my-project');

		// Open the new app modal
		await page.getByRole('button', { name: 'Add' }).click();
		await expect(page.getByText('What would you like to create?')).toBeVisible({
			timeout: 5000
		});

		// Select Git Repository
		await page.getByText('Git Repository').click();
		await expect(page.getByText('Git Provider')).toBeVisible({ timeout: 5000 });

		// Select repo
		await expect(page.getByText('org/web-app')).toBeVisible({ timeout: 5000 });
		await page.getByText('org/web-app').click();

		// Fill app name
		const appNameInput = page.getByPlaceholder('my-app');
		await appNameInput.clear();
		await appNameInput.fill('cron-job');

		// Change Kind to "Cron"
		await page.getByRole('button', { name: 'Cron' }).click();

		// Schedule input should appear
		await expect(page.getByPlaceholder('0 * * * *')).toBeVisible({ timeout: 3000 });

		// Fill in the schedule
		await page.getByPlaceholder('0 * * * *').fill('0 * * * *');

		// Click "Create app"
		await page.getByRole('button', { name: 'Create app' }).click();

		// Verify POST body contains cron fields
		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		const spec = (postBody as { spec?: Record<string, unknown> })?.spec;
		expect(spec).toBeTruthy();

		// The spec should have cron kind or schedule annotation
		const hasKindCron = spec?.kind === 'cron';
		const environments = spec?.environments as Array<{
			annotations?: Record<string, string>;
		}> | undefined;
		const hasScheduleAnnotation = environments?.some(
			(e) => e.annotations?.['mortise.dev/schedule']
		);

		expect(hasKindCron || hasScheduleAnnotation).toBe(true);
	});

	test('Test 5: App creation via modal navigates to app drawer on success', async ({
		page
	}) => {
		await setupBaseRoutes(page);

		const newApp = {
			...mockGitApp,
			metadata: { name: 'my-new-app', namespace: 'project-my-project' }
		};

		await page.route('/api/projects/my-project/apps', async (r) => {
			if (r.request().method() === 'POST') {
				await r.fulfill({ status: 201, json: newApp });
			} else {
				await r.fulfill({ json: [mockGitApp] });
			}
		});
		await page.route('/api/projects/my-project/apps/my-new-app', (r) =>
			r.fulfill({ json: newApp })
		);
		await page.route('/api/projects/my-project/apps/my-new-app/domains*', (r) =>
			r.fulfill({ json: { primary: '', custom: [] } })
		);
		await page.route('/api/projects/my-project/apps/my-new-app/tokens', (r) =>
			r.fulfill({ json: [] })
		);

		await injectAuth(page);
		await page.goto('/projects/my-project');

		await page.getByRole('button', { name: 'Add' }).click();
		await expect(page.getByText('What would you like to create?')).toBeVisible({
			timeout: 5000
		});

		// Select Docker Image (simpler path — no git provider needed)
		await page.getByText('Docker Image').click();
		await expect(page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest')).toBeVisible({
			timeout: 3000
		});

		// Fill image and app name
		await page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest').fill('nginx:1.27');
		const appNameInput = page.getByPlaceholder('my-app');
		await appNameInput.clear();
		await appNameInput.fill('my-new-app');

		// Create the app
		await page.getByRole('button', { name: 'Create app' }).click();

		// Drawer-in-place: the modal closes and the new app's drawer opens
		// inline on the project canvas (URL stays at /projects/my-project).
		await expect(page.getByRole('heading', { name: 'my-new-app', exact: true })).toBeVisible({
			timeout: 10_000
		});
		await expect(page.getByRole('button', { name: 'Deployments', exact: true })).toBeVisible({
			timeout: 5000
		});
	});
});
