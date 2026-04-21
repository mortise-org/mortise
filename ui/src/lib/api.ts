import { goto } from '$app/navigation';
import type {
	App,
	AppSpec,
	ActivityEvent,
	BindingEdge,
	Branch,
	BuildLogsResponse,
	CreateGitProviderRequest,
	DeployRecord,
	DeployToken,
	DeviceCodeResponse,
	DevicePollResponse,
	DomainsResponse,
	GitHubStatusResponse,
	GitProviderSummary,
	InviteResponse,
	Notification,
	PlatformResponse,
	PreviewSummary,
	Pod,
	Project,
	ProjectEnvironment,
	ProjectMember,
	Repository,
	SecretResponse,
	SharedVarEntry
} from './types';

const BASE = '/api';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const token = localStorage.getItem('mortise_token');
	const headers: Record<string, string> = {
		'Content-Type': 'application/json',
		...(init?.headers as Record<string, string>)
	};
	if (token) {
		headers['Authorization'] = `Bearer ${token}`;
	}

	const res = await fetch(`${BASE}${path}`, { ...init, headers });

	if (res.status === 401) {
		localStorage.removeItem('mortise_token');
		goto('/login');
		throw new Error('Unauthorized');
	}

	if (!res.ok) {
		const body = await res.json().catch(() => ({ error: res.statusText }));
		throw new Error(body.error || res.statusText);
	}

	// 204s and empty bodies - return undefined as T.
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

	// --- project environments ---
	listProjectEnvironments: (project: string) =>
		request<ProjectEnvironment[]>(`/projects/${enc(project)}/environments`),
	createProjectEnvironment: (project: string, name: string, displayOrder = 0) =>
		request<ProjectEnvironment>(`/projects/${enc(project)}/environments`, {
			method: 'POST',
			body: JSON.stringify({ name, displayOrder })
		}),
	updateProjectEnvironment: (
		project: string,
		currentName: string,
		patch: { name?: string; displayOrder?: number }
	) =>
		request<ProjectEnvironment>(`/projects/${enc(project)}/environments/${enc(currentName)}`, {
			method: 'PATCH',
			body: JSON.stringify(patch)
		}),
	deleteProjectEnvironment: (project: string, name: string) =>
		request<{ status: string; name: string }>(
			`/projects/${enc(project)}/environments/${enc(name)}`,
			{ method: 'DELETE' }
		),

	// --- bindings (project+env-scoped canvas edges) ---
	listBindings: (project: string, environment: string) =>
		request<BindingEdge[]>(
			`/projects/${enc(project)}/bindings?environment=${enc(environment)}`
		),

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

	// --- stacks ---
	listTemplates: () =>
		request<
			Array<{
				name: string;
				description: string;
				services: Array<{
					name: string;
					image: string;
					required: boolean;
				}>;
			}>
		>('/templates'),
	createStack: (
		project: string,
		body: {
			template?: string;
			compose?: string;
			name?: string;
			vars?: Record<string, string>;
			services?: string[];
		}
	) =>
		request<{ apps: string[] }>(`/projects/${enc(project)}/stacks`, {
			method: 'POST',
			body: JSON.stringify(body)
		}),
	execInApp: (project: string, app: string, command: string[]) =>
		request<{ stdout: string; stderr: string }>(
			`/projects/${enc(project)}/apps/${enc(app)}/exec`,
			{
				method: 'POST',
				body: JSON.stringify({ command })
			}
		),

	// --- deploy ---
	deploy: (project: string, app: string, environment: string, image: string) =>
		request<{ status: string; app: string; image: string }>(
			`/projects/${enc(project)}/apps/${enc(app)}/deploy`,
			{
				method: 'POST',
				body: JSON.stringify({ environment, image })
			}
		),

	// --- rollback ---
	rollback: (project: string, app: string, environment: string, index: number) =>
		request<DeployRecord>(`/projects/${enc(project)}/apps/${enc(app)}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ environment, index })
		}),

	// --- promote ---
	promote: (project: string, app: string, from: string, to: string) =>
		request<{ status: string; from: string; to: string; image: string }>(
			`/projects/${enc(project)}/apps/${enc(app)}/promote`,
			{
				method: 'POST',
				body: JSON.stringify({ from, to })
			}
		),

	rebuild: (project: string, app: string) =>
		request<{ status: string }>(`/projects/${enc(project)}/apps/${enc(app)}/rebuild`, { method: 'POST' }),

	redeploy: (project: string, app: string, environment?: string) =>
		request<{ status: string }>(`/projects/${enc(project)}/apps/${enc(app)}/redeploy${environment ? `?environment=${enc(environment)}` : ''}`, { method: 'POST' }),

	// --- pods: lightweight list used by the Logs drawer pod picker ---
	listPods: (project: string, app: string, env: string) =>
		request<Pod[]>(`/projects/${enc(project)}/apps/${enc(app)}/pods?env=${enc(env)}`),

	// --- logs: returns a ready-to-use SSE URL including the JWT ---
	logsURL: (
		project: string,
		app: string,
		opts: {
			env: string;
			follow?: boolean;
			tail?: number;
			pod?: string;
			previous?: boolean;
			sinceSeconds?: number;
			sinceTime?: string;
		}
	): string => {
		const token = localStorage.getItem('mortise_token') ?? '';
		const params = new URLSearchParams({ env: opts.env });
		if (opts.follow) params.set('follow', 'true');
		if (opts.tail !== undefined) params.set('tail', String(opts.tail));
		if (opts.pod) params.set('pod', opts.pod);
		if (opts.previous) params.set('previous', 'true');
		if (opts.sinceSeconds !== undefined) params.set('sinceSeconds', String(opts.sinceSeconds));
		if (opts.sinceTime) params.set('sinceTime', opts.sinceTime);
		if (token) params.set('token', token);
		return `/api/projects/${enc(project)}/apps/${enc(app)}/logs?${params.toString()}`;
	},

	getBuildLogs: (project: string, app: string) =>
		request<BuildLogsResponse>(`/projects/${enc(project)}/apps/${enc(app)}/build-logs`),
	connectApp: (project: string, app: string) =>
		request<{ port: number; url: string }>(`/projects/${enc(project)}/apps/${enc(app)}/connect`, { method: 'POST' }),
	disconnectApp: (project: string, app: string) =>
		request<void>(`/projects/${enc(project)}/apps/${enc(app)}/disconnect`, { method: 'POST' }),

	// --- Git provider device flow (per-user, requires JWT) ---
	gitDeviceCode: (provider: string) =>
		request<DeviceCodeResponse>(`/auth/git/${enc(provider)}/device`, { method: 'POST' }),
	gitDevicePoll: (provider: string, deviceCode: string) =>
		request<DevicePollResponse>(`/auth/git/${enc(provider)}/device/poll`, {
			method: 'POST',
			body: JSON.stringify({ device_code: deviceCode })
		}),
	gitTokenStatus: (provider: string) =>
		request<GitHubStatusResponse>(`/auth/git/${enc(provider)}/status`),
	storeGitToken: (provider: string, token: string, host?: string) =>
		request<{ ok: boolean }>(`/auth/git/${enc(provider)}/token`, {
			method: 'POST',
			body: JSON.stringify({ token, ...(host ? { host } : {}) })
		}),

	// --- git providers (admin-configured) ---
	listGitProviders: () => request<GitProviderSummary[]>('/gitproviders'),
	createGitProvider: (body: CreateGitProviderRequest) =>
		request<GitProviderSummary>('/gitproviders', {
			method: 'POST',
			body: JSON.stringify(body)
		}),
	deleteGitProvider: (name: string) =>
		request<void>(`/gitproviders/${enc(name)}`, { method: 'DELETE' }),
	getWebhookSecret: (name: string) =>
		request<{ webhookSecret: string }>(`/gitproviders/${enc(name)}/webhook-secret`),

	// --- repos ---
	// GitHub: no provider param needed (uses per-user token).
	// GitLab/Gitea: pass ?provider=name.
	listRepos: (provider: string) =>
		request<Repository[]>(`/repos?provider=${enc(provider)}`),
	listBranches: (owner: string, repo: string, provider: string) =>
		request<Branch[]>(`/repos/${enc(owner)}/${enc(repo)}/branches?provider=${enc(provider)}`),
	listRepoTree: (owner: string, repo: string, provider: string, branch: string, path = '') =>
		request<Array<{ name: string; type: string; path: string }>>(
			`/repos/${enc(owner)}/${enc(repo)}/tree?provider=${enc(provider)}&branch=${enc(branch)}${path ? `&path=${enc(path)}` : ''}`
		),

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
		),

	// --- domains ---
	listDomains: (project: string, app: string, environment: string) =>
		request<DomainsResponse>(
			`/projects/${enc(project)}/apps/${enc(app)}/domains?environment=${enc(environment)}`
		),
	addDomain: (project: string, app: string, environment: string, domain: string) =>
		request<DomainsResponse>(
			`/projects/${enc(project)}/apps/${enc(app)}/domains?environment=${enc(environment)}`,
			{
				method: 'POST',
				body: JSON.stringify({ domain })
			}
		),
	removeDomain: (project: string, app: string, environment: string, domain: string) =>
		request<DomainsResponse>(
			`/projects/${enc(project)}/apps/${enc(app)}/domains/${enc(domain)}?environment=${enc(environment)}`,
			{ method: 'DELETE' }
		),

	// --- platform config ---
	getPlatform: () => request<PlatformResponse>('/platform'),
	patchPlatform: (body: Partial<{ domain: string; tls: { certManagerClusterIssuer: string }; storage: { defaultStorageClass: string } }>) =>
		request<PlatformResponse>('/platform', {
			method: 'PATCH',
			body: JSON.stringify(body)
		}),

	// --- env management ---
	getEnv: (project: string, app: string, env: string) =>
		request<Array<{ name: string; value: string; source?: string }>>(
			`/projects/${enc(project)}/apps/${enc(app)}/env?environment=${enc(env)}`
		),
	setEnv: (project: string, app: string, env: string, vars: Record<string, string>) =>
		request<void>(
			`/projects/${enc(project)}/apps/${enc(app)}/env?environment=${enc(env)}`,
			{
				method: 'PUT',
				body: JSON.stringify(
					Object.entries(vars).map(([name, value]) => ({ name, value }))
				)
			}
		),
	importEnv: (project: string, app: string, env: string, raw: string) =>
		request<void>(`/projects/${enc(project)}/apps/${enc(app)}/env/import`, {
			method: 'POST',
			body: JSON.stringify({ env, content: raw })
		}),

	// --- deploy tokens ---
	listTokens: (project: string, app: string) =>
		request<DeployToken[]>(`/projects/${enc(project)}/apps/${enc(app)}/tokens`),
	createToken: (project: string, app: string, name: string, environment: string) =>
		request<DeployToken>(`/projects/${enc(project)}/apps/${enc(app)}/tokens`, {
			method: 'POST',
			body: JSON.stringify({ name, environment })
		}),
	revokeToken: (project: string, app: string, id: string) =>
		request<void>(`/projects/${enc(project)}/apps/${enc(app)}/tokens/${enc(id)}`, {
			method: 'DELETE'
		}),

	// --- project settings ---
	updateProject: (name: string, body: { description?: string }) =>
		request<Project>(`/projects/${enc(name)}`, {
			method: 'PATCH',
			body: JSON.stringify(body)
		}),

	// --- activity ---
	listActivity: (project: string) =>
		request<ActivityEvent[]>(`/projects/${enc(project)}/activity`),

	// --- shared variables (project-level, controller fans out to all envs) ---
	getSharedVars: (project: string) =>
		request<Array<{ name: string; value: string; source?: string }>>(
			`/projects/${enc(project)}/shared-vars`
		),
	setSharedVars: (project: string, vars: Array<{ name: string; value: string }>) =>
		request<void>(`/projects/${enc(project)}/shared-vars`, {
			method: 'PUT',
			body: JSON.stringify(vars)
		}),

	// --- preview environments ---
	listPreviewEnvironments: (project: string) =>
		request<PreviewSummary[]>(`/projects/${enc(project)}/previews`),

	// --- project preview settings ---
	setProjectPreview: (project: string, enabled: boolean, domainTemplate?: string, ttl?: string) =>
		request<Project>(`/projects/${enc(project)}`, {
			method: 'PATCH',
			body: JSON.stringify({ preview: { enabled, domainTemplate, ttl } })
		}),

	// --- project members ---
	listMembers: (project: string) =>
		request<ProjectMember[]>(`/projects/${enc(project)}/members`),
	inviteMember: (project: string, email: string, role: 'admin' | 'member') =>
		request<InviteResponse>(`/projects/${enc(project)}/members`, {
			method: 'POST',
			body: JSON.stringify({ email, role })
		}),
	removeMember: (project: string, email: string) =>
		request<void>(`/projects/${enc(project)}/members/${enc(email)}`, {
			method: 'DELETE'
		}),

	// --- env patch (single var update without full replace) ---
	patchEnvVar: (project: string, app: string, env: string, key: string, value: string) =>
		request<void>(`/projects/${enc(project)}/apps/${enc(app)}/env/${enc(env)}`, {
			method: 'PATCH',
			body: JSON.stringify({ [key]: value })
		}),
	deleteEnvVar: (project: string, app: string, env: string, key: string) =>
		request<void>(`/projects/${enc(project)}/apps/${enc(app)}/env/${enc(env)}`, {
			method: 'PATCH',
			body: JSON.stringify({ [key]: null })
		}),
};
