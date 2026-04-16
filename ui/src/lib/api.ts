import { goto } from '$app/navigation';
import type {
	App,
	AppSpec,
	CreateGitProviderRequest,
	GitProviderSummary,
	Project,
	SecretResponse
} from './types';

const BASE = '/api';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const token = localStorage.getItem('token');
	const headers: Record<string, string> = {
		'Content-Type': 'application/json',
		...(init?.headers as Record<string, string>)
	};
	if (token) {
		headers['Authorization'] = `Bearer ${token}`;
	}

	const res = await fetch(`${BASE}${path}`, { ...init, headers });

	if (res.status === 401) {
		localStorage.removeItem('token');
		goto('/login');
		throw new Error('Unauthorized');
	}

	if (!res.ok) {
		const body = await res.json().catch(() => ({ error: res.statusText }));
		throw new Error(body.error || res.statusText);
	}

	// 204s and empty bodies — return undefined as T.
	if (res.status === 204) {
		return undefined as T;
	}
	const text = await res.text();
	if (!text) {
		return undefined as T;
	}
	return JSON.parse(text) as T;
}

function enc(s: string): string {
	return encodeURIComponent(s);
}

export const api = {
	// --- projects ---
	listProjects: () => request<Project[]>('/projects'),
	createProject: (name: string, description?: string) =>
		request<Project>('/projects', {
			method: 'POST',
			body: JSON.stringify({ name, description })
		}),
	getProject: (name: string) => request<Project>(`/projects/${enc(name)}`),
	deleteProject: (name: string) =>
		request<{ status: string; project: string }>(`/projects/${enc(name)}`, {
			method: 'DELETE'
		}),

	// --- apps (project-scoped) ---
	listApps: (project: string) => request<App[]>(`/projects/${enc(project)}/apps`),
	createApp: (project: string, name: string, spec: AppSpec) =>
		request<App>(`/projects/${enc(project)}/apps`, {
			method: 'POST',
			body: JSON.stringify({ name, spec })
		}),
	getApp: (project: string, app: string) =>
		request<App>(`/projects/${enc(project)}/apps/${enc(app)}`),
	updateApp: (project: string, app: string, spec: AppSpec) =>
		request<App>(`/projects/${enc(project)}/apps/${enc(app)}`, {
			method: 'PUT',
			body: JSON.stringify(spec)
		}),
	deleteApp: (project: string, app: string) =>
		request<{ status: string }>(`/projects/${enc(project)}/apps/${enc(app)}`, {
			method: 'DELETE'
		}),

	// --- deploy ---
	deploy: (project: string, app: string, environment: string, image: string) =>
		request<{ status: string; app: string; image: string }>(
			`/projects/${enc(project)}/apps/${enc(app)}/deploy`,
			{
				method: 'POST',
				body: JSON.stringify({ environment, image })
			}
		),

	// --- logs: returns a ready-to-use SSE URL including the JWT ---
	logsURL: (project: string, app: string, env: string, tail = 200): string => {
		const token = localStorage.getItem('token') ?? '';
		const params = new URLSearchParams({
			env,
			follow: 'true',
			tail: String(tail)
		});
		if (token) {
			params.set('token', token);
		}
		return `/api/projects/${enc(project)}/apps/${enc(app)}/logs?${params.toString()}`;
	},

	// --- git providers ---
	listGitProviders: () => request<GitProviderSummary[]>('/gitproviders'),
	createGitProvider: (body: CreateGitProviderRequest) =>
		request<GitProviderSummary>('/gitproviders', {
			method: 'POST',
			body: JSON.stringify(body)
		}),
	deleteGitProvider: (name: string) =>
		request<void>(`/gitproviders/${enc(name)}`, { method: 'DELETE' }),

	// --- secrets ---
	listSecrets: (project: string, app: string) =>
		request<SecretResponse[]>(`/projects/${enc(project)}/apps/${enc(app)}/secrets`),
	createSecret: (project: string, app: string, name: string, value: string) =>
		request<SecretResponse>(`/projects/${enc(project)}/apps/${enc(app)}/secrets`, {
			method: 'POST',
			body: JSON.stringify({ name, data: { [name]: value } })
		}),
	deleteSecret: (project: string, app: string, secretName: string) =>
		request<{ status: string }>(
			`/projects/${enc(project)}/apps/${enc(app)}/secrets/${enc(secretName)}`,
			{ method: 'DELETE' }
		)
};
