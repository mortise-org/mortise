<script lang="ts">
	import type { App, SecretResponse } from '$lib/types';
	import { api } from '$lib/api';
	import { Search } from 'lucide-svelte';
	import { onMount } from 'svelte';

	let { project, app, activeEnv, onBindingSelect, onSecretSelect, onClose }: {
		project: string;
		app: App;
		activeEnv: string;
		onBindingSelect: (ref: string, key: string) => void;
		onSecretSelect: (secretName: string) => void;
		onClose: () => void;
	} = $props();

	let filterText = $state('');
	let allApps = $state<App[]>([]);
	let secrets = $state<SecretResponse[]>([]);
	let loading = $state(true);

	// Which bindings are declared on this app's current environment
	const currentBindings = $derived(
		app.spec.environments?.find(e => e.name === activeEnv)?.bindings ?? []
	);
	const boundAppNames = $derived(new Set(currentBindings.map(b => b.ref)));

	onMount(async () => {
		try {
			[allApps, secrets] = await Promise.all([
				api.listApps(project),
				api.listSecrets(project, app.metadata.name)
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

	// Only show apps that are in the current env's bindings list
	const bindingApps = $derived(
		allApps.filter(a => boundAppNames.has(a.metadata.name))
	);

	// Build binding rows from bound apps' credentials + auto-keys
	const bindingRows = $derived(
		bindingApps.flatMap(a => {
			const rows: Array<{appName: string; key: string; display: string}> = [
				{ appName: a.metadata.name, key: 'host', display: 'HOST' },
				{ appName: a.metadata.name, key: 'port', display: 'PORT' }
			];
			if (hasAutoUrl(a.spec.source.image ?? '')) {
				rows.push({ appName: a.metadata.name, key: 'url', display: 'URL' });
			}
			for (const c of a.spec.credentials ?? []) {
				if (c.name !== 'host' && c.name !== 'port') {
					rows.push({ appName: a.metadata.name, key: c.name, display: c.name.toUpperCase() });
				}
			}
			return rows;
		}).filter(r => !filterText || `${r.appName} ${r.display}`.toLowerCase().includes(filterText.toLowerCase()))
	);

	const secretRows = $derived(
		secrets.flatMap(s => s.keys.map(k => ({
			name: s.name,
			key: k
		}))).filter(r => !filterText || r.key.toLowerCase().includes(filterText.toLowerCase()))
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
			<input type="text" bind:value={filterText} placeholder="Search bindings, secrets..."
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
						<button type="button" onclick={() => { onBindingSelect(row.appName, row.key); onClose(); }}
							class="flex w-full items-center justify-between px-3 py-2 text-sm hover:bg-surface-700 transition-colors">
							<span class="font-mono text-gray-200">{row.display}</span>
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
						<button type="button" onclick={() => { onSecretSelect(row.name); onClose(); }}
							class="flex w-full items-center justify-between px-3 py-2 text-sm hover:bg-surface-700 transition-colors">
							<span class="font-mono text-gray-200">{row.key}</span>
							<span class="text-xs text-gray-500">{row.name}</span>
						</button>
					{/each}
				</div>
			{/if}

			{#if bindingRows.length === 0 && secretRows.length === 0}
				<div class="px-3 py-6 text-center text-xs text-gray-500">
					{filterText ? 'No matches' : 'No bindings or secrets available. Add a binding first.'}
				</div>
			{/if}
		{/if}
	</div>
</div>
