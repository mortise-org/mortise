<script lang="ts">
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import type { PreviewEnvironment } from '$lib/types';
	import { GitBranch, Clock, Globe } from 'lucide-svelte';

	const projectName = $derived(page.params.project ?? '');
	let previews = $state<PreviewEnvironment[]>([]);
	let loading = $state(true);

	onMount(async () => {
		// Preview environments API not yet implemented; show empty state
		loading = false;
	});

	function phaseClass(phase: string): string {
		if (phase === 'Ready') return 'bg-success/10 text-success';
		if (phase === 'Building') return 'bg-warning/10 text-warning';
		if (phase === 'Failed') return 'bg-danger/10 text-danger';
		if (phase === 'Expired') return 'bg-surface-700 text-gray-400';
		return 'bg-info/10 text-info';
	}
</script>

<div class="p-8">
	<div class="mb-6 flex items-center justify-between">
		<div>
			<h1 class="text-xl font-semibold text-white">PR Environments</h1>
			<p class="mt-1 text-sm text-gray-500">Active preview environments for open pull requests</p>
		</div>
		<a
			href="/projects/{encodeURIComponent(projectName)}/settings"
			class="text-xs text-gray-400 hover:text-white"
		>
			Configure in Project Settings &rarr;
		</a>
	</div>

	{#if loading}
		<div class="space-y-2">
			{#each [1, 2, 3] as _}
				<div class="h-16 animate-pulse rounded-lg bg-surface-800"></div>
			{/each}
		</div>
	{:else if previews.length === 0}
		<div
			class="rounded-lg border border-dashed border-surface-600 bg-surface-800/60 p-12 text-center"
		>
			<GitBranch class="mx-auto mb-4 h-12 w-12 text-gray-600" />
			<h2 class="text-sm font-medium text-gray-400">No active PR environments</h2>
			<p class="mx-auto mt-1 max-w-sm text-xs text-gray-500">
				When PR Environments are enabled and a pull request is opened, preview deployments will
				appear here.
			</p>
			<a
				href="/projects/{encodeURIComponent(projectName)}/settings"
				class="mt-4 inline-block rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover"
			>
				Enable PR Environments
			</a>
		</div>
	{:else}
		<div class="space-y-2">
			{#each previews as env}
				<div
					class="flex items-center justify-between rounded-lg border border-surface-600 bg-surface-800 px-4 py-3"
				>
					<div class="flex items-center gap-3">
						<GitBranch class="h-4 w-4 text-gray-400" />
						<div>
							<p class="text-sm font-medium text-white">PR #{env.pr.number} · {env.pr.branch}</p>
							<p class="text-xs text-gray-500">{env.appRef}</p>
						</div>
					</div>
					<div class="flex items-center gap-3">
						{#if env.url}
							<a
								href={env.url}
								target="_blank"
								class="flex items-center gap-1 text-xs text-accent hover:underline"
							>
								<Globe class="h-3.5 w-3.5" /> Open
							</a>
						{/if}
						{#if env.expiresAt}
							<span class="flex items-center gap-1 text-xs text-gray-500">
								<Clock class="h-3.5 w-3.5" />
								expires {new Date(env.expiresAt).toLocaleDateString()}
							</span>
						{/if}
						<span
							class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium {phaseClass(env.phase)}"
						>
							{env.phase}
						</span>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>
