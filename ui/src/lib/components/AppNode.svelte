<script lang="ts">
	import { onMount } from 'svelte';
	import type { NodeProps } from '@xyflow/svelte';
	import { Handle, Position } from '@xyflow/svelte';
	import { GitBranch, Container, Cloud, Clock, HardDrive, RotateCw } from 'lucide-svelte';
	import { appNeedsRedeploy } from '$lib/types';
	import type { App, AppPhase } from '$lib/types';

	interface AppNodeData {
		app: App;
		projectName: string;
		env: string;
		onOpen: (appName: string) => void;
	}

	let { data }: NodeProps = $props();
	const nodeData = $derived(data as unknown as AppNodeData);
	const app = $derived(nodeData.app);
	const envName = $derived(nodeData.env);
	const envEntry = $derived(app.spec.environments?.find((e) => e.name === envName));
	const envStatusEntry = $derived(app.status?.environments?.find((e) => e.name === envName));
	const enabled = $derived(envEntry?.enabled !== false);
	// Building is app-aggregate (one build serves all envs); everything else is
	// per-env from EnvironmentStatus.phase.
	const phase = $derived.by<AppPhase | undefined>(() => {
		if (app.status?.phase === 'Building') return 'Building';
		if (app.status?.phase === 'Failed') return 'Failed';
		return envStatusEntry?.phase ?? app.status?.phase;
	});
	const isExternal = $derived(app.spec.source.type === 'external' as string);
	const isPrivate = $derived(app.spec.network?.public === false);
	const isCron = $derived((app as { kind?: string }).kind === 'cron');

	const phaseClass: Record<AppPhase, string> = {
		Ready: 'bg-success/10 text-success',
		Building: 'bg-warning/10 text-warning',
		Deploying: 'bg-warning/10 text-warning',
		CrashLooping: 'bg-danger/10 text-danger',
		Failed: 'bg-danger/10 text-danger',
		Pending: 'bg-info/10 text-info'
	};

	const needsRedeploy = $derived(appNeedsRedeploy(app));

	const domain = $derived(envEntry?.domain ?? null);
	const replicas = $derived(envEntry?.replicas ?? 1);
	const volumes = $derived(app.spec.storage ?? []);

	function failedReason(a: App, envMsg: string | undefined): string | null {
		if (phase !== 'Failed' && phase !== 'CrashLooping') return null;
		if (envMsg) return envMsg;
		const cond = a.status?.conditions?.find(c => c.status === 'False');
		return cond?.message ?? null;
	}
	const errorMsg = $derived(failedReason(app, envStatusEntry?.message));

	// Build timer - synced to the BuildStarted condition timestamp from k8s.
	let buildElapsed = $state('');
	let timerHandle: ReturnType<typeof setInterval> | null = null;

	function buildStartTime(): number | null {
		const cond = app.status?.conditions?.find(c => c.type === 'BuildStarted' && c.status === 'True');
		if (!cond?.lastTransitionTime) return null;
		return new Date(cond.lastTransitionTime).getTime();
	}

	function startBuildTimer() {
		const start = buildStartTime() ?? Date.now();
		const tick = () => {
			const s = Math.floor((Date.now() - start) / 1000);
			const m = Math.floor(s / 60);
			buildElapsed = m > 0 ? `${m}m ${s % 60}s` : `${s}s`;
		};
		tick();
		timerHandle = setInterval(tick, 1000);
	}

	$effect(() => {
		if (phase === 'Building' && !timerHandle) {
			startBuildTimer();
		} else if (phase !== 'Building' && timerHandle) {
			clearInterval(timerHandle);
			timerHandle = null;
			buildElapsed = '';
		}
	});

	onMount(() => () => { if (timerHandle) clearInterval(timerHandle); });
</script>

<div
	role="button"
	tabindex="0"
	class="relative flex w-60 min-h-[7rem] flex-col gap-2 rounded-lg border border-surface-600 bg-surface-800 p-3 transition-all duration-150 hover:shadow-lg hover:shadow-black/20 hover:border-surface-500 cursor-pointer {isExternal ? 'border-dashed' : ''} {enabled ? '' : 'opacity-50'}"
	onclick={() => nodeData.onOpen(app.metadata.name)}
	onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') nodeData.onOpen(app.metadata.name); }}
>
	<!-- Header row -->
	<div class="flex items-center justify-between gap-2">
		<div class="flex items-center gap-1.5 min-w-0">
			{#if isCron}
				<Clock class="h-3.5 w-3.5 shrink-0 text-gray-400" />
			{/if}
			<span class="truncate text-sm font-medium text-white">{app.metadata.name}</span>
		</div>
		<span class="shrink-0 text-gray-400">
			{#if app.spec.source.type === 'git'}
				<GitBranch class="h-3.5 w-3.5" />
			{:else if app.spec.source.type === 'image'}
				<Container class="h-3.5 w-3.5" />
			{:else}
				<Cloud class="h-3.5 w-3.5" />
			{/if}
		</span>
	</div>

	<!-- Status chip (always shown, Pending when no phase yet) -->
	<div class="flex items-center gap-1.5">
		{#if !enabled}
			<span class="rounded bg-surface-700 px-1.5 py-0.5 text-xs font-medium text-gray-400">Disabled</span>
		{:else}
			<span class="rounded px-1.5 py-0.5 text-xs font-medium {phaseClass[phase ?? 'Pending'] ?? 'bg-surface-700 text-gray-400'}">
				{phase ?? 'Pending'}
			</span>
		{/if}
			{#if phase === 'Building' && buildElapsed}
				<span class="text-xs font-mono text-warning/70">{buildElapsed}</span>
			{/if}
			{#if (phase === 'Failed' || phase === 'CrashLooping') && errorMsg}
				<span class="h-3 w-3 shrink-0 text-danger" title={errorMsg}>
					<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="h-3 w-3">
						<path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z" clip-rule="evenodd" />
					</svg>
				</span>
			{/if}
		</div>
	{#if (phase === 'Failed' || phase === 'CrashLooping') && errorMsg}
		<span class="line-clamp-2 text-xs text-danger/80">{errorMsg}</span>
	{/if}

	{#if needsRedeploy && enabled}
		<span class="flex items-center gap-1 text-xs font-medium text-warning">
			<RotateCw class="h-3 w-3" />
			Needs redeploy
		</span>
	{/if}

	<!-- Domain or Private label -->
	{#if isPrivate}
		<span class="text-xs font-medium text-gray-500">Private</span>
	{:else if domain}
		<span class="truncate font-mono text-xs text-gray-500">{domain}</span>
	{/if}

	<!-- Replica count -->
	<span class="text-xs text-gray-500">{replicas} replica{replicas === 1 ? '' : 's'}</span>

	<!-- Volume pills -->
	{#if volumes.length > 0}
		<div class="flex flex-wrap gap-1">
			{#each volumes as vol}
				<span class="flex items-center gap-0.5 rounded bg-surface-700 px-1.5 py-0.5 text-xs text-gray-400">
					<HardDrive class="h-2.5 w-2.5" />
					{vol.name}
				</span>
			{/each}
		</div>
	{/if}

	<Handle type="target" position={Position.Left} class="!opacity-0 !w-1 !h-1 !min-w-0 !min-h-0 !border-0" />
	<Handle type="source" position={Position.Right} class="!opacity-0 !w-1 !h-1 !min-w-0 !min-h-0 !border-0" />
</div>
