<script lang="ts">
	import { seriesColor, maxDistinctSeries } from '$lib/series-colors';
	import {
		coverageStepOf,
		gapRanges as computeGapRanges,
		splitAtGaps,
		unobservedBuckets,
		type SeriesPoint
	} from '$lib/chart-gaps';

	export type { SeriesPoint };
	export type ChartSeries = { name: string; points: SeriesPoint[] };

	let {
		title,
		series,
		coverage = [],
		formatValue,
		limitValue,
		height = 240
	}: {
		title: string;
		series: ChartSeries[];
		// Gap-visibility contract from the observer: [bucketTs, 0|1] per step
		// bucket. 0-buckets were NOT observed and render as gaps — lines are
		// broken there and the band is hatched. Never interpolated.
		coverage?: [number, number][];
		formatValue: (v: number) => string;
		limitValue?: number;
		height?: number;
	} = $props();

	const width = 640;
	const basePadding = { top: 16, right: 16, bottom: 32 };

	// Stable order: sorted by name, so colors follow the entity across
	// re-fetches and pod churn.
	const sortedSeries = $derived([...series].sort((a, b) => a.name.localeCompare(b.name)));
	const shown = $derived(sortedSeries.slice(0, maxDistinctSeries));
	const overflow = $derived(sortedSeries.length - shown.length);

	// Gap math lives in $lib/chart-gaps — pure and unit-tested, since it IS
	// the no-interpolation contract.
	const gapRanges = $derived(computeGapRanges(coverage));
	const unobserved = $derived(unobservedBuckets(coverage));
	const coverageStep = $derived(coverageStepOf(coverage));

	const domain = $derived.by(() => {
		let minX = Infinity;
		let maxX = -Infinity;
		let maxY = 0;
		for (const s of shown) {
			for (const [ts, v] of s.points) {
				if (ts < minX) minX = ts;
				if (ts > maxX) maxX = ts;
				if (v > maxY) maxY = v;
			}
		}
		for (const [ts] of coverage) {
			if (ts < minX) minX = ts;
			if (ts + coverageStep > maxX) maxX = ts + coverageStep;
		}
		if (!isFinite(minX)) {
			const now = Date.now() / 1000;
			return { minX: now - 60, maxX: now, minY: 0, maxY: limitValue ?? 1 };
		}
		if (minX === maxX) maxX = minX + 1;
		if (limitValue !== undefined && limitValue > 0) maxY = Math.max(maxY, limitValue);
		if (maxY <= 0) maxY = 1;
		return { minX, maxX, minY: 0, maxY };
	});

	const padding = $derived.by(() => {
		let maxLen = 0;
		for (let i = 0; i < 5; i++) {
			const t = domain.minY + ((domain.maxY - domain.minY) * i) / 4;
			maxLen = Math.max(maxLen, formatValue(t).length);
		}
		return { ...basePadding, left: Math.max(56, maxLen * 8 + 12) };
	});

	function xScale(ts: number): number {
		const w = width - padding.left - padding.right;
		return padding.left + ((ts - domain.minX) / (domain.maxX - domain.minX)) * w;
	}
	function yScale(v: number): number {
		const h = height - padding.top - padding.bottom;
		return padding.top + (1 - (v - domain.minY) / (domain.maxY - domain.minY)) * h;
	}

	// splitAtGaps breaks a series at unobserved buckets so no line is ever
	// drawn across a gap. Single-point segments render as dots (a path can't).
	function segments(points: SeriesPoint[]): { paths: string[]; dots: SeriesPoint[] } {
		const paths: string[] = [];
		const dots: SeriesPoint[] = [];
		for (const seg of splitAtGaps(points, unobserved, coverageStep)) {
			if (seg.length === 1) dots.push(seg[0]);
			else
				paths.push(
					seg.map((p, i) => `${i === 0 ? 'M' : 'L'} ${xScale(p[0]).toFixed(2)} ${yScale(p[1]).toFixed(2)}`).join(' ')
				);
		}
		return { paths, dots };
	}

	function tickTime(ts: number): string {
		const d = new Date(ts * 1000);
		return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
	}

	const xTicks = $derived(Array.from({ length: 5 }, (_, i) => domain.minX + ((domain.maxX - domain.minX) * i) / 4));
	const yTicks = $derived(
		Array.from({ length: 5 }, (_, i) => domain.minY + ((domain.maxY - domain.minY) * i) / 4).reverse()
	);

	// Hover crosshair + tooltip: nearest bucket across all series.
	let hover: { ts: number; rows: Array<{ name: string; value: number | null; color: string }> } | null =
		$state(null);

	function onMove(e: MouseEvent) {
		const svg = e.currentTarget as SVGSVGElement;
		const rect = svg.getBoundingClientRect();
		const px = ((e.clientX - rect.left) / rect.width) * width;
		if (px < padding.left || px > width - padding.right) {
			hover = null;
			return;
		}
		const ts = domain.minX + ((px - padding.left) / (width - padding.left - padding.right)) * (domain.maxX - domain.minX);
		const bucket = coverageStep > 0 ? Math.round(ts / coverageStep) * coverageStep : ts;
		const rows = shown.map((s, i) => {
			let best: SeriesPoint | null = null;
			for (const p of s.points) {
				if (best === null || Math.abs(p[0] - bucket) < Math.abs(best[0] - bucket)) best = p;
			}
			const inRange = best !== null && Math.abs(best[0] - bucket) <= coverageStep;
			return { name: s.name, value: inRange && best ? best[1] : null, color: seriesColor(i) };
		});
		hover = { ts: bucket, rows };
	}
