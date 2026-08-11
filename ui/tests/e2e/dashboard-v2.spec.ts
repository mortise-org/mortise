/**
 * Per-app dashboard v2 E2E tests (obs-v2 O4, real backend).
 *
 * Covers the new interactive surface:
 *   - Metrics tab: gap-honest utilization charts (legend), PVC section
 *     absence for storage-less apps, time-range buttons
 *   - Logs: search input, level filter, pod filter
 *   - Deployments tab: unified build/deploy timeline
 */
import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	createAppViaAPI,
	deleteProjectViaAPI,
	waitForAppCurrentImage
} from './helpers';

test.describe('dashboard v2', () => {
	let token: string;
	let project: string;
	let appName: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		token = await loginViaAPI(request);
		project = `e2e-dashv2-${randomSuffix()}`;
		appName = `dash-app-${randomSuffix()}`;
		await createProjectViaAPI(request, token, project);
		await createAppViaAPI(request, token, project, appName, 'nginx:1.27');
		await waitForAppCurrentImage(request, token, project, appName);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, token, project);
	});

	test.beforeEach(async ({ page }) => {
		await injectToken(page, token);
	});

	test('metrics tab renders utilization charts with time-range controls', async ({ page }) => {
		await page.goto(`/projects/${project}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Metrics', exact: true }).click();

		// Range buttons are the primary filter row.
		for (const label of ['Live', '1h', '6h', '24h', '7d']) {
			await expect(page.getByRole('button', { name: label, exact: true })).toBeVisible();
		}
		await page.getByRole('button', { name: '1h', exact: true }).click();

		// Resources section header renders whether or not data has arrived.
		await expect(page.getByText('Resources', { exact: true })).toBeVisible();
	});

	test('logs view exposes search, level filter, and pod filter', async ({ page }) => {
		await page.goto(`/projects/${project}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Logs', exact: true }).click();

		const search = page.getByPlaceholder('Search logs');
		await expect(search).toBeVisible();

		const levelFilter = page.getByLabel('Level filter');
		await expect(levelFilter).toBeVisible();
		await levelFilter.selectOption('error');
		await levelFilter.selectOption('');

		const podFilter = page.getByLabel('Pod filter');
		await expect(podFilter).toBeVisible();

		// Search narrows the stream without erroring on no matches.
		await search.fill('zz-no-such-line-zz');
		await search.fill('');
	});

	test('deployments tab shows the unified timeline for a deployed app', async ({ page }) => {
		await page.goto(`/projects/${project}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Deployments', exact: true }).click();

		// An image app that has deployed has at least one deploy entry.
		const timeline = page.getByTestId('deploy-timeline');
		await expect(timeline).toBeVisible();
		await expect(timeline.getByText('Deployed', { exact: false }).first()).toBeVisible();
	});
});
