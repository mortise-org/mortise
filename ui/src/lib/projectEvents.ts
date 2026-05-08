import { api } from './api';
import type { App, BuildLogsResponse, Pod } from '$lib/types';

export interface ProjectEventsCallbacks {
	onAppUpdated: (app: App) => void;
	onAppDeleted: (name: string) => void;
	onPods: (app: string, env: string, pods: Pod[]) => void;
	onBuildLog: (app: string, resp: BuildLogsResponse) => void;
}

export async function connectProjectEvents(
	project: string,
	callbacks: ProjectEventsCallbacks
): Promise<{ close: () => void }> {
	const sseToken = await api.fetchSSEToken();
	const params = new URLSearchParams();
	if (sseToken) params.set('token', sseToken);
	const url = `/api/projects/${encodeURIComponent(project)}/events?${params.toString()}`;

	const es = new EventSource(url);

	es.addEventListener('app.updated', (e: MessageEvent) => {
		callbacks.onAppUpdated(JSON.parse(e.data as string) as App);
	});

	es.addEventListener('app.deleted', (e: MessageEvent) => {
		const d = JSON.parse(e.data as string) as { name: string };
		callbacks.onAppDeleted(d.name);
	});

	es.addEventListener('pods', (e: MessageEvent) => {
		const d = JSON.parse(e.data as string) as { app: string; env: string; pods: Pod[] };
		callbacks.onPods(d.app, d.env, d.pods);
	});

	es.addEventListener('build.log', (e: MessageEvent) => {
		const d = JSON.parse(e.data as string) as BuildLogsResponse & { app: string };
		callbacks.onBuildLog(d.app, d);
	});

	return {
		close: () => es.close()
	};
}
