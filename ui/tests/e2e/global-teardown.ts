import { request as playwrightRequest } from '@playwright/test';

const BASE_URL = process.env.MORTISE_BASE_URL ?? 'http://127.0.0.1:8080';
const ADMIN_EMAIL = process.env.MORTISE_ADMIN_EMAIL;
const ADMIN_PASSWORD = process.env.MORTISE_ADMIN_PASSWORD;

// Safety-net cleanup: after the whole suite finishes, delete any lingering
// `e2e-*` projects. Individual tests do their own cleanup, but a failure
// mid-test (especially in journey.spec.ts where cleanup is the last step)
// leaks the project. The leaked namespace then hot-loops in the operator's
// reconciler on subsequent runs, stealing CPU from the tests.
export default async function globalTeardown(): Promise<void> {
	if (!ADMIN_EMAIL || !ADMIN_PASSWORD) return;

	const ctx = await playwrightRequest.newContext({ baseURL: BASE_URL });
	try {
		const loginRes = await ctx.post('/api/auth/login', {
			data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
			failOnStatusCode: false
		});
		if (!loginRes.ok()) return;
		const { token } = (await loginRes.json()) as { token?: string };
		if (!token) return;

		const listRes = await ctx.get('/api/projects', {
			headers: { Authorization: `Bearer ${token}` },
			failOnStatusCode: false
		});
		if (!listRes.ok()) return;
		const projects = (await listRes.json()) as Array<{ name: string }>;
		const stragglers = projects
			.map((p) => p.name)
			.filter((n) => n.startsWith('e2e-'));

		if (stragglers.length === 0) return;
		console.log(`[e2e teardown] sweeping ${stragglers.length} leaked project(s): ${stragglers.join(', ')}`);

		await Promise.all(
			stragglers.map((name) =>
				ctx.delete(`/api/projects/${encodeURIComponent(name)}`, {
					headers: { Authorization: `Bearer ${token}` },
					failOnStatusCode: false
				})
			)
		);
	} finally {
		await ctx.dispose();
	}
}
