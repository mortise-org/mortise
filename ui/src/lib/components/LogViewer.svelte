<script lang="ts">
	import { onMount, onDestroy, tick } from 'svelte';
	import { api } from '$lib/api';
	import { hashPodColor } from '$lib/pod-colors';

	interface Props {
		project: string;
		appName: string;
		env: string;
		tail?: number;
	}

	let { project, appName, env, tail = 200 }: Props = $props();

	interface LogEntry {
		pod: string;
		line: string;
		stream?: string;
		receivedAt: number;
	}

	let entries = $state<LogEntry[]>([]);
	let pods = $state<Set<string>>(new Set());
	let selectedPod = $state<string>('');
	let paused = $state(false);
	let connected = $state(false);
	let errored = $state(false);
	let autoScroll = $state(true);

	let source: EventSource | null = null;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	let scrollContainer: HTMLDivElement | null = $state(null);
	let pausedBuffer = $state<LogEntry[]>([]);

	const MAX_ENTRIES = 5000;

	const filtered = $derived(
		selectedPod === '' ? entries : entries.filter((e) => e.pod === selectedPod)
	);

	const podList = $derived(Array.from(pods).sort());

	function connect() {
		if (source) {
			source.close();
			source = null;
		}
		const url = api.logsURL(project, appName, { env, follow: true, tail });
		errored = false;
		source = new EventSource(url);

		source.onopen = () => {
			connected = true;
			errored = false;
		};

		source.onmessage = (ev) => {
			try {
				const parsed = JSON.parse(ev.data);
				if (!parsed || typeof parsed.line !== 'string' || typeof parsed.pod !== 'string') {
					return;
				}
				const entry: LogEntry = {
					pod: parsed.pod,
					line: parsed.line,
					stream: typeof parsed.stream === 'string' ? parsed.stream : undefined,
					receivedAt: Date.now()
				};
				if (!pods.has(entry.pod)) {
					pods = new Set([...pods, entry.pod]);
				}
				if (paused) {
					pausedBuffer.push(entry);
					if (pausedBuffer.length > MAX_ENTRIES) {
						pausedBuffer = pausedBuffer.slice(-MAX_ENTRIES);
					}
					return;
				}
				appendEntry(entry);
			} catch {
				// Ignore malformed events.
			}
		};

		source.onerror = () => {
			connected = false;
			errored = true;
			if (source) {
				source.close();
				source = null;
			}
			scheduleReconnect();
		};
	}

	function appendEntry(entry: LogEntry) {
		const next = entries.length >= MAX_ENTRIES ? entries.slice(-MAX_ENTRIES + 1) : entries.slice();
		next.push(entry);
		entries = next;
		if (autoScroll) {
			void scrollToBottom();
		}
	}

	async function scrollToBottom() {
		await tick();
		if (scrollContainer) {
			scrollContainer.scrollTop = scrollContainer.scrollHeight;
		}
	}

	function scheduleReconnect() {
		if (reconnectTimer) return;
		reconnectTimer = setTimeout(() => {
			reconnectTimer = null;
			connect();
		}, 2000);
	}

	function handleScroll() {
		if (!scrollContainer) return;
		const threshold = 40;
		const atBottom =
			scrollContainer.scrollTop + scrollContainer.clientHeight >=
			scrollContainer.scrollHeight - threshold;
		autoScroll = atBottom;
	}

	function togglePause() {
		paused = !paused;
		if (!paused && pausedBuffer.length > 0) {
			// Flush the buffer all at once.
			const combined = entries.concat(pausedBuffer);
			entries = combined.length > MAX_ENTRIES ? combined.slice(-MAX_ENTRIES) : combined;
			pausedBuffer = [];
			if (autoScroll) {
				void scrollToBottom();
			}
		}
	}

	function clearLogs() {
		entries = [];
		pausedBuffer = [];
	}

	function downloadLogs() {
		const text = filtered
			.map((e) => `[${e.pod}] ${e.line}`)
			.join('\n');
		const blob = new Blob([text], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `${appName}-${env}-${new Date().toISOString().replace(/[:.]/g, '-')}.log`;
		document.body.appendChild(a);
		a.click();
		document.body.removeChild(a);
		URL.revokeObjectURL(url);
	}

	onMount(() => {
		connect();
	});

	onDestroy(() => {
		if (source) {
			source.close();
			source = null;
		}
		if (reconnectTimer) {
			clearTimeout(reconnectTimer);
			reconnectTimer = null;
		}
	});

	// Reconnect when project/appName/env changes.
	$effect(() => {
		void project;
		void appName;
		void env;
		entries = [];
		pausedBuffer = [];
		pods = new Set();
		selectedPod = '';
		connect();
	});
</script>

<section class="rounded-lg border border-surface-600 bg-surface-800 p-4">
	<div class="mb-3 flex flex-wrap items-center justify-between gap-2">
		<h2 class="text-sm font-medium text-gray-300">Logs</h2>
		<div class="flex flex-wrap items-center gap-2">
			<select
				bind:value={selectedPod}
				class="rounded-md border border-surface-600 bg-surface-700 px-2 py-1 text-xs text-white outline-none focus:border-accent"
			>
				<option value="">All pods ({podList.length})</option>
				{#each podList as pod}
					<option value={pod}>{pod}</option>
				{/each}
			</select>
			<button
				type="button"
				onclick={togglePause}
				class="rounded-md border border-surface-600 bg-surface-700 px-3 py-1 text-xs text-white transition-colors hover:bg-surface-600"
			>
				{paused ? `Resume (${pausedBuffer.length})` : 'Pause'}
			</button>
			<button
				type="button"
				onclick={clearLogs}
				class="rounded-md border border-surface-600 bg-surface-700 px-3 py-1 text-xs text-white transition-colors hover:bg-surface-600"
			>
				Clear
			</button>
			<button
				type="button"
				onclick={downloadLogs}
				disabled={filtered.length === 0}
				class="rounded-md border border-surface-600 bg-surface-700 px-3 py-1 text-xs text-white transition-colors hover:bg-surface-600 disabled:opacity-50"
			>
				Download
			</button>
		</div>
	</div>

	<div
		bind:this={scrollContainer}
		onscroll={handleScroll}
		class="h-96 overflow-auto rounded-md border border-surface-600 bg-black p-3 font-mono text-xs leading-relaxed"
	>
		{#if errored}
			<div class="text-danger">Disconnected. Retrying...</div>
		{/if}

		{#if filtered.length === 0 && !errored}
			<div class="flex items-center gap-2 text-gray-500">
				<span
					class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-gray-500 border-t-transparent"
					aria-hidden="true"
				></span>
				Waiting for logs...
			</div>
		{:else}
			{#each filtered as entry}
				<div class="flex gap-2 whitespace-pre-wrap break-all">
					<span style="color: {hashPodColor(entry.pod)};" class="shrink-0 opacity-80">
						[{entry.pod}]
					</span>
					<span class="text-gray-200">{entry.line}</span>
				</div>
			{/each}
		{/if}
	</div>

	<div class="mt-2 flex items-center justify-between text-xs text-gray-500">
		<span>
			{#if connected}
				<span class="text-success">connected</span>
			{:else if errored}
				<span class="text-danger">disconnected</span>
			{:else}
				<span>connecting...</span>
			{/if}
			· {entries.length} line{entries.length === 1 ? '' : 's'}
		</span>
		{#if !autoScroll}
			<button
				type="button"
				onclick={() => {
					autoScroll = true;
					void scrollToBottom();
				}}
				class="text-accent hover:text-accent-hover"
			>
				Jump to latest
			</button>
		{/if}
	</div>
</section>
