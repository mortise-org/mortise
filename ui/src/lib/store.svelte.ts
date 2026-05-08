import { browser } from '$app/environment';
import { api } from './api';
import type { App, AppSpec, PreviewSummary, Project, ProjectEnvironment, ProjectMember } from './types';

export type ProjectRole = 'owner' | 'developer' | 'viewer';

interface StagedChange {
	appName: string;
	original: AppSpec;
	dirty: AppSpec;
}

class MortiseStore {
	// Auth
	token = $state<string | null>(null);
	user = $state<{ email: string; role: 'admin' | 'member' } | null>(null);
	githubConnected = $state<boolean | null>(null);

	get isAdmin(): boolean { return this.user?.role === 'admin'; }
	get isAuthenticated(): boolean { return this.token !== null; }

	// Navigation
	currentProject = $state<string | null>(null);
	projects = $state<Project[]>([]);

	// Current environment, keyed per-project so switching projects preserves
	// each one's last-selected env. `null` means "not yet resolved" — callers
	// fall back to the first env on the project.
	currentEnvByProject = $state<Record<string, string>>({});

	// Project environments keyed by project name. Sole source of truth: the
	// navbar, settings page, drawer, and canvas all read from this map.
	projectEnvs = $state<Record<string, ProjectEnvironment[]>>({});

	// Preview environments keyed by project name.
	previewEnvs = $state<Record<string, PreviewSummary[]>>({});

	// Project-level role for the current user, keyed by project name.
	projectRoles = $state<Record<string, ProjectRole | null>>({});

	// Warnings when role loading fails (network errors, etc.).
	roleLoadWarnings = $state<Record<string, string>>({});

	// Staged changes (client-side only, in-memory)
	stagedChanges = $state<Map<string, StagedChange>>(new Map());
	get stagedChangeCount(): number { return this.stagedChanges.size; }
	get hasUnsavedChanges(): boolean { return this.stagedChanges.size > 0; }

	// UI preferences (session-scoped)
	drawerTab = $state<'deployments' | 'variables' | 'deployLogs' | 'buildLogs' | 'metrics' | 'settings'>('deployments');
	activityRailOpen = $state(false);
	viewMode = $state<'canvas' | 'list'>('canvas');
	newAppModalOpen = $state(false);

	constructor() {
		if (browser) {
			this.token = localStorage.getItem('mortise_token');
			this.currentProject = localStorage.getItem('mortise_project');
			this.viewMode =
				(sessionStorage.getItem('mortise_view') as 'canvas' | 'list') ?? 'canvas';
			const savedTab = sessionStorage.getItem('mortise_tab');
			if (savedTab === 'logs') {
				this.drawerTab = 'deployLogs';
			} else {
				this.drawerTab = (savedTab as typeof this.drawerTab) ?? 'deployments';
			}
			this.activityRailOpen =
				sessionStorage.getItem('mortise_activity') === 'true';
			const savedEnvs = localStorage.getItem('mortise_envs');
			if (savedEnvs) {
				try { this.currentEnvByProject = JSON.parse(savedEnvs) ?? {}; } catch { /* ignore */ }
			}
			const savedUser = localStorage.getItem('mortise_user');
			if (savedUser) {
				try { this.user = JSON.parse(savedUser); } catch { /* ignore */ }
			}
			// JWT decode fallback when token exists but no persisted user
			if (!this.user && this.token) {
				try {
					const payload = JSON.parse(atob(this.token.split('.')[1]));
					if (payload.email) {
						this.user = { email: payload.email, role: payload.role ?? 'member' };
					}
				} catch { /* ignore */ }
			}
		}
	}

	login(token: string, user: { email: string; role: 'admin' | 'member' }) {
		this.token = token;
		this.user = user;
		if (browser) {
			localStorage.setItem('mortise_token', token);
			localStorage.setItem('mortise_user', JSON.stringify(user));
		}
	}

	logout() {
		this.token = null;
		this.user = null;
		this.currentProject = null;
		this.projects = [];
		this.stagedChanges = new Map();
		if (browser) {
			localStorage.removeItem('mortise_token');
			localStorage.removeItem('mortise_project');
			localStorage.removeItem('mortise_user');
		}
	}

	setProject(name: string | null) {
		this.currentProject = name;
		if (browser) {
			if (name) localStorage.setItem('mortise_project', name);
			else localStorage.removeItem('mortise_project');
		}
	}

