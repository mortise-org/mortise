<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import { currentProject } from '$lib/context.svelte';
	// Lucide icons
	import { Folder, Puzzle, Settings, LayoutDashboard, List, Bell, Activity, User, LogOut, ChevronDown, Users, Rocket } from 'lucide-svelte';
	import ActivityRail from '$lib/components/ActivityRail.svelte';
	import NotificationDropdown from '$lib/components/NotificationDropdown.svelte';
	import type { EnvHealth } from '$lib/types';

	let { children } = $props();

	async function checkGitHubStatus() {
		try {
			const resp = await api.gitTokenStatus('github');
			store.githubConnected = resp.connected;
		} catch {
			store.githubConnected = null;
		}
	}

	// Determine layout type from URL
	const isLogin = $derived(page.url.pathname === '/login');
	const isSetup = $derived(
		page.url.pathname === '/setup' || page.url.pathname.startsWith('/setup/')
	);
	const bareLayout = $derived(isLogin || isSetup);

	// Are we in a project context?
	const urlProject = $derived.by<string | null>(() => {
		const m = page.url.pathname.match(/^\/projects\/([^/]+)/);
		return m ? decodeURIComponent(m[1]) : null;
	});
	const inProject = $derived(urlProject !== null);
	const activeProject = $derived(urlProject ?? store.currentProject);

	let checking = $state(true);
	let projectsLoaded = $state(false);
	let switcherOpen = $state(false);
	let userMenuOpen = $state(false);
	let switcherEl: HTMLDivElement | null = $state(null);
	let userMenuEl: HTMLDivElement | null = $state(null);
	let notificationsOpen = $state(false);
	let notificationsEl: HTMLDivElement | null = $state(null);
	let envSwitcherOpen = $state(false);
	let envSwitcherEl: HTMLDivElement | null = $state(null);

	// Keep the last-rendered envs around during project switches so the navbar
	// env chip doesn't flicker to empty while the new project's envs are in flight.
	let lastRenderedEnvs = $state<typeof store.projectEnvs[string]>([]);
	const projectEnvs = $derived.by(() => {
		if (!activeProject) return [] as typeof store.projectEnvs[string];
		const cached = store.projectEnvs[activeProject];
		if (cached && cached.length > 0) return cached;
		return lastRenderedEnvs;
	});
	$effect(() => {
		if (!activeProject) return;
		const cached = store.projectEnvs[activeProject];
		if (cached && cached.length > 0) lastRenderedEnvs = cached;
	});

	const currentEnv = $derived.by<string>(() => {
		if (!activeProject) return '';
		const stored = store.currentEnv(activeProject);
		if (stored && projectEnvs.some((e) => e.name === stored)) return stored;
		return projectEnvs[0]?.name ?? stored ?? '';
	});

	const currentEnvHealth = $derived<EnvHealth>(
		projectEnvs.find((e) => e.name === currentEnv)?.health ?? 'unknown'
	);

	function dotClass(h: EnvHealth | undefined): string {
		switch (h) {
			case 'healthy':
				return 'bg-success';
			case 'warning':
				return 'bg-warning';
			case 'danger':
				return 'bg-error';
			default:
				return 'bg-gray-500';
		}
	}

	async function checkSetupStatus() {
		try {
			const res = await fetch('/api/auth/status');
			if (!res.ok) return;
			const data = (await res.json()) as { setupRequired: boolean };
			const path = page.url.pathname;
			if (data.setupRequired && path !== '/setup' && !path.startsWith('/setup/')) {
				await goto('/setup', { replaceState: true });
			} else if (!data.setupRequired && path === '/setup') {
				await goto('/login', { replaceState: true });
			}
		} catch {
			// unreachable - fall through
		}
	}

	async function loadProjects() {
		try {
			const list = await api.listProjects();
			store.setProjects(list);
		} catch {
			store.setProjects([]);
		} finally {
			projectsLoaded = true;
		}
	}

	onMount(async () => {
		await checkSetupStatus();
		checking = false;
	});

	$effect(() => {
		if (urlProject && urlProject !== store.currentProject) {
			store.setProject(urlProject);
		}
	});

	$effect(() => {
		if (bareLayout || checking) return;
		if (!store.token) return;
		if (!projectsLoaded) void loadProjects();
		if (store.githubConnected === null) void checkGitHubStatus();
	});

	// Load the project's declared environments and seed store.currentEnv from
	// the URL's ?env= when present.
	$effect(() => {
		const proj = urlProject;
		if (!proj || !store.token) return;
		store
			.loadProjectEnvs(proj)
			.then((envs) => {
				const urlEnv = page.url.searchParams.get('env');
				if (urlEnv && envs.some((e) => e.name === urlEnv)) {
					store.setEnv(proj, urlEnv);
				} else if (!store.currentEnv(proj) && envs[0]) {
					store.setEnv(proj, envs[0].name);
				}
			})
			.catch(() => {
				/* keep previous envs on failure */
			});
	});

	// Keep ?env= in sync with store.currentEnv so deep links + reloads are stable.
	$effect(() => {
		if (!activeProject || !currentEnv) return;
		const url = new URL(page.url);
		if (url.searchParams.get('env') !== currentEnv) {
			url.searchParams.set('env', currentEnv);
			history.replaceState(history.state, '', url.toString());
		}
	});

	function selectEnv(name: string) {
		envSwitcherOpen = false;
		if (!activeProject) return;
		store.setEnv(activeProject, name);
		const url = new URL(page.url);
		url.searchParams.set('env', name);
		history.replaceState(history.state, '', url.toString());
	}

	function logout() {
		store.logout();
		goto('/login');
	}

	function toggleSwitcher() { switcherOpen = !switcherOpen; }
	function toggleUserMenu() { userMenuOpen = !userMenuOpen; }

	function selectProject(name: string) {
		switcherOpen = false;
		store.setProject(name);
		goto(`/projects/${encodeURIComponent(name)}`);
	}

	function handleDocClick(ev: MouseEvent) {
		if (switcherEl && !switcherEl.contains(ev.target as Node)) switcherOpen = false;
		if (userMenuEl && !userMenuEl.contains(ev.target as Node)) userMenuOpen = false;
		if (notificationsEl && !notificationsEl.contains(ev.target as Node)) notificationsOpen = false;
		if (envSwitcherEl && !envSwitcherEl.contains(ev.target as Node)) envSwitcherOpen = false;
	}

	// Left-rail icon classes
	const railIcon = 'flex flex-col items-center justify-center rounded-lg w-10 h-10 text-gray-500 hover:bg-surface-700 hover:text-white transition-all duration-150 cursor-pointer';
	const railIconActive = 'flex flex-col items-center justify-center rounded-lg w-10 h-10 bg-surface-700 text-white';

	function isActive(href: string) {
		return page.url.pathname === href || page.url.pathname.startsWith(href + '/');
	}
