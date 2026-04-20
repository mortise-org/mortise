<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { Loader2 } from 'lucide-svelte';
	import type { App, BuildLogsResponse, LogLineEvent, Pod, ProjectEnvironment } from '$lib/types';
	import LogLine from '$lib/components/LogLine.svelte';

	let {
		project,
		app,
		projectEnvs = [],
		selectedEnv: selectedEnvProp = '',
		onSelectEnv
	}: {
		project: string;
		app: App;
		projectEnvs?: ProjectEnvironment[];
		selectedEnv?: string;
		onSelectEnv?: (name: string) => void;
	} = $props();

	function chooseEnv(name: string) {
		onSelectEnv?.(name);
	}

	// --- Sub-tabs ---
	type SubTab = 'live' | 'build';
	let subTab = $state<SubTab>('live');

	// --- Derived app-level flags ---
	const isBuilding = $derived(app.status?.phase === 'Building');
	const isFailed = $derived(app.status?.phase === 'Failed');
	const isCrashLooping = $derived(app.status?.phase === 'CrashLooping');
	const isImageSource = $derived(app.spec.source?.type === 'image');
	const failedMessage = $derived(
		app.status?.conditions?.find((c) => c.status === 'False')?.message ?? null
	);

	// --- Live sub-tab state ---
	const selectedEnv = $derived(
		selectedEnvProp || projectEnvs[0]?.name || app.spec.environments?.[0]?.name || 'production'
	);
	let pods = $state<Pod[]>([]);
	let podsLoaded = $state(false);
	let selectedPod = $state(''); // '' = all pods
	let previous = $state(false);
	let liveFollow = $state(true);
	let events = $state<LogLineEvent[]>([]);
	let es: EventSource | null = null;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	let disconnected = $state(false);
	let logContainer: HTMLElement | null = $state(null);
	let podPollHandle: ReturnType<typeof setInterval> | null = null;

	// --- Build sub-tab state ---
	let buildResp = $state<BuildLogsResponse | null>(null);
	let buildPollHandle: ReturnType<typeof setInterval> | null = null;

	const MAX_EVENTS = 2000;

	function scrollToBottom() {
		if (logContainer) {
			logContainer.scrollTop = logContainer.scrollHeight;
		}
	}

	function closeStream() {
		if (es) {
			es.close();
			es = null;
		}
		if (reconnectTimer) {
			clearTimeout(reconnectTimer);
			reconnectTimer = null;
		}
	}

	function parseEvent(data: string): LogLineEvent | null {
		// New contract: JSON shape {pod, ts, line, stream}.
		// Fall back to treating plain strings as a bare line (backward-compat
		// during the backend rollout window - still render cleanly).
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

	function connectLive(fresh: boolean = true) {
		closeStream();
		if (isBuilding) return; // no pods to stream from yet
		if (pods.length === 0) return; // nothing to stream from - avoid reconnect flicker
		if (fresh) {
			events = [];
			disconnected = false;
		}

		const url = api.logsURL(project, app.metadata.name, {
			env: selectedEnv,
			follow: true,
			tail: 200,
			pod: selectedPod || undefined,
			previous: selectedPod && previous ? true : undefined,
		});

		es = new EventSource(url);
		es.onmessage = (e: MessageEvent) => {
			const evt = parseEvent(e.data as string);
			if (!evt) return;
			const next = events.length >= MAX_EVENTS ? events.slice(-(MAX_EVENTS - 1)) : events.slice();
			next.push(evt);
			events = next;
			if (liveFollow) setTimeout(scrollToBottom, 0);
		};
		es.onerror = () => {
			disconnected = true;
			closeStream();
			if (!isBuilding && pods.length > 0) {
				reconnectTimer = setTimeout(() => {
					reconnectTimer = null;
					disconnected = false;
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
			// If there's exactly one pod and nothing selected, scope the stream to it
			// so we skip the pod-badge path entirely.
			if (pods.length === 1 && !selectedPod) {
				selectedPod = pods[0].name;
			}
			// If the selected pod disappeared (rollout), fall back to "all".
			if (selectedPod && !pods.some((p) => p.name === selectedPod)) {
				selectedPod = '';
				previous = false;
			}
		} catch {
			/* ignore - keep previous list */
		}
	}

	async function pollBuildLogs() {
		try {
			buildResp = await api.getBuildLogs(project, app.metadata.name);
		} catch {
			/* ignore */
		}
	}

	function startBuildPoll() {
		if (buildPollHandle) return;
		void pollBuildLogs();
		buildPollHandle = setInterval(() => {
			void pollBuildLogs();
			// Stop polling once a terminal state is reached.
			if (buildResp && !buildResp.building) {
				if (buildPollHandle) {
					clearInterval(buildPollHandle);
					buildPollHandle = null;
				}
			}
		}, 2000);
	}

	function stopBuildPoll() {
		if (buildPollHandle) {
			clearInterval(buildPollHandle);
			buildPollHandle = null;
		}
	}

	function startPodPoll() {
		if (podPollHandle) return;
		void loadPods();
		podPollHandle = setInterval(() => void loadPods(), 10000);
	}

	function stopPodPoll() {
		if (podPollHandle) {
			clearInterval(podPollHandle);
			podPollHandle = null;
		}
	}

	// --- Reactive wiring ---

	// Sub-tab lifecycle.
	$effect(() => {
		if (subTab === 'live') {
			startPodPoll();
			connectLive();
		} else {
			stopPodPoll();
			closeStream();
		}
		// Build polling is driven by the build sub-tab + isBuilding state below.
	});

	// Build tab: fetch once on entry, poll only while building.
	$effect(() => {
		if (subTab === 'build') {
			if (!buildResp) void pollBuildLogs();
			if (isBuilding && !isImageSource) startBuildPoll();
			else stopBuildPoll();
		} else {
			stopBuildPoll();
		}
	});

	// Reconnect the stream whenever the user changes knobs that affect the URL.
	$effect(() => {
		// Touch all inputs for reactivity.
		void selectedEnv;
		void selectedPod;
		void previous;
		void pods.length; // reconnect when pods transition 0 → >0
		if (subTab === 'live') connectLive();
	});

	// If a pod is deselected, always clear "previous".
	$effect(() => {
		if (!selectedPod && previous) previous = false;
	});

	onMount(() => {
		return () => {
			closeStream();
			stopBuildPoll();
			stopPodPoll();
		};
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

	// Build sub-tab helpers.
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

	const selectedPodObj = $derived(pods.find((p) => p.name === selectedPod) ?? null);
	const showPreviousToggle = $derived(
		!!selectedPod && !!selectedPodObj && selectedPodObj.restartCount > 0
	);
	const showPodBadge = $derived(subTab === 'live' && !selectedPod);

	const buildEvents = $derived<LogLineEvent[]>(
		(buildResp?.lines ?? []).map((l) => ({ pod: '', ts: '', line: l, stream: 'stdout' }))
	);
</script>

<div class="flex h-full flex-col gap-1">
	<!-- Sub-tab bar -->
	<div class="flex items-center gap-1 border-b border-surface-600">
		<button
			type="button"
			onclick={() => (subTab = 'live')}
			class="-mb-px border-b-2 px-3 py-1.5 text-xs font-medium transition-colors {subTab ===
			'live'
				? 'border-accent text-white'
				: 'border-transparent text-gray-500 hover:text-white'}"
		>
			Live
		</button>
		<button
			type="button"
			onclick={() => (subTab = 'build')}
			class="-mb-px border-b-2 px-3 py-1.5 text-xs font-medium transition-colors {subTab ===
			'build'
				? 'border-accent text-white'
				: 'border-transparent text-gray-500 hover:text-white'}"
		>
			Build
		</button>
		{#if subTab === 'live' && podsLoaded && pods.length > 1}
			<select
				bind:value={selectedPod}
				class="ml-auto mb-1 rounded-md border border-surface-600 bg-surface-800 px-2 py-1 text-xs text-white outline-none focus:border-accent"
			>
				<option value="">All pods ({pods.length})</option>
				{#each pods as p}
					<option value={p.name}>
						{p.name} · {p.phase}{p.restartCount > 0 ? ` ⟳ ${p.restartCount}` : ''}
					</option>
				{/each}
			</select>
		{/if}
	</div>

	{#if subTab === 'live'}
		<!-- Header row 1: env switcher (multi-env only) -->
		{#if projectEnvs.length > 1}
			<div class="flex gap-1">
				{#each projectEnvs as env}
					<button
						type="button"
						onclick={() => chooseEnv(env.name)}
						class="rounded px-2.5 py-1 text-xs transition-colors {selectedEnv === env.name
							? 'bg-surface-600 text-white'
							: 'text-gray-400 hover:text-white'}"
					>
						{env.name}
					</button>
				{/each}
			</div>
		{/if}

		<!-- Controls row: previous (left) + live tail / copy / clear (right) -->
		<div class="flex items-center justify-between gap-2">
			<div class="flex gap-1">
				{#if showPreviousToggle}
					<button
						type="button"
						onclick={() => (previous = !previous)}
						aria-pressed={previous}
						title="Show logs from the previous container (crash diagnosis)"
						class="rounded px-2 py-0.5 text-xs transition-colors {previous
							? 'bg-accent text-white'
							: 'border border-surface-600 text-gray-400 hover:text-white'}"
					>
						Previous
					</button>
				{/if}
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
				<button
					type="button"
					onclick={copyLogs}
					class="rounded px-2 py-0.5 text-xs text-gray-500 hover:bg-surface-700 hover:text-white"
				>
					Copy
				</button>
				<button
					type="button"
					onclick={clearLogs}
					class="rounded px-2 py-0.5 text-xs text-gray-500 hover:bg-surface-700 hover:text-white"
				>
					Clear
				</button>
			</div>
		</div>

		{#if disconnected}
			<div class="rounded-md bg-danger/10 px-3 py-1.5 text-xs text-danger">
				Stream disconnected. Reconnecting…
			</div>
		{/if}

		<!-- Log body -->
		<div
			bind:this={logContainer}
			class="flex-1 overflow-y-auto rounded-md bg-surface-900 p-3"
			style="min-height: 300px; max-height: calc(100vh - 320px)"
		>
			{#if isBuilding}
				<div class="flex items-center gap-2 text-xs text-warning">
					<Loader2 class="h-3.5 w-3.5 animate-spin" />
					<span>Waiting for build output…</span>
				</div>
			{:else if isFailed && pods.length === 0 && failedMessage}
				<div class="space-y-2">
					<p class="text-xs font-medium text-danger">Build failed:</p>
					<pre
						class="whitespace-pre-wrap break-all rounded bg-surface-800 p-2 text-xs text-danger/80">{failedMessage}</pre>
				</div>
			{:else if isCrashLooping && failedMessage}
				<div class="space-y-2">
					<p class="text-xs font-medium text-danger">Container crash:</p>
					<pre
						class="whitespace-pre-wrap break-all rounded bg-surface-800 p-2 text-xs text-danger/80">{failedMessage}</pre>
				</div>
			{:else if events.length === 0 && pods.length === 0}
				<p class="text-xs italic text-gray-600">Deploy this app to see logs</p>
			{:else if events.length === 0}
				<p class="text-xs italic text-gray-600">No logs yet…</p>
			{:else}
				{#each events as evt, i (i)}
					<LogLine event={evt} {showPodBadge} />
				{/each}
			{/if}
		</div>
	{:else}
		<!-- Build sub-tab -->
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
					<span
						class="rounded-full px-2 py-0.5 text-[11px] font-medium {buildStatusColor(
							buildResp.status
						)}"
					>
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

		<div
			class="flex-1 overflow-y-auto rounded-md bg-surface-900 p-3"
			style="min-height: 300px; max-height: calc(100vh - 280px)"
		>
			{#if isImageSource}
				<p class="text-xs italic text-gray-600">Image source: builds are skipped</p>
			{:else if buildResp === null}
				<div class="flex items-center gap-2 text-xs text-gray-500">
					<Loader2 class="h-3.5 w-3.5 animate-spin" />
					<span>Loading…</span>
				</div>
			{:else if buildResp.error}
				<div class="space-y-2">
					<p class="text-xs font-medium text-danger">Build error:</p>
					<pre
						class="whitespace-pre-wrap break-all rounded bg-surface-800 p-2 text-xs text-danger/80">{buildResp.error}</pre>
				</div>
			{:else if buildEvents.length === 0}
				{#if buildResp.building}
					<div class="flex items-center gap-2 text-xs text-warning">
						<Loader2 class="h-3.5 w-3.5 animate-spin" />
						<span>Waiting for build output…</span>
					</div>
				{:else}
					<p class="text-xs italic text-gray-600">
						No build yet - pushes to git will appear here
					</p>
				{/if}
			{:else}
				{#each buildEvents as evt, i (i)}
					<LogLine event={evt} showPodBadge={false} />
				{/each}
			{/if}
		</div>
	{/if}
</div>
