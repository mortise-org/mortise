<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { App, AppPhase, Project } from '$lib/types';
	import ProjectCanvas from '$lib/components/ProjectCanvas.svelte';
	import NewAppModal from '$lib/components/NewAppModal.svelte';
	import AppDrawer from '$lib/components/AppDrawer.svelte';
	import { LayoutDashboard, List, Plus, GitBranch, Container, Cloud, Clock } from 'lucide-svelte';

	const projectName = $derived(page.params.project ?? '');
	// App name from URL (e.g. /projects/foo/apps/bar → 'bar')
	const urlApp = $derived(page.params.app ?? null);

	let showNewApp = $state(false);
	let selectedApp = $state<string | null>(null);
	let project = $state<Project | null>(null);
	let apps = $state<App[]>([]);
	let loading = $state(true);
	let error = $state('');
	let deploying = $state(false);
	let deployError = $state('');
	let showDetailsModal = $state(false);

	onMount(async () => {
		if (!localStorage.getItem('mortise_token')) {
			goto('/login');
			return;
		}
		await load();
	});

	async function deployAll() {
		if (deploying || !store.hasUnsavedChanges) return;
		deploying = true;
		deployError = '';
		showDetailsModal = false;
		try {
			for (const [appName, change] of store.stagedChanges) {
				await api.updateApp(projectName, appName, change.dirty);
			}
			store.discardAll();
			await load();
		} catch (e) {
			deployError = e instanceof Error ? e.message : 'Deploy failed';
		} finally {
			deploying = false;
		}
	}

	$effect(() => {
		function handleKey(e: KeyboardEvent) {
			if (e.key === 'Enter' && e.shiftKey && store.hasUnsavedChanges) {
				e.preventDefault();
				void deployAll();
			}
			if (e.key === 'Escape') showDetailsModal = false;
		}
		window.addEventListener('keydown', handleKey);
		return () => window.removeEventListener('keydown', handleKey);
	});

	$effect(() => {
		void projectName;
		if (!loading && projectName && projectName !== project?.name) {
			void load();
		}
	});

	async function load() {
		loading = true;
		error = '';
		try {
			[project, apps] = await Promise.all([
				api.getProject(projectName),
				api.listApps(projectName)
			]);
			store.setProject(projectName);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load project';
		} finally {
			loading = false;
		}
	}

	function lastDeploy(app: App): string {
		const envs = app.status?.environments;
		if (!envs?.length) return '-';
		const history = envs[0].deployHistory;
		if (!history?.length) return 'Never';
		const ts = history[history.length - 1].timestamp;
		const d = new Date(ts);
		const now = new Date();
		const diff = (now.getTime() - d.getTime()) / 1000;
		if (diff < 60) return 'just now';
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}

	const phaseStyles: Record<AppPhase, { dot: string; text: string }> = {
		Ready: { dot: 'bg-success', text: 'text-success' },
		Deploying: { dot: 'bg-warning animate-pulse', text: 'text-warning' },
		Building: { dot: 'bg-warning animate-pulse', text: 'text-warning' },
		Pending: { dot: 'bg-info', text: 'text-info' },
		Failed: { dot: 'bg-danger', text: 'text-danger' }
	};

	function sourceIcon(app: App) {
		const t = app.spec.source.type;
		if (t === 'git') return GitBranch;
		if (t === 'image') return Container;
		return Cloud;
	}
</script>

