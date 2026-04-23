<script lang="ts">
	import { onMount } from 'svelte';
	import { BarChart3, Activity } from 'lucide-svelte';
	import { api } from '$lib/api';
	import { hashPodColor } from '$lib/pod-colors';
	import MetricsLineChart from '$lib/components/drawer/MetricsLineChart.svelte';
	import TrafficChart from '$lib/components/drawer/TrafficChart.svelte';
	import type { App, PodMetricsCurrent, PodMetricsSeries, TrafficSeries } from '$lib/types';

	let { app, project, env }: { app: App; project: string; env: string } = $props();

	function parseK8sResource(val: string | undefined): number {
		if (!val) return 0;
		const match = val.match(/^([0-9.]+)\s*([A-Za-z]*)$/);
		if (!match) return 0;
		const n = parseFloat(match[1]);
		const unit = match[2];
		if (!unit) return n;
		switch (unit) {
			case 'm': return n / 1000;
			case 'Ki': return n * 1024;
			case 'Mi': return n * 1024 * 1024;
			case 'Gi': return n * 1024 * 1024 * 1024;
			case 'Ti': return n * 1024 * 1024 * 1024 * 1024;
			case 'K': case 'k': return n * 1000;
			case 'M': return n * 1000 * 1000;
			case 'G': return n * 1000 * 1000 * 1000;
			default: return n;
		}
	}

	const envResources = $derived(app.spec.environments?.find((e) => e.name === env)?.resources);
	const cpuLimit = $derived(parseK8sResource(envResources?.cpu));
	const memoryLimit = $derived(parseK8sResource(envResources?.memory));

	type TimeRange = 'live' | '1h' | '6h' | '24h' | '7d';
	let range: TimeRange = $state('live');

	let podDropdownOpen = $state(false);
	let metricsAvailable = $state(false);
	let currentPods = $state<PodMetricsCurrent[]>([]);
	let historyPods = $state<PodMetricsSeries[]>([]);
	let trafficAvailable = $state(false);
	let trafficSeries = $state<TrafficSeries | null>(null);
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

	function formatRPS(v: number): string {
		if (v < 1) return v.toFixed(2);
		if (v < 100) return v.toFixed(1);
		return v.toFixed(0);
	}

	function formatLatency(ms: number): string {
		if (ms < 1) return `${(ms * 1000).toFixed(0)}µs`;
		if (ms < 1000) return `${ms.toFixed(0)}ms`;
		return `${(ms / 1000).toFixed(1)}s`;
	}

	function formatPercent(v: number): string {
		return `${v.toFixed(1)}%`;
	}

	function formatBytes(bytes: number): string {
		if (bytes < 1024) return `${bytes.toFixed(0)} B`;
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
		if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
		return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
	}

	const errorRateSeries = $derived.by(() => {
		if (!trafficSeries) return [];
		const result: [number, number][] = [];
		for (let i = 0; i < trafficSeries.requests.length; i++) {
			const ts = trafficSeries.requests[i][0];
			const total = trafficSeries.requests[i][1];
			const errors = (trafficSeries.status4xx[i]?.[1] ?? 0) + (trafficSeries.status5xx[i]?.[1] ?? 0);
			result.push([ts, total > 0 ? (errors / total) * 100 : 0]);
		}
		return result;
	});

	const hasTrafficData = $derived(trafficSeries != null && (trafficSeries.requests?.length ?? 0) > 0);

	async function fetchSeries(hours: number, step: number) {
		loading = true;
		const end = Math.floor(Date.now() / 1000);
		const start = end - hours * 3600;
		const trafficStep = Math.max(step, 5);

		const [metricsRes, trafficRes] = await Promise.allSettled([
			api.getMetricsHistory(project, app.metadata.name, env, start, end, step),
			api.getTrafficHistory(project, app.metadata.name, env, start, end, trafficStep)
		]);

		if (metricsRes.status === 'fulfilled') {
			metricsAvailable = metricsRes.value.available !== false;
			const pods = metricsRes.value.pods ?? [];
			historyPods = pods;
			currentPods = historyPodsToCurrent(pods);
		}

		if (trafficRes.status === 'fulfilled') {
			trafficAvailable = trafficRes.value.available !== false;
			trafficSeries = trafficRes.value.series ?? null;
		} else {
			trafficAvailable = false;
			trafficSeries = null;
		}

		loading = false;
	}

	function historyPodsToCurrent(series: PodMetricsSeries[]): PodMetricsCurrent[] {
		return series.map((pod) => ({
			name: pod.name,
			cpu: pod.cpu.length > 0 ? pod.cpu[pod.cpu.length - 1][1] : 0,
			memory: pod.memory.length > 0 ? pod.memory[pod.memory.length - 1][1] : 0
		}));
	}

	function setRange(r: TimeRange) {
		range = r;
		if (pollTimer) { clearInterval(pollTimer); pollTimer = undefined; }
		if (r === 'live') {
			const run = () => fetchSeries(10 / 60, 15);
			run();
			pollTimer = setInterval(run, 15000);
		} else {
			const hours = { '1h': 1, '6h': 6, '24h': 24, '7d': 168 }[r];
			const step = hours <= 1 ? 15 : hours <= 6 ? 60 : hours <= 24 ? 300 : 900;
			fetchSeries(hours, step);
		}
	}

	$effect(() => {
		setRange('live');
		return () => { if (pollTimer) clearInterval(pollTimer); };
	});

	onMount(() => {
		const handler = (e: MouseEvent) => {
			if (podDropdownOpen && !(e.target as HTMLElement)?.closest?.('.relative')) {
				podDropdownOpen = false;
			}
		};
		document.addEventListener('click', handler, true);
		return () => document.removeEventListener('click', handler, true);
	});