</script>

<svelte:window onclick={handleDocClick} />

{#if checking}
	<div class="flex min-h-screen items-center justify-center bg-surface-900">
		<div class="inline-block h-5 w-5 animate-spin rounded-full border-2 border-gray-500 border-t-transparent"></div>
	</div>
{:else if bareLayout}
	{@render children()}
{:else}
	<div class="flex h-screen w-full min-w-0 flex-col overflow-hidden bg-surface-900 text-gray-300">
		<!-- Top header -->
		<header class="z-30 flex h-14 shrink-0 items-center justify-between border-b border-surface-600 bg-surface-800 px-4">
			<div class="flex items-center gap-3">
				<!-- Logo -->
				<a href="/" class="text-base font-semibold tracking-tight text-white hover:text-accent transition-colors">
					Mortise
				</a>

				{#if inProject && activeProject}
					<!-- Project switcher (inside project) -->
					<div class="relative" bind:this={switcherEl}>
						<button
							type="button"
							onclick={toggleSwitcher}
							class="flex items-center gap-1.5 rounded-md px-2 py-1 text-sm text-gray-300 hover:bg-surface-700 hover:text-white transition-colors"
						>
							<span class="max-w-[20ch] truncate">{activeProject}</span>
							<ChevronDown class="h-3.5 w-3.5 text-gray-500" />
						</button>
						{#if switcherOpen}
							<div class="absolute left-0 top-full z-50 mt-1 w-56 overflow-hidden rounded-md border border-surface-600 bg-surface-800 shadow-xl">
								<div class="max-h-64 overflow-y-auto py-1">
									{#each store.projects as p}
										<button
											type="button"
											onclick={() => selectProject(p.name)}
											class="flex w-full items-center justify-between gap-2 px-3 py-2 text-sm {p.name === activeProject ? 'bg-surface-600 text-white' : 'text-gray-300 hover:bg-surface-700 hover:text-white'}"
										>
											<span class="truncate">{p.name}</span>
											<span class="text-xs text-gray-500">{p.appCount} apps</span>
										</button>
									{/each}
								</div>
								<div class="border-t border-surface-600">
									<button
										type="button"
										onclick={() => { switcherOpen = false; goto('/projects/new'); }}
										class="flex w-full items-center gap-2 px-3 py-2 text-sm text-accent hover:bg-surface-700"
									>
										<span>+ New project</span>
									</button>
								</div>
							</div>
						{/if}
					</div>

					<!-- Environment switcher -->
					{#if projectEnvs.length > 0}
						<div class="relative" bind:this={envSwitcherEl}>
							<button
								type="button"
								aria-label="Switch environment: {currentEnv}"
								onclick={() => (envSwitcherOpen = !envSwitcherOpen)}
								class="flex items-center gap-1.5 rounded-md px-2 py-1 text-sm text-gray-400 hover:bg-surface-700 hover:text-white transition-colors"
							>
								<span class="h-2 w-2 rounded-full {dotClass(currentEnvHealth)}"></span>
								<span>{currentEnv}</span>
								<ChevronDown class="h-3.5 w-3.5 text-gray-500" />
							</button>
							{#if envSwitcherOpen}
								<div
									class="absolute left-0 top-full z-50 mt-1 w-40 rounded-md border border-surface-600 bg-surface-800 shadow-xl"
								>
									{#each projectEnvs as env}
										<button
											type="button"
											onclick={() => selectEnv(env.name)}
											class="flex w-full items-center gap-2 px-3 py-2 text-sm {currentEnv === env.name
												? 'bg-surface-600 text-white'
												: 'text-gray-300 hover:bg-surface-700 hover:text-white'}"
										>
											<span
												class="h-1.5 w-1.5 rounded-full {dotClass(env.health)}"
											></span>
											{env.name}
										</button>
									{/each}
								</div>
							{/if}
						</div>
					{/if}
				{/if}
			</div>

			<!-- Right side -->
			<div class="flex items-center gap-1">
				{#if inProject}
					<!-- Activity pulse button (project context only) -->
					<button
						type="button"
						onclick={() => store.toggleActivityRail()}
						class="rounded-md p-2 text-gray-500 hover:bg-surface-700 hover:text-white transition-colors {store.activityRailOpen ? 'bg-surface-700 text-white' : ''}"
						title="Activity"
					>
						<Activity class="h-4 w-4" />
					</button>
				{/if}

				<!-- Notifications bell (all authenticated pages) -->
				<div class="relative" bind:this={notificationsEl}>
					<button
						type="button"
						onclick={() => (notificationsOpen = !notificationsOpen)}
						class="rounded-md p-2 text-gray-500 hover:bg-surface-700 hover:text-white transition-colors {notificationsOpen
							? 'bg-surface-700 text-white'
							: ''}"
						title="Notifications"
					>
						<Bell class="h-4 w-4" />
					</button>
					{#if notificationsOpen}
						<NotificationDropdown onClose={() => (notificationsOpen = false)} />
					{/if}
				</div>

				<!-- User menu -->
				<div class="relative" bind:this={userMenuEl}>
					<button
						type="button"
						onclick={toggleUserMenu}
						class="rounded-md p-2 text-gray-500 hover:bg-surface-700 hover:text-white transition-colors"
					>
						<User class="h-4 w-4" />
					</button>
					{#if userMenuOpen}
						<div class="absolute right-0 top-full z-50 mt-1 w-56 overflow-hidden rounded-md border border-surface-600 bg-surface-800 shadow-xl">
							<div class="border-b border-surface-600 px-3 py-2">
								<p class="text-xs text-gray-500 truncate">{store.user?.email ?? 'Signed in'}</p>
							</div>
							<div class="py-1">
								<a href="/settings" class="flex items-center gap-2 px-3 py-2 text-sm text-gray-300 hover:bg-surface-700 hover:text-white">
									<Settings class="h-4 w-4" /> Settings
								</a>
								<button
									type="button"
									onclick={logout}
									class="flex w-full items-center gap-2 px-3 py-2 text-sm text-gray-300 hover:bg-surface-700 hover:text-white"
								>
									<LogOut class="h-4 w-4" /> Sign out
								</button>
							</div>
						</div>
					{/if}
				</div>
			</div>
		</header>

		<!-- Body: left rail + main content -->
		<div class="flex min-h-0 min-w-0 flex-1 overflow-hidden">
			<!-- Left rail (w-14, icons only) -->
			<nav class="z-20 flex h-full w-14 shrink-0 flex-col items-center gap-2 border-r border-surface-600 bg-surface-800 py-3">
				{#if inProject && activeProject}
					<!-- Project scope: Canvas + Settings -->
					<a
						href="/projects/{encodeURIComponent(activeProject)}"
						class="{isActive('/projects/' + encodeURIComponent(activeProject)) && !isActive('/projects/' + encodeURIComponent(activeProject) + '/settings') ? railIconActive : railIcon}"
						title="Canvas"
					>
						<LayoutDashboard class="h-5 w-5" />
					</a>
					<a
						href="/projects/{encodeURIComponent(activeProject)}/settings"
						class="{isActive('/projects/' + encodeURIComponent(activeProject) + '/settings') ? railIconActive : railIcon}"
						title="Project Settings"
					>
						<Settings class="h-5 w-5" />
					</a>
				{:else}
					<!-- Dashboard scope: Projects + Extensions + Platform Settings -->
					<a
						href="/"
						class="{page.url.pathname === '/' || page.url.pathname.startsWith('/projects/new') ? railIconActive : railIcon}"
						title="Projects"
					>
						<Folder class="h-5 w-5" />
					</a>
					<a
						href="/extensions"
						class="{isActive('/extensions') ? railIconActive : railIcon}"
						title="Extensions"
					>
						<Puzzle class="h-5 w-5" />
					</a>
					<a
						href="/settings"
						class="{isActive('/settings') ? railIconActive : railIcon}"
						title="Settings"
					>
						<Settings class="h-5 w-5" />
					</a>
					<a
						href="/getting-started"
						class="{isActive('/getting-started') ? railIconActive : railIcon}"
						title="Getting Started"
					>
						<Rocket class="h-5 w-5" />
					</a>
				{/if}
			</nav>

			<!-- Main content area -->
			<main class="min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto">
				{@render children()}
			</main>
		</div>

		{#if inProject && activeProject}
			<ActivityRail project={activeProject} />
		{/if}
	</div>

{/if}
