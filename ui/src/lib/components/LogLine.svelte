<script lang="ts">
	import type { LogLineEvent } from '$lib/types';

	let { event, showPodBadge }: { event: LogLineEvent; showPodBadge: boolean } = $props();

	// WCAG-AA palette on surface-900 background (8 colors).
	const POD_COLORS = [
		'#a78bfa', // violet-400
		'#22d3ee', // cyan-400
		'#4ade80', // green-400
		'#fbbf24', // amber-400
		'#fb7185', // rose-400
		'#c08050', // warm brown
		'#f472b6', // pink-400
		'#34d399'  // emerald-400
	];

	function hashPodColor(pod: string): string {
		let h = 0;
		for (let i = 0; i < pod.length; i++) {
			h = (h * 31 + pod.charCodeAt(i)) | 0;
		}
		return POD_COLORS[Math.abs(h) % POD_COLORS.length];
	}

	function formatClock(ts: string): string {
		if (!ts) return '';
		const d = new Date(ts);
		if (Number.isNaN(d.getTime())) return '';
		const hh = String(d.getHours()).padStart(2, '0');
		const mm = String(d.getMinutes()).padStart(2, '0');
		const ss = String(d.getSeconds()).padStart(2, '0');
		return `${hh}:${mm}:${ss}`;
	}

	const clock = $derived(formatClock(event.ts));
	const podBadge = $derived(
		event.pod ? event.pod.slice(Math.max(0, event.pod.length - 5)) : ''
	);
	const podColor = $derived(event.pod ? hashPodColor(event.pod) : '');

	// Try to parse structured JSON once.
	type Parsed = { obj: Record<string, unknown>; level?: string; message: string };
	function parseStructured(line: string): Parsed | null {
		const trimmed = line.trim();
		if (!trimmed.startsWith('{') || !trimmed.endsWith('}')) return null;
		try {
			const obj = JSON.parse(trimmed);
			if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return null;
			const rec = obj as Record<string, unknown>;
			const level = typeof rec.level === 'string' ? (rec.level as string).toLowerCase() : undefined;
			let message: string;
			if (typeof rec.message === 'string') message = rec.message;
			else if (typeof rec.msg === 'string') message = rec.msg;
			else message = JSON.stringify(rec);
			return { obj: rec, level, message };
		} catch {
			return null;
		}
	}

	const parsed = $derived(parseStructured(event.line));

	function levelBorderClass(level: string | undefined): string {
		switch (level) {
			case 'error':
				return 'border-l-red-500';
			case 'warn':
			case 'warning':
				return 'border-l-amber-500';
			case 'debug':
				return 'border-l-gray-500/60';
			case 'info':
				return 'border-l-gray-500';
			default:
				return 'border-l-transparent';
		}
	}

	let expanded = $state(false);

	function toggleExpand() {
		if (parsed) expanded = !expanded;
	}

	function stringifyValue(v: unknown): string {
		if (typeof v === 'string') return v;
		try {
			return JSON.stringify(v);
		} catch {
			return String(v);
		}
	}
</script>

<div
	class="flex items-start gap-2 border-l-2 py-0.5 pl-2 font-mono text-xs leading-5 {levelBorderClass(
		parsed?.level
	)}"
>
	<!-- Timestamp gutter (~64px, content-sized) -->
	<span
		class="shrink-0 whitespace-nowrap text-gray-500"
		title={event.ts}
	>
		{#if clock}
			<span class="text-gray-400">{clock}</span>
		{:else}
			<span class="text-gray-600">-</span>
		{/if}
	</span>

	<!-- Pod badge (~60px) -->
	{#if showPodBadge && event.pod}
		<span
			class="shrink-0 rounded px-1.5 text-[10px] font-medium"
			style="color: {podColor}; background-color: {podColor}1a; min-width: 60px; max-width: 60px; text-align: center;"
			title={event.pod}
		>
			{podBadge}
		</span>
	{/if}

	<!-- Line content -->
	<div class="min-w-0 flex-1">
		{#if parsed}
			<button
				type="button"
				onclick={toggleExpand}
				class="w-full whitespace-pre-wrap break-all text-left text-gray-200 hover:text-white"
			>
				{#if parsed.level}<span class="mr-1 text-gray-500">[{parsed.level}]</span>{/if}
				<span>{parsed.message}</span>
			</button>
			{#if expanded}
				<div class="mt-1 rounded bg-surface-800 p-2 text-[11px]">
					<table class="w-full">
						<tbody>
							{#each Object.entries(parsed.obj) as [k, v]}
								<tr class="align-top">
									<td class="pr-2 text-gray-500">{k}</td>
									<td class="break-all text-gray-300">{stringifyValue(v)}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		{:else}
			<span class="whitespace-pre-wrap break-all text-gray-200">{event.line}</span>
		{/if}
	</div>
</div>
