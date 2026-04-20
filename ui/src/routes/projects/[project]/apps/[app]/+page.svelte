<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import AppDrawer from '$lib/components/AppDrawer.svelte';
	import ProjectCanvas from '$lib/components/ProjectCanvas.svelte';
	import ViewModeToggle from '$lib/components/ViewModeToggle.svelte';
	import type { App } from '$lib/types';

	const projectName = $derived(page.params.project ?? '');
	const appName = $derived(page.params.app ?? '');

	let apps = $state<App[]>([]);
	let loading = $state(true);

	onMount(async () => {
		if (!localStorage.getItem('mortise_token')) {
			goto('/login');
			return;
		}
		const envQ = page.url.searchParams.get('env');
		if (envQ) store.setEnv(projectName, envQ);
		try {
			apps = await api.listApps(projectName);
		} catch {
			apps = [];
		} finally {
			loading = false;
		}
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
	<div class="relative flex-1 overflow-hidden" style="height: calc(100vh - 57px)">
		<!-- Floating controls overlay -->
		<div class="absolute top-3 right-3 z-10 flex items-center gap-2">
			<ViewModeToggle />
		</div>
		{#if !loading}
			<ProjectCanvas
				{projectName}
				{apps}
				selectedApp={appName}
				onAppOpen={(name) => goto(`/projects/${enc(projectName)}/apps/${enc(name)}`)}
			/>
		{/if}

		<!-- Drawer overlay -->
		{#key appName}
			<AppDrawer
				project={projectName}
				{appName}
				liveApp={apps.find(a => a.metadata.name === appName) ?? null}
				onClose={closeDrawer}
			/>
		{/key}
	</div>
</div>
