<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { App } from '$lib/types';
	import { Plus, Trash2 } from 'lucide-svelte';

	let { project, app }: { project: string; app: App } = $props();

	let selectedEnv = $state(app.spec.environments?.[0]?.name ?? 'production');
	let envVars = $state<Record<string, string>>({});
	let loading = $state(true);
	let saving = $state(false);
	let errorMsg = $state('');
	let newKey = $state('');
	let newValue = $state('');
	let showNewRow = $state(false);
	let rawMode = $state(false);
	let rawText = $state('');

	async function loadEnv() {
		loading = true;
		errorMsg = '';
		try {
			envVars = await api.getEnv(project, app.metadata.name, selectedEnv);
		} catch {
			envVars = {};
		} finally {
			loading = false;
		}
	}

	onMount(loadEnv);

	$effect(() => {
		void selectedEnv;
		if (!loading) void loadEnv();
	});

	function isSecret(value: string): boolean {
		return value.startsWith('${{secrets') || value.startsWith('${secrets');
	}

	function sourceBadge(value: string): string {
		if (isSecret(value)) return 'secret';
		if (value.startsWith('${{bindings') || value.startsWith('${bindings')) return 'binding';
		return 'literal';
	}

	const badgeClass: Record<string, string> = {
		literal: 'bg-surface-700 text-gray-400',
		secret: 'bg-warning/10 text-warning',
		binding: 'bg-info/10 text-info'
	};

	async function deleteVar(key: string) {
		const updated = { ...envVars };
		delete updated[key];
		saving = true;
		errorMsg = '';
		try {
			await api.setEnv(project, app.metadata.name, selectedEnv, updated);
			envVars = updated;
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to delete variable';
		} finally {
			saving = false;
		}
	}

	async function addVar() {
		if (!newKey.trim()) return;
		const updated = { ...envVars, [newKey.trim()]: newValue };
		saving = true;
		errorMsg = '';
		try {
			await api.setEnv(project, app.metadata.name, selectedEnv, updated);
			envVars = updated;
			newKey = '';
			newValue = '';
			showNewRow = false;
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to add variable';
		} finally {
			saving = false;
		}
	}

	async function saveRaw() {
		// parse key=value lines
		const parsed: Record<string, string> = {};
		for (const line of rawText.split('\n')) {
			const trimmed = line.trim();
			if (!trimmed || trimmed.startsWith('#')) continue;
			const eq = trimmed.indexOf('=');
			if (eq < 1) continue;
			const k = trimmed.slice(0, eq).trim();
			const v = trimmed.slice(eq + 1).trim();
			if (k) parsed[k] = v;
		}
		saving = true;
		errorMsg = '';
		try {
			await api.setEnv(project, app.metadata.name, selectedEnv, parsed);
			envVars = parsed;
			rawMode = false;
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to import variables';
		} finally {
			saving = false;
		}
	}

	function enterRawMode() {
		rawText = Object.entries(envVars)
			.map(([k, v]) => `${k}=${v}`)
			.join('\n');
		rawMode = true;
	}

	const varEntries = $derived(Object.entries(envVars));
</script>

<div class="space-y-4">
	{#if errorMsg}
		<div class="rounded-md bg-danger/10 px-3 py-2 text-xs text-danger">{errorMsg}</div>
	{/if}

	<!-- Environment tabs -->
	{#if app.spec.environments && app.spec.environments.length > 1}
		<div class="flex gap-1 border-b border-surface-600">
			{#each app.spec.environments as env}
				<button
					type="button"
					onclick={() => (selectedEnv = env.name)}
					class="px-3 py-1.5 text-xs transition-colors {selectedEnv === env.name
						? 'border-b-2 border-accent text-white'
						: 'text-gray-400 hover:text-white'}"
				>
					{env.name}
				</button>
			{/each}
		</div>
	{/if}

	{#if loading}
		<div class="space-y-2 animate-pulse">
			{#each Array(3) as _}
				<div class="h-9 rounded bg-surface-700"></div>
			{/each}
		</div>
	{:else if rawMode}
		<!-- Raw import mode -->
		<div class="space-y-2">
			<textarea
				bind:value={rawText}
				rows={12}
				placeholder="KEY=value&#10;ANOTHER_KEY=another_value"
				class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 font-mono text-xs text-white placeholder-gray-600 outline-none focus:border-accent resize-none"
			></textarea>
			<div class="flex justify-end gap-2">
				<button
					type="button"
					onclick={() => (rawMode = false)}
					class="rounded-md border border-surface-600 px-3 py-1.5 text-xs text-gray-400 hover:text-white"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={saveRaw}
					disabled={saving}
					class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50"
				>
					{saving ? 'Saving…' : 'Save'}
				</button>
			</div>
		</div>
	{:else}
		<!-- Variable table -->
		{#if varEntries.length === 0 && !showNewRow}
			<div class="rounded-lg border border-dashed border-surface-600 p-8 text-center">
				<p class="text-sm text-gray-500">No variables yet</p>
			</div>
		{:else}
			<div class="overflow-hidden rounded-md border border-surface-600">
				{#each varEntries as [key, value]}
					<div class="flex items-center border-b border-surface-600 last:border-b-0">
						<div class="flex-1 overflow-hidden px-3 py-2">
							<p class="font-mono text-xs text-white">{key}</p>
							<p class="mt-0.5 truncate font-mono text-xs text-gray-500">
								{isSecret(value) ? '••••••••' : value || '(empty)'}
							</p>
						</div>
						<div class="flex items-center gap-2 px-3">
							<span class="rounded px-1.5 py-0.5 text-xs {badgeClass[sourceBadge(value)] ?? badgeClass.literal}">
								{sourceBadge(value)}
							</span>
							<button
								type="button"
								onclick={() => deleteVar(key)}
								disabled={saving}
								class="text-gray-600 transition-colors hover:text-danger disabled:opacity-40"
								aria-label="Delete {key}"
							>
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						</div>
					</div>
				{/each}

				<!-- New variable inline row -->
				{#if showNewRow}
					<div class="flex items-center gap-2 border-t border-surface-600 px-3 py-2">
						<input
							type="text"
							bind:value={newKey}
							placeholder="KEY"
							class="w-32 rounded border border-surface-600 bg-surface-700 px-2 py-1 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
						<input
							type="text"
							bind:value={newValue}
							placeholder="value"
							class="flex-1 rounded border border-surface-600 bg-surface-700 px-2 py-1 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
						<button
							type="button"
							onclick={addVar}
							disabled={saving || !newKey.trim()}
							class="rounded bg-accent px-2 py-1 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50"
						>
							Add
						</button>
						<button
							type="button"
							onclick={() => { showNewRow = false; newKey = ''; newValue = ''; }}
							class="text-xs text-gray-500 hover:text-white"
						>
							Cancel
						</button>
					</div>
				{/if}
			</div>
		{/if}

		<!-- Action row -->
		<div class="flex items-center justify-between">
			<div class="flex gap-2">
				<button
					type="button"
					onclick={() => (showNewRow = true)}
					class="flex items-center gap-1 rounded-md border border-surface-600 px-2.5 py-1.5 text-xs text-gray-400 transition-colors hover:border-accent hover:text-white"
				>
					<Plus class="h-3 w-3" /> New variable
				</button>
				<button
					type="button"
					onclick={enterRawMode}
					class="rounded-md border border-surface-600 px-2.5 py-1.5 text-xs text-gray-400 transition-colors hover:text-white"
				>
					Raw / Import
				</button>
			</div>
		</div>
	{/if}
</div>
