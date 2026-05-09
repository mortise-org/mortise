<script lang="ts">
	import { onDestroy, untrack } from 'svelte';
	import { AuthRequiredError, api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import { Loader2, Search } from 'lucide-svelte';
	import type { App, LogLineEvent, Pod } from '$lib/types';
	import LogLine from '$lib/components/LogLine.svelte';

	let {
		project,
		app,
		livePods = null
	}: {
		project: string;
		app: App;
		livePods?: Pod[] | null;
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
	let lastMessageTime = 0;
	let reconnectDelay = 2000;
	let logContainer: HTMLElement | null = $state(null);
	let connectionID = 0;

	type Mode = 'live' | 'history';
	type TimeRange = '1h' | '6h' | '24h' | '7d';

	let mode = $state<Mode>('live');
	let historyLines = $state<LogLineEvent[]>([]);
	let historyLoading = $state(false);
	let historyHasMore = $state(false);
	let historyCursor = $state<string | undefined>(undefined);
	let selectedRange = $state<TimeRange>('1h');
	let filterText = $state('');

	const rangeMs: Record<TimeRange, number> = {
		'1h': 3600_000,
		'6h': 21600_000,
		'24h': 86400_000,
		'7d': 604800_000
	};

	function setMode(m: Mode) {
		if (m === mode) return;
		mode = m;
		if (m === 'live') {
			void connectLive(false);
		} else {
			closeStream();
			if (historyLines.length === 0) fetchHistory(true);
		}
	}

	async function fetchHistory(fresh: boolean) {
		historyLoading = true;
		if (fresh) {
			historyLines = [];
			historyCursor = undefined;
			historyHasMore = false;
		}
		try {
			const now = Date.now();
			const start = Math.floor((now - rangeMs[selectedRange]) / 1000);
			const end = Math.floor(now / 1000);
			const resp = await api.getLogHistory(project, app.metadata.name, selectedEnv, start, end, {
				limit: 500,
				filter: filterText || undefined,
				before: historyCursor || undefined
			});
			if (!resp.available) return;
			const mapped: LogLineEvent[] = (resp.lines ?? []).map((l) => ({
				pod: l.pod,
				ts: l.ts,
				line: l.text,
				stream: l.stream
			}));
			historyLines = fresh ? mapped : [...historyLines, ...mapped];
			historyHasMore = resp.hasMore ?? false;
			if (mapped.length > 0) {
				historyCursor = mapped[mapped.length - 1].ts;
			}
		} catch {
			/* ignore */
		} finally {
			historyLoading = false;
		}
	}

	const MAX_EVENTS = 2000;

	function scrollToBottom() {
		if (logContainer) {
			logContainer.scrollTop = logContainer.scrollHeight;
		}
	}

	function clearReconnectTimer() {
		if (reconnectTimer) {
			clearTimeout(reconnectTimer);
			reconnectTimer = null;
		}
	}

	function closeEventSource(target?: EventSource) {
		if (target) {
			target.close();
			if (es === target) {
				es = null;
			}
			return;
		}
		if (es) {
			es.close();
			es = null;
		}
	}

	function closeStream() {
		connectionID += 1;
		closeEventSource();
		streamKey = '';
		clearReconnectTimer();
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

	async function connectLive(fresh = false) {
		if (isBuilding || pods.length === 0) {
			closeStream();
			return;
		}

		const key = `${selectedEnv}|${selectedPod}|${previous ? '1' : '0'}`;
		if (es && streamKey === key) return;

		const id = ++connectionID;
		clearReconnectTimer();
		closeEventSource();
		streamKey = key;
		disconnected = false;
		lastMessageTime = 0;
		reconnectDelay = 2000;
		if (fresh) {
			events = [];
		}

		try {
			const url = await api.logsURL(project, app.metadata.name, {
				env: selectedEnv,
				follow: true,
				tail: 200,
				pod: selectedPod || undefined,
				previous: selectedPod && previous ? true : undefined,
			});
			if (id !== connectionID || streamKey !== key || mode !== 'live') return;

			const next = new EventSource(url);
			if (id !== connectionID || streamKey !== key || mode !== 'live') {
				next.close();
				return;
			}

			const openTime = Date.now();
			es = next;
			next.onopen = () => {
				if (id !== connectionID || es !== next) {
					next.close();
					return;
				}
				disconnected = false;
				reconnectDelay = 2000;
			};
			next.onmessage = (e: MessageEvent) => {
				if (id !== connectionID || es !== next) return;
				const evt = parseEvent(e.data as string);
				if (!evt) return;
				lastMessageTime = Date.now();
				const nextEvents =
					events.length >= MAX_EVENTS ? events.slice(-(MAX_EVENTS - 1)) : events.slice();
				nextEvents.push(evt);
				events = nextEvents;
				if (liveFollow) setTimeout(scrollToBottom, 0);
			};
			next.onerror = () => {
				if (id !== connectionID || es !== next) return;
				const receivedData = lastMessageTime > openTime;
				closeEventSource(next);
				if (!receivedData) {
					disconnected = true;
					if (selectedPod) void loadPods();
				}
				if (!isBuilding && pods.length > 0 && mode === 'live') {
					reconnectTimer = setTimeout(() => {
						reconnectTimer = null;
						void connectLive(false);
					}, reconnectDelay);
					reconnectDelay = Math.min(reconnectDelay * 2, 30000);
				}
			};
		} catch (error) {
			if (id !== connectionID || streamKey !== key || mode !== 'live') return;
			disconnected = true;
			if (error instanceof AuthRequiredError) {
				closeStream();
				return;
			}
			reconnectTimer = setTimeout(() => {
				reconnectTimer = null;
				void connectLive(false);
			}, reconnectDelay);
			reconnectDelay = Math.min(reconnectDelay * 2, 30000);
		}
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
		untrack(() => {
			if (mode === 'live') void connectLive(false);
		});
	});

	$effect(() => {
		if (livePods) {
			pods = livePods;
			podsLoaded = true;
			reconcilePodSelection();
		}
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
		untrack(() => {
			events = [];
			if (mode === 'live') void connectLive(false);
		});
	});

	$effect(() => {
		if (!selectedPod && previous) previous = false;
	});

	onDestroy(() => {
		closeStream();
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
			<div class="flex rounded-md border border-surface-600">
				<button
					type="button"
					onclick={() => setMode('live')}
					class="rounded-l-md px-2.5 py-0.5 text-xs transition-colors {mode === 'live' ? 'bg-accent text-white' : 'text-gray-400 hover:text-white'}"
				>Live</button>
				<button
					type="button"
					onclick={() => setMode('history')}
					class="rounded-r-md px-2.5 py-0.5 text-xs transition-colors {mode === 'history' ? 'bg-accent text-white' : 'text-gray-400 hover:text-white'}"
				>History</button>
			</div>
			{#if mode === 'live'}
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
			{/if}
			{#if mode === 'history'}
				<div class="flex rounded-md border border-surface-600">
					{#each (['1h', '6h', '24h', '7d'] as const) as range}
						<button
							type="button"
							onclick={() => { selectedRange = range; fetchHistory(true); }}
							class="px-2 py-0.5 text-xs transition-colors first:rounded-l-md last:rounded-r-md {selectedRange === range ? 'bg-surface-600 text-white' : 'text-gray-400 hover:text-white'}"
						>{range}</button>
					{/each}
				</div>
				<div class="relative min-w-0 flex-1">
					<Search class="absolute left-2 top-1/2 h-3 w-3 -translate-y-1/2 text-gray-500" />
					<input
						type="text"
						bind:value={filterText}
						placeholder="Filter logs..."
						onkeydown={(e) => { if (e.key === 'Enter') fetchHistory(true); }}
						class="w-full rounded-md border border-surface-600 bg-surface-800 py-1 pl-7 pr-2 text-xs text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
				</div>
				<button
					type="button"
					onclick={() => fetchHistory(true)}
					class="rounded px-2 py-0.5 text-xs text-gray-400 hover:bg-surface-700 hover:text-white"
				>Search</button>
			{/if}
		</div>
		<div class="flex items-center gap-2">
			{#if mode === 'live'}
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
			{/if}
		</div>
	</div>

	{#if mode === 'live' && disconnected}
		<div class="rounded-md bg-danger/10 px-3 py-1.5 text-xs text-danger">Stream disconnected. Reconnecting...</div>
	{/if}

	<div bind:this={logContainer} class="flex-1 overflow-y-auto rounded-md bg-surface-900 p-3" style="min-height: 300px; max-height: calc(100vh - 320px)">
		{#if mode === 'live'}
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
		{:else}
			{#if historyLoading && historyLines.length === 0}
				<div class="flex items-center gap-2 text-xs text-gray-400">
					<Loader2 class="h-3.5 w-3.5 animate-spin" />
					<span>Loading log history...</span>
				</div>
			{:else if historyLines.length === 0}
				<p class="text-xs italic text-gray-600">No logs found for this time range</p>
			{:else}
				{#each historyLines as evt, i (i)}
					<LogLine event={evt} showPodBadge={true} />
				{/each}
				{#if historyHasMore}
					<div class="mt-2 flex justify-center">
						<button
							type="button"
							disabled={historyLoading}
							onclick={() => fetchHistory(false)}
							class="rounded px-3 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white disabled:opacity-50"
						>
							{#if historyLoading}
								<Loader2 class="inline h-3 w-3 animate-spin" />
								Loading...
							{:else}
								Load more
							{/if}
						</button>
					</div>
				{/if}
			{/if}
		{/if}
	</div>
</div>
