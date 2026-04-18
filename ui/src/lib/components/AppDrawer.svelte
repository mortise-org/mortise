<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { App } from '$lib/types';
	import { X, GitBranch, Container, Cloud, AlertTriangle, Loader2, ExternalLink } from 'lucide-svelte';
	import DeploymentsTab from './drawer/DeploymentsTab.svelte';
	import VariablesTab from './drawer/VariablesTab.svelte';
	import LogsTab from './drawer/LogsTab.svelte';
	import MetricsTab from './drawer/MetricsTab.svelte';
	import SettingsTab from './drawer/SettingsTab.svelte';

	let {
		project,
		appName,
		onClose
	}: {
		project: string;
		appName: string;
		onClose: () => void;
	} = $props();

	let app = $state<App | null>(null);
	let loading = $state(true);
	let error = $state('');

	onMount(async () => {
		try {
			app = await api.getApp(project, appName);
			// Auto-open logs tab when app is building or failed (so user sees build output)
			if (app?.status?.phase === 'Building' || app?.status?.phase === 'Failed') {
				store.setDrawerTab('logs');
			}
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load app';
		} finally {
			loading = false;
		}
	});

	function conditionMessage(a: App): string | null {
		const cond = a.status?.conditions?.find(c => c.status === 'False');
		return cond?.message ?? null;
	}

	const buildError = $derived(
		app?.status?.phase === 'Failed' ? conditionMessage(app) : null
	);
	const isBuilding = $derived(app?.status?.phase === 'Building');

	// Build timer — synced to BuildStarted condition timestamp from k8s.
	let buildElapsed = $state('');
	let buildTimerHandle: ReturnType<typeof setInterval> | null = null;

	$effect(() => {
		if (isBuilding && !buildTimerHandle) {
			const cond = app?.status?.conditions?.find(c => c.type === 'BuildStarted' && c.status === 'True');
			const start = cond?.lastTransitionTime ? new Date(cond.lastTransitionTime).getTime() : Date.now();
			const tick = () => {
				const s = Math.floor((Date.now() - start) / 1000);
				const m = Math.floor(s / 60);
				buildElapsed = m > 0 ? `${m}m ${s % 60}s` : `${s}s`;
			};
			tick();
			buildTimerHandle = setInterval(tick, 1000);
		} else if (!isBuilding && buildTimerHandle) {
			clearInterval(buildTimerHandle);
			buildTimerHandle = null;
			buildElapsed = '';
		}
	});

	const tabs = ['deployments', 'variables', 'logs', 'metrics', 'settings'] as const;

	const phaseChip: Record<string, string> = {
		Ready: 'bg-success/10 text-success',
		Building: 'bg-warning/10 text-warning',
		Deploying: 'bg-warning/10 text-warning',
		Failed: 'bg-danger/10 text-danger',
		Pending: 'bg-info/10 text-info'
	};

	function chipClass(p: string): string {
		return phaseChip[p] ?? 'bg-surface-700 text-gray-400';
	}
</script>

<!-- Backdrop: click to close -->
<div
	class="fixed inset-0 z-40"
	onclick={onClose}
	role="presentation"
></div>

<!-- Drawer panel -->
<div
	class="fixed right-0 top-14 bottom-0 z-50 flex w-[45%] flex-col border-l border-surface-600 bg-surface-800 shadow-2xl transition-transform duration-200 ease-out"
>
	<!-- Header -->
	<div class="flex shrink-0 items-center justify-between border-b border-surface-600 px-4 py-3">
		<div class="flex items-center gap-2">
			{#if app}
				{#if app.spec.source.type === 'git'}
					<GitBranch class="h-4 w-4 text-gray-400" />
				{:else if app.spec.source.type === 'image'}
					<Container class="h-4 w-4 text-gray-400" />
				{:else}
					<Cloud class="h-4 w-4 text-gray-400" />
				{/if}
			{/if}
			<h2 class="text-sm font-semibold text-white">{appName}</h2>
			{#if app?.status?.phase}
				<span class="rounded-full px-2 py-0.5 text-xs font-medium {chipClass(app.status.phase)}">
					{app.status.phase}
				</span>
			{/if}
		</div>
		<div class="flex items-center gap-2">
			{#if app?.status?.phase === 'Ready'}
				<a
					href="/proxy/{project}/{appName}/"
					target="_blank"
					rel="noopener noreferrer"
					class="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white transition-colors"
				>
					<ExternalLink class="h-3 w-3" />
					Open
				</a>
			{/if}
			<button
				type="button"
				onclick={onClose}
				class="rounded-md p-1.5 text-gray-500 transition-colors hover:bg-surface-700 hover:text-white"
				aria-label="Close drawer"
			>
				<X class="h-4 w-4" />
			</button>
		</div>
	</div>

	<!-- Tabs -->
	<div class="flex shrink-0 gap-1 border-b border-surface-600 px-4 py-2">
		{#each tabs as tab}
			<button
				type="button"
				onclick={() => store.setDrawerTab(tab)}
				class="{store.drawerTab === tab
					? 'rounded px-2.5 py-1 text-xs bg-surface-600 text-white'
					: 'rounded px-2.5 py-1 text-xs text-gray-400 hover:text-white'}"
			>
				{tab.charAt(0).toUpperCase() + tab.slice(1)}
			</button>
		{/each}
	</div>

	<!-- Build status banner -->
	{#if isBuilding}
		<div class="mx-4 mt-3 flex items-center justify-between rounded-md border border-warning/30 bg-warning/10 px-3 py-2">
			<span class="text-sm text-warning">Building...</span>
			{#if buildElapsed}
				<span class="font-mono text-xs text-warning/70">{buildElapsed}</span>
			{/if}
		</div>
	{/if}
	{#if buildError}
		<div class="mx-4 mt-3 flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2">
			<AlertTriangle class="mt-0.5 h-4 w-4 shrink-0 text-danger" />
			<span class="text-sm text-danger">{buildError}</span>
		</div>
	{/if}

	<!-- Tab content -->
	<div class="flex-1 overflow-y-auto p-4">
		{#if loading}
			<div class="space-y-3 animate-pulse">
				<div class="h-6 w-40 rounded bg-surface-700"></div>
				<div class="h-24 rounded-lg bg-surface-700"></div>
				<div class="h-12 rounded-lg bg-surface-700"></div>
				<div class="h-32 rounded-lg bg-surface-700"></div>
			</div>
		{:else if error}
			<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
		{:else if app}
			{#if store.drawerTab === 'deployments'}
				<DeploymentsTab {project} {app} />
			{:else if store.drawerTab === 'variables'}
				<VariablesTab {project} {app} onAppUpdated={(updated) => { app = updated; }} />
			{:else if store.drawerTab === 'logs'}
				<LogsTab {project} {app} />
			{:else if store.drawerTab === 'metrics'}
				<MetricsTab {app} />
			{:else if store.drawerTab === 'settings'}
				<SettingsTab
					{project}
					{app}
					onAppUpdated={(updated) => { app = updated; }}
					onAppDeleted={() => onClose()}
				/>
			{/if}
		{/if}
	</div>
</div>
