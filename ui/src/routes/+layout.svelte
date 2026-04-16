<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { currentProject } from '$lib/context';
	import type { Project } from '$lib/types';

	let { children } = $props();

	const isLogin = $derived(page.url.pathname === '/login');
	const isSetup = $derived(page.url.pathname === '/setup');
	const bareLayout = $derived(isLogin || isSetup);

	let checking = $state(true);
	let projects = $state<Project[]>([]);
	let projectsLoaded = $state(false);
	let switcherOpen = $state(false);
	let switcherEl: HTMLDivElement | null = $state(null);

	// The active project comes from the URL when we're on a /projects/{p} page.
	// Otherwise we fall back to the last-used project from localStorage.
	const urlProject = $derived.by<string | null>(() => {
		const m = page.url.pathname.match(/^\/projects\/([^/]+)/);
		return m ? decodeURIComponent(m[1]) : null;
	});
	const activeProject = $derived(urlProject ?? currentProject.value);

	async function checkSetupStatus() {
		try {
			const res = await fetch('/api/auth/status');
			if (!res.ok) {
				return;
			}
			const data = (await res.json()) as { setupRequired: boolean };
			const path = page.url.pathname;
			if (data.setupRequired && path !== '/setup') {
				await goto('/setup', { replaceState: true });
			} else if (!data.setupRequired && path === '/setup') {
				await goto('/login', { replaceState: true });
			}
		} catch {
			// Status endpoint unreachable — fall through and let the page render.
		}
	}

	async function loadProjects() {
		try {
			projects = await api.listProjects();
		} catch {
			// 401 already handled by the api wrapper; anything else just means
			// the switcher stays empty. Don't block the page.
			projects = [];
		} finally {
			projectsLoaded = true;
		}
	}

	onMount(async () => {
		await checkSetupStatus();
		checking = false;
	});

	// Keep the stored "current project" in sync with the URL whenever we're
	// actually on a project-scoped page.
	$effect(() => {
		if (urlProject && urlProject !== currentProject.value) {
			currentProject.set(urlProject);
		}
	});

	// Load projects lazily once the user is past the auth gate.
	$effect(() => {
		if (bareLayout || checking) return;
		if (!localStorage.getItem('token')) return;
		if (!projectsLoaded) {
			void loadProjects();
		}
	});

	function logout() {
		localStorage.removeItem('token');
		currentProject.set(null);
		goto('/login');
	}

	function toggleSwitcher() {
		switcherOpen = !switcherOpen;
	}

	function selectProject(name: string) {
		switcherOpen = false;
		goto(`/projects/${encodeURIComponent(name)}`);
	}

	function newProject() {
		switcherOpen = false;
		goto('/projects/new');
	}

	function handleDocClick(ev: MouseEvent) {
		if (!switcherEl) return;
		if (!switcherEl.contains(ev.target as Node)) {
			switcherOpen = false;
		}
	}
</script>

<svelte:window onclick={handleDocClick} />

{#if checking}
	<div class="flex min-h-screen items-center justify-center bg-surface-900"></div>
{:else if bareLayout}
	{@render children()}
{:else}
	<div class="flex min-h-screen flex-col bg-surface-900 text-gray-100">
		<header
			class="flex h-14 shrink-0 items-center justify-between border-b border-surface-600 bg-surface-800 px-5"
		>
			<div class="flex items-center gap-4">
				<a href="/" class="text-lg font-semibold tracking-tight text-white">Mortise</a>

				<!-- Project switcher -->
				<div class="relative" bind:this={switcherEl}>
					<button
						type="button"
						onclick={toggleSwitcher}
						class="flex items-center gap-2 rounded-md border border-surface-600 bg-surface-700 px-3 py-1.5 text-sm text-white transition-colors hover:bg-surface-600"
					>
						<span class="text-gray-500">Project:</span>
						<span class="max-w-[18ch] truncate font-medium">
							{activeProject ?? 'All projects'}
						</span>
						<svg
							xmlns="http://www.w3.org/2000/svg"
							fill="none"
							viewBox="0 0 24 24"
							stroke-width="1.5"
							stroke="currentColor"
							class="h-4 w-4 text-gray-400"
							aria-hidden="true"
						>
							<path stroke-linecap="round" stroke-linejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
						</svg>
					</button>

					{#if switcherOpen}
						<div
							class="absolute left-0 top-full z-20 mt-1 w-64 overflow-hidden rounded-md border border-surface-600 bg-surface-800 shadow-lg"
						>
							<div class="max-h-72 overflow-y-auto py-1">
								{#if !projectsLoaded}
									<div class="px-3 py-2 text-xs text-gray-500">Loading...</div>
								{:else if projects.length === 0}
									<div class="px-3 py-2 text-xs text-gray-500">No projects yet</div>
								{:else}
									{#each projects as p}
										<button
											type="button"
											onclick={() => selectProject(p.name)}
											class="flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-sm transition-colors {p.name ===
											activeProject
												? 'bg-surface-600 text-white'
												: 'text-gray-300 hover:bg-surface-700 hover:text-white'}"
										>
											<span class="min-w-0 flex-1 truncate">{p.name}</span>
											<span class="shrink-0 text-xs text-gray-500">
												{p.appCount} app{p.appCount === 1 ? '' : 's'}
											</span>
										</button>
									{/each}
								{/if}
							</div>
							<button
								type="button"
								onclick={newProject}
								class="flex w-full items-center gap-2 border-t border-surface-600 px-3 py-2 text-left text-sm text-accent transition-colors hover:bg-surface-700"
							>
								<span aria-hidden="true">+</span>
								Create new project...
							</button>
						</div>
					{/if}
				</div>
			</div>

			<div class="flex items-center gap-2">
				<a
					href="/settings/git-providers"
					class="rounded-md px-3 py-1.5 text-sm text-gray-400 transition-colors hover:bg-surface-700 hover:text-white"
				>
					Settings
				</a>
				<button
					onclick={logout}
					class="rounded-md px-3 py-1.5 text-sm text-gray-400 transition-colors hover:bg-surface-700 hover:text-white"
				>
					Sign out
				</button>
			</div>
		</header>

		<main class="flex-1 overflow-y-auto p-8">
			{@render children()}
		</main>
	</div>
{/if}
