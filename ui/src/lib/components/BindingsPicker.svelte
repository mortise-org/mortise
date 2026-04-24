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
	let sharedVars = $state<Array<{ name: string; value: string; source?: string }>>([]);
	let loading = $state(true);

	onMount(async () => {
		try {
			[allApps, secrets, sharedVars] = await Promise.all([
				api.listApps(project),
				api.listSecrets(project, app.metadata.name),
				api.getSharedVars(project).catch(() => [])
			]);
		} finally {
			loading = false;
		}
	});

	function imageBaseName(image: string): string {
		let img = image.toLowerCase();
		const slash = img.lastIndexOf('/');
		if (slash >= 0) img = img.slice(slash + 1);
		const colon = img.indexOf(':');
		if (colon >= 0) img = img.slice(0, colon);
		return img;
	}

	function hasAutoUrl(image: string): boolean {
		return ['postgres', 'redis', 'mysql', 'mariadb', 'mongo'].includes(imageBaseName(image));
	}

	// All other apps in the project are bindable
	const bindingApps = $derived(
		allApps.filter(a => a.metadata.name !== app.metadata.name)
	);

	// Build binding rows: auto-generated HOST/PORT/URL + declared credentials
	const bindingRows = $derived(
		bindingApps.flatMap(a => {
			const prefix = a.metadata.name.toUpperCase().replace(/[^A-Z0-9_]/g, '_');
			const rows = [
				{ appName: a.metadata.name, key: 'HOST', ref: `\${{bindings.${a.metadata.name}.host}}` },
				{ appName: a.metadata.name, key: 'PORT', ref: `\${{bindings.${a.metadata.name}.port}}` }
			];
			if (hasAutoUrl(a.spec.source.image ?? '')) {
				rows.push({ appName: a.metadata.name, key: 'URL', ref: `\${{bindings.${a.metadata.name}.url}}` });
			}
			for (const c of a.spec.credentials ?? []) {
				if (c.name !== 'host' && c.name !== 'port') {
					rows.push({ appName: a.metadata.name, key: c.name.toUpperCase(), ref: `\${{bindings.${a.metadata.name}.${c.name}}}` });
				}
			}
			return rows;
		}).filter(r => !filterText || `${r.appName} ${r.key}`.toLowerCase().includes(filterText.toLowerCase()))
	);

	const secretRows = $derived(
		secrets.flatMap(s => s.keys.map(k => ({
			name: s.name,
			key: k,
			ref: `\${{secrets.${s.name}}}`
		}))).filter(r => !filterText || r.key.toLowerCase().includes(filterText.toLowerCase()))
	);

	const sharedRows = $derived(
		sharedVars.map(v => ({
			key: v.name,
			ref: `\${{shared.${v.name}}}`
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
