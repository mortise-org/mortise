<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { Project, ProjectPhase } from '$lib/types';

	let projects = $state<Project[]>([]);
	let loading = $state(true);
	let error = $state('');

	onMount(async () => {
		if (!localStorage.getItem('token')) {
			goto('/login');
			return;
		}
		try {
			projects = await api.listProjects();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load projects';
		} finally {
			loading = false;
		}
	});

	const phaseStyles: Record<ProjectPhase, { dot: string; text: string }> = {
		Ready: { dot: 'bg-success', text: 'text-success' },
		Pending: { dot: 'bg-warning', text: 'text-warning' },
		Terminating: { dot: 'bg-accent animate-pulse', text: 'text-accent' },
		Failed: { dot: 'bg-danger', text: 'text-danger' }
	};
</script>

<div>
	<div class="mb-6 flex items-center justify-between">
		<h1 class="text-xl font-semibold text-white">Projects</h1>
		<a
			href="/projects/new"
			class="inline-flex items-center gap-2 rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
		>
			<span aria-hidden="true">+</span>
			New project
		</a>
	</div>

	{#if loading}
		<div class="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
			{#each Array(3) as _}
				<div class="animate-pulse rounded-lg border border-surface-600 bg-surface-800 p-5">
					<div class="flex items-start justify-between">
						<div class="h-4 w-32 rounded bg-surface-700"></div>
						<div class="h-5 w-16 rounded-full bg-surface-700"></div>
					</div>
					<div class="mt-4 space-y-2">
						<div class="h-3 w-40 rounded bg-surface-700"></div>
						<div class="h-3 w-28 rounded bg-surface-700"></div>
					</div>
				</div>
			{/each}
		</div>
	{:else if error}
		<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
	{:else if projects.length === 0}
		<div
			class="rounded-lg border border-dashed border-surface-600 bg-surface-800/60 p-12 text-center"
		>
			<div
				class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-surface-700 text-accent"
				aria-hidden="true"
			>
				<svg
					xmlns="http://www.w3.org/2000/svg"
					fill="none"
					viewBox="0 0 24 24"
					stroke-width="1.5"
					stroke="currentColor"
					class="h-7 w-7"
				>
					<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
				</svg>
			</div>
			<h2 class="text-base font-medium text-white">No projects yet</h2>
			<p class="mx-auto mt-1 max-w-sm text-sm text-gray-500">
				Projects group your apps, domains, and secrets. Start by creating one.
			</p>
			<a
				href="/projects/new"
				class="mt-5 inline-block rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
			>
				Create your first project
			</a>
		</div>
	{:else}
		<div class="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
			{#each projects as p}
				{@const style = p.phase ? phaseStyles[p.phase] : undefined}
				<a
					href="/projects/{encodeURIComponent(p.name)}"
					class="group block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-all duration-150 hover:-translate-y-0.5 hover:border-surface-500 hover:shadow-lg hover:shadow-black/20"
				>
					<div class="flex items-start justify-between gap-3">
						<h2 class="truncate font-medium text-white group-hover:text-accent">
							{p.name}
						</h2>
						{#if p.phase}
							<span
								class="inline-flex shrink-0 items-center gap-1.5 text-xs font-medium {style?.text ??
									'text-gray-400'}"
							>
								<span class="h-1.5 w-1.5 rounded-full {style?.dot ?? 'bg-gray-500'}"></span>
								{p.phase}
							</span>
						{/if}
					</div>
					{#if p.description}
						<p class="mt-2 line-clamp-2 text-sm text-gray-400">{p.description}</p>
					{/if}
					<dl class="mt-4 space-y-1.5 text-xs">
						<div class="flex justify-between">
							<dt class="text-gray-500">Apps</dt>
							<dd class="text-gray-300">{p.appCount}</dd>
						</div>
						<div class="flex justify-between">
							<dt class="text-gray-500">Namespace</dt>
							<dd class="font-mono text-gray-300">{p.namespace}</dd>
						</div>
					</dl>
				</a>
			{/each}
		</div>
	{/if}
</div>
