<script lang="ts">
	import { api } from '$lib/api';
	import type { App } from '$lib/types';
	import { Plus, Trash2, Link, X, ChevronDown } from 'lucide-svelte';

	let {
		project,
		app,
		activeEnv,
		onStale
	}: {
		project: string;
		app: App;
		activeEnv: string;
		onStale: () => void;
	} = $props();

	let showAddBinding = $state(false);
	let newBindingRef = $state('');
	let savingBinding = $state(false);
	let bindingError = $state('');
	let allApps = $state<App[]>([]);
	let bindingsOpen = $state(true);

	let pendingBindings = $state<Array<{ref: string}> | null>(null);
	let pendingClearTimer = $state<ReturnType<typeof setTimeout> | null>(null);

	let lastLoadedEnv = $state('');
	let lastLoadedApp = $state('');

	function setPendingBindings(bindings: Array<{ref: string}>) {
		pendingBindings = bindings;
		if (pendingClearTimer) clearTimeout(pendingClearTimer);
		pendingClearTimer = setTimeout(() => { pendingBindings = null; }, 3000);
	}

	$effect(() => {
		void app.metadata.name;
		api.listApps(project).then(a => allApps = a).catch(() => {});
	});

	$effect(() => {
		const env = activeEnv;
		const appName = app.metadata.name;
		if (env === lastLoadedEnv && appName === lastLoadedApp) return;
		lastLoadedEnv = env;
		lastLoadedApp = appName;
		showAddBinding = false;
		newBindingRef = '';
		bindingError = '';
		pendingBindings = null;
	});

	const currentBindings = $derived(
		pendingBindings ?? (app.spec.environments?.find(e => e.name === activeEnv)?.bindings ?? [])
	);
	const bindableApps = $derived(allApps.filter(a =>
		a.metadata.name !== app.metadata.name &&
		!currentBindings.some(b => b.ref === a.metadata.name)
	));

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

	function bindingPreviewVars(boundApp: App): string[] {
		const prefix = boundApp.metadata.name.toUpperCase().replace(/[^A-Z0-9_]/g, '_');
		const vars = [`${prefix}_HOST`, `${prefix}_PORT`];
		if (hasAutoUrl(boundApp.spec.source.image ?? '')) {
			vars.push(`${prefix}_URL`);
		}
		for (const cred of boundApp.spec.credentials ?? []) {
			if (cred.name !== 'host' && cred.name !== 'port') {
				vars.push(`${prefix}_${cred.name.toUpperCase()}`);
			}
		}
		return vars;
	}

	async function addBinding() {
		if (!newBindingRef || !activeEnv) return;
		savingBinding = true;
		bindingError = '';
		const savedRef = newBindingRef;
		const spec = JSON.parse(JSON.stringify(app.spec));
		spec.environments = spec.environments ?? [];
		let envIdx = spec.environments.findIndex((e: { name: string }) => e.name === activeEnv);
		if (envIdx < 0) {
			spec.environments.push({ name: activeEnv });
			envIdx = spec.environments.length - 1;
		}
		spec.environments[envIdx].bindings = [
			...(spec.environments[envIdx].bindings ?? []),
			{ ref: savedRef }
		];
		showAddBinding = false;
		newBindingRef = '';
		try {
			await api.updateApp(project, app.metadata.name, spec);
			setPendingBindings(spec.environments[envIdx].bindings);
			onStale();
		} catch (e) {
			bindingError = e instanceof Error ? e.message : 'Failed to add binding';
			showAddBinding = true;
			newBindingRef = savedRef;
		} finally {
			savingBinding = false;
		}
	}

	async function removeBinding(ref: string) {
		if (!activeEnv) return;
		bindingError = '';
		const spec = JSON.parse(JSON.stringify(app.spec));
		spec.environments = (spec.environments ?? []).map((e: { name: string; bindings?: Array<{ ref: string }> }) =>
			e.name === activeEnv
				? { ...e, bindings: (e.bindings ?? []).filter(b => b.ref !== ref) }
				: e
		);
		try {
			await api.updateApp(project, app.metadata.name, spec);
			const updatedEnv = spec.environments.find((e: { name: string }) => e.name === activeEnv);
			setPendingBindings(updatedEnv?.bindings ?? []);
			onStale();
		} catch (e) {
			bindingError = e instanceof Error ? e.message : 'Failed to remove binding';
		}
	}