<div class="flex h-full flex-col">
	<!-- Top toolbar: staged-changes bar only -->
	{#if store.hasUnsavedChanges || deployError}
	<div class="flex shrink-0 items-center justify-center border-b border-surface-600 bg-surface-800 px-4 py-2 gap-2">
		{#if store.hasUnsavedChanges}
			<div class="flex items-center gap-2 rounded-md border border-accent/30 bg-accent/10 px-3 py-1.5">
				<span class="text-xs text-accent">
					{store.stagedChangeCount} change{store.stagedChangeCount === 1 ? '' : 's'} to apply
				</span>
				<button
					type="button"
					onclick={() => showDetailsModal = true}
					class="text-xs text-gray-400 hover:text-white"
				>
					Details
				</button>
				<button
					type="button"
					onclick={() => store.discardAll()}
					class="text-xs text-gray-400 hover:text-white"
				>
					Discard
				</button>
				<button
					type="button"
					onclick={deployAll}
					disabled={deploying}
					class="text-xs font-medium text-accent hover:text-accent-hover disabled:opacity-50"
				>
					{deploying ? 'Deploying…' : 'Deploy ⇧+Enter'}
				</button>
			</div>
		{/if}
		{#if deployError}
			<div class="text-xs text-danger">{deployError}</div>
		{/if}
	</div>
	{/if}

	<!-- Content area -->
	{#if loading}
		<div class="flex-1 animate-pulse p-6">
			<div class="mb-4 h-5 w-40 rounded bg-surface-700"></div>
			<div class="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
				{#each Array(3) as _}
					<div class="h-28 rounded-lg bg-surface-800"></div>
				{/each}
			</div>
		</div>
	{:else if error && !project}
		<div class="m-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
	{:else if project}
		{#if error}
			<div class="mx-4 mt-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
		{/if}

		{#if store.viewMode === 'canvas'}
			<!-- Canvas view -->
			<div class="relative flex-1 overflow-hidden" style="height: calc(100vh - 57px)">
				<!-- Floating controls overlay -->
				<div class="absolute top-3 right-3 z-10 flex items-center gap-2">
					<div class="flex overflow-hidden rounded-md border border-surface-600 bg-surface-800/90 backdrop-blur-sm">
						<button
							type="button"
							onclick={() => store.setViewMode('canvas')}
							class="px-2 py-1.5 {store.viewMode === 'canvas' ? 'bg-surface-600 text-white' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
							title="Canvas view"
						>
							<LayoutDashboard class="h-4 w-4" />
						</button>
						<button
							type="button"
							onclick={() => store.setViewMode('list')}
							class="px-2 py-1.5 {store.viewMode === 'list' ? 'bg-surface-600 text-white' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
							title="List view"
						>
							<List class="h-4 w-4" />
						</button>
					</div>
					<button
						type="button"
						onclick={() => showNewApp = true}
						class="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-accent-hover shadow"
					>
						<Plus class="h-4 w-4" /> Add
					</button>
				</div>
				<ProjectCanvas
					{projectName}
					{apps}
					selectedApp={urlApp}
					onAppOpen={(name) => selectedApp = name}
					onAddApp={() => showNewApp = true}
					onDeleteApp={async (name) => {
						if (confirm(`Delete app "${name}"? This cannot be undone.`)) {
							await api.deleteApp(projectName, name);
							await load();
						}
					}}
				/>
			</div>
		{:else}
			<!-- List view -->
			<div class="relative flex-1 overflow-auto p-4">
				<!-- Floating controls overlay -->
				<div class="absolute top-3 right-3 z-10 flex items-center gap-2">
					<div class="flex overflow-hidden rounded-md border border-surface-600 bg-surface-800/90">
						<button
							type="button"
							onclick={() => store.setViewMode('canvas')}
							class="px-2 py-1.5 {store.viewMode === 'canvas' ? 'bg-surface-600 text-white' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
							title="Canvas view"
						>
							<LayoutDashboard class="h-4 w-4" />
						</button>
						<button
							type="button"
							onclick={() => store.setViewMode('list')}
							class="px-2 py-1.5 {store.viewMode === 'list' ? 'bg-surface-600 text-white' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
							title="List view"
						>
							<List class="h-4 w-4" />
						</button>
					</div>
					<button
						type="button"
						onclick={() => showNewApp = true}
						class="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-accent-hover shadow"
					>
						<Plus class="h-4 w-4" /> Add
					</button>
				</div>
				{#if apps.length === 0}
					<div class="rounded-lg border border-dashed border-surface-600 bg-surface-800/60 p-12 text-center">
						<div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-surface-700 text-accent" aria-hidden="true">
							<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="h-7 w-7">
								<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
							</svg>
						</div>
						<h2 class="text-base font-medium text-white">No apps in this project</h2>
						<p class="mx-auto mt-1 max-w-sm text-sm text-gray-500">Deploy your first app - pick a template or start from a container image.</p>
						<button
							type="button"
							onclick={() => showNewApp = true}
							class="mt-5 inline-block rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
						>
							Deploy an app
						</button>
					</div>
				{:else}
					<table class="w-full text-sm">
						<thead>
							<tr class="border-b border-surface-600 text-left text-xs text-gray-500">
								<th class="pb-2 pr-4 font-medium">Name</th>
								<th class="pb-2 pr-4 font-medium">Source</th>
								<th class="pb-2 pr-4 font-medium">Kind</th>
								<th class="pb-2 pr-4 font-medium">Status</th>
								<th class="pb-2 pr-4 font-medium">Domain</th>
								<th class="pb-2 font-medium">Last deploy</th>
							</tr>
						</thead>
						<tbody>
							{#each apps as app}
								{@const phase = app.status?.phase}
								{@const style = phase ? phaseStyles[phase] : undefined}
								{@const Icon = sourceIcon(app)}
								{@const domain = app.spec.environments?.[0]?.domain}
								<tr
									class="group border-b border-surface-700 hover:bg-surface-800/60 cursor-pointer"
									onclick={() => selectedApp = app.metadata.name}
								>
									<td class="py-3 pr-4">
										<div class="flex items-center gap-2 font-medium text-white group-hover:text-accent">
											{#if (app as { kind?: string }).kind === 'cron'}
												<Clock class="h-3.5 w-3.5 shrink-0 text-gray-400" />
											{/if}
											{app.metadata.name}
										</div>
									</td>
									<td class="py-3 pr-4">
										<div class="flex items-center gap-1.5 text-gray-400">
											<Icon class="h-3.5 w-3.5" />
											<span>{app.spec.source.type}</span>
										</div>
									</td>
									<td class="py-3 pr-4 text-gray-400 capitalize">
										{(app as { kind?: string }).kind ?? 'web'}
									</td>
									<td class="py-3 pr-4">
										{#if phase}
											<span class="inline-flex items-center gap-1.5 text-xs font-medium {style?.text ?? 'text-gray-400'}">
												<span class="h-1.5 w-1.5 rounded-full {style?.dot ?? 'bg-gray-500'}"></span>
												{phase}
											</span>
										{:else}
											<span class="text-gray-500">-</span>
										{/if}
									</td>
									<td class="py-3 pr-4 font-mono text-xs text-gray-500">
										{#if app.spec.network?.public === false}
											<span class="text-gray-500">Private</span>
										{:else}
											{domain ?? '-'}
										{/if}
									</td>
									<td class="py-3 text-gray-500">{lastDeploy(app)}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			</div>
		{/if}
	{/if}
</div>

{#if selectedApp}
	<AppDrawer project={projectName} appName={selectedApp} onClose={() => selectedApp = null} />
{/if}

{#if showNewApp}
  <NewAppModal
    project={projectName}
    onClose={() => showNewApp = false}
    onCreated={async (name) => { showNewApp = false; await load(); selectedApp = name; }}
  />
{/if}

{#if showDetailsModal}
	<!-- Details modal -->
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60" role="dialog" aria-modal="true">
		<div class="w-full max-w-lg rounded-lg border border-surface-600 bg-surface-800 shadow-2xl">
			<div class="flex items-center justify-between border-b border-surface-600 px-4 py-3">
				<h2 class="text-sm font-semibold text-white">Pending changes</h2>
				<button type="button" onclick={() => showDetailsModal = false} class="text-gray-500 hover:text-white">✕</button>
			</div>
			<div class="max-h-80 overflow-y-auto p-4 space-y-3">
				{#each [...store.stagedChanges.entries()] as [appName, change]}
					<div class="rounded-md border border-surface-600 p-3">
						<div class="flex items-center justify-between">
							<span class="text-sm font-medium text-white">{appName}</span>
							<button type="button" onclick={() => store.discardChange(appName)} class="text-xs text-gray-500 hover:text-danger">Discard</button>
						</div>
						{#if change.original.source?.image !== change.dirty.source?.image}
							<div class="mt-1 text-xs text-gray-400">Image: <span class="font-mono text-warning">{change.dirty.source?.image ?? '-'}</span></div>
						{/if}
						{#if (change.original.environments?.[0]?.replicas ?? 1) !== (change.dirty.environments?.[0]?.replicas ?? 1)}
							<div class="mt-1 text-xs text-gray-400">Replicas: {change.original.environments?.[0]?.replicas ?? 1} → <span class="text-accent">{change.dirty.environments?.[0]?.replicas ?? 1}</span></div>
						{/if}
					</div>
				{/each}
			</div>
			<div class="flex justify-end gap-2 border-t border-surface-600 px-4 py-3">
				<button type="button" onclick={() => showDetailsModal = false} class="rounded px-3 py-1.5 text-sm text-gray-400 hover:text-white">Cancel</button>
				<button type="button" onclick={deployAll} disabled={deploying} class="rounded bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
					{deploying ? 'Deploying…' : 'Deploy Changes'}
				</button>
			</div>
		</div>
	</div>
{/if}
