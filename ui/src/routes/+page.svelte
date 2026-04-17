<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { Project } from '$lib/types';
	import { Folder, Plus } from 'lucide-svelte';

	let projects = $state<Project[]>([]);
	let loading = $state(true);
	let error = $state('');

	onMount(async () => {
		if (!localStorage.getItem('mortise_token')) {
			goto('/login');
			return;
		}
		try {
			projects = await api.listProjects();
			store.setProjects(projects);
		} catch(e) {
			error = e instanceof Error ? e.message : 'Failed to load projects';
		} finally {
			loading = false;
		}
	});

	function phaseColor(phase?: string): string {
		if (phase === 'Ready') return 'text-success';
		if (phase === 'Failed') return 'text-danger';
		if (phase === 'Terminating') return 'text-warning';
		return 'text-info';
	}
</script>

<div class="p-8">
	<div class="mb-6 flex items-center justify-between">
		<h1 class="text-xl font-semibold text-white">Projects</h1>
		{#if store.isAdmin}
			<a href="/projects/new"
				class="flex items-center gap-1.5 rounded-md bg-accent px-3 py-2 text-sm font-medium text-white hover:bg-accent-hover transition-colors">
				<Plus class="h-4 w-4" /> New Project
			</a>
		{/if}
	</div>

	{#if loading}
		<div class="grid gap-4 grid-cols-1 sm:grid-cols-2 lg:grid-cols-3">
			{#each [1,2,3] as _}
				<div class="h-28 animate-pulse rounded-lg bg-surface-800 border border-surface-600"></div>
			{/each}
		</div>
	{:else if error}
		<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
	{:else if projects.length === 0}
		<div class="rounded-lg border border-dashed border-surface-600 bg-surface-800/60 p-16 text-center">
			<Folder class="mx-auto mb-4 h-12 w-12 text-gray-600" />
			<h2 class="text-sm font-medium text-gray-400">No projects yet</h2>
			<p class="mx-auto mt-1 max-w-sm text-xs text-gray-500">
				Create your first project to start deploying apps.
			</p>
			{#if store.isAdmin}
				<a href="/projects/new"
					class="mt-4 inline-flex items-center gap-1.5 rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover">
					<Plus class="h-4 w-4" /> Create project
				</a>
			{/if}
		</div>
	{:else}
		<div class="grid gap-4 grid-cols-1 sm:grid-cols-2 lg:grid-cols-3">
			{#each projects as project}
				<a
					href="/projects/{encodeURIComponent(project.name)}"
					class="group block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-all duration-150 hover:-translate-y-0.5 hover:border-surface-500 hover:shadow-lg hover:shadow-black/20 cursor-pointer"
				>
					<div class="flex items-start justify-between gap-2">
						<div class="flex items-center gap-2 min-w-0">
							<Folder class="h-4 w-4 shrink-0 text-gray-400 group-hover:text-accent" />
							<h2 class="truncate text-sm font-medium text-white group-hover:text-accent">{project.name}</h2>
						</div>
						<span class="shrink-0 text-xs {phaseColor(project.phase)}">{project.phase ?? 'Pending'}</span>
					</div>
					{#if project.description}
						<p class="mt-2 text-xs text-gray-500 line-clamp-2">{project.description}</p>
					{/if}
					<div class="mt-3 flex items-center justify-between text-xs text-gray-500">
						<span class="font-mono">{project.namespace}</span>
						<span>{project.appCount} app{project.appCount === 1 ? '' : 's'}</span>
					</div>
				</a>
			{/each}
		</div>
	{/if}
</div>
