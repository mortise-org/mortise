import { browser } from '$app/environment';
import type { App, AppSpec, Project } from './types';

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

	// Staged changes (client-side only, in-memory)
	stagedChanges = $state<Map<string, StagedChange>>(new Map());
	get stagedChangeCount(): number { return this.stagedChanges.size; }
	get hasUnsavedChanges(): boolean { return this.stagedChanges.size > 0; }

	// UI preferences (session-scoped)
	drawerTab = $state<'deployments' | 'variables' | 'logs' | 'metrics' | 'settings'>('deployments');
	activityRailOpen = $state(false);
	viewMode = $state<'canvas' | 'list'>('canvas');
	newAppModalOpen = $state(false);

	constructor() {
		if (browser) {
			this.token = localStorage.getItem('mortise_token');
			this.currentProject = localStorage.getItem('mortise_project');
			this.viewMode =
				(sessionStorage.getItem('mortise_view') as 'canvas' | 'list') ?? 'canvas';
			this.drawerTab =
				(sessionStorage.getItem('mortise_tab') as typeof this.drawerTab) ?? 'deployments';
			this.activityRailOpen =
				sessionStorage.getItem('mortise_activity') === 'true';
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

	setViewMode(mode: typeof this.viewMode) {
		this.viewMode = mode;
		if (browser) sessionStorage.setItem('mortise_view', mode);
	}
}

export const store = new MortiseStore();
