<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import { Loader2 } from 'lucide-svelte';
	import type { App, LogLineEvent, Pod } from '$lib/types';
	import LogLine from '$lib/components/LogLine.svelte';

	let {
		project,
		app
	}: {
		project: string;
		app: App;
	} = $props();

	const isBuilding = $derived(app.status?.phase === 'Building');
	const isFailed = $derived(app.status?.phase === 'Failed');
	const failedMessage = $derived(
		app.status?.conditions?.find((c) => c.status === 'False')?.message ?? null
	);

	const selectedEnv = $derived(
		store.currentEnv(project) || app.spec.environments?.[0]?.name || 'production'
	);

	let pods = $state<Pod[]>([]);
	let podsLoaded = $state(false);
	let selectedPod = $state('');
	let previous = $state(false);
	let liveFollow = $state(true);
	let events = $state<LogLineEvent[]>([]);
	let es: EventSource | null = null;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	let streamKey = '';
	let disconnected = $state(false);
	let intentionalClose = false;
	let logContainer: HTMLElement | null = $state(null);

	const MAX_EVENTS = 2000;

	function scrollToBottom() {
		if (logContainer) {
			logContainer.scrollTop = logContainer.scrollHeight;
		}
	}

	function closeStream(manual = true) {
		if (manual) intentionalClose = true;
		if (es) {
			es.close();
			es = null;
		}
		streamKey = '';
		if (reconnectTimer) {
			clearTimeout(reconnectTimer);
			reconnectTimer = null;
		}
	}

	function parseEvent(data: string): LogLineEvent | null {
		try {
			const obj = JSON.parse(data);
			if (obj && typeof obj === 'object' && typeof obj.line === 'string') {
				return {
					pod: typeof obj.pod === 'string' ? obj.pod : '',
					ts: typeof obj.ts === 'string' ? obj.ts : '',
					line: obj.line,
					stream: typeof obj.stream === 'string' ? obj.stream : undefined
				};
			}
		} catch {
			/* not JSON */
		}
		return { pod: '', ts: '', line: data, stream: undefined };
	}

	function connectLive(fresh = false) {
		if (isBuilding || pods.length === 0) {
			closeStream(true);
			return;
		}

		const key = `${selectedEnv}|${selectedPod}|${previous ? '1' : '0'}`;
		if (es && streamKey === key) return;

		closeStream(true);
		streamKey = key;
		disconnected = false;
		if (fresh) {
			events = [];
		}

		const url = api.logsURL(project, app.metadata.name, {
			env: selectedEnv,
			follow: true,
			tail: 200,
			pod: selectedPod || undefined,
			previous: selectedPod && previous ? true : undefined,
		});

		es = new EventSource(url);
		es.onopen = () => {
			intentionalClose = false;
			disconnected = false;
		};
		es.onmessage = (e: MessageEvent) => {
			const evt = parseEvent(e.data as string);
			if (!evt) return;
			const next = events.length >= MAX_EVENTS ? events.slice(-(MAX_EVENTS - 1)) : events.slice();
			next.push(evt);
			events = next;
			if (liveFollow) setTimeout(scrollToBottom, 0);
		};
		es.onerror = () => {
			if (intentionalClose || streamKey !== key) return;
			disconnected = true;
			closeStream(false);
			if (selectedPod) void loadPods();
			if (!isBuilding && pods.length > 0) {
				reconnectTimer = setTimeout(() => {
					reconnectTimer = null;
					connectLive(false);
				}, 2000);
			}
		};
	}

	async function loadPods() {
		try {
			const list = await api.listPods(project, app.metadata.name, selectedEnv);
			pods = list ?? [];
			podsLoaded = true;
			reconcilePodSelection();
		} catch {
			/* ignore */
		}
	}

	function reconcilePodSelection() {
		if (selectedPod && !pods.some((p) => p.name === selectedPod)) {
			selectedPod = '';
			previous = false;
		}
	}

	$effect(() => {
		if (!podsLoaded) void loadPods();
		untrack(() => connectLive(false));
	});

	let lastEnv = $state('');
	$effect(() => {
		if (lastEnv === selectedEnv) return;
		const firstRun = lastEnv === '';
		lastEnv = selectedEnv;
		if (firstRun) return;
		selectedPod = '';
		previous = false;
		pods = [];
		podsLoaded = false;
		events = [];
		void loadPods();
	});

	$effect(() => {
		void selectedEnv;
		void selectedPod;
		void previous;
		untrack(() => connectLive(false));
	});

	$effect(() => {
		if (!selectedPod && previous) previous = false;
	});

	onMount(() => {
		return () => closeStream(true);
	});

	function clearLogs() {
		events = [];
	}

	async function copyLogs() {
		try {
			const text = events
				.map((e) => (e.pod ? `[${e.pod}] ${e.line}` : e.line))
				.join('\n');
			await navigator.clipboard.writeText(text);
		} catch {
			/* ignore */
		}
	}

	const selectedPodObj = $derived(pods.find((p) => p.name === selectedPod) ?? null);
	const showPreviousToggle = $derived(!!selectedPod && !!selectedPodObj && selectedPodObj.restartCount > 0);
	const showPodBadge = $derived(!selectedPod);
