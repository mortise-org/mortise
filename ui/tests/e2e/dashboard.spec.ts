import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	deleteProjectViaAPI,
	createAppViaAPI
} from './helpers';

// Cluster rollup dashboard (obs-v2 O5): strip, apps table, project health
// cards, activity feed. Metrics columns show "—" here — the e2e cluster has
// no adapter usage for a just-created app, and the dashboard's honesty
// contract is exactly that absence renders as absence.
test.describe('cluster dashboard', () => {
	let adminToken: string;
	let project: string;
	const appName = `dash-web-${randomSuffix()}`;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		project = `dash-${randomSuffix()}`;
		await createProjectViaAPI(request, adminToken, project);
		await createAppViaAPI(request, adminToken, project, appName);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, project);
	});

	test('renders strip, apps table, project health, and activity', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto('/');

		await page.getByTitle('Dashboard', { exact: true }).click();
		await expect(page).toHaveURL(/\/dashboard$/);
		await expect(page.getByRole('heading', { name: 'Dashboard', exact: true })).toBeVisible();

		// Strip cards: counts include the seeded project and app.
		await expect(page.getByText('Projects', { exact: true })).toBeVisible();
		await expect(page.getByText('Builds', { exact: true })).toBeVisible();

		// Apps table row links to the project canvas.
		await expect(page.getByRole('heading', { name: 'Apps', exact: true })).toBeVisible();
		const row = page.getByRole('link', { name: `${project} / ${appName}`, exact: true });
		await expect(row).toBeVisible();

		// Project health card carries the seeded env.
		await expect(page.getByRole('heading', { name: 'Project environments', exact: true })).toBeVisible();

		// Activity feed shows the project-creation event.
		await expect(page.getByRole('heading', { name: 'Recent activity', exact: true })).toBeVisible();
		await expect(page.getByText(`Created project ${project}`, { exact: true })).toBeVisible();

		// Refresh button is wired.
		await page.getByRole('button', { name: 'Refresh', exact: true }).click();
		await expect(page.getByRole('heading', { name: 'Apps', exact: true })).toBeVisible();

		// Row click-through lands on the project page. The project route may
		// carry an env query param (added by the O4 dashboard work), so
		// anchor on the path and tolerate a query string.
		//
		// Refresh above refetches and re-renders the table; clicking the row
		// while it is being replaced lands on a detached element and no
		// navigation happens (first failure seen the moment retries went to
		// zero, #546). Re-assert the row after the refresh, then click.
		await expect(row).toBeVisible();
		await row.click();
		await expect(page).toHaveURL(new RegExp(`/projects/${project}(\\?.*)?$`), { timeout: 15_000 });
	});
});
