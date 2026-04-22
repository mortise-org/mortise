<script lang="ts">
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { App, BuildLogsResponse, Pod } from '$lib/types';
	import { X, GitBranch, Container, Cloud, ExternalLink, Rocket } from 'lucide-svelte';
	import DeploymentsTab from './drawer/DeploymentsTab.svelte';
	import VariablesTab from './drawer/VariablesTab.svelte';
	import LogsTab from './drawer/LogsTab.svelte';
	import MetricsTab from './drawer/MetricsTab.svelte';
	import SettingsTab from './drawer/SettingsTab.svelte';

	let {
		project,
		appName,
		liveApp = null,
		liveBuildLogs = null,
		livePods = new Map(),
		onClose
	}: {
		project: string;
		appName: string;
		liveApp?: App | null;
		liveBuildLogs?: BuildLogsResponse | null;
		livePods?: Map<string, Pod[]>;
		onClose: () => void;
	} = $props();

	// Navbar is the single source of truth for env selection. Tabs read the
	// current env from the store; the drawer does not own env state.
	const selectedEnv = $derived(store.currentEnv(project) ?? '');
	const envStatusEntry = $derived(liveApp?.status?.environments?.find((e) => e.name === selectedEnv));
	const envSpecEntry = $derived(liveApp?.spec.environments?.find((e) => e.name === selectedEnv));
	const envEnabled = $derived(envSpecEntry?.enabled !== false);
	// Building is app-aggregate (one build serves all envs); everything else
	// derives from per-env EnvironmentStatus.phase.
	const envPhase = $derived.by<string | null>(() => {
		const agg = liveApp?.status?.phase ?? null;
		if (agg === 'Building') return 'Building';
		if (agg === 'Failed') return 'Failed';
		return envStatusEntry?.phase ?? agg;
	});

	let appURL = $state<string | null>(null);
	let connecting = $state(false);
	let reloading = $state(false);
	let errorMsg = $state('');
	let logsEverViewed = $state(false);

	const envImage = $derived(
		liveApp?.status?.environments?.find((e) => e.name === selectedEnv)?.currentImage ??
		liveApp?.status?.environments?.[0]?.currentImage ?? null
	);

	$effect(() => {
		if (liveApp?.status?.phase === 'Building' || liveApp?.status?.phase === 'Failed') {
			if (store.drawerTab !== 'logs') store.setDrawerTab('logs');
		}
	});

	async function connectApp() {
		if (!liveApp) return;
		connecting = true;
		try {
			const resp = await api.connectApp(project, liveApp.metadata.name);
			appURL = resp.url;
			window.open(resp.url, '_blank');
		} catch {
			// ignore
		} finally {
			connecting = false;
		}
	}

	// Status display derives from liveApp (polled) with an optimistic override layer.
	// $derived memoises by value: "Building"==="Building" → zero downstream updates
	// when the phase hasn't actually changed, even though liveApp is a new object each poll.
	let optimisticPhase = $state<string | null>(null);
	const effectivePhase = $derived(
		optimisticPhase ?? envPhase
	);

	// Clear optimistic override once the real polled phase catches up.
	$effect(() => {
		if (optimisticPhase && liveApp?.status?.phase && liveApp.status.phase !== optimisticPhase) {
			optimisticPhase = null;
		}
	});

	$effect(() => { if (store.drawerTab === 'logs') logsEverViewed = true; });

	function applyOptimisticPhase(phase: string) {
		optimisticPhase = phase;
	}

	async function doRebuild() {
		if (!liveApp) return;
		errorMsg = '';
		const prevPhase = liveApp.status?.phase;
		applyOptimisticPhase('Building');
		reloading = true;
		try {
			await api.rebuild(project, liveApp.metadata.name);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Rebuild failed';
			if (prevPhase) applyOptimisticPhase(prevPhase);
		} finally {
			reloading = false;
		}
	}

	async function doRedeploy() {
		if (!liveApp || !envImage) return;
		const envName = selectedEnv || liveApp.spec.environments?.[0]?.name;
		if (!envName) return;
		errorMsg = '';
		const prevPhase = liveApp.status?.phase;
		applyOptimisticPhase('Deploying');
		reloading = true;
		try {
			await api.deploy(project, liveApp.metadata.name, envName, envImage);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Redeploy failed';
			if (prevPhase) applyOptimisticPhase(prevPhase);
		} finally {
			reloading = false;
		}
	}

	const isBuilding = $derived(effectivePhase === 'Building');

	// Build timer — synced to BuildStarted condition timestamp from k8s.
	let buildElapsed = $state('');
	let buildTimerHandle: ReturnType<typeof setInterval> | null = null;

	$effect(() => {
		if (isBuilding && !buildTimerHandle) {
			const cond = liveApp?.status?.conditions?.find(c => c.type === 'BuildStarted' && c.status === 'True');
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
		CrashLooping: 'bg-danger/10 text-danger',
		Failed: 'bg-danger/10 text-danger',
		Pending: 'bg-info/10 text-info'
	};

	function chipClass(p: string): string {
		return phaseChip[p] ?? 'bg-surface-700 text-gray-400';
	}
</script>

<!-- Drawer panel -->
<div
	class="fixed right-0 top-14 bottom-0 z-50 flex w-[45%] flex-col border-l border-surface-600 bg-surface-800 shadow-2xl transition-transform duration-200 ease-out"
>
	<!-- Header -->
	<div class="flex shrink-0 items-center justify-between border-b border-surface-600 px-4 py-3">
		<div class="flex items-center gap-2">
			{#if liveApp}
				{#if liveApp.spec.source.type === 'git'}
					<GitBranch class="h-4 w-4 text-gray-400" />
				{:else if liveApp.spec.source.type === 'image'}
					<Container class="h-4 w-4 text-gray-400" />
				{:else}
					<Cloud class="h-4 w-4 text-gray-400" />
				{/if}
			{/if}
			<h2 class="text-sm font-semibold text-white">{appName}</h2>
			{#if !envEnabled}
				<span class="rounded-full bg-surface-700 px-2 py-0.5 text-xs font-medium text-gray-400">Disabled</span>
			{:else if effectivePhase}
				<span class="rounded-full px-2 py-0.5 text-xs font-medium {chipClass(effectivePhase)}">
					{effectivePhase}
				</span>
			{/if}
		</div>
		<div class="flex items-center gap-2">
			{#if liveApp && liveApp.spec.source.type === 'git' && effectivePhase !== 'Building'}
				<button
					type="button"
					onclick={doRebuild}
					disabled={reloading || !envEnabled}
					class="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white transition-colors disabled:opacity-50"
				>
					<Rocket class="h-3 w-3" />
					{reloading ? 'Rebuilding…' : 'Rebuild'}
				</button>
			{/if}
			{#if liveApp && envImage}
				<button
					type="button"
					onclick={doRedeploy}
					disabled={reloading || !envEnabled || effectivePhase === 'Building' || effectivePhase === 'Deploying'}
					class="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white transition-colors disabled:opacity-50"
				>
					{reloading ? 'Redeploying…' : 'Redeploy'}
				</button>
			{/if}
			{#if envEnabled && effectivePhase === 'Ready'}
				{#if appURL}
					<a
						href={appURL}
						target="_blank"
						rel="noopener noreferrer"
						class="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-success hover:bg-surface-700 transition-colors"
					>
						<ExternalLink class="h-3 w-3" />
						Open
					</a>
				{:else}
					<button
						type="button"
						onclick={connectApp}
						disabled={connecting}
						class="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white transition-colors disabled:opacity-50"
					>
						<ExternalLink class="h-3 w-3" />
						{connecting ? 'Connecting...' : 'Open'}
					</button>
				{/if}
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
	<div class="flex shrink-0 gap-1 border-b border-surface-600 px-4">
		{#each tabs as tab}
			<button
				type="button"
				onclick={() => store.setDrawerTab(tab)}
				class="-mb-px border-b-2 px-3 py-1.5 text-xs font-medium transition-colors {store.drawerTab === tab
					? 'border-accent text-white'
					: 'border-transparent text-gray-500 hover:text-white'}"
			>
				{tab.charAt(0).toUpperCase() + tab.slice(1)}
			</button>
		{/each}
	</div>

	<!-- Tab content -->
	<div class="flex-1 overflow-y-auto px-4 pb-4 {store.drawerTab === 'logs' ? 'pt-0' : 'pt-4'}">
		{#if errorMsg}
			<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger mb-2">{errorMsg}</div>
		{/if}
		{#if !liveApp}
			<div class="space-y-3 animate-pulse">
				<div class="h-6 w-40 rounded bg-surface-700"></div>
				<div class="h-24 rounded-lg bg-surface-700"></div>
				<div class="h-12 rounded-lg bg-surface-700"></div>
				<div class="h-32 rounded-lg bg-surface-700"></div>
			</div>
		{:else}
			{#if store.drawerTab === 'deployments'}
				<DeploymentsTab
					{project}
					app={liveApp}
					phase={effectivePhase}
					onOptimisticPhase={applyOptimisticPhase}
				/>
			{:else if store.drawerTab === 'variables'}
				<VariablesTab {project} app={liveApp} />
			{:else if store.drawerTab === 'metrics'}
				<MetricsTab app={liveApp} {project} env={selectedEnv} />
			{:else if store.drawerTab === 'settings'}
				<SettingsTab
					{project}
					app={liveApp}
					onAppDeleted={() => onClose()}
				/>
			{/if}
			{#if logsEverViewed || store.drawerTab === 'logs'}
				<div class="{store.drawerTab !== 'logs' ? 'hidden' : ''}">
					<LogsTab {project} app={liveApp} sseBuildLogs={liveBuildLogs} ssePods={livePods} />
				</div>
			{/if}
		{/if}
	</div>
</div>
