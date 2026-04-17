/**
 * Shared helpers for Mortise UI Playwright E2E tests.
 *
 * Every test file imports from here so that admin bootstrap, login,
 * project/app creation, and cleanup are consistent across the suite.
 */
import { expect, type APIRequestContext, type Page } from '@playwright/test';

export const BASE_URL = process.env.MORTISE_BASE_URL ?? 'http://127.0.0.1:8080';
export const ADMIN_EMAIL = process.env.MORTISE_ADMIN_EMAIL!;
export const ADMIN_PASSWORD = process.env.MORTISE_ADMIN_PASSWORD!;

if (!ADMIN_EMAIL || !ADMIN_PASSWORD) {
	throw new Error(
		'MORTISE_ADMIN_EMAIL and MORTISE_ADMIN_PASSWORD must be set for the E2E suite.'
	);
}

/** Random 6-char suffix safe for DNS labels. */
export function randomSuffix(): string {
	return Math.random().toString(36).slice(2, 8);
}

/**
 * Idempotent admin bootstrap. 409 = already set up, which is fine.
 */
export async function ensureAdmin(request: APIRequestContext): Promise<void> {
	const res = await request.post('/api/auth/setup', {
		data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
		failOnStatusCode: false
	});
	if (res.status() === 409) return;
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`admin setup failed: HTTP ${res.status()} ${body}`);
	}
}

/** Get a JWT token via the login API. */
export async function loginViaAPI(request: APIRequestContext): Promise<string> {
	const res = await request.post('/api/auth/login', {
		data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD }
	});
	if (!res.ok()) {
		throw new Error(`login failed: HTTP ${res.status()}`);
	}
	const body = await res.json();
	if (!body.token) throw new Error('login response missing token');
	return body.token as string;
}

/** Full browser-driven login flow. Ends at /. */
export async function loginViaUI(page: Page): Promise<void> {
	await page.goto('/login');
	await page.getByLabel('Email').fill(ADMIN_EMAIL);
	await page.getByLabel('Password').fill(ADMIN_PASSWORD);
	await Promise.all([
		page.waitForURL((url) => url.pathname === '/'),
		page.getByRole('button', { name: 'Sign in' }).click()
	]);
}

/**
 * Inject a JWT token into localStorage so subsequent page.goto() calls
 * are authenticated without needing the login UI.
 */
export async function injectToken(page: Page, token: string): Promise<void> {
	await page.goto('/login');
	await page.evaluate((t) => localStorage.setItem('token', t), token);
}

/** Create a project via the API and wait for its namespace to be ready. */
export async function createProjectViaAPI(
	request: APIRequestContext,
	token: string,
	name: string,
	description?: string
): Promise<string> {
	const res = await request.post('/api/projects', {
		headers: { Authorization: `Bearer ${token}` },
		data: { name, description }
	});
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`create project failed: HTTP ${res.status()} ${body}`);
	}
	// The project controller creates the namespace asynchronously.
	// Poll until creating an app in this project would succeed (namespace exists).
	for (let i = 0; i < 30; i++) {
		const check = await request.get(
			`/api/projects/${encodeURIComponent(name)}/apps`,
			{ headers: { Authorization: `Bearer ${token}` }, failOnStatusCode: false }
		);
		if (check.ok()) return name;
		await new Promise((r) => setTimeout(r, 500));
	}
	throw new Error(`project ${name}: namespace not ready after 15s`);
}

/** Create an image-source app via the API. */
export async function createAppViaAPI(
	request: APIRequestContext,
	token: string,
	project: string,
	appName: string,
	image: string = 'nginx:1.27'
): Promise<void> {
	const res = await request.post(`/api/projects/${encodeURIComponent(project)}/apps`, {
		headers: { Authorization: `Bearer ${token}` },
		data: {
			name: appName,
			spec: {
				source: { type: 'image', image },
				network: { public: true },
				environments: [{ name: 'production', replicas: 1 }]
			}
		}
	});
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`create app failed: HTTP ${res.status()} ${body}`);
	}
}

