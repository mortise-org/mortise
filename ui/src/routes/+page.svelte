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

	const phaseStyles: Record<AppPhase, { dot: string; text: string }> = {
		Ready: { dot: 'bg-success', text: 'text-success' },
		Deploying: { dot: 'bg-accent animate-pulse', text: 'text-accent' },
		Building: { dot: 'bg-accent animate-pulse', text: 'text-accent' },
		Pending: { dot: 'bg-warning', text: 'text-warning' },
		Failed: { dot: 'bg-danger', text: 'text-danger' }
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

	function replicaSummary(app: App): string {
		const env = app.spec.environments?.[0];
		const envStatus = app.status?.environments?.find((e) => e.name === env?.name);
		const ready = envStatus?.readyReplicas ?? 0;
		const desired = env?.replicas ?? 1;
		return `${ready} / ${desired}`;
	}
</script>

<div>
	<div class="mb-6 flex items-center justify-between">
		<h1 class="text-xl font-semibold text-white">Apps</h1>
		<a
			href="/apps/new"
			class="inline-flex items-center gap-2 rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
		>
			<span aria-hidden="true">+</span>
			Deploy from template
		</a>
	</div>

	{#if loading}
		<!-- Skeleton grid -->
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
	{:else if apps.length === 0}
		<!-- Friendly empty state -->
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
					<path
						stroke-linecap="round"
						stroke-linejoin="round"
						d="M12 4.5v15m7.5-7.5h-15"
					/>
				</svg>
			</div>
			<h2 class="text-base font-medium text-white">No apps yet</h2>
			<p class="mx-auto mt-1 max-w-sm text-sm text-gray-500">
				Mortise manages builds, deploys, domains, and TLS. Pick a template or start from a
				container image.
			</p>
			<a
				href="/apps/new"
				class="mt-5 inline-block rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
			>
				Deploy your first app
			</a>
		</div>
	{:else}
		<div class="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
			{#each apps as app}
				{@const phase = app.status?.phase}
				{@const style = phase ? phaseStyles[phase] : undefined}
				<a
					href="/apps/{app.metadata.name}"
					class="group block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-all duration-150 hover:-translate-y-0.5 hover:border-surface-500 hover:shadow-lg hover:shadow-black/20"
				>
					<div class="flex items-start justify-between gap-3">
						<h2 class="truncate font-medium text-white group-hover:text-accent">
							{app.metadata.name}
						</h2>
						{#if phase}
							<span
								class="inline-flex shrink-0 items-center gap-1.5 text-xs font-medium {style?.text ??
									'text-gray-400'}"
							>
								<span class="h-1.5 w-1.5 rounded-full {style?.dot ?? 'bg-gray-500'}"></span>
								{phase}
							</span>
						{/if}
					</div>
					<dl class="mt-4 space-y-1.5 text-xs">
						<div class="flex justify-between">
							<dt class="text-gray-500">Source</dt>
							<dd class="text-gray-300">{app.spec.source.type}</dd>
						</div>
						<div class="flex justify-between">
							<dt class="text-gray-500">Replicas</dt>
							<dd class="text-gray-300">{replicaSummary(app)}</dd>
						</div>
						<div class="flex justify-between">
							<dt class="text-gray-500">Last deploy</dt>
							<dd class="text-gray-300">{lastDeploy(app)}</dd>
						</div>
					</dl>
				</a>
			{/each}
		</div>
	{/if}
</div>
