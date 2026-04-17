<script lang="ts">
	import { Activity, X } from 'lucide-svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { ActivityEvent } from '$lib/types';

	let { project }: { project: string } = $props();

	let events = $state<ActivityEvent[]>([]);
	let loading = $state(false);
	let filter = $state<'all' | 'deploys' | 'changes' | 'members'>('all');

	$effect(() => {
		if (store.activityRailOpen && project) {
			void loadActivity();
		}
	});

	async function loadActivity() {
		loading = true;
		try {
			events = await api.listActivity(project);
		} catch {
			events = [];
		} finally {
			loading = false;
		}
	}

	const filteredEvents = $derived.by(() => {
		if (filter === 'all') return events;
		if (filter === 'deploys') return events.filter(e => e.action === 'deploy' || e.action === 'rollback');
		if (filter === 'changes') return events.filter(e => e.action !== 'deploy' && e.action !== 'rollback' && e.action !== 'member');
		return events.filter(e => e.action === 'member');
	});

	function relativeTime(ts: string): string {
		const d = new Date(ts);
		const diff = (Date.now() - d.getTime()) / 1000;
		if (diff < 60) return 'just now';
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}
</script>

{#if store.activityRailOpen}
<!-- Backdrop (click to close) -->
<div class="fixed inset-0 z-30" onclick={() => store.toggleActivityRail()} role="presentation"></div>

<!-- Rail panel — slides in from right, sits OVER canvas/drawer -->
<div
	class="fixed right-0 top-14 bottom-0 z-40 flex w-80 flex-col border-l border-surface-600 bg-surface-800 shadow-2xl transition-transform duration-200 ease-out"
	style="transform: {store.activityRailOpen ? 'translateX(0)' : 'translateX(100%)'}"
>
	<!-- Header -->
	<div class="flex items-center justify-between border-b border-surface-600 px-4 py-3">
		<h3 class="text-sm font-semibold text-white">Activity</h3>
		<button
			onclick={() => store.toggleActivityRail()}
			class="rounded-md p-1.5 text-gray-500 hover:bg-surface-700 hover:text-white"
		>
			<X class="h-4 w-4" />
		</button>
	</div>

	<!-- Filter chips -->
	<div class="flex gap-1 border-b border-surface-600 px-3 py-2">
		{#each (['all', 'deploys', 'changes', 'members'] as const) as f}
			<button
				type="button"
				onclick={() => (filter = f)}
				class="rounded px-2 py-1 text-xs {filter === f
					? 'bg-surface-600 text-white'
					: 'text-gray-400 hover:text-white'}"
			>
				{f.charAt(0).toUpperCase() + f.slice(1)}
			</button>
		{/each}
	</div>

	<!-- Events list -->
	<div class="flex-1 overflow-y-auto">
		{#if loading}
			{#each [1, 2, 3, 4, 5] as _}
				<div class="px-4 py-3 border-b border-surface-600">
					<div class="h-3 w-3/4 animate-pulse rounded bg-surface-700 mb-1"></div>
					<div class="h-2.5 w-1/2 animate-pulse rounded bg-surface-700"></div>
				</div>
			{/each}
		{:else if filteredEvents.length === 0}
			<div class="flex flex-col items-center justify-center py-16 text-center">
				<Activity class="h-10 w-10 text-gray-600 mb-3" />
				<p class="text-xs text-gray-500">No activity yet</p>
			</div>
		{:else}
			{#each filteredEvents as ev}
				<div class="border-b border-surface-600 px-4 py-3 hover:bg-surface-700">
					<div class="flex items-start justify-between gap-2">
						<div class="flex items-start gap-2 min-w-0">
							<!-- Actor avatar (initials) -->
							<div
								class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-accent/20 text-xs font-medium text-accent"
							>
								{ev.actor ? ev.actor.charAt(0).toUpperCase() : '?'}
							</div>
							<div class="min-w-0">
								<p class="text-xs text-gray-300 leading-snug">{ev.msg}</p>
								<p class="mt-0.5 text-xs text-gray-500">{ev.actor}</p>
							</div>
						</div>
						<span class="shrink-0 text-xs text-gray-600">{relativeTime(ev.ts)}</span>
					</div>
				</div>
			{/each}
		{/if}
	</div>
</div>
{/if}
