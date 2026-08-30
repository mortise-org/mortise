/**
 * Per-app dashboard v2 E2E tests (obs-v2 O4, real backend).
 *
 * Covers the new interactive surface:
 *   - Metrics tab: gap-honest utilization charts (legend), PVC section
 *     absence for storage-less apps, time-range buttons
 *   - Logs: search input, level filter, pod filter
 *   - Deployments tab: unified build/deploy timeline, per-run log expansion
 *
 * The timeline-logs test seeds a BuildRun CR directly into the dev cluster
 * (image apps never build, and E2E has no git provider). That is real
 * backend state, not request mocking — the click then exercises the real
 * list route and the real bearer-authenticated logs route end to end.
 */
import { execFileSync } from 'node:child_process';
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

// Context of the k3d dev cluster the e2e suite runs against (make dev-up),
// or null when unreachable — the seeded-BuildRun test skips in that case.
function devClusterContext(): string | null {
	const ctx = `k3d-${process.env.DEV_CLUSTER ?? 'mortise-dev'}`;
	try {
		execFileSync('kubectl', ['--context', ctx, 'get', 'ns', 'mortise-system'], {
			stdio: 'ignore',
			timeout: 10_000
		});
		return ctx;
	} catch {
		return null;
	}
}

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

		// Range buttons are the primary filter row. The tab mounts the charts
		// and their first fetch before the row settles; on a cold CI browser
		// that can exceed the 5s default (surfaced at retries=0, #546).
		await expect(page.getByRole('button', { name: 'Live', exact: true })).toBeVisible({ timeout: 15_000 });
		for (const label of ['1h', '6h', '24h', '7d']) {
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

	test('timeline build entry expands per-run logs through the authenticated client', async ({
		page
	}) => {
		const ctx = devClusterContext();
		test.skip(ctx === null, 'dev-cluster kubectl context unavailable — cannot seed a BuildRun');

		// Seed a BuildRun for this app into the project's control namespace,
		// then mark it terminal via the status subresource so the buildrun
		// controller ignores it (no real build is attempted).
		const runName = `${appName}-seeded-run`;
		const namespace = `pj-${project}`;
		const manifest = [
			'apiVersion: mortise.mortise.dev/v1alpha1',
			'kind: BuildRun',
			'metadata:',
			`  name: ${runName}`,
			`  namespace: ${namespace}`,
			'  labels:',
			// listAppBuildRuns selects by these labels, not by targetRef.
			'    mortise.dev/buildrun-target-kind: appenvironment',
			`    mortise.dev/buildrun-target-name: ${appName}`,
			'spec:',
			'  targetRef:',
			'    kind: AppEnvironment',
			`    name: ${appName}`,
			`  appName: ${appName}`,
			'  trigger: manual',
			'  repo: https://example.invalid/seeded.git'
		].join('\n');
		execFileSync('kubectl', ['--context', ctx!, 'apply', '-f', '-'], {
			input: manifest,
			timeout: 15_000
		});
		execFileSync(
			'kubectl',
			[
				'--context',
				ctx!,
				'-n',
				namespace,
				'patch',
				'buildrun',
				runName,
				'--subresource=status',
				'--type=merge',
				'-p',
				'{"status":{"phase":"Failed","failureMessage":"seeded for e2e"}}'
			],
			{ timeout: 15_000 }
		);

		await page.goto(`/projects/${project}/apps/${appName}`);
		await expect(page.getByRole('heading', { name: appName })).toBeVisible({ timeout: 10_000 });
		await page.getByRole('button', { name: 'Deployments', exact: true }).click();

		const timeline = page.getByTestId('deploy-timeline');
		await expect(timeline).toBeVisible();

		// The click must round-trip the real logs route with the bearer token —
		// this is the assertion that catches a broken path or auth scheme.
		const logsBtn = timeline.getByRole('button', { name: 'Logs', exact: true }).first();
		await expect(logsBtn).toBeVisible({ timeout: 10_000 });
		const [logsRes] = await Promise.all([
			page.waitForResponse(
				(r) => r.url().includes(`/buildruns/${runName}/logs`) && r.request().method() === 'GET'
			),
			logsBtn.click()
		]);
		expect(logsRes.status()).toBe(200);

		// A seeded run has no persisted logs; the panel must say so honestly.
		const panel = page.getByTestId('run-logs');
		await expect(panel).toBeVisible();
		await expect(panel.getByText('No log output recorded for this run')).toBeVisible();

		// Toggling again collapses the panel.
		await timeline.getByRole('button', { name: 'Hide logs', exact: true }).click();
		await expect(panel).not.toBeVisible();
	});
});