	setProjects(list: Project[]) {
		this.projects = list;
	}

	// currentEnv returns the active env for the current project, or null if
	// nothing has been selected yet. Callers (navbar, drawer) resolve null to
	// the first env on the project.
	currentEnv(project: string | null = this.currentProject): string | null {
		if (!project) return null;
		return this.currentEnvByProject[project] ?? null;
	}

	setEnv(project: string, env: string) {
		this.currentEnvByProject = { ...this.currentEnvByProject, [project]: env };
		if (browser) {
			localStorage.setItem('mortise_envs', JSON.stringify(this.currentEnvByProject));
		}
	}

	async loadProjectEnvs(projectName: string): Promise<ProjectEnvironment[]> {
		const envs = await api.listProjectEnvironments(projectName);
		const sorted = [...envs].sort((a, b) => a.displayOrder - b.displayOrder);
		this.projectEnvs = { ...this.projectEnvs, [projectName]: sorted };
		return sorted;
	}

	async invalidateProjectEnvs(projectName: string): Promise<ProjectEnvironment[]> {
		return this.loadProjectEnvs(projectName);
	}

	async loadPreviewEnvs(projectName: string): Promise<PreviewSummary[]> {
		try {
			const previews = await api.listPreviewEnvironments(projectName);
			this.previewEnvs = { ...this.previewEnvs, [projectName]: previews };
			return previews;
		} catch {
			return [];
		}
	}

	async loadProjectRole(project: string): Promise<ProjectRole | null> {
		if (!this.user) return null;
		if (this.isAdmin) {
			this.projectRoles = { ...this.projectRoles, [project]: 'owner' };
			return 'owner';
		}
		try {
			const members = await api.listMembers(project);
			const me = members.find((m: ProjectMember) => m.email === this.user?.email);
			const role = me?.role ?? null;
			this.projectRoles = { ...this.projectRoles, [project]: role };
			this.roleLoadWarnings = { ...this.roleLoadWarnings };
			delete this.roleLoadWarnings[project];
			return role;
		} catch {
			// Don't restrict the UI on network/server errors — the server
			// enforces 403 anyway. Show a warning instead of silently
			// locking the user out.
			this.roleLoadWarnings = {
				...this.roleLoadWarnings,
				[project]: 'Could not verify your project permissions. Some actions may fail.'
			};
			return this.projectRoles[project] ?? null;
		}
	}

	projectRole(project: string | null): ProjectRole | null {
		if (!project) return null;
		if (this.isAdmin) return 'owner';
		return this.projectRoles[project] ?? null;
	}

	roleLoadWarning(project: string | null): string | null {
		if (!project) return null;
		return this.roleLoadWarnings[project] ?? null;
	}

	/** True when the user is a platform admin or project owner. */
	private isOwnerOrAdmin(project: string | null): boolean {
		if (!project) return false;
		if (this.isAdmin) return true;
		return this.projectRole(project) === 'owner';
	}

	canManageMembers(project: string | null): boolean {
		return this.isOwnerOrAdmin(project);
	}

	// Separate from canManageMembers so the two can diverge when
	// finer-grained RBAC is introduced (e.g. "editor" may delete apps
	// but not manage members).
	canDeleteInProject(project: string | null): boolean {
		return this.isOwnerOrAdmin(project);
	}

	stageChange(appName: string, original: AppSpec, dirty: AppSpec) {
		const map = new Map(this.stagedChanges);
		map.set(appName, { appName, original, dirty });
		this.stagedChanges = map;
	}

	discardChange(appName: string) {
		const map = new Map(this.stagedChanges);
		map.delete(appName);
		this.stagedChanges = map;
	}

	discardAll() {
		this.stagedChanges = new Map();
	}

	setDrawerTab(tab: typeof this.drawerTab) {
		this.drawerTab = tab;
		if (browser) sessionStorage.setItem('mortise_tab', tab);
	}

	toggleActivityRail() {
		this.activityRailOpen = !this.activityRailOpen;
		if (browser) sessionStorage.setItem('mortise_activity', String(this.activityRailOpen));
	}

	setActivityRailOpen(open: boolean) {
		this.activityRailOpen = open;
		if (browser) sessionStorage.setItem('mortise_activity', String(open));
	}

	setViewMode(mode: typeof this.viewMode) {
		this.viewMode = mode;
		if (browser) sessionStorage.setItem('mortise_view', mode);
	}
}

export const store = new MortiseStore();
