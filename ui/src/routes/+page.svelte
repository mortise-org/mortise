<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { App, AppPhase } from '$lib/types';

	let apps = $state<App[]>([]);
	let loading = $state(true);
	let error = $state('');

	onMount(async () => {
		if (!localStorage.getItem('token')) {
			goto('/login');
			return;
		}
		try {
			apps = await api.get<App[]>('/apps');
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load apps';
		} finally {
			loading = false;
		}
	});

	const phaseColors: Record<AppPhase, string> = {
		Ready: 'bg-success/20 text-success',
		Deploying: 'bg-accent/20 text-accent',
		Building: 'bg-accent/20 text-accent',
		Pending: 'bg-warning/20 text-warning',
		Failed: 'bg-danger/20 text-danger'
	};

	function lastDeploy(app: App): string {
		const envs = app.status?.environments;
		if (!envs?.length) return 'Never';
		const history = envs[0].deployHistory;
		if (!history?.length) return 'Never';
		const ts = history[history.length - 1].timestamp;
		return new Date(ts).toLocaleDateString('en-US', {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}
</script>

<div>
	<div class="mb-6 flex items-center justify-between">
		<h1 class="text-xl font-semibold text-white">Apps</h1>
		<a
			href="/apps/new"
			class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
		>
			New App
		</a>
	</div>

	{#if loading}
		<div class="text-sm text-gray-500">Loading...</div>
	{:else if error}
		<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
	{:else if apps.length === 0}
		<div class="rounded-lg border border-surface-600 bg-surface-800 p-12 text-center">
			<p class="text-gray-400">No apps yet</p>
			<a href="/apps/new" class="mt-2 inline-block text-sm text-accent hover:text-accent-hover">
				Create your first app
			</a>
		</div>
	{:else}
		<div class="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
			{#each apps as app}
				<a
					href="/apps/{app.metadata.name}"
					class="group rounded-lg border border-surface-600 bg-surface-800 p-5 transition-colors hover:border-surface-500"
				>
					<div class="flex items-start justify-between">
						<h2 class="font-medium text-white group-hover:text-accent">{app.metadata.name}</h2>
						{#if app.status?.phase}
							<span class="rounded-full px-2 py-0.5 text-xs font-medium {phaseColors[app.status.phase]}">
								{app.status.phase}
							</span>
						{/if}
					</div>
					<div class="mt-3 space-y-1 text-xs text-gray-500">
						<div>Source: {app.spec.source.type}</div>
						<div>Last deploy: {lastDeploy(app)}</div>
					</div>
				</a>
			{/each}
		</div>
	{/if}
</div>
