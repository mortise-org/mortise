<script lang="ts">
	let {
		title,
		series,
		colors,
		formatValue,
		stacked = false,
		height = 240
	}: {
		title: string;
		series: Array<{ name: string; data: [number, number][]; color: string }>;
		colors?: Record<string, string>;
		formatValue: (v: number) => string;
		stacked?: boolean;
		height?: number;
	} = $props();

	const width = 640;
	const padding = { top: 16, right: 16, bottom: 32, left: 56 };

	const allPoints = $derived.by(() => {
		const out: Array<{ ts: number; value: number }> = [];
		for (const s of series) {
			for (const p of s.data) out.push({ ts: p[0], value: p[1] });
		}
		return out;
	});

	const stackedMax = $derived.by(() => {
		if (!stacked || series.length === 0) return 0;
		const byTs = new Map<number, number>();
		for (const s of series) {
			for (const [ts, val] of s.data) {
				byTs.set(ts, (byTs.get(ts) ?? 0) + val);
			}
		}
		let max = 0;
		for (const v of byTs.values()) {
			if (v > max) max = v;
		}
		return max;
	});

	const domain = $derived.by(() => {
		if (allPoints.length === 0) {
			const now = Date.now() / 1000;
			return { minX: now - 60, maxX: now, minY: 0, maxY: 1 };
		}
		let minX = allPoints[0].ts;
		let maxX = allPoints[0].ts;
		let maxY = allPoints[0].value;
		for (const p of allPoints) {
			if (p.ts < minX) minX = p.ts;
			if (p.ts > maxX) maxX = p.ts;
			if (p.value > maxY) maxY = p.value;
		}
		if (minX === maxX) maxX = minX + 1;
		if (stacked) maxY = stackedMax;
		if (maxY <= 0) maxY = 1;
		maxY *= 1.1;
		return { minX, maxX, minY: 0, maxY };
	});

	function xScale(ts: number): number {
		const w = width - padding.left - padding.right;
		return padding.left + ((ts - domain.minX) / (domain.maxX - domain.minX)) * w;
	}

	function yScale(value: number): number {
		const h = height - padding.top - padding.bottom;
		return padding.top + (1 - (value - domain.minY) / (domain.maxY - domain.minY)) * h;
	}

	function linePath(data: [number, number][]): string {
		if (data.length === 0) return '';
		const sorted = [...data].sort((a, b) => a[0] - b[0]);
		return sorted
			.map((p, i) => `${i === 0 ? 'M' : 'L'} ${xScale(p[0]).toFixed(2)} ${yScale(p[1]).toFixed(2)}`)
			.join(' ');
	}

	function areaPath(data: [number, number][], baseline: Map<number, number>): string {
		if (data.length === 0) return '';
		const sorted = [...data].sort((a, b) => a[0] - b[0]);
		const top = sorted
			.map((p, i) => {
				const y = (baseline.get(p[0]) ?? 0) + p[1];
				return `${i === 0 ? 'M' : 'L'} ${xScale(p[0]).toFixed(2)} ${yScale(y).toFixed(2)}`;
			})
			.join(' ');
		const bottom = [...sorted]
			.reverse()
			.map((p) => {
				const y = baseline.get(p[0]) ?? 0;
				return `L ${xScale(p[0]).toFixed(2)} ${yScale(y).toFixed(2)}`;
			})
			.join(' ');
		return top + ' ' + bottom + ' Z';
	}

	const stackedAreas = $derived.by(() => {
		if (!stacked) return [];
		const cumulative = new Map<number, number>();
		const areas: Array<{ path: string; color: string; name: string }> = [];
		for (const s of series) {
			const baseline = new Map(cumulative);
			const path = areaPath(s.data, baseline);
			areas.push({ path, color: s.color, name: s.name });
			for (const [ts, val] of s.data) {
				cumulative.set(ts, (cumulative.get(ts) ?? 0) + val);
			}
		}
		return areas.reverse();
	});

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
		for (let i = 0; i < 5; i++) {
			ticks.push(domain.minY + ((domain.maxY - domain.minY) * i) / 4);
		}
		return ticks.reverse();
	});
</script>

<div class="rounded-lg border border-surface-600 bg-surface-800/60 p-3">
	<div class="mb-2 flex items-center gap-3">
		<p class="text-sm font-medium text-gray-200">{title}</p>
		<div class="flex gap-2">
			{#each series as s}
				<span class="flex items-center gap-1 text-xs text-gray-400">
					<span class="inline-block h-2 w-2 rounded-full" style={`background-color:${s.color}`}></span>
					{s.name}
				</span>
			{/each}
		</div>
	</div>
	<svg viewBox={`0 0 ${width} ${height}`} class="h-[240px] w-full">
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

		{#if stacked}
			{#each stackedAreas as area}
				<path d={area.path} fill={area.color} opacity="0.6" />
			{/each}
		{:else}
			{#each series as s}
				{#if s.data.length > 0}
					<path
						d={linePath(s.data)}
						fill="none"
						stroke={s.color}
						stroke-width="2"
						stroke-linejoin="round"
						stroke-linecap="round"
					/>
				{/if}
			{/each}
		{/if}
	</svg>
</div>
