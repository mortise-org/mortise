import { request as playwrightRequest } from '@playwright/test';

const BASE_URL = process.env.MORTISE_BASE_URL ?? 'http://127.0.0.1:8080';
const ADMIN_EMAIL = process.env.MORTISE_ADMIN_EMAIL;
const ADMIN_PASSWORD = process.env.MORTISE_ADMIN_PASSWORD;

export default async function globalSetup(): Promise<void> {
	if (!ADMIN_EMAIL || !ADMIN_PASSWORD) {
		throw new Error(
			'MORTISE_ADMIN_EMAIL and MORTISE_ADMIN_PASSWORD must be set for the E2E suite.'
		);
	}

	const ctx = await playwrightRequest.newContext({ baseURL: BASE_URL });
	try {
		const res = await ctx.post('/api/auth/setup', {
			data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
			failOnStatusCode: false
		});
		if (res.status() !== 201 && res.status() !== 409) {
			const body = await res.text().catch(() => '');
			throw new Error(`global admin setup failed: HTTP ${res.status()} ${body}`);
		}
	} finally {
		await ctx.dispose();
	}
}