</script>

<div class="rounded-lg border border-surface-600 bg-surface-800/60 p-3">
	<p class="mb-2 text-sm font-medium text-gray-200">{title}</p>
	<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
	<svg
		viewBox={`0 0 ${width} ${height}`}
		class="w-full"
		style={`height:${height}px`}
		role="img"
		aria-label={title}
		onmousemove={onMove}
		onmouseleave={() => (hover = null)}
	>
		{#each yTicks as t}
			<line
				x1={padding.left}
				y1={yScale(t)}
				x2={width - padding.right}
				y2={yScale(t)}
				stroke="rgba(148,163,184,0.18)"
				stroke-width="1"
			/>
			<text x={padding.left - 8} y={yScale(t) + 4} text-anchor="end" class="fill-gray-400" style="font-size:13px">
				{formatValue(t)}
			</text>
		{/each}

		{#each xTicks as t}
			<text x={xScale(t)} y={height - 8} text-anchor="middle" class="fill-gray-400" style="font-size:13px">
				{tickTime(t)}
			</text>
		{/each}

		<!-- Unobserved windows: hatched bands, the honest absence of data. -->
		<defs>
			<pattern id="gap-hatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
				<line x1="0" y1="0" x2="0" y2="6" stroke="rgba(148,163,184,0.25)" stroke-width="1.5" />
			</pattern>
		</defs>
		{#each gapRanges as gap}
			<rect
				data-testid="coverage-gap"
				x={xScale(Math.max(gap.start, domain.minX))}
				y={padding.top}
				width={Math.max(0, xScale(Math.min(gap.end, domain.maxX)) - xScale(Math.max(gap.start, domain.minX)))}
				height={height - padding.top - padding.bottom}
				fill="url(#gap-hatch)"
			/>
		{/each}

		{#if limitValue !== undefined && limitValue > 0}
			<line
				x1={padding.left}
				y1={yScale(limitValue)}
				x2={width - padding.right}
				y2={yScale(limitValue)}
				stroke="rgba(148,163,184,0.5)"
				stroke-width="1"
				stroke-dasharray="4 3"
			/>
		{/if}

		{#each shown as s, i}
			{@const seg = segments(s.points)}
			{#each seg.paths as d}
				<path {d} fill="none" stroke={seriesColor(i)} stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
			{/each}
			{#each seg.dots as p}
				<circle cx={xScale(p[0])} cy={yScale(p[1])} r="3" fill={seriesColor(i)} />
			{/each}
		{/each}

		{#if hover}
			<line
				x1={xScale(hover.ts)}
				y1={padding.top}
				x2={xScale(hover.ts)}
				y2={height - padding.bottom}
				stroke="rgba(226,232,240,0.4)"
				stroke-width="1"
			/>
		{/if}
	</svg>

	{#if hover}
		<div class="mt-1 rounded border border-surface-600 bg-surface-800 px-2 py-1 text-xs" data-testid="chart-tooltip">
			<span class="text-gray-400">{tickTime(hover.ts)}</span>
			{#each hover.rows as row}
				<span class="ml-3 inline-flex items-center gap-1">
					<span class="inline-block h-2 w-2 rounded-sm" style={`background:${row.color}`}></span>
					<span class="text-gray-300">{row.name}</span>
					<span class="text-gray-100">{row.value === null ? 'no data' : formatValue(row.value)}</span>
				</span>
			{/each}
		</div>
	{/if}

	{#if shown.length > 1}
		<div class="mt-2 flex flex-wrap gap-3" data-testid="chart-legend">
			{#each shown as s, i}
				<span class="inline-flex items-center gap-1.5 text-xs text-gray-300">
					<span class="inline-block h-2 w-2 rounded-sm" style={`background:${seriesColor(i)}`}></span>
					{s.name}
				</span>
			{/each}
			{#if overflow > 0}
				<span class="text-xs text-gray-500">+{overflow} more (not charted)</span>
			{/if}
		</div>
	{/if}
</div>
