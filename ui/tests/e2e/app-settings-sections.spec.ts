/**
 * App drawer Settings tab section tests (real backend).
 *
 * Tests cover the SettingsTab sections for an app: source, networking,
 * scale, annotations, secret mounts, domains, TLS overrides, danger zone,
 * credentials, and bindings. All tests use the real backend API.
 */
import { test, expect } from '@playwright/test';
import {
	randomSuffix,
	ensureAdmin,
	loginViaAPI,
	injectToken,
	createProjectViaAPI,
	createAppViaAPI,
	deleteProjectViaAPI,
	deleteAppViaAPI,
	getAppViaAPI,
	listDomainsViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// Main settings tests (shared project + app)
// ---------------------------------------------------------------------------
test.describe('app drawer settings tab sections', () => {
	let token: string;
	let project: string;
	let appName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-settings-${randomSuffix()}`;
		appName = `settings-app-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
		await createAppViaAPI(request, token, project, appName, 'nginx:1.27');
	});

	test.afterAll(async ({ request }) => {
		await deleteAppViaAPI(request, token, project, appName);
		await deleteProjectViaAPI(request, token, project);
	});

	async function navigateToSettingsTab(page: import('@playwright/test').Page) {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await expect(page.getByPlaceholder('Filter settings…')).toBeVisible({ timeout: 5_000 });
	}

	test('Test 1: Settings tab opens and shows filter input with Source/Networking headings', async ({ page }) => {
		await navigateToSettingsTab(page);

		const filterInput = page.getByPlaceholder('Filter settings…');
		await expect(filterInput).toBeVisible();

		await expect(page.getByRole('heading', { name: 'Source' })).toBeVisible({ timeout: 3_000 });
		await expect(page.getByRole('heading', { name: 'Networking' })).toBeVisible({ timeout: 3_000 });
	});

	test('Test 2: Update image reference, verify via API', async ({ page, request }) => {
		await navigateToSettingsTab(page);

		const srcImageInput = page.locator('#src-image');
		await srcImageInput.scrollIntoViewIfNeeded();
		await srcImageInput.clear();
		await srcImageInput.fill('nginx:1.28');

		// Source section "Update" button
		await page.getByRole('button', { name: 'Update', exact: true }).first().click();

		// Wait for save, then verify via API
		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as { source: { image: string } };
			expect(spec.source.image).toBe('nginx:1.28');
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });

		// Restore original image for subsequent tests
		await srcImageInput.clear();
		await srcImageInput.fill('nginx:1.27');
		await page.getByRole('button', { name: 'Update', exact: true }).first().click();
		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as { source: { image: string } };
			expect(spec.source.image).toBe('nginx:1.27');
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });
	});

	test('Test 3: Toggle networking from public to private, verify via API', async ({ page, request }) => {
		await navigateToSettingsTab(page);

		// The public toggle is a role="switch"
		const publicToggle = page.getByRole('switch', { name: 'Toggle public access' });
		await publicToggle.scrollIntoViewIfNeeded();
		await expect(publicToggle).toHaveAttribute('aria-checked', 'true');
		await publicToggle.click();
		await expect(publicToggle).toHaveAttribute('aria-checked', 'false');

		// Click the Networking section "Update" button (second Update on the page)
		// Filter to networking to isolate the button
		await page.getByPlaceholder('Filter settings…').fill('networking');
		await page.getByRole('button', { name: 'Update', exact: true }).click();

		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as { network: { public: boolean } };
			expect(spec.network.public).toBe(false);
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });

		// Restore: toggle back to public
		await page.getByPlaceholder('Filter settings…').clear();
		const toggle2 = page.getByRole('switch', { name: 'Toggle public access' });
		await toggle2.click();
		await expect(toggle2).toHaveAttribute('aria-checked', 'true');
		await page.getByPlaceholder('Filter settings…').fill('networking');
		await page.getByRole('button', { name: 'Update', exact: true }).click();
		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as { network: { public: boolean } };
			expect(spec.network.public).toBe(true);
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });
	});

	test('Test 4: Update port, verify via API', async ({ page, request }) => {
		await navigateToSettingsTab(page);

		// Filter to networking to isolate inputs and buttons
		await page.getByPlaceholder('Filter settings…').fill('networking');

		const netPortInput = page.locator('#net-port');
		await netPortInput.scrollIntoViewIfNeeded();
		await netPortInput.clear();
		await netPortInput.fill('3000');

		await page.getByRole('button', { name: 'Update', exact: true }).click();

		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as { network: { port: number } };
			expect(spec.network.port).toBe(3000);
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });
	});

	test('Test 5: Update replicas, verify via API', async ({ page, request }) => {
		await navigateToSettingsTab(page);

		// Filter to scale section
		await page.getByPlaceholder('Filter settings…').fill('scale');

		const replicasInput = page.locator('#scale-replicas');
		await replicasInput.scrollIntoViewIfNeeded();
		await replicasInput.clear();
		await replicasInput.fill('3');

		await page.getByRole('button', { name: 'Update', exact: true }).click();

		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as { environments: Array<{ name: string; replicas: number }> };
			const env = spec.environments?.find(e => e.name === 'production');
			expect(env?.replicas).toBe(3);
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });
	});

	test('Test 6: Update CPU and Memory, verify via API', async ({ page, request }) => {
		await navigateToSettingsTab(page);

		// Filter to scale section
		await page.getByPlaceholder('Filter settings…').fill('scale');

		const cpuInput = page.locator('#scale-cpu');
		await cpuInput.scrollIntoViewIfNeeded();
		await cpuInput.clear();
		await cpuInput.fill('1000m');

		const memInput = page.locator('#scale-mem');
		await memInput.clear();
		await memInput.fill('1Gi');

		await page.getByRole('button', { name: 'Update', exact: true }).click();

		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as {
				environments: Array<{
					name: string;
					resources: { cpu: string; memory: string };
				}>;
			};
			const env = spec.environments?.find(e => e.name === 'production');
			expect(env?.resources?.cpu).toBe('1000m');
			expect(env?.resources?.memory).toBe('1Gi');
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });
	});

	test('Test 7: Add annotation, verify via API', async ({ page, request }) => {
		// The save may fail silently due to resource version conflicts from the
		// controller reconciling concurrently. Retry the entire fill+save flow.
		await expect(async () => {
			await navigateToSettingsTab(page);

			await page.getByPlaceholder('Filter settings…').fill('advanced');

			const advancedBtn = page.getByRole('button', { name: 'Advanced', exact: true });
			await advancedBtn.scrollIntoViewIfNeeded();
			await advancedBtn.click();

			await page.getByText('Add annotation').click();

			const annotationKeyInput = page.getByPlaceholder('annotation.example.com/key');
			await annotationKeyInput.fill('linkerd.io/inject');

			const annotationValueInput = page.getByPlaceholder('value');
			await annotationValueInput.fill('enabled');

			await page.getByRole('button', { name: 'Save annotations', exact: true }).click();

			await new Promise(r => setTimeout(r, 1_000));
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as {
				environments: Array<{
					name: string;
					annotations?: Record<string, string>;
				}>;
			};
			const env = spec.environments?.find(e => e.name === 'production');
			expect(env?.annotations?.['linkerd.io/inject']).toBe('enabled');
		}).toPass({ timeout: 20_000, intervals: [2_000, 3_000, 5_000] });
	});

	test('Test 8: Add secret mount, verify via API', async ({ page, request }) => {
		await navigateToSettingsTab(page);

		// Filter to advanced section
		await page.getByPlaceholder('Filter settings…').fill('advanced');

		// Expand the Advanced section
		const advancedBtn = page.getByRole('button', { name: 'Advanced', exact: true });
		await advancedBtn.scrollIntoViewIfNeeded();
		await advancedBtn.click();

		// Click "Add secret mount"
		await page.getByText('Add secret mount').click();

		// Fill in the secret name and mount path
		const secretNameInput = page.getByPlaceholder('k8s-secret-name');
		await secretNameInput.fill('my-tls-secret');

		const mountPathInput = page.getByPlaceholder('/etc/certs');
		await mountPathInput.fill('/etc/ssl/certs');

		// Click "Add" in the secret mount form
		await page.getByRole('button', { name: 'Add', exact: true }).last().click();

		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as {
				environments: Array<{
					name: string;
					secretMounts?: Array<{ name: string; secret: string; path: string }>;
				}>;
			};
			const env = spec.environments?.find(e => e.name === 'production');
			expect(env?.secretMounts).toEqual(
				expect.arrayContaining([
					expect.objectContaining({ secret: 'my-tls-secret', path: '/etc/ssl/certs' })
				])
			);
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });
	});

	test('Test 9: Add custom domain, verify via API', async ({ page, request }) => {
		await navigateToSettingsTab(page);

		// Filter to domains section
		await page.getByPlaceholder('Filter settings…').fill('domains');
		await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible({ timeout: 3_000 });

		const domainToAdd = `custom-${randomSuffix()}.example.com`;
		const domainInput = page.getByPlaceholder('custom.example.com');
		await domainInput.scrollIntoViewIfNeeded();
		await domainInput.fill(domainToAdd);

		await page.getByRole('button', { name: 'Add', exact: true }).click();

		await expect(async () => {
			const domains = await listDomainsViaAPI(request, token, project, appName);
			expect(domains.custom).toContain(domainToAdd);
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });
	});

	test('Test 10: Save TLS override, verify via API', async ({ page, request }) => {
		// The save may fail silently due to resource version conflicts from the
		// controller reconciling concurrently. Retry the entire fill+save flow.
		await expect(async () => {
			await navigateToSettingsTab(page);

			await page.getByPlaceholder('Filter settings…').fill('domains');
			await expect(page.getByRole('heading', { name: 'Domains' })).toBeVisible({ timeout: 3_000 });

			const tlsSummary = page.getByText('TLS overrides (advanced)');
			await tlsSummary.scrollIntoViewIfNeeded();
			await tlsSummary.click();

			const tlsIssuerInput = page.locator('#tls-issuer-ovr');
			await expect(tlsIssuerInput).toBeVisible({ timeout: 3_000 });
			await tlsIssuerInput.fill('letsencrypt-staging');

			await page.getByRole('button', { name: 'Save TLS overrides', exact: true }).click();

			await new Promise(r => setTimeout(r, 1_000));
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as {
				environments: Array<{
					name: string;
					tls?: { clusterIssuer?: string };
				}>;
			};
			const env = spec.environments?.find(e => e.name === 'production');
			expect(env?.tls?.clusterIssuer).toBe('letsencrypt-staging');
		}).toPass({ timeout: 20_000, intervals: [2_000, 3_000, 5_000] });
	});

	test('Test 12: Credentials section - add a credential, verify via API', async ({ page, request }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Variables', exact: true }).click();

		await expect(page.getByText('Exposed Credentials')).toBeVisible({ timeout: 5_000 });

		// Click the + button in the Exposed Credentials header
		const credHeader = page.locator('div[role="button"]').filter({ hasText: 'Exposed Credentials' });
		await credHeader.locator('button').click();

		await page.locator('#cred-name').fill('password');
		await page.locator('#cred-value').fill('secret123');
		await page.getByRole('button', { name: 'Add', exact: true }).click();

		// Verify the credential appears in the UI
		await expect(page.locator('.font-mono').filter({ hasText: 'password' })).toBeVisible({ timeout: 5_000 });

		// Verify via API
		await expect(async () => {
			const app = await getAppViaAPI(request, token, project, appName);
			const spec = app.spec as { credentials: Array<{ name: string; value?: string }> };
			expect(spec.credentials).toEqual(
				expect.arrayContaining([
					expect.objectContaining({ name: 'password' })
				])
			);
		}).toPass({ timeout: 10_000, intervals: [500, 1_000, 2_000] });
	});
});

