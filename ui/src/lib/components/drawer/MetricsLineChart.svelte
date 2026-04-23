<script lang="ts">
	import type { PodMetricsSeries } from '$lib/types';
	import { hashPodColor } from '$lib/pod-colors';

	let {
		title,
		pods,
		metric,
		formatValue,
		height = 180
	}: {
		title: string;
		pods: PodMetricsSeries[];
		metric: 'cpu' | 'memory';
		formatValue: (v: number) => string;
		height?: number;
	} = $props();

	const width = 640;
	const padding = { top: 12, right: 12, bottom: 24, left: 44 };

	const allPoints = $derived.by(() => {
		const out: Array<{ ts: number; value: number }> = [];
		for (const pod of pods) {
			const points = metric === 'cpu' ? pod.cpu : pod.memory;
			for (const p of points) out.push({ ts: p[0], value: p[1] });
		}
		return out;
	});

	const domain = $derived.by(() => {
		if (allPoints.length === 0) {
			const now = Date.now() / 1000;
			return { minX: now - 60, maxX: now, minY: 0, maxY: 1 };
		}
		let minX = allPoints[0].ts;
		let maxX = allPoints[0].ts;
		let minY = 0;
		let maxY = allPoints[0].value;
		for (const p of allPoints) {
			if (p.ts < minX) minX = p.ts;
			if (p.ts > maxX) maxX = p.ts;
			if (p.value > maxY) maxY = p.value;
		}
		if (minX === maxX) maxX = minX + 1;
		if (maxY <= minY) maxY = minY + 1;
		return { minX, maxX, minY, maxY };
	});

	function xScale(ts: number): number {
		const w = width - padding.left - padding.right;
		return padding.left + ((ts - domain.minX) / (domain.maxX - domain.minX)) * w;
	}

	function yScale(value: number): number {
		const h = height - padding.top - padding.bottom;
		return padding.top + (1 - (value - domain.minY) / (domain.maxY - domain.minY)) * h;
	}

	function linePath(series: [number, number][]): string {
		if (series.length === 0) return '';
		const sorted = [...series].sort((a, b) => a[0] - b[0]);
		return sorted
			.map((p, i) => `${i === 0 ? 'M' : 'L'} ${xScale(p[0]).toFixed(2)} ${yScale(p[1]).toFixed(2)}`)
			.join(' ');
	}

	function tickTime(ts: number): string {
		const d = new Date(ts * 1000);
		const hh = String(d.getHours()).padStart(2, '0');
		const mm = String(d.getMinutes()).padStart(2, '0');
		return `${hh}:${mm}`;
	}

	const xTicks = $derived.by(() => {
		const ticks: number[] = [];
		for (let i = 0; i < 5; i++) {
			ticks.push(domain.minX + ((domain.maxX - domain.minX) * i) / 4);
		}
		return ticks;
	});

	const yTicks = $derived.by(() => {
		const ticks: number[] = [];
		for (let i = 0; i < 4; i++) {
			ticks.push(domain.minY + ((domain.maxY - domain.minY) * i) / 3);
		}
		return ticks.reverse();
	});
</script>

<div class="rounded-lg border border-surface-600 bg-surface-800/60 p-3">
	<div class="mb-2 flex items-center justify-between">
		<p class="text-xs font-medium text-gray-200">{title}</p>
		<p class="text-[10px] text-gray-500">{pods.length} pod{pods.length === 1 ? '' : 's'}</p>
	</div>
	<svg viewBox={`0 0 ${width} ${height}`} class="h-[180px] w-full">
		{#each yTicks as t}
			<line
				x1={padding.left}
				y1={yScale(t)}
				x2={width - padding.right}
				y2={yScale(t)}
				stroke="rgba(148,163,184,0.18)"
				stroke-width="1"
			/>
			<text x={padding.left - 6} y={yScale(t) + 3} text-anchor="end" class="fill-gray-500 text-[10px]">
				{formatValue(t)}
			</text>
		{/each}

		{#each xTicks as t}
			<text x={xScale(t)} y={height - 6} text-anchor="middle" class="fill-gray-500 text-[10px]">
				{tickTime(t)}
			</text>
		{/each}

		{#each pods as pod}
			{@const points = metric === 'cpu' ? pod.cpu : pod.memory}
			{#if points.length > 0}
				<path
					d={linePath(points)}
					fill="none"
					stroke={hashPodColor(pod.name)}
					stroke-width="2"
					stroke-linejoin="round"
					stroke-linecap="round"
				/>
			{/if}
		{/each}
	</svg>
</div>
