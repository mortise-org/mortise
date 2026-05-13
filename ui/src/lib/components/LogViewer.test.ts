import { render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import LogViewer from './LogViewer.svelte';

const { logsURL } = vi.hoisted(() => ({
	logsURL: vi.fn()
}));

vi.mock('$lib/api', () => ({
	api: {
		logsURL
	},
	AuthRequiredError: class AuthRequiredError extends Error {}
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

describe('LogViewer', () => {
	beforeEach(() => {
		logsURL.mockResolvedValue('http://example.test/logs');
		FakeEventSource.reset();
		vi.stubGlobal('EventSource', FakeEventSource);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		vi.clearAllMocks();
	});

	it('surfaces fatal structured SSE events and keeps raw lines visible', async () => {
		render(LogViewer, {
			props: {
				project: 'demo',
				appName: 'web',
				env: 'production'
			}
		});

		await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));

		const source = FakeEventSource.instances[0];
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
		expect(screen.getAllByText(/\[pod-1\]/)).toHaveLength(2);
	});

	it('does not show the fatal banner for non-fatal events', async () => {
		render(LogViewer, {
			props: {
				project: 'demo',
				appName: 'web',
				env: 'production'
			}
		});

		await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));

		const source = FakeEventSource.instances[0];
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
