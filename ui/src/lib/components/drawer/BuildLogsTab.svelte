<script lang="ts">
	import { onMount } from 'svelte';
	import { Loader2 } from 'lucide-svelte';
	import { api } from '$lib/api';
	import type { App, BuildLogsResponse, LogLineEvent } from '$lib/types';
	import LogLine from '$lib/components/LogLine.svelte';

	let {
		project,
		app,
		sseBuildLogs = null
	}: {
		project: string;
		app: App;
		sseBuildLogs?: BuildLogsResponse | null;
	} = $props();

	const isImageSource = $derived(app.spec.source?.type === 'image');
	let fetchedBuildLogs = $state<BuildLogsResponse | null>(null);
	const buildResp = $derived(sseBuildLogs ?? fetchedBuildLogs);

	const buildEvents = $derived<LogLineEvent[]>(
		(buildResp?.lines ?? []).map((l) => ({ pod: '', ts: '', line: l, stream: 'stdout' }))
	);

	function buildStatusColor(status?: string): string {
		switch (status) {
			case 'Succeeded':
				return 'bg-success/20 text-success';
			case 'Failed':
				return 'bg-danger/20 text-danger';
			case 'Running':
				return 'bg-warning/20 text-warning';
			default:
				return 'bg-surface-700 text-gray-400';
		}
	}

	function relTime(ts?: string): string {
		if (!ts) return '';
		const d = new Date(ts).getTime();
		if (Number.isNaN(d)) return '';
		const secs = Math.max(0, Math.floor((Date.now() - d) / 1000));
		if (secs < 60) return `${secs}s ago`;
		const mins = Math.floor(secs / 60);
		if (mins < 60) return `${mins}m ago`;
		const hrs = Math.floor(mins / 60);
		if (hrs < 24) return `${hrs}h ago`;
		return `${Math.floor(hrs / 24)}d ago`;
	}

	onMount(() => {
		if (isImageSource || sseBuildLogs) return;
		api.getBuildLogs(project, app.metadata.name).then((resp) => {
			if (!sseBuildLogs) fetchedBuildLogs = resp;
		}).catch(() => {});
	});
</script>

<div class="flex h-full flex-col gap-1 pt-4">
	<div class="flex flex-wrap items-center justify-between gap-2">
		<div class="flex items-center gap-2">
			<span class="text-sm font-medium text-white">Build</span>
			{#if buildResp?.building}
				<span
					class="inline-block h-2 w-2 animate-pulse rounded-full bg-warning"
					aria-hidden="true"
					title="streaming"
				></span>
			{/if}
		</div>
		<div class="flex items-center gap-2 text-xs">
			{#if buildResp?.status}
				<span class="rounded-full px-2 py-0.5 text-[11px] font-medium {buildStatusColor(buildResp.status)}">
					{buildResp.status}
				</span>
			{/if}
			<span class="font-mono text-gray-500" title={buildResp?.commitSHA ?? ''}>
				{buildResp?.commitSHA ? buildResp.commitSHA.slice(0, 7) : '-'}
			</span>
			<span class="text-gray-500" title={buildResp?.timestamp ?? ''}>
				{relTime(buildResp?.timestamp) || '-'}
			</span>
		</div>
	</div>

	<div class="flex-1 overflow-y-auto rounded-md bg-surface-900 p-3" style="min-height: 300px; max-height: calc(100vh - 280px)">
		{#if isImageSource}
			<p class="text-xs italic text-gray-600">Image source: builds are skipped</p>
		{:else if buildResp === null}
			<div class="flex items-center gap-2 text-xs text-gray-500">
				<Loader2 class="h-3.5 w-3.5 animate-spin" />
				<span>Loading...</span>
			</div>
		{:else if buildResp.error}
			<div class="space-y-2">
				<p class="text-xs font-medium text-danger">Build error:</p>
				<pre class="whitespace-pre-wrap break-all rounded bg-surface-800 p-2 text-xs text-danger/80">{buildResp.error}</pre>
			</div>
		{:else if buildEvents.length === 0}
			{#if buildResp.building}
				<div class="flex items-center gap-2 text-xs text-warning">
					<Loader2 class="h-3.5 w-3.5 animate-spin" />
					<span>Waiting for build output...</span>
				</div>
			{:else}
				<p class="text-xs italic text-gray-600">No build yet - pushes to git will appear here</p>
			{/if}
		{:else}
			{#each buildEvents as evt, i (i)}
				<LogLine event={evt} showPodBadge={false} />
			{/each}
		{/if}
	</div>
</div>