</script>

<div class="rounded-lg border border-surface-600 bg-surface-900">
	<div
		role="button"
		tabindex="0"
		onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') bindingsOpen = !bindingsOpen; }}
		onclick={() => bindingsOpen = !bindingsOpen}
		class="flex w-full cursor-pointer items-center justify-between px-3 py-2.5">
		<div class="flex items-center gap-2">
			<span class="text-sm font-medium text-white">Bindings</span>
			{#if currentBindings.length > 0}
				<span class="rounded-full bg-surface-700 px-1.5 py-0.5 text-[10px] font-medium text-gray-400">{currentBindings.length}</span>
			{/if}
		</div>
		<div class="flex items-center gap-2">
			{#if bindingsOpen}
				<button type="button" onclick={(e) => { e.stopPropagation(); showAddBinding = true; }}
					class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
					<Plus class="h-3 w-3" />
				</button>
			{/if}
			<ChevronDown class="h-4 w-4 text-gray-500 transition-transform {bindingsOpen ? 'rotate-180' : ''}" />
		</div>
	</div>

	{#if bindingsOpen}
		<div class="border-t border-surface-600">
			{#if bindingError}
				<div class="px-3 py-2 text-xs text-danger">{bindingError}</div>
			{/if}

			{#if showAddBinding}
				<div class="border-b border-surface-600 bg-surface-700/30 px-3 py-2.5 space-y-2">
					<div class="flex items-center gap-2">
						<select id="binding-ref" bind:value={newBindingRef}
							class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 text-sm text-white outline-none focus:border-accent">
							<option value="">Select an app…</option>
							{#each bindableApps as a}
								<option value={a.metadata.name}>{a.metadata.name}</option>
							{/each}
						</select>
						<button type="button" onclick={addBinding} disabled={!newBindingRef || savingBinding}
							class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
							{savingBinding ? 'Adding…' : 'Add'}
						</button>
						<button type="button" onclick={() => { showAddBinding = false; newBindingRef = ''; }}
							class="rounded p-1.5 text-gray-500 hover:text-white"><X class="h-3.5 w-3.5" /></button>
					</div>
					{#if newBindingRef}
						{@const boundApp = allApps.find(a => a.metadata.name === newBindingRef)}
						{#if boundApp}
							<div class="flex flex-wrap gap-1 text-[11px]">
								<span class="text-gray-500">Injects:</span>
								{#each bindingPreviewVars(boundApp) as v}
									<span class="rounded bg-surface-800 px-1.5 py-0.5 font-mono text-gray-400">{v}</span>
								{/each}
							</div>
						{/if}
					{/if}
				</div>
			{/if}

			{#if currentBindings.length === 0 && !showAddBinding}
				<div class="py-6 text-center text-xs text-gray-500">
					No bindings. Connect to another app to inject its HOST, PORT, and URL.
				</div>
			{:else}
				{#each currentBindings as binding}
					{@const bound = allApps.find(a => a.metadata.name === binding.ref)}
					<div class="group flex items-center justify-between border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
						<div class="flex items-center gap-2">
							<Link class="h-3.5 w-3.5 text-gray-500" />
							<span class="text-sm text-gray-200">{binding.ref}</span>
							{#if bound && hasAutoUrl(bound.spec.source.image ?? '')}
								<span class="rounded-full bg-info/10 px-1.5 py-0.5 text-[10px] font-medium text-info">{imageBaseName(bound.spec.source.image ?? '')}</span>
							{/if}
						</div>
						<button type="button" onclick={() => removeBinding(binding.ref)}
							class="shrink-0 rounded p-1 text-gray-500 hover:text-danger transition-colors">
							<Trash2 class="h-3.5 w-3.5" />
						</button>
					</div>
				{/each}
			{/if}
		</div>
	{/if}
</div>
