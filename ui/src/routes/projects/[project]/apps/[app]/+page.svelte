<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import AppDrawer from '$lib/components/AppDrawer.svelte';
	import ProjectCanvas from '$lib/components/ProjectCanvas.svelte';
	import { LayoutDashboard, List } from 'lucide-svelte';
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
			<div class="flex overflow-hidden rounded-md border border-surface-600 bg-surface-800/90 backdrop-blur-sm">
				<button
					type="button"
					onclick={() => store.setViewMode('canvas')}
					class="px-2 py-1.5 {store.viewMode === 'canvas' ? 'bg-surface-600 text-white' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
					title="Canvas view"
				>
					<LayoutDashboard class="h-4 w-4" />
				</button>
				<button
					type="button"
					onclick={() => store.setViewMode('list')}
					class="px-2 py-1.5 {store.viewMode === 'list' ? 'bg-surface-600 text-white' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
					title="List view"
				>
					<List class="h-4 w-4" />
				</button>
			</div>
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
		<AppDrawer
			project={projectName}
			{appName}
			onClose={closeDrawer}
		/>
	</div>
</div>
