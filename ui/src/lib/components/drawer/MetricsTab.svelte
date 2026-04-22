<script lang="ts">
	import { BarChart3 } from 'lucide-svelte';
	import { api } from '$lib/api';
	import type { App, PodMetricsCurrent, PodMetricsSeries } from '$lib/types';

	let { app, project, env }: { app: App; project: string; env: string } = $props();

	type TimeRange = 'live' | '1h' | '6h' | '24h' | '7d';
	let range: TimeRange = $state('live');

	let currentAvailable = $state(false);
	let currentPods = $state<PodMetricsCurrent[]>([]);
	let historyAvailable = $state<boolean | null>(null);
	let historyPods = $state<PodMetricsSeries[]>([]);
	let loading = $state(true);
	let pollTimer: ReturnType<typeof setInterval> | undefined;

	function formatCPU(cores: number): string {
		if (cores < 0.01) return `${(cores * 1000).toFixed(0)}m`;
		return `${cores.toFixed(2)} cores`;
	}

	function formatMemory(bytes: number): string {
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
		if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(0)} MB`;
		return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
	}

	async function fetchCurrent() {
		try {
			const res = await api.getMetricsCurrent(project, app.metadata.name, env);
			currentAvailable = res.available;
			currentPods = res.pods ?? [];
		} catch { /* ignore */ }
		loading = false;
	}

	async function fetchHistory(hours: number) {
		loading = true;
		const end = Math.floor(Date.now() / 1000);
		const start = end - hours * 3600;
		const step = hours <= 1 ? 15 : hours <= 6 ? 60 : hours <= 24 ? 300 : 900;
		try {
			const res = await api.getMetricsHistory(project, app.metadata.name, env, start, end, step);
			historyAvailable = res.available;
			historyPods = res.pods ?? [];
		} catch { /* ignore */ }
		loading = false;
	}

	function setRange(r: TimeRange) {
		range = r;
		if (pollTimer) { clearInterval(pollTimer); pollTimer = undefined; }
		if (r === 'live') {
			fetchCurrent();
			pollTimer = setInterval(fetchCurrent, 15000);
		} else {
			const hours = { '1h': 1, '6h': 6, '24h': 24, '7d': 168 }[r];
			fetchHistory(hours);
		}
	}

	$effect(() => {
		setRange('live');
		return () => { if (pollTimer) clearInterval(pollTimer); };
	});
</script>

<div class="flex flex-col gap-3 p-4">
	<div class="flex items-center gap-2">
		{#each ['live', '1h', '6h', '24h', '7d'] as r}
			<button
				onclick={() => setRange(r as TimeRange)}
				class="rounded px-2 py-1 text-xs font-medium transition-colors {range === r ? 'bg-accent text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-200'}"
			>{r === 'live' ? 'Live' : r}</button>
		{/each}
	</div>

	{#if loading}
		<div class="flex items-center justify-center py-12">
			<div class="h-5 w-5 animate-spin rounded-full border-2 border-gray-600 border-t-accent"></div>
		</div>
	{:else if range === 'live'}
		{#if !currentAvailable}
			<div class="flex flex-col items-center justify-center py-12 text-center">
				<BarChart3 class="mb-4 h-10 w-10 text-gray-600" />
				<p class="text-sm text-gray-400">Install metrics-server to enable real-time metrics.</p>
			</div>
		{:else if currentPods.length === 0}
			<p class="py-8 text-center text-sm text-gray-500">No pods running.</p>
		{:else}
			{#each currentPods as pod}
				<div class="rounded-lg border border-gray-700 bg-gray-800/50 p-3">
					<p class="mb-2 text-xs font-medium text-gray-300">{pod.name}</p>
					<div class="grid grid-cols-2 gap-4">
						<div>
							<p class="text-[10px] uppercase tracking-wide text-gray-500">CPU</p>
							<p class="text-sm font-mono text-gray-200">{formatCPU(pod.cpu)}</p>
						</div>
						<div>
							<p class="text-[10px] uppercase tracking-wide text-gray-500">Memory</p>
							<p class="text-sm font-mono text-gray-200">{formatMemory(pod.memory)}</p>
						</div>
					</div>
				</div>
			{/each}
		{/if}
	{:else}
		{#if historyAvailable === false}
			<div class="flex flex-col items-center justify-center py-12 text-center">
				<BarChart3 class="mb-4 h-10 w-10 text-gray-600" />
				<p class="text-sm text-gray-400">Enable a metrics adapter for historical data.</p>
			</div>
		{:else if historyPods.length === 0}
			<p class="py-8 text-center text-sm text-gray-500">No metrics data for this range.</p>
		{:else}
			{#each historyPods as pod}
				<div class="rounded-lg border border-gray-700 bg-gray-800/50 p-3">
					<p class="mb-2 text-xs font-medium text-gray-300">{pod.name}</p>
					<div class="grid grid-cols-2 gap-4">
						<div>
							<p class="text-[10px] uppercase tracking-wide text-gray-500">CPU ({pod.cpu.length} pts)</p>
							<p class="text-sm font-mono text-gray-200">
								{formatCPU(pod.cpu.length > 0 ? pod.cpu[pod.cpu.length - 1][1] : 0)}
							</p>
						</div>
						<div>
							<p class="text-[10px] uppercase tracking-wide text-gray-500">Memory ({pod.memory.length} pts)</p>
							<p class="text-sm font-mono text-gray-200">
								{formatMemory(pod.memory.length > 0 ? pod.memory[pod.memory.length - 1][1] : 0)}
							</p>
						</div>
					</div>
				</div>
			{/each}
		{/if}
	{/if}
</div>
