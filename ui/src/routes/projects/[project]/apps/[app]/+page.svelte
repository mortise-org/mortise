<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import { connectProjectEvents } from '$lib/projectEvents';
	import AppDrawer from '$lib/components/AppDrawer.svelte';
	import ProjectCanvas from '$lib/components/ProjectCanvas.svelte';
	import ViewModeToggle from '$lib/components/ViewModeToggle.svelte';
	import { appPhaseForEnvironment, resolveAppEnvironment } from '$lib/types';
	import type { App, AppPhase } from '$lib/types';

	const projectName = $derived(page.params.project ?? '');
	const appName = $derived(page.params.app ?? '');

	let apps = $state<App[]>([]);
	let loading = $state(true);
	let eventStream: Awaited<ReturnType<typeof connectProjectEvents>> | null = null;
	let destroyed = false;
	let phaseOverrides = $state<Record<string, AppPhase | null>>({});
	const liveApp = $derived(apps.find(a => a.metadata.name === appName) ?? null);

	function phaseOverrideKey(targetApp: string, envName: string): string {
		return `${targetApp}:${envName}`;
	}

	function applyPhaseOverride(targetApp: string, envName: string, phase: AppPhase | null) {
		const key = phaseOverrideKey(targetApp, envName);
		if (phase) {
			phaseOverrides = { ...phaseOverrides, [key]: phase };
			return;
		}
		if (!(key in phaseOverrides)) return;
		const next = { ...phaseOverrides };
		delete next[key];
		phaseOverrides = next;
	}

	function reconcilePhaseOverrides(app: App) {
		const next = { ...phaseOverrides };
		let changed = false;
		for (const [key, phase] of Object.entries(phaseOverrides)) {
			if (!key.startsWith(`${app.metadata.name}:`) || !phase) continue;
			const envName = key.slice(app.metadata.name.length + 1);
			const realPhase = appPhaseForEnvironment(app, envName);
			if (realPhase && (realPhase === phase || realPhase !== 'Ready')) {
				delete next[key];
				changed = true;
			}
		}
		if (changed) phaseOverrides = next;
	}

	onMount(async () => {
		if (!localStorage.getItem('mortise_token')) {
			goto('/login');
			return;
		}
		const envQ = page.url.searchParams.get('env');
		if (envQ) store.setEnv(projectName, envQ);
		try {
			apps = await api.listApps(projectName);
			if (destroyed) return;
			eventStream = connectProjectEvents(projectName, {
				onAppUpdated: (app) => {
					reconcilePhaseOverrides(app);
					const idx = apps.findIndex(a => a.metadata.name === app.metadata.name);
					if (idx >= 0) {
						apps[idx] = app;
						apps = apps;
					} else {
						apps = [...apps, app];
					}
				},
				onAppDeleted: (name) => {
					apps = apps.filter(a => a.metadata.name !== name);
				},
				onPods: () => {},
				onBuildLog: () => {}
			});
		} catch {
			apps = [];
		} finally {
			loading = false;
		}
	});

	onDestroy(() => {
		destroyed = true;
		eventStream?.close();
	});

	function closeDrawer() {
		goto(`/projects/${encodeURIComponent(projectName)}`);
	}

	function enc(s: string) {
		return encodeURIComponent(s);
	}
</script>

<!-- Full-height canvas layout -->
<div class="flex h-full flex-col">
	<!-- Canvas behind the drawer -->
	<div class="relative min-h-0 flex-1 overflow-hidden">
		<!-- Floating controls overlay -->
		<div class="absolute top-3 right-3 z-10 flex items-center gap-2">
			<ViewModeToggle />
		</div>
		{#if !loading}
			<ProjectCanvas
				{projectName}
				{apps}
				{phaseOverrides}
				selectedApp={appName}
				onAppOpen={(name) => goto(`/projects/${enc(projectName)}/apps/${enc(name)}`)}
			/>
		{/if}

		<!-- Drawer overlay -->
		{#key appName}
			<AppDrawer
				project={projectName}
				{appName}
				{liveApp}
				phaseOverride={liveApp ? (phaseOverrides[phaseOverrideKey(appName, resolveAppEnvironment(liveApp, store.currentEnv(projectName)))] ?? null) : null}
				onPhaseOverride={(envName, phase) => applyPhaseOverride(appName, envName, phase)}
				onClose={closeDrawer}
			/>
		{/key}
	</div>
</div>
