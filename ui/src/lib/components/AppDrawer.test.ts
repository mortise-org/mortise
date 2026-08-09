import { render } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { App, AppPhase } from '$lib/types';
import AppDrawer from './AppDrawer.svelte';

const { setDrawerTab, currentEnv, drawerStore } = vi.hoisted(() => {
	const setDrawerTab = vi.fn();
	const currentEnv = vi.fn(() => 'production');
	return {
		setDrawerTab,
		currentEnv,
		drawerStore: { drawerTab: 'deployments', setDrawerTab, currentEnv }
	};
});

vi.mock('$lib/store.svelte', () => ({ store: drawerStore }));
vi.mock('$lib/api', () => ({
	api: {
		redeploy: vi.fn(),
		rebuild: vi.fn(),
		rollback: vi.fn(),
		getEnv: vi.fn().mockResolvedValue([]),
		getSharedVars: vi.fn().mockResolvedValue([]),
		getBuildArgs: vi.fn().mockResolvedValue([]),
		listApps: vi.fn().mockResolvedValue([])
	}
}));

function appWithPhase(phase: AppPhase): App {
	return {
		metadata: { name: 'web', generation: 3 },
		spec: {
			source: { type: 'git', repo: 'https://example.test/repo.git' },
			environments: [{ name: 'production' }]
		},
		status: { phase, environments: [{ name: 'production', phase }] }
	};
}

describe('AppDrawer phase navigation', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		drawerStore.drawerTab = 'variables';
		currentEnv.mockReturnValue('production');
	});

	afterEach(() => vi.useRealTimers());

	it('does not navigate to Build Logs during background phase changes', async () => {
		const view = render(AppDrawer, {
			props: { project: 'demo', appName: 'web', liveApp: appWithPhase('Ready'), onClose: vi.fn() }
		});
		for (const phase of ['Building', 'Degraded', 'Failed'] as AppPhase[]) {
			await view.rerender({ project: 'demo', appName: 'web', liveApp: appWithPhase(phase), onClose: vi.fn() });
		}
		expect(setDrawerTab).not.toHaveBeenCalledWith('buildLogs');
		expect(drawerStore.drawerTab).toBe('variables');
		view.unmount();
	});
});
