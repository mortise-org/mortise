<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { Loader2 } from 'lucide-svelte';
	import type { App } from '$lib/types';

	let { project, app }: { project: string; app: App } = $props();

	const isBuilding = $derived(app.status?.phase === 'Building');
	const isFailed = $derived(app.status?.phase === 'Failed');
	const failedMessage = $derived(
		app.status?.conditions?.find(c => c.status === 'False')?.message ?? null
	);

	let selectedEnv = $state(app.spec.environments?.[0]?.name ?? 'production');
	let lines = $state<string[]>([]);
	let following = $state(true);
	let es: EventSource | null = null;
	let logContainer: HTMLElement | null = $state(null);
	let buildPollHandle: ReturnType<typeof setInterval> | null = null;

	function scrollToBottom() {
		if (logContainer) {
			logContainer.scrollTop = logContainer.scrollHeight;
		}
	}

	function connect() {
		es?.close();
		// Don't try to stream pod logs while building — there are no pods yet.
		if (isBuilding) return;
		lines = [];
		const url = api.logsURL(project, app.metadata.name, selectedEnv);
		es = new EventSource(url);
		es.onmessage = (e: MessageEvent) => {
			lines = [...lines.slice(-499), e.data as string];
			if (following) {
				setTimeout(scrollToBottom, 0);
			}
		};
		es.onerror = () => {};
	}

	async function pollBuildLogs() {
		try {
			const resp = await api.getBuildLogs(project, app.metadata.name);
			lines = resp.lines;
			if (following) setTimeout(scrollToBottom, 0);
		} catch { /* ignore */ }
	}

	// Start/stop build log polling based on build state.
	$effect(() => {
		if (isBuilding && !buildPollHandle) {
			void pollBuildLogs();
			buildPollHandle = setInterval(pollBuildLogs, 2000);
		} else if (!isBuilding && buildPollHandle) {
			clearInterval(buildPollHandle);
			buildPollHandle = null;
			// Build finished — switch to pod log streaming.
			connect();
		}
	});

	onMount(() => {
		if (!isBuilding) {
			connect();
		}
		return () => {
			es?.close();
			es = null;
			if (buildPollHandle) {
				clearInterval(buildPollHandle);
				buildPollHandle = null;
			}
		};
	});

	$effect(() => {
		void selectedEnv;
		if (!isBuilding) connect();
	});

	function clearLogs() {
		lines = [];
	}

	let searchQuery = $state('');
	const filteredLines = $derived(
		searchQuery.trim()
			? lines.filter(l => l.toLowerCase().includes(searchQuery.toLowerCase()))
			: lines
	);

	async function copyLogs() {
		try {
			await navigator.clipboard.writeText(lines.join('\n'));
		} catch {
			// ignore
		}
	}
</script>

<div class="flex h-full flex-col gap-3">
	<!-- Top bar -->
	<div class="flex items-center justify-between">
		{#if isBuilding}
			<div class="flex items-center gap-1.5 text-xs text-warning">
				<Loader2 class="h-3 w-3 animate-spin" />
				<span>Build logs</span>
			</div>
		{:else if app.spec.environments && app.spec.environments.length > 1}
			<div class="flex gap-1">
				{#each app.spec.environments as env}
					<button
						type="button"
						onclick={() => (selectedEnv = env.name)}
						class="rounded px-2.5 py-1 text-xs transition-colors {selectedEnv === env.name
							? 'bg-surface-600 text-white'
							: 'text-gray-400 hover:text-white'}"
					>
						{env.name}
					</button>
				{/each}
			</div>
		{:else}
			<span class="text-xs text-gray-500">{selectedEnv}</span>
		{/if}

		<div class="flex items-center gap-2">
			<label class="flex cursor-pointer items-center gap-1.5 text-xs text-gray-400">
				<span>Live tail</span>
				<button
					type="button"
					role="switch"
					aria-checked={following}
					onclick={() => (following = !following)}
					class="relative inline-flex h-4 w-7 items-center rounded-full transition-colors {following ? 'bg-accent' : 'bg-surface-600'}"
				>
					<span
						class="inline-block h-3 w-3 transform rounded-full bg-white shadow transition-transform {following ? 'translate-x-3.5' : 'translate-x-0.5'}"
					></span>
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

	<!-- Search -->
	<input
		type="text"
		bind:value={searchQuery}
		placeholder="Filter logs…"
		class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-1.5 text-xs text-white placeholder-gray-500 outline-none focus:border-accent"
	/>

	<!-- Log body -->
	<div
		bind:this={logContainer}
		class="flex-1 overflow-y-auto rounded-md bg-surface-900 p-3"
		style="min-height: 300px; max-height: calc(100vh - 280px)"
	>
		{#if filteredLines.length === 0}
			{#if isBuilding}
				<div class="flex items-center gap-2 text-xs text-warning">
					<Loader2 class="h-3.5 w-3.5 animate-spin" />
					<span>Waiting for build output...</span>
				</div>
			{:else if isFailed && failedMessage}
				<div class="space-y-2">
					<p class="text-xs font-medium text-danger">Build failed:</p>
					<pre class="whitespace-pre-wrap break-all rounded bg-surface-800 p-2 text-xs text-danger/80">{failedMessage}</pre>
				</div>
			{:else}
				<p class="text-xs text-gray-600 italic">{searchQuery.trim() ? 'No matching lines.' : 'No logs yet…'}</p>
			{/if}
		{:else}
			{#each filteredLines as line}
				<div class="font-mono text-xs leading-5 text-gray-300 whitespace-pre-wrap break-all">{line}</div>
			{/each}
		{/if}
	</div>
</div>