</script>

<div class="flex h-full flex-col gap-1">
	<div class="flex items-center justify-between gap-2">
		<div class="flex items-center gap-2">
			{#if podsLoaded && pods.length > 1}
			<select
				bind:value={selectedPod}
				class="rounded-md border border-surface-600 bg-surface-800 px-2 py-1 text-xs text-white outline-none focus:border-accent"
			>
				<option value="">All pods ({pods.length})</option>
				{#each pods as p}
					<option value={p.name}>
						{p.name} · {p.phase}{p.restartCount > 0 ? ` ⟳ ${p.restartCount}` : ''}
					</option>
				{/each}
			</select>
			{/if}
			<div class="flex gap-1">
				{#if showPreviousToggle}
					<button
						type="button"
						onclick={() => (previous = !previous)}
						aria-pressed={previous}
						title="Show logs from the previous container (crash diagnosis)"
						class="rounded px-2 py-0.5 text-xs transition-colors {previous ? 'bg-accent text-white' : 'border border-surface-600 text-gray-400 hover:text-white'}"
					>
						Previous
					</button>
				{/if}
			</div>
		</div>
		<div class="flex items-center gap-2">
				<label class="flex cursor-pointer items-center gap-1.5 text-xs text-gray-400">
					<span>Live tail</span>
					<button
						type="button"
						role="switch"
						aria-label="Live tail"
						aria-checked={liveFollow}
						onclick={() => (liveFollow = !liveFollow)}
						class="relative inline-flex h-4 w-7 items-center rounded-full transition-colors {liveFollow ? 'bg-accent' : 'bg-surface-600'}"
					>
						<span class="inline-block h-3 w-3 transform rounded-full bg-white shadow transition-transform {liveFollow ? 'translate-x-3.5' : 'translate-x-0.5'}"></span>
					</button>
				</label>
				<button type="button" onclick={copyLogs} class="rounded px-2 py-0.5 text-xs text-gray-500 hover:bg-surface-700 hover:text-white">Copy</button>
				<button type="button" onclick={clearLogs} class="rounded px-2 py-0.5 text-xs text-gray-500 hover:bg-surface-700 hover:text-white">Clear</button>
		</div>
	</div>

	{#if disconnected}
		<div class="rounded-md bg-danger/10 px-3 py-1.5 text-xs text-danger">Stream disconnected. Reconnecting...</div>
	{/if}

	<div bind:this={logContainer} class="flex-1 overflow-y-auto rounded-md bg-surface-900 p-3" style="min-height: 300px; max-height: calc(100vh - 320px)">
		{#if isBuilding}
			<div class="flex items-center gap-2 text-xs text-warning">
				<Loader2 class="h-3.5 w-3.5 animate-spin" />
				<span>Waiting for build output...</span>
			</div>
		{:else if isFailed && pods.length === 0 && failedMessage}
			<div class="space-y-2">
				<p class="text-xs font-medium text-danger">Build failed:</p>
				<pre class="whitespace-pre-wrap break-all rounded bg-surface-800 p-2 text-xs text-danger/80">{failedMessage}</pre>
			</div>
		{:else if events.length === 0 && pods.length === 0}
			<p class="text-xs italic text-gray-600">Deploy this app to see logs</p>
		{:else if events.length === 0}
			<p class="text-xs italic text-gray-600">No logs yet...</p>
		{:else}
			{#each events as evt, i (i)}
				<LogLine event={evt} {showPodBadge} />
			{/each}
		{/if}
	</div>
</div>