/** Delete a project via the API (best-effort, swallows errors). */
export async function deleteProjectViaAPI(
	request: APIRequestContext,
	token: string,
	name: string
): Promise<void> {
	try {
		await request.delete(`/api/projects/${encodeURIComponent(name)}`, {
			headers: { Authorization: `Bearer ${token}` },
			failOnStatusCode: false
		});
	} catch {
		// swallow
	}
}

/** Delete an app via the API (best-effort, swallows errors). */
export async function deleteAppViaAPI(
	request: APIRequestContext,
	token: string,
	project: string,
	appName: string
): Promise<void> {
	try {
		await request.delete(
			`/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}`,
			{
				headers: { Authorization: `Bearer ${token}` },
				failOnStatusCode: false
			}
		);
	} catch {
		// swallow
	}
}

/** Delete a git provider via the API (best-effort, swallows errors). */
export async function deleteGitProviderViaAPI(
	request: APIRequestContext,
	token: string,
	name: string
): Promise<void> {
	try {
		await request.delete(`/api/gitproviders/${encodeURIComponent(name)}`, {
			headers: { Authorization: `Bearer ${token}` },
			failOnStatusCode: false
		});
	} catch {
		// swallow
	}
}

/**
 * Wait for an element matching `locator` to appear within `timeout` ms.
 * Returns the locator for chaining.
 */
export async function waitForVisible(page: Page, text: string, timeout = 10_000) {
	const loc = page.getByText(text);
	await expect(loc).toBeVisible({ timeout });
	return loc;
}

/** Fetch a single app via the API. Returns the full App CRD object. */
export async function getAppViaAPI(
	request: APIRequestContext,
	token: string,
	project: string,
	appName: string
): Promise<Record<string, unknown>> {
	const res = await request.get(
		`/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}`,
		{ headers: { Authorization: `Bearer ${token}` } }
	);
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`getAppViaAPI failed: HTTP ${res.status()} ${body}`);
	}
	return (await res.json()) as Record<string, unknown>;
}

/** Fetch env vars for an app's environment. Returns [{name, value}, ...]. */
export async function getEnvViaAPI(
	request: APIRequestContext,
	token: string,
	project: string,
	appName: string,
	environment: string = 'production'
): Promise<Array<{ name: string; value: string }>> {
	const res = await request.get(
		`/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}/env?environment=${encodeURIComponent(environment)}`,
		{ headers: { Authorization: `Bearer ${token}` } }
	);
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`getEnvViaAPI failed: HTTP ${res.status()} ${body}`);
	}
	return (await res.json()) as Array<{ name: string; value: string }>;
}

/** List secrets for an app. Returns [{name, keys}, ...]. */
export async function listSecretsViaAPI(
	request: APIRequestContext,
	token: string,
	project: string,
	appName: string
): Promise<Array<{ name: string; keys: string[] }>> {
	const res = await request.get(
		`/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}/secrets`,
		{ headers: { Authorization: `Bearer ${token}` } }
	);
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`listSecretsViaAPI failed: HTTP ${res.status()} ${body}`);
	}
	return (await res.json()) as Array<{ name: string; keys: string[] }>;
}

/** List domains for an app's environment. Returns {primary, custom}. */
export async function listDomainsViaAPI(
	request: APIRequestContext,
	token: string,
	project: string,
	appName: string,
	environment: string = 'production'
): Promise<{ primary: string; custom: string[] }> {
	const res = await request.get(
		`/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}/domains?environment=${encodeURIComponent(environment)}`,
		{ headers: { Authorization: `Bearer ${token}` } }
	);
	if (!res.ok()) {
		const body = await res.text().catch(() => '');
		throw new Error(`listDomainsViaAPI failed: HTTP ${res.status()} ${body}`);
	}
	return (await res.json()) as { primary: string; custom: string[] };
}

/** Delete a secret via the API (best-effort, swallows errors). */
export async function deleteSecretViaAPI(
	request: APIRequestContext,
	token: string,
	project: string,
	appName: string,
	secretName: string
): Promise<void> {
	try {
		await request.delete(
			`/api/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(appName)}/secrets/${encodeURIComponent(secretName)}`,
			{ headers: { Authorization: `Bearer ${token}` }, failOnStatusCode: false }
		);
	} catch {
		// swallow
	}
}
