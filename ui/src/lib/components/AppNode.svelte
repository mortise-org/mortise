<script lang="ts">
	import type { NodeProps } from '@xyflow/svelte';
	import { Handle, Position } from '@xyflow/svelte';
	import { GitBranch, Container, Cloud, Clock, HardDrive } from 'lucide-svelte';
	import type { App, AppPhase } from '$lib/types';

	interface AppNodeData {
		app: App;
		projectName: string;
		onOpen: (appName: string) => void;
	}

	let { data }: NodeProps = $props();
	const nodeData = $derived(data as unknown as AppNodeData);
	const app = $derived(nodeData.app);
	const phase = $derived(app.status?.phase);
	const isExternal = $derived(app.spec.source.type === 'external' as string);
	const isPrivate = $derived(app.spec.network?.public === false);
	const isCron = $derived((app as { kind?: string }).kind === 'cron');

	const phaseClass: Record<AppPhase, string> = {
		Ready: 'bg-success/10 text-success',
		Building: 'bg-warning/10 text-warning',
		Deploying: 'bg-warning/10 text-warning',
		Failed: 'bg-danger/10 text-danger',
		Pending: 'bg-info/10 text-info'
	};

	function primaryDomain(a: App): string | null {
		const env = a.spec.environments?.[0];
		if (!env) return null;
		return env.domain ?? null;
	}

	function replicaCount(a: App): number {
		return a.spec.environments?.[0]?.replicas ?? 1;
	}

	const domain = $derived(primaryDomain(app));
	const replicas = $derived(replicaCount(app));
	const volumes = $derived(app.spec.storage ?? []);

	function failedReason(a: App): string | null {
		if (a.status?.phase !== 'Failed') return null;
		const cond = a.status.conditions?.find(c => c.status === 'False');
		return cond?.message ?? null;
	}
	const errorMsg = $derived(failedReason(app));
</script>

<div
	role="button"
	tabindex="0"
	class="relative flex w-60 flex-col gap-2 rounded-lg border border-surface-600 bg-surface-800 p-3 transition-all duration-150 hover:shadow-lg hover:shadow-black/20 hover:border-surface-500 cursor-pointer {isExternal ? 'border-dashed' : ''}"
	onclick={() => nodeData.onOpen(app.metadata.name)}
	onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') nodeData.onOpen(app.metadata.name); }}
>
	<!-- Handles -->
	<Handle type="target" position={Position.Left} />
	<Handle type="source" position={Position.Right} />

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

	<!-- Status chip -->
	{#if phase}
		<div class="flex items-center gap-1.5">
			<span class="rounded px-1.5 py-0.5 text-xs font-medium {phaseClass[phase] ?? 'bg-surface-700 text-gray-400'}">
				{phase}
			</span>
			{#if phase === 'Failed' && errorMsg}
				<span class="h-3 w-3 shrink-0 text-danger" title={errorMsg}>
					<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="h-3 w-3">
						<path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z" clip-rule="evenodd" />
					</svg>
				</span>
			{/if}
		</div>
		{#if phase === 'Failed' && errorMsg}
			<span class="line-clamp-2 text-xs text-danger/80">{errorMsg}</span>
		{/if}
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
</div>
