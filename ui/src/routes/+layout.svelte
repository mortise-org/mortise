<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import { currentProject } from '$lib/context.svelte';
	// Lucide icons
	import { Folder, Puzzle, Settings, LayoutDashboard, List, Bell, Activity, User, LogOut, ChevronDown, Users, GitBranch } from 'lucide-svelte';
	import ActivityRail from '$lib/components/ActivityRail.svelte';
	import NotificationDropdown from '$lib/components/NotificationDropdown.svelte';

	let { children } = $props();

	// GitHub connection state (per-user device flow)
	let githubFlowActive = $state(false);
	let githubUserCode = $state('');
	let githubError = $state('');
	let githubPollTimer: ReturnType<typeof setInterval> | null = null;

	async function checkGitHubStatus() {
		try {
			const resp = await api.gitTokenStatus('github');
			store.githubConnected = resp.connected;
		} catch {
			store.githubConnected = null;
		}
	}

	let githubCodeCopied = $state(false);

	async function connectGitHub() {
		githubError = '';
		githubFlowActive = true;
		githubCodeCopied = false;
		userMenuOpen = false; // close dropdown, modal takes over
		try {
			const data = await api.gitDeviceCode('github');
			githubUserCode = data.user_code;
			const dc = data.device_code;

			// Copy code to clipboard and open GitHub in new tab
			try {
				await navigator.clipboard.writeText(githubUserCode);
				githubCodeCopied = true;
			} catch {}
			window.open('https://github.com/login/device', '_blank');

			let currentInterval = (data.interval || 5) * 1000;
			let polling = false;

			const doPoll = async () => {
				if (polling) return;
				polling = true;
				try {
					const pd = await api.gitDevicePoll('github', dc);
					if (pd.status === 'complete') {
						if (githubPollTimer) clearInterval(githubPollTimer);
						githubFlowActive = false;
						githubUserCode = '';
						store.githubConnected = true;
					} else if (pd.status === 'slow_down') {
						if (githubPollTimer) clearInterval(githubPollTimer);
						currentInterval += 5000;
						githubPollTimer = setInterval(doPoll, currentInterval);
					} else if (pd.status === 'expired' || pd.status === 'denied') {
						if (githubPollTimer) clearInterval(githubPollTimer);
						githubError = `Authorization ${pd.status}. Try again.`;
						githubFlowActive = false;
						githubUserCode = '';
					}
				} catch { /* network hiccup, keep polling */ }
				finally { polling = false; }
			};

			githubPollTimer = setInterval(doPoll, currentInterval);
		} catch (e) {
			githubError = e instanceof Error ? e.message : 'Connection failed';
			githubFlowActive = false;
		}
	}

	function cancelGitHubFlow() {
		if (githubPollTimer) clearInterval(githubPollTimer);
		githubFlowActive = false;
		githubUserCode = '';
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
	let currentEnv = $state<string>('production');
	let envSwitcherOpen = $state(false);
	let envSwitcherEl: HTMLDivElement | null = $state(null);
	let projectEnvs = $state<string[]>(['production', 'staging']);

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

	$effect(() => {
		const proj = urlProject;
		if (!proj || !store.token) return;
		api.listApps(proj)
			.then(apps => {
				const envNames = new Set<string>(['production', 'staging']);
				for (const app of apps) {
					for (const env of app.spec.environments ?? []) {
						envNames.add(env.name);
					}
				}
				projectEnvs = [...envNames];
			})
			.catch(() => {
				projectEnvs = ['production', 'staging'];
			});
	});

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
	<div class="flex min-h-screen flex-col bg-surface-900 text-gray-300" style="min-width:1280px">
		<!-- Top header -->
		<header class="flex h-14 shrink-0 items-center justify-between border-b border-surface-600 bg-surface-800 px-4 z-30">
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
					<div class="relative" bind:this={envSwitcherEl}>
						<button
							type="button"
							aria-label="Switch environment: {currentEnv}"
							onclick={() => (envSwitcherOpen = !envSwitcherOpen)}
							class="flex items-center gap-1.5 rounded-md px-2 py-1 text-sm text-gray-400 hover:bg-surface-700 hover:text-white transition-colors"
						>
							<span class="h-2 w-2 rounded-full bg-success"></span>
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
										onclick={() => {
											currentEnv = env;
											envSwitcherOpen = false;
										}}
										class="flex w-full items-center gap-2 px-3 py-2 text-sm {currentEnv === env
											? 'bg-surface-600 text-white'
											: 'text-gray-300 hover:bg-surface-700 hover:text-white'}"
									>
										<span
											class="h-1.5 w-1.5 rounded-full {env === 'production'
												? 'bg-success'
												: 'bg-info'}"
										></span>
										{env}
									</button>
								{/each}
							</div>
						{/if}
					</div>
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
								<!-- GitHub connection -->
								{#if githubFlowActive}
									<div class="flex items-center gap-2 px-3 py-2 text-sm text-info">
										<GitBranch class="h-4 w-4 animate-pulse" /> Waiting for GitHub...
									</div>
								{:else if store.githubConnected}
									<div class="flex items-center gap-2 px-3 py-2 text-sm text-success">
										<GitBranch class="h-4 w-4" /> GitHub: Connected
									</div>
								{:else}
									<button
										type="button"
										onclick={connectGitHub}
										class="flex w-full items-center gap-2 px-3 py-2 text-sm text-gray-300 hover:bg-surface-700 hover:text-white"
									>
										<GitBranch class="h-4 w-4" /> Connect GitHub
									</button>
								{/if}
								{#if githubError}
									<p class="px-3 text-xs text-danger">{githubError}</p>
								{/if}
								{#if store.isAdmin}
									<a href="/admin/settings" class="flex items-center gap-2 px-3 py-2 text-sm text-gray-300 hover:bg-surface-700 hover:text-white">
										<Settings class="h-4 w-4" /> Platform Settings
									</a>
								{/if}
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
		<div class="flex flex-1 overflow-hidden">
			<!-- Left rail (w-14, icons only) -->
			<nav class="flex w-14 shrink-0 flex-col items-center gap-2 border-r border-surface-600 bg-surface-800 py-3 z-20">
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
					{#if store.isAdmin}
						<a
							href="/admin/settings"
							class="{isActive('/admin') ? railIconActive : railIcon}"
							title="Settings"
						>
							<Settings class="h-5 w-5" />
						</a>
					{/if}
				{/if}
			</nav>

			<!-- Main content area -->
			<main class="flex-1 overflow-y-auto">
				{@render children()}
			</main>
		</div>

		{#if inProject && activeProject}
			<ActivityRail project={activeProject} />
		{/if}
	</div>

	<!-- GitHub Device Flow Modal -->
	{#if githubFlowActive && githubUserCode}
		<div class="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 backdrop-blur-sm">
			<div class="w-full max-w-sm rounded-lg border border-surface-600 bg-surface-800 p-6 shadow-2xl">
				<h3 class="text-lg font-semibold text-white mb-1">Connect GitHub</h3>
				<p class="text-sm text-gray-400 mb-4">Enter this code on GitHub to authorize Mortise:</p>
				<code class="block rounded-lg bg-surface-900 border border-surface-600 px-4 py-3 text-center text-2xl font-mono font-bold text-white tracking-[0.3em] select-all mb-2">{githubUserCode}</code>
				{#if githubCodeCopied}
					<p class="text-xs text-success text-center mb-4">Copied to clipboard</p>
				{:else}
					<p class="text-xs text-gray-500 text-center mb-4">Click code to select, or copy manually</p>
				{/if}
				<a
					href="https://github.com/login/device"
					target="_blank"
					rel="noopener noreferrer"
					class="block w-full rounded-md bg-accent px-4 py-2 text-center text-sm font-medium text-white hover:bg-accent-hover mb-3"
				>
					Open github.com/login/device
				</a>
				<div class="flex items-center justify-center gap-2 text-xs text-gray-500">
					<div class="h-3 w-3 animate-spin rounded-full border-2 border-gray-600 border-t-accent"></div>
					Waiting for authorization...
				</div>
				<button
					type="button"
					onclick={cancelGitHubFlow}
					class="mt-4 w-full text-center text-xs text-gray-500 hover:text-gray-300"
				>
					Cancel
				</button>
			</div>
		</div>
	{/if}
{/if}