// ---------------------------------------------------------------------------
// Bindings test (needs two apps)
// ---------------------------------------------------------------------------
test.describe('app settings - bindings', () => {
	let token: string;
	let project: string;
	let webAppName: string;
	let pgAppName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-bindings-${randomSuffix()}`;
		webAppName = `web-${randomSuffix()}`;
		pgAppName = `postgres-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
		await createAppViaAPI(request, token, project, webAppName, 'nginx:1.27');
		await createAppViaAPI(request, token, project, pgAppName, 'postgres:16');
	});

	test.afterAll(async ({ request }) => {
		await deleteAppViaAPI(request, token, project, webAppName);
		await deleteAppViaAPI(request, token, project, pgAppName);
		await deleteProjectViaAPI(request, token, project);
	});

	test('Test 13: Bindings in Variables tab - select app shows injected var preview', async ({ page }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${webAppName}`);
		await expect(page.getByRole('heading', { name: webAppName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Variables', exact: true }).click();

		// Expand bindings section and click + to add
		const bindingsHeader = page.locator('div[role="button"]').filter({ hasText: 'Bindings' });
		await expect(bindingsHeader).toBeVisible({ timeout: 5_000 });
		await bindingsHeader.locator('button').click();

		const bindingSelect = page.locator('#binding-ref');
		await expect(bindingSelect).toBeVisible({ timeout: 5_000 });

		// The postgres app should appear in the dropdown
		const options = bindingSelect.locator('option');
		const texts = await options.allTextContents();
		expect(texts).toContain(pgAppName);

		// Select the postgres app
		await bindingSelect.selectOption(pgAppName);

		// Should show injected variable preview
		const prefix = pgAppName.toUpperCase().replace(/[^A-Z0-9_]/g, '_');
		await expect(page.getByText(`${prefix}_HOST`)).toBeVisible({ timeout: 3_000 });
		await expect(page.getByText(`${prefix}_PORT`)).toBeVisible({ timeout: 3_000 });
		await expect(page.getByText(`${prefix}_URL`)).toBeVisible({ timeout: 3_000 });
	});
});

// ---------------------------------------------------------------------------
// Danger zone delete (needs its own app to delete)
// ---------------------------------------------------------------------------
test.describe('app settings - danger zone delete', () => {
	let token: string;
	let project: string;
	let deleteAppName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-danger-${randomSuffix()}`;
		deleteAppName = `del-app-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
		await createAppViaAPI(request, token, project, deleteAppName, 'nginx:1.27');
	});

	test.afterAll(async ({ request }) => {
		// Project cleanup (app should already be deleted by the test)
		await deleteAppViaAPI(request, token, project, deleteAppName);
		await deleteProjectViaAPI(request, token, project);
	});

	test('Test 11: Danger zone - delete app with confirmation, verify redirect + app gone', async ({ page, request }) => {
		await injectToken(page, token);
		await page.goto(`/projects/${project}/apps/${deleteAppName}`);
		await expect(page.getByRole('heading', { name: deleteAppName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Settings', exact: true }).click();
		await expect(page.getByPlaceholder('Filter settings…')).toBeVisible({ timeout: 5_000 });

		// Filter to danger section
		await page.getByPlaceholder('Filter settings…').fill('danger');

		// Click the initial "Delete" button to open the confirmation form
		const dangerDeleteBtn = page.getByRole('button', { name: 'Delete', exact: true });
		await dangerDeleteBtn.scrollIntoViewIfNeeded();
		await dangerDeleteBtn.click();

		// Confirm input appears with placeholder matching the app name
		const confirmInput = page.getByPlaceholder(deleteAppName);
		await expect(confirmInput).toBeVisible({ timeout: 3_000 });

		// "Delete App" button should be disabled until name is typed
		const deleteAppBtn = page.getByRole('button', { name: 'Delete App', exact: true });
		await expect(deleteAppBtn).toBeDisabled();

		// Type the app name to enable the button
		await confirmInput.fill(deleteAppName);
		await expect(deleteAppBtn).not.toBeDisabled();

		// Click "Delete App"
		await deleteAppBtn.click();

		// Should redirect back to the project canvas (URL may include ?env= query param)
		await expect(page).toHaveURL(new RegExp(`/projects/${project}(\\?|$)`), { timeout: 10_000 });

		// Verify app is gone via API
		await expect(async () => {
			const res = await request.get(
				`/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(deleteAppName)}`,
				{
					headers: { Authorization: `Bearer ${token}` },
					failOnStatusCode: false
				}
			);
			expect(res.status()).toBe(404);
		}).toPass({ timeout: 15_000, intervals: [1_000, 2_000, 3_000] });
	});
});
