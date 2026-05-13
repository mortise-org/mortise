import { render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { App } from '$lib/types';
import DeployLogsTab from './DeployLogsTab.svelte';

const { logsURL, listPods, getLogHistory, currentEnv } = vi.hoisted(() => ({
	logsURL: vi.fn(),
	listPods: vi.fn(),
	getLogHistory: vi.fn(),
	currentEnv: vi.fn()
}));

vi.mock('$lib/api', () => ({
	api: {
		logsURL,
		listPods,
		getLogHistory
	},
	AuthRequiredError: class AuthRequiredError extends Error {}
}));

vi.mock('$lib/store.svelte', () => ({
	store: {
		currentEnv
	}
}));

class FakeEventSource {
	static instances: FakeEventSource[] = [];

	onopen: (() => void) | null = null;
	onmessage: ((event: MessageEvent) => void) | null = null;
	onerror: (() => void) | null = null;
	close = vi.fn();

	constructor(public readonly url: string) {
		FakeEventSource.instances.push(this);
	}

	emitOpen() {
		this.onopen?.();
	}

	emitMessage(data: string) {
		this.onmessage?.({ data } as MessageEvent);
	}

	static reset() {
		FakeEventSource.instances = [];
	}
}

const app: App = {
	metadata: { name: 'web' },
	spec: {
		source: { type: 'image', image: 'nginx:1.27' },
		environments: [{ name: 'production', replicas: 1 }]
	},
	status: { phase: 'Ready' }
};

describe('DeployLogsTab', () => {
	beforeEach(() => {
		logsURL.mockResolvedValue('http://example.test/logs');
		listPods.mockResolvedValue([
			{
				name: 'pod-1',
				phase: 'Running',
				restartCount: 0,
				ready: true,
				createdAt: '2026-05-11T00:00:00Z'
			}
		]);
		getLogHistory.mockResolvedValue({ available: true, lines: [], hasMore: false });
		currentEnv.mockReturnValue('production');
		FakeEventSource.reset();
		vi.stubGlobal('EventSource', FakeEventSource);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		vi.clearAllMocks();
	});

	it('renders fatal stream-open failures distinctly while preserving surrounding log lines', async () => {
		render(DeployLogsTab, {
			props: {
				project: 'demo',
				app
			}
		});

		await waitFor(() => expect(FakeEventSource.instances.length).toBeGreaterThan(0));

		const source = FakeEventSource.instances.at(-1);
		if (!source) {
			throw new Error('expected an EventSource instance');
		}

		source.emitOpen();
		source.emitMessage(
			JSON.stringify({
				pod: 'pod-1',
				ts: '2026-05-11T00:00:00Z',
				line: 'error opening log stream: permission denied',
				stream: 'stderr',
				kind: 'error',
				code: 'stream_open_failed',
				fatal: true
			})
		);
		source.emitMessage(
			JSON.stringify({
				pod: 'pod-1',
				ts: '2026-05-11T00:00:01Z',
				line: 'normal line',
				stream: 'stdout'
			})
		);

		expect(await screen.findByRole('alert')).toHaveTextContent(
			'Fatal log stream error · stream_open_failed'
		);
		expect(screen.getByText('error opening log stream: permission denied')).toBeVisible();
		expect(screen.getByText('normal line')).toBeVisible();
		expect(screen.getAllByText('pod-1')).not.toHaveLength(0);
	});

	it('does not show the fatal banner for non-fatal error events', async () => {
		render(DeployLogsTab, {
			props: {
				project: 'demo',
				app
			}
		});

		await waitFor(() => expect(FakeEventSource.instances.length).toBeGreaterThan(0));

		const source = FakeEventSource.instances.at(-1);
		if (!source) {
			throw new Error('expected an EventSource instance');
		}

		source.emitOpen();
		source.emitMessage(
			JSON.stringify({
				pod: 'pod-1',
				ts: '2026-05-11T00:00:00Z',
				line: 'some error happened',
				stream: 'stderr',
				kind: 'error',
				code: 'transient_failure'
			})
		);
		source.emitMessage(
			JSON.stringify({
				pod: 'pod-1',
				ts: '2026-05-11T00:00:01Z',
				line: 'normal line',
				stream: 'stdout'
			})
		);

		expect(await screen.findByText('normal line')).toBeVisible();
		expect(screen.getByText('some error happened')).toBeVisible();
		expect(screen.queryByRole('alert')).toBeNull();
	});
});
