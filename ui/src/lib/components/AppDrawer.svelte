<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { App, ProjectEnvironment } from '$lib/types';
	import { X, GitBranch, Container, Cloud, Loader2, ExternalLink, Rocket } from 'lucide-svelte';
	import DeploymentsTab from './drawer/DeploymentsTab.svelte';
	import VariablesTab from './drawer/VariablesTab.svelte';
	import LogsTab from './drawer/LogsTab.svelte';
	import MetricsTab from './drawer/MetricsTab.svelte';
	import SettingsTab from './drawer/SettingsTab.svelte';

	let {
		project,
		appName,
		liveApp = null,
		initialEnv = '',
		onClose
	}: {
		project: string;
		appName: string;
		liveApp?: App | null;
		initialEnv?: string;
		onClose: () => void;
	} = $props();

	// Selected env in the drawer. Seeds from navbar's currentEnv; user can
	// switch within the drawer (DeploymentsTab / LogsTab) and that write
	// propagates back to the store so the navbar stays in sync.
	const selectedEnv = $derived(store.currentEnv(project) ?? initialEnv);
	const envStatusEntry = $derived(liveApp?.status?.environments?.find((e) => e.name === selectedEnv));
	const envSpecEntry = $derived(liveApp?.spec.environments?.find((e) => e.name === selectedEnv));
	const envEnabled = $derived(envSpecEntry?.enabled !== false);
	const envPhase = $derived.by<string | null>(() => {
		const p = liveApp?.status?.phase ?? app?.status?.phase ?? null;
		if (p === 'Ready' && envStatusEntry && envStatusEntry.readyReplicas === 0) return 'Deploying';
		return p;
	});

	// app: stable snapshot set on mount, updated only by user actions (onAppUpdated).
	// Never replaced by polling — so tabs never re-render from background polls.
	let app = $state<App | null>(null);
	let loading = $state(true);
	let error = $state('');
	let logsEverViewed = $state(false);
	let projectEnvs = $state<ProjectEnvironment[]>([]);

	onMount(async () => {
		try {
			app = await api.getApp(project, appName);
			if (app?.status?.phase === 'Building' || app?.status?.phase === 'Failed') {
				store.setDrawerTab('logs');
			}
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load app';
		} finally {
			loading = false;
		}
		try {
			projectEnvs = await api.listProjectEnvironments(project);
		} catch {
			projectEnvs = [];
		}
	});

	function setEnv(name: string) {
		store.setEnv(project, name);
	}

	let appURL = $state<string | null>(null);
	let connecting = $state(false);
	let reloading = $state(false);
	let errorMsg = $state('');

	const envImage = $derived(
		(liveApp ?? app)?.status?.environments?.find((e) => e.name === selectedEnv)?.currentImage ??
		(liveApp ?? app)?.status?.environments?.[0]?.currentImage ?? null
	);

	async function connectApp() {
		if (!app) return;
		connecting = true;
		try {
			const resp = await api.connectApp(project, app.metadata.name);
			appURL = resp.url;
			window.open(resp.url, '_blank');
		} catch (e) {
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
		if (!app) return;
		errorMsg = '';
		const prevPhase = app.status?.phase;
		applyOptimisticPhase('Building');
		reloading = true;
		try {
			await api.rebuild(project, app.metadata.name);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Rebuild failed';
			if (prevPhase) applyOptimisticPhase(prevPhase);
		} finally {
			reloading = false;
		}
	}

	async function doRedeploy() {
		if (!app || !envImage) return;
		const envName = selectedEnv || app.spec.environments?.[0]?.name;
		if (!envName) return;
		errorMsg = '';
		const prevPhase = app.status?.phase;
		applyOptimisticPhase('Deploying');
		reloading = true;
		try {
			await api.deploy(project, app.metadata.name, envName, envImage);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Redeploy failed';
			if (prevPhase) applyOptimisticPhase(prevPhase);
		} finally {
			reloading = false;
		}
	}

	const liveConditions = $derived(liveApp?.status?.conditions ?? app?.status?.conditions ?? []);
	const buildError = $derived(
		effectivePhase === 'Failed'
			? (liveConditions.find(c => c.status === 'False')?.message ?? null)
			: null
	);
	const crashError = $derived(
		effectivePhase === 'CrashLooping'
			? (liveConditions.find(c => c.type === 'PodHealthy' && c.status === 'False')?.message ?? null)
			: null
	);
	const isBuilding = $derived(effectivePhase === 'Building');

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
			{#if selectedEnv}
				<span class="rounded-full px-2 py-0.5 text-xs font-medium text-gray-300 bg-surface-700">{selectedEnv}</span>
			{/if}
			{#if !envEnabled}
				<span class="rounded-full bg-surface-700 px-2 py-0.5 text-xs font-medium text-gray-400">Disabled</span>
			{:else if effectivePhase}
				<span class="rounded-full px-2 py-0.5 text-xs font-medium {chipClass(effectivePhase)}">
					{effectivePhase}
				</span>
			{/if}
		</div>
		<div class="flex items-center gap-2">
			{#if app && app.spec.source.type === 'git' && effectivePhase !== 'Building'}
				<button
					type="button"
					onclick={doRebuild}
					disabled={reloading}
					class="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white transition-colors disabled:opacity-50"
				>
					<Rocket class="h-3 w-3" />
					{reloading ? 'Rebuilding…' : 'Rebuild'}
				</button>
			{/if}
			{#if app && envImage}
				<button
					type="button"
					onclick={doRedeploy}
					disabled={reloading || effectivePhase === 'Building' || effectivePhase === 'Deploying'}
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
				<DeploymentsTab
					{project}
					{app}
					{projectEnvs}
					selectedEnv={selectedEnv}
					onSelectEnv={setEnv}
					phase={effectivePhase}
					onOptimisticPhase={applyOptimisticPhase}
				/>
			{:else if store.drawerTab === 'variables'}
				<VariablesTab {project} {app} {projectEnvs} onAppUpdated={(updated) => { app = updated; }} />
			{:else if store.drawerTab === 'metrics'}
				<MetricsTab {app} />
			{:else if store.drawerTab === 'settings'}
				<SettingsTab
					{project}
					{app}
					{projectEnvs}
					selectedEnv={selectedEnv}
					onAppUpdated={(updated) => { app = updated; }}
					onAppDeleted={() => onClose()}
				/>
			{/if}
			{#if logsEverViewed || store.drawerTab === 'logs'}
				<div class="{store.drawerTab !== 'logs' ? 'hidden' : ''}">
					<LogsTab
						{project}
						{app}
						{projectEnvs}
						selectedEnv={selectedEnv}
						onSelectEnv={setEnv}
					/>
				</div>
			{/if}
		{/if}
	</div>
</div>