</script>

<div class="flex flex-col gap-2 px-0 pb-0 pt-1">
	<div class="flex items-center justify-between">
		<div class="flex items-center gap-2">
			{#each ['live', '1h', '6h', '24h', '7d'] as r}
				<button
					onclick={() => setRange(r as TimeRange)}
					class="rounded px-2 py-1 text-xs font-medium transition-colors {range === r ? 'bg-accent text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-200'}"
				>{r === 'live' ? 'Live' : r}</button>
			{/each}
		</div>
		{#if metricsAvailable && currentPods.length > 0}
			<div class="relative">
				<button
					type="button"
					onclick={() => (podDropdownOpen = !podDropdownOpen)}
					class="flex items-center gap-1.5 rounded border border-surface-600 bg-surface-800 px-2.5 py-1 text-xs text-gray-300 hover:border-surface-500 hover:text-white transition-colors"
				>
					<span>{currentPods.length} pod{currentPods.length === 1 ? '' : 's'}</span>
					<svg class="h-3 w-3 text-gray-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path d="M19 9l-7 7-7-7" /></svg>
				</button>
				{#if podDropdownOpen}
					<div class="absolute right-0 top-full z-10 mt-1 min-w-[220px] rounded-lg border border-surface-600 bg-surface-800 p-2 shadow-xl">
						{#each currentPods as pod}
							<div class="flex items-center gap-2 rounded px-2 py-1.5 text-xs text-gray-300">
								<span class="h-2.5 w-2.5 shrink-0 rounded-full" style={`background-color:${hashPodColor(pod.name)}`}></span>
								<span class="truncate font-medium">{pod.name}</span>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{/if}
	</div>

	{#if loading}
		<div class="flex items-center justify-center py-12">
			<div class="h-5 w-5 animate-spin rounded-full border-2 border-gray-600 border-t-accent"></div>
		</div>
	{:else}
		<!-- Resources Section -->
		<p class="mt-1 text-xs font-semibold uppercase tracking-wider text-gray-500">Resources</p>
		{#if !metricsAvailable}
			<div class="flex flex-col items-center justify-center py-8 text-center">
				<BarChart3 class="mb-3 h-8 w-8 text-gray-600" />
				<p class="text-sm text-gray-400">Resources unavailable</p>
			</div>
		{:else if historyPods.length === 0}
			<p class="py-6 text-center text-sm text-gray-500">No resource data for this range.</p>
		{:else}
			<div class="grid grid-cols-1 gap-3">
				<MetricsLineChart title="CPU" pods={historyPods} metric="cpu" formatValue={formatCPU} limitValue={cpuLimit || undefined} />
				<MetricsLineChart title="Memory" pods={historyPods} metric="memory" formatValue={formatMemory} limitValue={memoryLimit || undefined} />
			</div>
		{/if}

		<!-- Traffic Section -->
		<p class="mt-3 text-xs font-semibold uppercase tracking-wider text-gray-500">Traffic</p>
		{#if !trafficAvailable}
			<div class="flex flex-col items-center justify-center py-8 text-center">
				<Activity class="mb-3 h-8 w-8 text-gray-600" />
				<p class="text-sm text-gray-400">Traffic unavailable</p>
			</div>
		{:else if !hasTrafficData}
			<p class="py-6 text-center text-sm text-gray-500">No traffic data for this range.</p>
		{:else}
			<div class="grid grid-cols-1 gap-3">
				<TrafficChart
					title="Request Rate"
					stacked
					series={[
						{ name: '2xx', data: trafficSeries!.status2xx, color: '#22c55e' },
						{ name: '3xx', data: trafficSeries!.status3xx, color: '#3b82f6' },
						{ name: '4xx', data: trafficSeries!.status4xx, color: '#f59e0b' },
						{ name: '5xx', data: trafficSeries!.status5xx, color: '#ef4444' }
					]}
					formatValue={formatRPS}
				/>
				<TrafficChart
					title="Response Time"
					series={[
						{ name: 'p50', data: trafficSeries!.latencyP50, color: '#22c55e' },
						{ name: 'p95', data: trafficSeries!.latencyP95, color: '#f59e0b' },
						{ name: 'p99', data: trafficSeries!.latencyP99, color: '#ef4444' }
					]}
					formatValue={formatLatency}
				/>
				<TrafficChart
					title="Error Rate"
					series={[
						{ name: 'errors', data: errorRateSeries, color: '#ef4444' }
					]}
					formatValue={formatPercent}
				/>
				<TrafficChart
					title="Throughput"
					series={[
						{ name: 'in', data: trafficSeries!.bytesIn, color: '#3b82f6' },
						{ name: 'out', data: trafficSeries!.bytesOut, color: '#a855f7' }
					]}
					formatValue={formatBytes}
				/>
			</div>
		{/if}
	{/if}
</div>
