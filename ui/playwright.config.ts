import { defineConfig, devices } from '@playwright/test';

// Playwright config for Mortise UI end-to-end tests.
//
// The suite assumes an operator is already running and reachable at
// MORTISE_BASE_URL (default http://127.0.0.1:8080). It does NOT spin up a
// cluster itself — see `make test-e2e`.
const baseURL = process.env.MORTISE_BASE_URL ?? 'http://127.0.0.1:8080';

export default defineConfig({
	testDir: 'tests/e2e',
	globalSetup: './tests/e2e/global-setup.ts',
	globalTeardown: './tests/e2e/global-teardown.ts',
	fullyParallel: true,
	forbidOnly: !!process.env.CI,
	retries: process.env.CI ? 2 : 0,
	workers: 8,
	reporter: 'html',
	globalTimeout: 30 * 60 * 1000,
	// 60s covers beforeAll hooks that create projects under operator load.
	timeout: 60_000,
	use: {
		baseURL,
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure',
		video: 'retain-on-failure'
	},
	projects: [
		{
			name: 'chromium',
			use: { ...devices['Desktop Chrome'] }
		}
	]
});
