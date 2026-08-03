import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { App, AppPhase, AppSpec } from '$lib/types';
import SettingsTab from './SettingsTab.svelte';

const { updateApp, currentEnv } = vi.hoisted(() => ({
	updateApp: vi.fn(),
	currentEnv: vi.fn(() => 'production')
}));

vi.mock('$lib/store.svelte', () => ({ store: { currentEnv } }));
vi.mock('$lib/api', () => ({
	api: {
		updateApp,
		listDomains: vi.fn().mockResolvedValue({ primary: '', custom: [] }),
		listTokens: vi.fn().mockResolvedValue([])
	}
}));

function appAt(generation: number, overrides: Partial<AppSpec> = {}, phase = 'Ready'): App {
	return {
		metadata: { name: 'web', generation, resourceVersion: String(generation) },
		spec: {
			source: { type: 'git', repo: 'https://example.test/original.git', branch: 'main', path: '/' },
			network: { public: true, port: 8080 },
			environments: [{ name: 'production', replicas: 1, resources: { cpu: '100m', memory: '128Mi' } }],
			...overrides
		},
		status: { phase: phase as AppPhase }
	};
}

describe('SettingsTab live update coordination', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		currentEnv.mockReturnValue('production');
	});

	it('preserves drafts, expansion, and filter state across same-generation status updates and filtering', async () => {
		const first = appAt(7);
		const view = render(SettingsTab, { props: { project: 'demo', app: first, onAppDeleted: vi.fn() } });

		const repo = await screen.findByLabelText('Repository') as HTMLInputElement;
		const port = screen.getByLabelText('Port') as HTMLInputElement;
		const replicas = screen.getByLabelText('Replicas') as HTMLInputElement;
		await fireEvent.input(repo, { target: { value: 'https://example.test/draft.git' } });
		await fireEvent.input(port, { target: { value: '9090' } });
		await fireEvent.input(replicas, { target: { value: '4' } });

		await fireEvent.click(screen.getByText('TLS overrides (advanced)'));
		const issuer = screen.getByLabelText('Cluster issuer override') as HTMLInputElement;
		await fireEvent.input(issuer, { target: { value: 'draft-issuer' } });

		await fireEvent.click(screen.getByRole('button', { name: 'Advanced' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Add annotation' }));
		const annotationKey = screen.getByPlaceholderText('annotation.example.com/key') as HTMLInputElement;
		await fireEvent.input(annotationKey, { target: { value: 'example.test/draft' } });

		const filter = screen.getByPlaceholderText('Filter settings…') as HTMLInputElement;
		await fireEvent.input(filter, { target: { value: 'domains' } });
		expect(repo.value).toBe('https://example.test/draft.git');
		await fireEvent.input(filter, { target: { value: '' } });
		expect(screen.getByLabelText('Repository')).toBe(repo);

		await view.rerender({ project: 'demo', app: appAt(7, {}, 'Building'), onAppDeleted: vi.fn() });
		expect(repo.value).toBe('https://example.test/draft.git');
		expect(port.value).toBe('9090');
		expect(replicas.value).toBe('4');
		expect(issuer.value).toBe('draft-issuer');
		expect(annotationKey.value).toBe('example.test/draft');
		expect(filter.value).toBe('');
		expect(screen.getByPlaceholderText('annotation.example.com/key')).toBeVisible();
	});

	it('refreshes pristine fields but warns and preserves dirty fields on a newer generation', async () => {
		const view = render(SettingsTab, { props: { project: 'demo', app: appAt(10), onAppDeleted: vi.fn() } });
		const repo = await screen.findByLabelText('Repository') as HTMLInputElement;

		await view.rerender({
			project: 'demo',
			app: appAt(11, { source: { type: 'git', repo: 'https://example.test/server.git', branch: 'main' } }),
			onAppDeleted: vi.fn()
		});
		await waitFor(() => expect(repo.value).toBe('https://example.test/server.git'));

		await fireEvent.input(repo, { target: { value: 'https://example.test/local.git' } });
		const externallyChanged = appAt(12, {
			source: { type: 'git', repo: 'https://example.test/other.git', branch: 'release' },
			network: { public: false, port: 7070 },
			storage: [{ name: 'external', mountPath: '/external' }]
		});
		await view.rerender({ project: 'demo', app: externallyChanged, onAppDeleted: vi.fn() });
		expect(repo.value).toBe('https://example.test/local.git');
		expect(screen.getByRole('alert')).toHaveTextContent('This app changed elsewhere. Your unsaved edits are preserved.');

		await fireEvent.click(screen.getByRole('button', { name: 'Keep editing' }));
		expect(repo.value).toBe('https://example.test/local.git');
		expect(screen.queryByRole('alert')).toBeNull();

		await view.rerender({ project: 'demo', app: appAt(13, { ...externallyChanged.spec, network: { public: false, port: 6060 } }), onAppDeleted: vi.fn() });
		expect(screen.getByRole('alert')).toBeVisible();
		await fireEvent.click(screen.getByRole('button', { name: 'Reload latest' }));
		await waitFor(() => expect(repo.value).toBe('https://example.test/other.git'));
	});

	it('saves a dirty section from the latest accepted external spec', async () => {
		const view = render(SettingsTab, { props: { project: 'demo', app: appAt(20), onAppDeleted: vi.fn() } });
		const repo = await screen.findByLabelText('Repository') as HTMLInputElement;
		const port = screen.getByLabelText('Port') as HTMLInputElement;
		await fireEvent.input(repo, { target: { value: 'https://example.test/local.git' } });
		await fireEvent.input(port, { target: { value: '9999' } });

		const external = appAt(21, {
			source: { type: 'git', repo: 'https://example.test/server.git', branch: 'release' },
			network: { public: false, port: 5050 },
			storage: [{ name: 'server-volume', mountPath: '/data' }]
		});
		await view.rerender({ project: 'demo', app: external, onAppDeleted: vi.fn() });
		updateApp.mockImplementation(async (_project: string, _name: string, spec: AppSpec) => ({
			...external,
			metadata: { ...external.metadata, generation: 22 },
			spec
		}));

		await fireEvent.click(screen.getAllByRole('button', { name: 'Update' })[0]);
		await waitFor(() => expect(updateApp).toHaveBeenCalledOnce());
		const payload = updateApp.mock.calls[0][2] as AppSpec;
		expect(payload.source.repo).toBe('https://example.test/local.git');
		expect(payload.source.branch).toBe('main');
		expect(payload.network).toEqual({ public: false, port: 5050 });
		expect(payload.storage).toEqual([{ name: 'server-volume', mountPath: '/data' }]);

		await fireEvent.click(screen.getByRole('button', { name: 'Reload latest' }));
		await waitFor(() => expect(port.value).toBe('5050'));
		expect(repo.value).toBe('https://example.test/local.git');
	});

	it('keeps sibling drafts dirty when another form in the same visual section saves', async () => {
		const first = appAt(30);
		const view = render(SettingsTab, { props: { project: 'demo', app: first, onAppDeleted: vi.fn() } });
		await screen.findByLabelText('Repository');
		await fireEvent.click(screen.getByText('TLS overrides (advanced)'));
		const issuer = screen.getByLabelText('Cluster issuer override') as HTMLInputElement;
		const primary = screen.getByPlaceholderText('app.example.com') as HTMLInputElement;
		await fireEvent.input(issuer, { target: { value: 'unsaved-issuer' } });
		await fireEvent.input(primary, { target: { value: 'app.example.test' } });

		updateApp.mockImplementation(async (_project: string, _name: string, spec: AppSpec) => ({
			...first,
			metadata: { ...first.metadata, generation: 31 },
			spec
		}));
		await fireEvent.click(screen.getByRole('button', { name: 'Save TLS overrides' }));
		await waitFor(() => expect(updateApp).toHaveBeenCalledOnce());

		const external = appAt(32, {
			...first.spec,
			environments: [{ name: 'production', replicas: 2 }]
		});
		await view.rerender({ project: 'demo', app: external, onAppDeleted: vi.fn() });
		expect(primary.value).toBe('app.example.test');
		expect(screen.getByRole('alert')).toHaveTextContent('unsaved edits are preserved');
	});
});
