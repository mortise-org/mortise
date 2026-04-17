/**
 * App drawer Settings tab section tests.
 *
 * Tests cover ALL sections of the SettingsTab for an app at
 * /projects/my-project/apps/web-app. ALL API calls are mocked via
 * page.route(). No live backend required. Tests are fully independent.
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

const mockApp = {
	metadata: { name: 'web-app', namespace: 'project-my-project' },
	spec: {
		source: { type: 'git' as const, repo: 'https://github.com/org/web-app', branch: 'main' },
		network: { public: true, port: 8080 },
		environments: [
			{
				name: 'production',
				replicas: 2,
				resources: { cpu: '500m', memory: '512Mi' },
				domain: 'web-app.example.com'
			}
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
						timestamp: new Date().toISOString()
					}
				]
			}
		]
	}
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function injectAuth(page: Page, isAdmin = true) {
	await page.goto('/');
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

async function setupMocks(page: Page, appOverride = mockApp) {
	await page.route('/api/auth/status', (r) => r.fulfill({ json: { setupRequired: false } }));
	await page.route('/api/projects', (r) => r.fulfill({ json: [mockProject] }));
	await page.route('/api/projects/my-project', (r) => r.fulfill({ json: mockProject }));
	await page.route('/api/projects/my-project/apps', (r) => r.fulfill({ json: [appOverride] }));
	await page.route('/api/projects/my-project/apps/web-app', (r) => r.fulfill({ json: appOverride }));
	await page.route('/api/projects/my-project/activity', (r) => r.fulfill({ json: [] }));
	await page.route('/api/projects/my-project/apps/web-app/domains*', (r) =>
		r.fulfill({ json: { primary: 'web-app.example.com', custom: [] } })
	);
	await page.route('/api/projects/my-project/apps/web-app/tokens', (r) => r.fulfill({ json: [] }));
	await page.route('/api/gitproviders', (r) => r.fulfill({ json: [] }));
	await page.route('/api/platform', (r) =>
		r.fulfill({
			json: { domain: 'example.com', dns: { provider: 'cloudflare' }, tls: { certManagerClusterIssuer: 'letsencrypt-prod' } }
		})
	);
}

async function navigateToSettingsTab(page: Page) {
	await page.goto('/projects/my-project/apps/web-app');
	// Wait for the drawer to open
	await expect(page.getByRole('button', { name: 'Deployments' })).toBeVisible({ timeout: 5000 });
	await page.getByRole('button', { name: 'Settings' }).click();
	await expect(page.getByPlaceholder('Filter settings…')).toBeVisible({ timeout: 5000 });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('app drawer settings tab sections', () => {
	test('Test 1: Settings tab opens and shows filter input', async ({ page }) => {
		await setupMocks(page);
		await injectAuth(page);

		await navigateToSettingsTab(page);

		const filterInput = page.getByPlaceholder('Filter settings…');
		await expect(filterInput).toBeVisible();

		// Key sections should be present
		await expect(page.getByText('Source')).toBeVisible({ timeout: 3000 });
		await expect(page.getByText('Networking')).toBeVisible({ timeout: 3000 });
	});

	test('Test 2: Update git source (repo + branch) → verifies PUT body has updated source', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		const srcRepoInput = page.locator('#src-repo');
		await srcRepoInput.scrollIntoViewIfNeeded();
		await srcRepoInput.clear();
		await srcRepoInput.fill('https://github.com/org/new-repo');

		const srcBranchInput = page.locator('#src-branch');
		await srcBranchInput.clear();
		await srcBranchInput.fill('develop');

		// "Update" button in Source section — first one
		await page.getByRole('button', { name: 'Update' }).first().click();

		await expect.poll(() => capturedBody).toMatchObject({
			source: {
				repo: 'https://github.com/org/new-repo',
				branch: 'develop'
			}
		});
	});

	test('Test 3: Change build mode to Dockerfile + set path → "Save build config" → verifies PUT body', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		const buildModeSelect = page.locator('#build-mode');
		await buildModeSelect.scrollIntoViewIfNeeded();
		await buildModeSelect.selectOption('dockerfile');

		// Dockerfile path input should appear after selecting dockerfile mode
		const dockerfilePathInput = page.locator('#dockerfile-path');
		await expect(dockerfilePathInput).toBeVisible({ timeout: 3000 });
		await dockerfilePathInput.fill('docker/Dockerfile.prod');

		await page.getByRole('button', { name: 'Save build config' }).click();

		await expect.poll(() => capturedBody).toMatchObject({
			source: {
				build: {
					mode: 'dockerfile',
					dockerfilePath: 'docker/Dockerfile.prod'
				}
			}
		});
	});

	test('Test 4: Toggle networking from public to private → "Update" → verifies PUT body has network.public: false', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		// The public toggle is a role="switch" button
		const publicToggle = page.getByRole('switch');
		await publicToggle.scrollIntoViewIfNeeded();
		// Currently true (public), click to set false (private)
		await expect(publicToggle).toHaveAttribute('aria-checked', 'true');
		await publicToggle.click();
		await expect(publicToggle).toHaveAttribute('aria-checked', 'false');

		// Networking "Update" button — second Update button on the page (after Source's Update)
		await page.getByRole('button', { name: 'Update' }).nth(1).click();

		await expect.poll(() => capturedBody).toMatchObject({
			network: { public: false }
		});
	});

	test('Test 5: Update port to 3000 → "Update" → verifies PUT body has network.port: 3000', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		const netPortInput = page.locator('#net-port');
		await netPortInput.scrollIntoViewIfNeeded();
		await netPortInput.clear();
		await netPortInput.fill('3000');

		// Networking "Update" button
		await page.getByRole('button', { name: 'Update' }).nth(1).click();

		await expect.poll(() => capturedBody).toMatchObject({
			network: { port: 3000 }
		});
	});

	test('Test 6: Update replicas to 3 → "Update" → verifies PUT body has environments[0].replicas: 3', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		const replicasInput = page.locator('#scale-replicas');
		await replicasInput.scrollIntoViewIfNeeded();
		await replicasInput.clear();
		await replicasInput.fill('3');

		// Scale "Update" button — last Update button on the page
		await page.getByRole('button', { name: 'Update' }).last().click();

		await expect.poll(() => capturedBody).toMatchObject({
			environments: [
				{ name: 'production', replicas: 3 }
			]
		});
	});

	test('Test 7: Update CPU and Memory → "Update" → verifies PUT body has resources', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		const cpuInput = page.locator('#scale-cpu');
		await cpuInput.scrollIntoViewIfNeeded();
		await cpuInput.clear();
		await cpuInput.fill('1000m');

		const memInput = page.locator('#scale-mem');
		await memInput.clear();
		await memInput.fill('1Gi');

		await page.getByRole('button', { name: 'Update' }).last().click();

		await expect.poll(() => capturedBody).toMatchObject({
			environments: [
				{
					name: 'production',
					resources: { cpu: '1000m', memory: '1Gi' }
				}
			]
		});
	});

	test('Test 8: Add annotation → "Save annotations" → verifies PUT body has environments[0].annotations', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		// Expand the Advanced section
		const advancedBtn = page.getByRole('button', { name: 'Advanced' });
		await advancedBtn.scrollIntoViewIfNeeded();
		await advancedBtn.click();

		// Click "Add annotation"
		await page.getByRole('button', { name: 'Add annotation' }).click();

		// Fill in the annotation key/value inputs that appear
		const annotationKeyInput = page.getByPlaceholder('annotation.example.com/key');
		await annotationKeyInput.fill('linkerd.io/inject');

		const annotationValueInput = page.getByPlaceholder('value');
		await annotationValueInput.fill('enabled');

		await page.getByRole('button', { name: 'Save annotations' }).click();

		await expect.poll(() => capturedBody).toMatchObject({
			environments: [
				{
					annotations: { 'linkerd.io/inject': 'enabled' }
				}
			]
		});
	});

	test('Test 9: Add secret mount (secretName + mountPath) → verifies PUT body has environments[0].secretMounts', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		// Expand the Advanced section
		const advancedBtn = page.getByRole('button', { name: 'Advanced' });
		await advancedBtn.scrollIntoViewIfNeeded();
		await advancedBtn.click();

		// Click "Add secret mount"
		await page.getByRole('button', { name: 'Add secret mount' }).click();

		// Fill in the secret name and mount path inputs
		const secretNameInput = page.getByPlaceholder('k8s-secret-name');
		await secretNameInput.fill('my-tls-secret');

		const mountPathInput = page.getByPlaceholder('/etc/certs');
		await mountPathInput.fill('/etc/ssl/certs');

		// Click "Add" to save the mount (auto-saves)
		await page.getByRole('button', { name: 'Add' }).last().click();

		await expect.poll(() => capturedBody).toMatchObject({
			environments: [
				{
					secretMounts: [
						{ secretName: 'my-tls-secret', mountPath: '/etc/ssl/certs' }
					]
				}
			]
		});
	});

	test('Test 10: Add custom domain → "Add" → verifies POST /api/projects/my-project/apps/web-app/domains', async ({ page }) => {
		let postWasCalled = false;
		let capturedPostUrl = '';

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app/domains*', async (route) => {
			if (route.request().method() === 'POST') {
				postWasCalled = true;
				capturedPostUrl = route.request().url();
				return route.fulfill({
					status: 200,
					json: { primary: 'web-app.example.com', custom: ['custom.example.com'] }
				});
			}
			return route.fulfill({ json: { primary: 'web-app.example.com', custom: [] } });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		// Filter to domains section to avoid ambiguity
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByText('Domains')).toBeVisible({ timeout: 3000 });

		const domainInput = page.getByPlaceholder('custom.example.com');
		await domainInput.scrollIntoViewIfNeeded();
		await domainInput.fill('custom.example.com');

		await page.getByRole('button', { name: 'Add' }).click();

		await expect.poll(() => postWasCalled).toBe(true);
		expect(capturedPostUrl).toContain('/api/projects/my-project/apps/web-app/domains');
	});

	test('Test 11: Save TLS override (open details, fill issuer) → verifies PUT body has environments[0].tls', async ({ page }) => {
		let capturedBody: Record<string, unknown> | null = null;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'PUT') {
				capturedBody = JSON.parse(route.request().postData() ?? '{}');
				return route.fulfill({ status: 200, json: mockApp });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		// Filter to domains section
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByText('Domains')).toBeVisible({ timeout: 3000 });

		// Open TLS overrides <details>
		const tlsSummary = page.getByText('TLS overrides (advanced)');
		await tlsSummary.scrollIntoViewIfNeeded();
		await tlsSummary.click();

		// Fill the cluster issuer override input
		const tlsIssuerInput = page.locator('#tls-issuer-ovr');
		await expect(tlsIssuerInput).toBeVisible({ timeout: 3000 });
		await tlsIssuerInput.fill('letsencrypt-staging');

		await page.getByRole('button', { name: 'Save TLS overrides' }).click();

		await expect.poll(() => capturedBody).toMatchObject({
			environments: [
				{
					tls: { clusterIssuer: 'letsencrypt-staging' }
				}
			]
		});
	});

	test('Test 12: Danger zone: click Delete, type app name, click "Delete App" → verifies DELETE /api/projects/my-project/apps/web-app called', async ({ page }) => {
		let deleteWasCalled = false;

		await setupMocks(page);
		await page.route('/api/projects/my-project/apps/web-app', async (route) => {
			if (route.request().method() === 'DELETE') {
				deleteWasCalled = true;
				return route.fulfill({ status: 200, json: { status: 'ok' } });
			}
			return route.fulfill({ json: mockApp });
		});

		await injectAuth(page);
		await navigateToSettingsTab(page);

		// Scroll down to Danger Zone
		const dangerSection = page.getByText('Danger Zone');
		await dangerSection.scrollIntoViewIfNeeded();

		// Click the initial "Delete" button to open the confirmation form
		await page.getByRole('button', { name: 'Delete' }).last().click();

		// Confirm input appears with placeholder matching the app name
		const confirmInput = page.getByPlaceholder('web-app');
		await expect(confirmInput).toBeVisible({ timeout: 3000 });

		// The "Delete App" confirmation button should be disabled until name is typed
		const deleteAppBtn = page.getByRole('button', { name: 'Delete App' });
		await expect(deleteAppBtn).toBeDisabled();

		// Type the app name to enable the button
		await confirmInput.fill('web-app');
		await expect(deleteAppBtn).not.toBeDisabled();

		// Click "Delete App"
		await deleteAppBtn.click();

		await expect.poll(() => deleteWasCalled).toBe(true);
	});
});
