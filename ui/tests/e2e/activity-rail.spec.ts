import { expect, test } from '@playwright/test';
import {
	ensureAdmin,
	loginViaAPI,
	injectToken,
	randomSuffix,
	createProjectViaAPI,
	deleteProjectViaAPI
} from './helpers';

// Baseline coverage for the activity rail: open it, see recorded activity,
// close it. The Load-more control only renders past one page (100+ events),
// which real-API seeding cannot reasonably produce here — its pagination
// logic is covered at the store and handler layers
// (internal/activity/option_c_test.go, internal/api/activity_test.go).
test.describe('activity rail', () => {
	let adminToken: string;
	let project: string;

	test.beforeAll(async ({ request }) => {
		await ensureAdmin(request);
		adminToken = await loginViaAPI(request);
		project = `act-rail-${randomSuffix()}`;
		await createProjectViaAPI(request, adminToken, project);
	});

	test.afterAll(async ({ request }) => {
		await deleteProjectViaAPI(request, adminToken, project);
	});

	test('opens, shows project activity, closes', async ({ page }) => {
		await injectToken(page, adminToken);
		await page.goto(`/projects/${project}`);

		await page.getByTitle('Activity', { exact: true }).click();
		await expect(page.getByRole('heading', { name: 'Activity', exact: true })).toBeVisible();

		// Project creation is backfilled into activity by the controller.
		await expect(page.getByText(`Created project ${project}`, { exact: true })).toBeVisible();

		await page.getByRole('button', { name: 'Close activity rail', exact: true }).click();
		await expect(page.getByRole('heading', { name: 'Activity', exact: true })).not.toBeVisible();
	});
});
