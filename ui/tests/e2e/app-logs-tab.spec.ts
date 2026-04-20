import { expect, test, type Page } from '@playwright/test';
import {
  randomSuffix,
  ensureAdmin,
  loginViaAPI,
  injectToken,
  createProjectViaAPI,
  createAppViaAPI,
  deleteProjectViaAPI
} from './helpers';

// ---------------------------------------------------------------------------
// LogsTab E2E tests
//
// Covers the rebuilt LogsTab (UI_SPEC §3.12):
//   - Live | Build sub-tab bar
//   - Live: env pills, always-visible pod picker, Previous toggle visibility
//     rules, time-range chips, Live tail switch, Copy/Clear
//   - Build: status badge, commit SHA, timestamp, image-source placeholder
//
// Tests hit the real Mortise backend (image-source app; no pods in
// integration envs). Two targeted /pods overrides simulate the restarted-pod
// case so we can assert Previous-toggle visibility rules without a crashing
// workload — everything else uses the real API per CLAUDE.md.
// ---------------------------------------------------------------------------

test.describe('app drawer Logs tab', () => {
  let token: string;
  let project: string;
  const appName = 'web-app';

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-logs-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);
    // Use the default single-env app from the helper (production only).
    // Tests that need multi-env create their own apps below.
    await createAppViaAPI(request, token, project, appName, 'nginx:1.27');
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  async function openLogsTab(page: Page) {
    await injectToken(page, token);
    await page.goto(`/projects/${project}/apps/${appName}`);
    // The AppDrawer is always rendered on this route — wait for its close button.
    await expect(page.getByRole('button', { name: 'Close drawer' })).toBeVisible({
      timeout: 15_000
    });
    await page.getByRole('button', { name: 'Logs', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Live', exact: true })).toBeVisible({
      timeout: 10_000
    });
  }

  // -------------------------------------------------------------------------
  // Tab bar + tab switching
  // -------------------------------------------------------------------------

  test('shows Live and Build sub-tab buttons', async ({ page }) => {
    await openLogsTab(page);
    await expect(page.getByRole('button', { name: 'Live', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Build', exact: true })).toBeVisible();
  });

  test('Build sub-tab shows image-source placeholder for image apps', async ({ page }) => {
    await openLogsTab(page);
    await page.getByRole('button', { name: 'Build', exact: true }).click();
    await expect(page.getByText('Image source — builds are skipped')).toBeVisible({
      timeout: 5_000
    });
  });

  // -------------------------------------------------------------------------
  // Live sub-tab: pod picker always visible, time-range chips, actions
  // -------------------------------------------------------------------------

  test('pod picker is always visible and defaults to "All pods"', async ({ page }) => {
    await openLogsTab(page);
    const picker = page.getByRole('combobox');
    await expect(picker).toBeVisible();
    await expect(picker).toContainText('All pods');
  });

  test('time-range chips render Now, 15m, 1h, 6h, 24h', async ({ page }) => {
    await openLogsTab(page);
    const main = page.getByRole('main');
    await expect(main.getByRole('button', { name: 'Now', exact: true })).toBeVisible();
    await expect(main.getByRole('button', { name: '15m', exact: true })).toBeVisible();
    await expect(main.getByRole('button', { name: '1h', exact: true })).toBeVisible();
    await expect(main.getByRole('button', { name: '6h', exact: true })).toBeVisible();
    await expect(main.getByRole('button', { name: '24h', exact: true })).toBeVisible();
  });

  test('selecting a non-Now range disables the Live tail switch', async ({ page }) => {
    await openLogsTab(page);
    const toggle = page.getByRole('switch', { name: 'Live tail' });
    await expect(toggle).toHaveAttribute('aria-checked', 'true');

    await page.getByRole('button', { name: '1h', exact: true }).click();
    await expect(toggle).toBeDisabled();
  });

  test('Live tab shows Copy and Clear buttons', async ({ page }) => {
    await openLogsTab(page);
    await expect(page.getByRole('button', { name: 'Copy', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Clear', exact: true })).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Previous toggle visibility rules — uses a targeted /pods mock to
  // simulate a restarted pod without relying on a real crashing workload.
  // -------------------------------------------------------------------------

  test('Previous toggle is hidden when "All pods" is selected', async ({ page }) => {
    await page.route(`**/api/projects/${project}/apps/${appName}/pods*`, (route) =>
      route.fulfill({
        json: [
          {
            name: `${appName}-abc123`,
            phase: 'Running',
            restartCount: 3,
            ready: true,
            createdAt: new Date().toISOString()
          }
        ]
      })
    );
    await openLogsTab(page);
    await expect(page.getByRole('button', { name: 'Previous', exact: true })).toHaveCount(0);
  });

  test('Previous toggle is hidden when selected pod has no restarts', async ({ page }) => {
    await page.route(`**/api/projects/${project}/apps/${appName}/pods*`, (route) =>
      route.fulfill({
        json: [
          {
            name: `${appName}-clean`,
            phase: 'Running',
            restartCount: 0,
            ready: true,
            createdAt: new Date().toISOString()
          }
        ]
      })
    );
    await openLogsTab(page);
    await page.getByRole('combobox').selectOption(`${appName}-clean`);
    await expect(page.getByRole('button', { name: 'Previous', exact: true })).toHaveCount(0);
  });

  test('Previous toggle appears when a restarted pod is selected', async ({ page }) => {
    await page.route(`**/api/projects/${project}/apps/${appName}/pods*`, (route) =>
      route.fulfill({
        json: [
          {
            name: `${appName}-flaky`,
            phase: 'Running',
            restartCount: 2,
            ready: true,
            createdAt: new Date().toISOString()
          }
        ]
      })
    );
    await openLogsTab(page);
    await page.getByRole('combobox').selectOption(`${appName}-flaky`);
    const prev = page.getByRole('button', { name: 'Previous', exact: true });
    await expect(prev).toBeVisible();
    await expect(prev).toHaveAttribute('aria-pressed', 'false');
    await prev.click();
    await expect(prev).toHaveAttribute('aria-pressed', 'true');
  });
});

// ---------------------------------------------------------------------------
// Multi-environment case — creates its own app with production + staging.
// Scopes env-pill queries to the drawer via `#app-drawer` to avoid colliding
// with the global env switcher in the top nav.
// ---------------------------------------------------------------------------

test.describe('app drawer Logs tab — multi-env', () => {
  let token: string;
  let project: string;
  const appName = 'multi-env-app';

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    token = await loginViaAPI(request);
    project = `e2e-logs-multi-${randomSuffix()}`;
    await createProjectViaAPI(request, token, project);

    const res = await request.post(`/api/projects/${project}/apps`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: appName,
        spec: {
          source: { type: 'image', image: 'nginx:1.27' },
          network: { public: true },
          environments: [
            { name: 'production', replicas: 1 },
            { name: 'staging', replicas: 1 }
          ]
        }
      }
    });
    if (!res.ok()) {
      throw new Error(`create multi-env app failed: HTTP ${res.status()}`);
    }
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, token, project);
  });

  test('shows env pills for each environment and switches selection', async ({ page }) => {
    await injectToken(page, token);
    await page.goto(`/projects/${project}/apps/${appName}`);
    await expect(page.getByRole('button', { name: 'Close drawer' })).toBeVisible({
      timeout: 15_000
    });
    await page.getByRole('button', { name: 'Logs', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Live', exact: true })).toBeVisible({
      timeout: 10_000
    });

    // Scope env-pill queries to main (drawer content lives there) to avoid
    // colliding with the top-nav env switcher, which renders the same names.
    const main = page.getByRole('main');
    const stagingBtn = main.getByRole('button', { name: 'staging', exact: true });
    const productionBtn = main.getByRole('button', { name: 'production', exact: true });
    await expect(productionBtn).toBeVisible();
    await expect(stagingBtn).toBeVisible();

    await stagingBtn.click();
    // Active pill carries bg-surface-600; inactive carries text-gray-400.
    const stagingClass = await stagingBtn.getAttribute('class');
    expect(stagingClass).toContain('bg-surface-600');
    const prodClass = await productionBtn.getAttribute('class');
    expect(prodClass).toContain('text-gray-400');
  });
});
