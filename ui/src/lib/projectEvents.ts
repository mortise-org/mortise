import { AuthRequiredError, api } from './api';
import type { App, BuildLogsResponse, Pod } from '$lib/types';

export interface ProjectEventsCallbacks {
	onAppUpdated: (app: App) => void;
	onAppDeleted: (name: string) => void;
	onPods: (app: string, env: string, pods: Pod[]) => void;
	onBuildLog: (app: string, resp: BuildLogsResponse) => void;
}

export function connectProjectEvents(
	project: string,
	callbacks: ProjectEventsCallbacks
): { close: () => void } {
	let es: EventSource | null = null;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	let closed = false;
	let connectionID = 0;

	function clearReconnectTimer() {
		if (reconnectTimer) {
			clearTimeout(reconnectTimer);
			reconnectTimer = null;
		}
	}

	function closeEventSource(target?: EventSource) {
		if (target) {
			target.close();
			if (es === target) {
				es = null;
			}
			return;
		}
		if (es) {
			es.close();
			es = null;
		}
	}

	function scheduleReconnect() {
		if (closed || reconnectTimer) return;
		reconnectTimer = setTimeout(() => {
			reconnectTimer = null;
			void connect();
		}, 2000);
	}

	async function connect() {
		const id = ++connectionID;
		clearReconnectTimer();
		closeEventSource();

		try {
			const sseToken = await api.fetchSSEToken();
			if (closed || id !== connectionID) return;

			const params = new URLSearchParams({ token: sseToken });
			const url = `/api/projects/${encodeURIComponent(project)}/events?${params.toString()}`;
			const next = new EventSource(url);
			if (closed || id !== connectionID) {
				next.close();
				return;
			}

			es = next;

			next.addEventListener('app.updated', (e: MessageEvent) => {
				if (closed || id !== connectionID || es !== next) return;
				callbacks.onAppUpdated(JSON.parse(e.data as string) as App);
			});

			next.addEventListener('app.deleted', (e: MessageEvent) => {
				if (closed || id !== connectionID || es !== next) return;
				const d = JSON.parse(e.data as string) as { name: string };
				callbacks.onAppDeleted(d.name);
			});

			next.addEventListener('pods', (e: MessageEvent) => {
				if (closed || id !== connectionID || es !== next) return;
				const d = JSON.parse(e.data as string) as { app: string; env: string; pods: Pod[] };
				callbacks.onPods(d.app, d.env, d.pods);
			});

			next.addEventListener('build.log', (e: MessageEvent) => {
				if (closed || id !== connectionID || es !== next) return;
				const d = JSON.parse(e.data as string) as BuildLogsResponse & { app: string };
				callbacks.onBuildLog(d.app, d);
			});

			next.onerror = () => {
				if (closed || id !== connectionID || es !== next) return;
				closeEventSource(next);
				scheduleReconnect();
			};
		} catch (error) {
			if (closed || id !== connectionID) return;
			if (error instanceof AuthRequiredError) {
				close();
				return;
			}
			scheduleReconnect();
		}
	}

	function close() {
		closed = true;
		connectionID += 1;
		clearReconnectTimer();
		closeEventSource();
	}

	void connect();

	return {
		close
	};
}
