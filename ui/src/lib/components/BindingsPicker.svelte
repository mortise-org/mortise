<script lang="ts">
	import type { App, SecretResponse } from '$lib/types';
	import { api } from '$lib/api';
	import { Search } from 'lucide-svelte';
	import { onMount } from 'svelte';

	let { project, app, onInsert, onClose }: {
		project: string;
		app: App;
		onInsert: (ref: string) => void;
		onClose: () => void;
	} = $props();

	let filterText = $state('');
	let allApps = $state<App[]>([]);
	let secrets = $state<SecretResponse[]>([]);
	let sharedVars = $state<Record<string, string>>({});
	let loading = $state(true);

	onMount(async () => {
		try {
			[allApps, secrets, sharedVars] = await Promise.all([
				api.listApps(project),
				api.listSecrets(project, app.metadata.name),
				api.getSharedVars(project, app.metadata.name).catch(() => ({}))
			]);
		} finally {
			loading = false;
		}
	});

	// Apps with credentials (binding sources)
	const bindingApps = $derived(
		allApps.filter(a => a.spec.credentials && a.spec.credentials.length > 0)
	);

	// Flatten bindings: { appName, key, ref }
	const bindingRows = $derived(
		bindingApps.flatMap(a =>
			(a.spec.credentials ?? []).map(c => ({
				appName: a.metadata.name,
				key: c.name,
				ref: `\${{bindings.${a.metadata.name}.${c.name}}}`
			}))
		).filter(r => !filterText || r.key.toLowerCase().includes(filterText.toLowerCase()) || r.appName.toLowerCase().includes(filterText.toLowerCase()))
	);

	const secretRows = $derived(
		secrets.flatMap(s => s.keys.map(k => ({
			name: s.name,
			key: k,
			ref: `\${{secrets.${s.name}}}`
		}))).filter(r => !filterText || r.key.toLowerCase().includes(filterText.toLowerCase()))
	);

	const sharedRows = $derived(
		Object.keys(sharedVars).map(k => ({
			key: k,
			ref: `\${{shared.${k}}}`
		})).filter(r => !filterText || r.key.toLowerCase().includes(filterText.toLowerCase()))
	);
</script>

<!-- Backdrop -->
<div class="fixed inset-0 z-30" onclick={onClose} role="presentation"></div>

<!-- Picker panel -->
<div class="absolute left-0 top-full z-40 mt-1 max-h-96 overflow-hidden rounded-md border border-surface-600 bg-surface-800 shadow-xl flex flex-col" style="width:360px">
	<!-- Filter input -->
	<div class="border-b border-surface-600 p-2">
		<div class="flex items-center gap-2 rounded-md border border-surface-600 bg-surface-700 px-2 py-1.5">
			<Search class="h-3.5 w-3.5 text-gray-500 shrink-0" />
			<input type="text" bind:value={filterText} placeholder="Search bindings, secrets, shared..."
				class="flex-1 bg-transparent text-sm text-white placeholder-gray-500 outline-none" />
		</div>
	</div>

	<div class="overflow-y-auto flex-1">
		{#if loading}
			<div class="px-3 py-4 text-xs text-gray-500">Loading...</div>
		{:else}
			<!-- Bindings section -->
			{#if bindingRows.length > 0}
				<div>
					<div class="px-3 py-1.5 text-xs font-medium text-gray-500 uppercase tracking-wide bg-surface-700/50">Bindings</div>
					{#each bindingRows.slice(0, 8) as row}
						<button type="button" onclick={() => onInsert(row.ref)}
							class="flex w-full items-center justify-between px-3 py-2 text-sm hover:bg-surface-700 transition-colors">
							<span class="font-mono text-gray-200">{row.key}</span>
							<span class="text-xs text-gray-500">{row.appName}</span>
						</button>
					{/each}
					{#if bindingRows.length > 8}
						<button class="w-full px-3 py-1.5 text-xs text-accent hover:bg-surface-700 text-left">
							Show {bindingRows.length - 8} more
						</button>
					{/if}
				</div>
			{/if}

			<!-- Secrets section -->
			{#if secretRows.length > 0}
				<div class="border-t border-surface-600">
					<div class="px-3 py-1.5 text-xs font-medium text-gray-500 uppercase tracking-wide bg-surface-700/50">Secrets</div>
					{#each secretRows.slice(0, 8) as row}
						<button type="button" onclick={() => onInsert(row.ref)}
							class="flex w-full items-center justify-between px-3 py-2 text-sm hover:bg-surface-700 transition-colors">
							<span class="font-mono text-gray-200">{row.key}</span>
							<span class="text-xs text-gray-500">{row.name}</span>
						</button>
					{/each}
				</div>
			{/if}

			<!-- Shared vars section -->
			{#if sharedRows.length > 0}
				<div class="border-t border-surface-600">
					<div class="px-3 py-1.5 text-xs font-medium text-gray-500 uppercase tracking-wide bg-surface-700/50">Shared Variables</div>
					{#each sharedRows.slice(0, 8) as row}
						<button type="button" onclick={() => onInsert(row.ref)}
							class="flex w-full items-center justify-between px-3 py-2 text-sm hover:bg-surface-700 transition-colors">
							<span class="font-mono text-gray-200">{row.key}</span>
							<span class="text-xs text-gray-400">shared</span>
						</button>
					{/each}
				</div>
			{/if}

			{#if bindingRows.length === 0 && secretRows.length === 0 && sharedRows.length === 0}
				<div class="px-3 py-6 text-center text-xs text-gray-500">
					{filterText ? 'No matches' : 'No bindings, secrets, or shared variables defined'}
				</div>
			{/if}
		{/if}
	</div>
</div>
