import { describe, expect, it } from 'vitest';

import { appPhaseForEnvironment, type App } from './types';

function makeApp(status: App['status']): App {
	return {
		metadata: { name: 'web' },
		spec: {
			source: { type: 'git' },
			environments: [{ name: 'production' }, { name: 'staging' }]
		},
		status
	};
}

describe('appPhaseForEnvironment', () => {
	it('prefers an in-progress build run for the selected environment', () => {
		const app = makeApp({
			phase: 'Building',
			environments: [
				{
					name: 'production',
					phase: 'Ready',
					currentBuildRunRef: { name: 'run-1', phase: 'Running' }
				},
				{
					name: 'staging',
					phase: 'Ready'
				}
			]
		});

		expect(appPhaseForEnvironment(app, 'production')).toBe('Building');
		expect(appPhaseForEnvironment(app, 'staging')).toBe('Ready');
	});

	it('does not let a stale failed build ref override the environment phase', () => {
		const app = makeApp({
			phase: 'Failed',
			environments: [
				{
					name: 'production',
					phase: 'Deploying',
					currentBuildRunRef: { name: 'run-2', phase: 'Failed' }
				}
			]
		});

		expect(appPhaseForEnvironment(app, 'production')).toBe('Deploying');
	});

	it('falls back to the top-level phase only when no per-environment status exists', () => {
		const app = makeApp({
			phase: 'Pending',
			environments: []
		});

		expect(appPhaseForEnvironment(app, 'production')).toBe('Pending');
	});
});
