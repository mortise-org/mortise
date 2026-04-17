/**
 * Comprehensive tests for the NewAppModal — all source types and options.
 *
 * All API calls mocked via page.route(). No live backend required.
 * The modal is opened via the "+ Add" button on /projects/my-project.
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

const mockRepos = [
	{
		fullName: 'org/web-app',
		name: 'web-app',
		description: 'Main web app',
		defaultBranch: 'main',
		cloneURL: 'https://github.com/org/web-app.git',
		updatedAt: new Date().toISOString(),
		language: 'TypeScript',
		private: false
	},
	{
		fullName: 'org/api-server',
		name: 'api-server',
		description: 'API server',
		defaultBranch: 'main',
		cloneURL: 'https://github.com/org/api-server.git',
		updatedAt: new Date().toISOString(),
		language: 'Go',
		private: false
	}
];

const mockBranches = [
	{ name: 'main', default: true },
	{ name: 'feature/new-ui', default: false },
	{ name: 'develop', default: false }
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
	await page.route('/api/projects/my-project/previews', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/members', (r) => r.fulfill({ json: [] }));
	await page.route('/api/gitproviders', (r) => r.fulfill({ json: [mockGitProvider] }));
	await page.route('/api/repos*', (r) => r.fulfill({ json: mockRepos }));
	await page.route('/api/repos/**', (r) => r.fulfill({ json: mockBranches }));
}

/** Navigate to the project page and open the NewAppModal via the Add button. */
async function openModal(page: Page) {
	await page.goto('/projects/my-project');
	await page.getByRole('button', { name: 'Add' }).click();
	await expect(page.getByText('What would you like to create?')).toBeVisible({ timeout: 5000 });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('NewAppModal — all source types', () => {
	// 1. Type picker shows all 6 options
	test('Test 1: type picker shows all 6 source type options', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await openModal(page);

		await expect(page.getByText('Git Repository')).toBeVisible();
		await expect(page.getByText('Database')).toBeVisible();
		await expect(page.getByText('Template', { exact: true })).toBeVisible();
		await expect(page.getByText('Docker Image')).toBeVisible();
		await expect(page.getByText('External Service')).toBeVisible();
		await expect(page.getByText('Empty App')).toBeVisible();
	});

	// 2. Back button from git form returns to type picker
	test('Test 2: back button from git form returns to type picker', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await openModal(page);

		await page.getByText('Git Repository').click();
		await expect(page.getByText('Git Provider')).toBeVisible({ timeout: 5000 });

		await page.getByRole('button', { name: /Back/ }).click();

		await expect(page.getByText('What would you like to create?')).toBeVisible({ timeout: 3000 });
	});

	// 3. Cancel button closes modal
	test('Test 3: cancel button closes the modal', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await openModal(page);

		await page.getByText('Docker Image').click();
		await expect(page.getByRole('button', { name: 'Cancel' })).toBeVisible({ timeout: 3000 });

		await page.getByRole('button', { name: 'Cancel' }).click();

		// Modal should be gone — the heading is no longer visible
		await expect(page.getByText('What would you like to create?')).not.toBeVisible();
		await expect(page.getByRole('button', { name: 'Create app' })).not.toBeVisible();
	});

	// 4. Git source: selecting git loads provider dropdown, then repos from API
	test('Test 4: git source loads provider dropdown and repos from API', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await openModal(page);

		await page.getByText('Git Repository').click();

		// Provider select appears with the mock provider
		await expect(page.getByText('Git Provider')).toBeVisible({ timeout: 5000 });
		await expect(page.locator('select').filter({ hasText: /github-main/ })).toBeVisible({
			timeout: 5000
		});

		// Repos load automatically (provider auto-selected)
		await expect(page.getByText('org/web-app')).toBeVisible({ timeout: 5000 });
		await expect(page.getByText('org/api-server')).toBeVisible({ timeout: 5000 });
	});

	// 5. Git source: selecting a repo loads branches
	test('Test 5: selecting a repo loads branches into branch select', async ({ page }) => {
		await setupCommonMocks(page);
		// Branch route specifically for org/web-app
		await page.route('/api/repos/org/web-app/branches*', (r) =>
			r.fulfill({ json: mockBranches })
		);
		await injectAuth(page);
		await openModal(page);

		await page.getByText('Git Repository').click();
		await expect(page.getByText('org/web-app')).toBeVisible({ timeout: 5000 });
		await page.getByText('org/web-app').click();

		// Branch select should show the mock branches — scope to the Branch label's sibling
		const branchSelect = page.locator('select').filter({ hasText: /main/ }).last();
		await expect(branchSelect).toBeVisible({ timeout: 5000 });
		// feature/new-ui should appear as an option
		await expect(page.locator('option', { hasText: 'feature/new-ui' })).toHaveCount(1);
	});

	// 6. Git source: fill name + select provider + repo + branch → POST with correct git source spec
	test('Test 6: git source — POST body contains correct git source spec', async ({ page }) => {
		await setupCommonMocks(page);
		await page.route('/api/repos/org/web-app/branches*', (r) =>
			r.fulfill({ json: mockBranches })
		);

		let postBody: unknown = null;
		await page.route('/api/projects/my-project/apps', async (route) => {
			if (route.request().method() === 'POST') {
				postBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ json: { ...mockApp, metadata: { name: 'web-app', namespace: 'project-my-project' } } });
			}
			return route.fulfill({ json: [mockApp] });
		});

		await injectAuth(page);
		await openModal(page);

		await page.getByText('Git Repository').click();
		await expect(page.getByText('org/web-app')).toBeVisible({ timeout: 5000 });
		await page.getByText('org/web-app').click();

		// App name should be auto-populated; verify it then clear and set explicitly
		const appNameInput = page.getByPlaceholder('my-app');
		await expect(appNameInput).toHaveValue('web-app', { timeout: 3000 });

		await page.getByRole('button', { name: 'Create app' }).click();

		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		const body = postBody as { name: string; spec: { source: { type: string; repo: string; branch: string } } };
		expect(body.name).toBe('web-app');
		expect(body.spec.source.type).toBe('git');
		expect(body.spec.source.repo).toBe('https://github.com/org/web-app.git');
		expect(body.spec.source.branch).toBe('main');
	});

	// 7. Git source: selecting Dockerfile build mode shows path input
	test('Test 7: git source — selecting Dockerfile build mode shows path input', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await openModal(page);

		await page.getByText('Git Repository').click();
		await expect(page.getByText('Build mode')).toBeVisible({ timeout: 5000 });

		// Select Dockerfile mode
		await page.locator('select').filter({ hasText: 'Auto-detect' }).selectOption('dockerfile');

		// Dockerfile path input should appear
		await expect(page.getByText('Dockerfile path')).toBeVisible({ timeout: 3000 });
		await expect(page.getByPlaceholder('Dockerfile')).toBeVisible();
	});

	// 8. Image source: fill image ref + pull secret → POST with correct image spec
	test('Test 8: image source — POST body contains correct image spec', async ({ page }) => {
		await setupCommonMocks(page);

		let postBody: unknown = null;
		await page.route('/api/projects/my-project/apps', async (route) => {
			if (route.request().method() === 'POST') {
				postBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ json: { ...mockApp, metadata: { name: 'my-app', namespace: 'project-my-project' } } });
			}
			return route.fulfill({ json: [mockApp] });
		});

		await injectAuth(page);
		await openModal(page);

		await page.getByText('Docker Image').click();
		await expect(page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest')).toBeVisible({
			timeout: 3000
		});

		await page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest').fill('ghcr.io/org/app:v1.2.3');
		await page.getByPlaceholder('my-registry-secret').fill('my-pull-secret');

		const appNameInput = page.getByPlaceholder('my-app');
		await appNameInput.fill('my-app');

		await page.getByRole('button', { name: 'Create app' }).click();

		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		const body = postBody as { name: string; spec: { source: { type: string; image: string; pullSecretRef: string } } };
		expect(body.name).toBe('my-app');
		expect(body.spec.source.type).toBe('image');
		expect(body.spec.source.image).toBe('ghcr.io/org/app:v1.2.3');
		expect(body.spec.source.pullSecretRef).toBe('my-pull-secret');
	});

	// 9. Database: Postgres preset fills name "postgres" and image → POST with correct image spec
	test('Test 9: database — Postgres preset prefills name and submits correct image spec', async ({ page }) => {
		await setupCommonMocks(page);

		let postBody: unknown = null;
		await page.route('/api/projects/my-project/apps', async (route) => {
			if (route.request().method() === 'POST') {
				postBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ json: { ...mockApp, metadata: { name: 'postgres', namespace: 'project-my-project' } } });
			}
			return route.fulfill({ json: [mockApp] });
		});

		await injectAuth(page);
		await openModal(page);

		await page.getByText('Database').click();

		// DB template grid — click Postgres card
		await expect(page.getByText('Postgres', { exact: true })).toBeVisible({ timeout: 3000 });
		await page.getByText('Postgres', { exact: true }).click();

		// App name should be prefilled to 'postgres'
		const appNameInput = page.getByPlaceholder('my-app');
		await expect(appNameInput).toHaveValue('postgres', { timeout: 3000 });

		await page.getByRole('button', { name: 'Create app' }).click();

		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		const body = postBody as { name: string; spec: { source: { type: string; image: string } } };
		expect(body.name).toBe('postgres');
		expect(body.spec.source.type).toBe('image');
		expect(body.spec.source.image).toBe('postgres:16');
	});

	// 10. External service: fill host + port + credential → POST with external source spec
	test('Test 10: external service — POST body contains correct external source spec', async ({ page }) => {
		await setupCommonMocks(page);

		let postBody: unknown = null;
		await page.route('/api/projects/my-project/apps', async (route) => {
			if (route.request().method() === 'POST') {
				postBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ json: { ...mockApp, metadata: { name: 'ext-db', namespace: 'project-my-project' } } });
			}
			return route.fulfill({ json: [mockApp] });
		});

		await injectAuth(page);
		await openModal(page);

		await page.getByText('External Service').click();

		await expect(page.getByPlaceholder('db.internal.example.com')).toBeVisible({ timeout: 3000 });
		await page.getByPlaceholder('db.internal.example.com').fill('db.internal.example.com');

		// Port input — it has value 80 by default; clear and fill
		const portInput = page.locator('input[type="number"]');
		await portInput.fill('5432');

		// Add a credential
		await page.getByText('+ Add credential key').click();
		const credNameInputs = page.locator('input[placeholder="DATABASE_URL"]');
		await expect(credNameInputs).toBeVisible({ timeout: 3000 });
		await credNameInputs.fill('DATABASE_URL');
		await page.locator('input[placeholder="value or leave empty"]').fill('postgres://user:pass@db:5432/app');

		// Fill app name
		await page.getByPlaceholder('my-app').fill('ext-db');

		await page.getByRole('button', { name: 'Create app' }).click();

		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		const body = postBody as {
			name: string;
			spec: {
				source: { type: string; host: string; port: number };
				credentials: Array<{ name: string; value: string }>;
			};
		};
		expect(body.name).toBe('ext-db');
		expect(body.spec.source.type).toBe('external');
		expect(body.spec.source.host).toBe('db.internal.example.com');
		expect(body.spec.source.port).toBe(5432);
		expect(body.spec.credentials).toEqual(
			expect.arrayContaining([expect.objectContaining({ name: 'DATABASE_URL' })])
		);
	});

	// 11. Cron kind: click "Cron" → schedule input appears → POST with kind: 'cron' and schedule annotation
	test('Test 11: cron kind — schedule input appears and POST contains cron spec', async ({ page }) => {
		await setupCommonMocks(page);
		await page.route('/api/repos/org/web-app/branches*', (r) =>
			r.fulfill({ json: mockBranches })
		);

		let postBody: unknown = null;
		await page.route('/api/projects/my-project/apps', async (route) => {
			if (route.request().method() === 'POST') {
				postBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ json: { ...mockApp, metadata: { name: 'my-cron', namespace: 'project-my-project' } } });
			}
			return route.fulfill({ json: [mockApp] });
		});

		await injectAuth(page);
		await openModal(page);

		// Use Git source since kind selector only appears for git + image
		await page.getByText('Git Repository').click();
		await expect(page.getByText('org/web-app')).toBeVisible({ timeout: 5000 });
		await page.getByText('org/web-app').click();

		// Fill app name
		const appNameInput = page.getByPlaceholder('my-app');
		await appNameInput.clear();
		await appNameInput.fill('my-cron');

		// Switch kind to Cron
		await page.getByRole('button', { name: 'Cron' }).click();

		// Schedule input should appear
		await expect(page.getByPlaceholder('0 * * * *')).toBeVisible({ timeout: 3000 });

		// Fill schedule
		await page.getByPlaceholder('0 * * * *').fill('0 2 * * *');

		await page.getByRole('button', { name: 'Create app' }).click();

		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		const body = postBody as {
			name: string;
			spec: {
				kind?: string;
				environments?: Array<{ annotations?: Record<string, string> }>;
			};
		};
		expect(body.name).toBe('my-cron');
		const hasKindCron = body.spec.kind === 'cron';
		const hasScheduleAnnotation = body.spec.environments?.some(
			(e) => e.annotations?.['mortise.dev/schedule'] === '0 2 * * *'
		);
		expect(hasKindCron || hasScheduleAnnotation).toBe(true);
	});

	// 12. App name validation: empty name keeps Create button disabled
	test('Test 12: app name validation — empty name keeps Create button disabled', async ({ page }) => {
		await setupCommonMocks(page);
		await injectAuth(page);
		await openModal(page);

		await page.getByText('Docker Image').click();
		await expect(page.getByRole('button', { name: 'Create app' })).toBeVisible({ timeout: 3000 });

		// No app name entered — button should be disabled
		const createBtn = page.getByRole('button', { name: 'Create app' });
		await expect(createBtn).toBeDisabled();

		// Enter a name — button should become enabled
		await page.getByPlaceholder('my-app').fill('my-new-app');
		await expect(createBtn).toBeEnabled();

		// Clear the name — button should be disabled again
		await page.getByPlaceholder('my-app').fill('');
		await expect(createBtn).toBeDisabled();
	});

	// Bonus: image source — Dockerfile build mode path in POST body
	test('Test 13: git source — Dockerfile mode includes path in build spec', async ({ page }) => {
		await setupCommonMocks(page);
		await page.route('/api/repos/org/web-app/branches*', (r) =>
			r.fulfill({ json: mockBranches })
		);

		let postBody: unknown = null;
		await page.route('/api/projects/my-project/apps', async (route) => {
			if (route.request().method() === 'POST') {
				postBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ json: { ...mockApp, metadata: { name: 'web-app', namespace: 'project-my-project' } } });
			}
			return route.fulfill({ json: [mockApp] });
		});

		await injectAuth(page);
		await openModal(page);

		await page.getByText('Git Repository').click();
		await expect(page.getByText('org/web-app')).toBeVisible({ timeout: 5000 });
		await page.getByText('org/web-app').click();

		// Select Dockerfile build mode
		await page.locator('select').filter({ hasText: 'Auto-detect' }).selectOption('dockerfile');
		await expect(page.getByPlaceholder('Dockerfile')).toBeVisible({ timeout: 3000 });
		await page.getByPlaceholder('Dockerfile').fill('docker/Dockerfile.prod');

		await page.getByRole('button', { name: 'Create app' }).click();

		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		const body = postBody as {
			spec: { source: { build: { mode: string; dockerfilePath: string } } };
		};
		expect(body.spec.source.build.mode).toBe('dockerfile');
		expect(body.spec.source.build.dockerfilePath).toBe('docker/Dockerfile.prod');
	});

	// Bonus: Image source — Service kind (default) has no cron fields in spec
	test('Test 14: image source — service kind (default) — POST body lacks cron fields', async ({ page }) => {
		await setupCommonMocks(page);

		let postBody: unknown = null;
		await page.route('/api/projects/my-project/apps', async (route) => {
			if (route.request().method() === 'POST') {
				postBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ json: { ...mockApp, metadata: { name: 'svc-app', namespace: 'project-my-project' } } });
			}
			return route.fulfill({ json: [mockApp] });
		});

		await injectAuth(page);
		await openModal(page);

		await page.getByText('Docker Image').click();
		await page.getByPlaceholder('nginx:1.27 or ghcr.io/org/app:latest').fill('nginx:1.27');
		await page.getByPlaceholder('my-app').fill('svc-app');

		// "Service" button is the default
		await expect(page.getByRole('button', { name: 'Service' })).toBeVisible({ timeout: 3000 });

		await page.getByRole('button', { name: 'Create app' }).click();

		await expect(async () => {
			expect(postBody).toBeTruthy();
		}).toPass({ timeout: 5000 });

		const body = postBody as { spec: { kind?: string } };
		// No cron kind in service mode
		expect(body.spec.kind).toBeUndefined();
	});
});
