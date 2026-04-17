<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { App } from '$lib/types';
	import BindingsPicker from '$lib/components/BindingsPicker.svelte';
	import { Plus, Trash2, Link, Upload, FileText } from 'lucide-svelte';

	let { project, app }: { project: string; app: App } = $props();

	const envNames = $derived(app.spec.environments?.map(e => e.name) ?? ['production']);
	let selectedEnv = $state<string | 'shared'>(app.spec.environments?.[0]?.name ?? 'production');
	let vars = $state<Record<string, string>>({});
	let sharedVars = $state<Record<string, string>>({});
	let loading = $state(true);
	let saving = $state(false);
	let error = $state('');

	// New var row
	let showNewRow = $state(false);
	let newKey = $state('');
	let newValue = $state('');
	let showPicker = $state(false);

	// Raw/import mode
	let rawMode = $state(false);
	let rawText = $state('');

	// Track which keys have unsaved edits (for purple chips)
	let editedKeys = $state<Set<string>>(new Set());
	let originalVars = $state<Record<string, string>>({});

	onMount(() => void load());

	$effect(() => {
		void selectedEnv;
		void load();
	});

	async function load() {
		loading = true; error = '';
		try {
			if (selectedEnv === 'shared') {
				sharedVars = await api.getSharedVars(project, app.metadata.name).catch(() => ({}));
				vars = { ...sharedVars };
			} else {
				vars = await api.getEnv(project, app.metadata.name, selectedEnv as string);
			}
			originalVars = { ...vars };
			editedKeys = new Set();
		} catch(e) {
			error = e instanceof Error ? e.message : 'Failed to load';
			vars = {};
		} finally {
			loading = false;
		}
	}

	async function addVar() {
		if (!newKey.trim()) return;
		const key = newKey.trim();
		saving = true;
		try {
			const updated = { ...vars, [key]: newValue };
			if (selectedEnv === 'shared') {
				await api.setSharedVars(project, app.metadata.name, updated);
				sharedVars = updated;
			} else {
				await api.setEnv(project, app.metadata.name, selectedEnv as string, updated);
			}
			vars = updated;
			originalVars = { ...updated };
			editedKeys = new Set();
			newKey = ''; newValue = ''; showNewRow = false;
		} catch(e) {
			error = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			saving = false;
		}
	}

	async function deleteVar(key: string) {
		const updated = Object.fromEntries(Object.entries(vars).filter(([k]) => k !== key));
		saving = true;
		try {
			if (selectedEnv === 'shared') {
				await api.setSharedVars(project, app.metadata.name, updated);
				sharedVars = updated;
			} else {
				await api.setEnv(project, app.metadata.name, selectedEnv as string, updated);
			}
			vars = updated;
			originalVars = { ...updated };
			const next = new Set(editedKeys);
			next.delete(key);
			editedKeys = next;
		} catch(e) {
			error = e instanceof Error ? e.message : 'Failed to delete';
		} finally {
			saving = false;
		}
	}

	function handleValueEdit(key: string, value: string) {
		vars = { ...vars, [key]: value };
		const next = new Set(editedKeys);
		if (value !== originalVars[key]) next.add(key);
		else next.delete(key);
		editedKeys = next;
	}

	async function saveEdited() {
		if (editedKeys.size === 0) return;
		saving = true;
		try {
			if (selectedEnv === 'shared') {
				await api.setSharedVars(project, app.metadata.name, vars);
			} else {
				await api.setEnv(project, app.metadata.name, selectedEnv as string, vars);
			}
			originalVars = { ...vars };
			editedKeys = new Set();
		} catch(e) {
			error = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			saving = false;
		}
	}

	async function importRaw() {
		const parsed: Record<string, string> = {};
		for (const line of rawText.split('\n')) {
			const trimmed = line.trim();
			if (!trimmed || trimmed.startsWith('#')) continue;
			const idx = trimmed.indexOf('=');
			if (idx < 0) continue;
			parsed[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1).trim();
		}
		const merged = { ...vars, ...parsed };
		saving = true;
		try {
			if (selectedEnv === 'shared') {
				await api.setSharedVars(project, app.metadata.name, merged);
			} else {
				await api.setEnv(project, app.metadata.name, selectedEnv as string, merged);
			}
			vars = merged;
			originalVars = { ...merged };
			editedKeys = new Set();
			rawText = ''; rawMode = false;
		} catch(e) {
			error = e instanceof Error ? e.message : 'Import failed';
		} finally {
			saving = false;
		}
	}

	function insertRef(ref: string) {
		newValue = newValue + ref;
		showPicker = false;
	}

	function isSecret(value: string): boolean {
		return value.startsWith('${{secrets.');
	}
</script>

<div class="flex h-full flex-col">
	<!-- Env tabs + Shared -->
	<div class="flex gap-1 border-b border-surface-600 px-1 py-2 flex-wrap">
		{#each envNames as env}
			<button type="button" onclick={() => selectedEnv = env}
				class="{selectedEnv === env ? 'rounded px-2.5 py-1 text-xs bg-surface-600 text-white' : 'rounded px-2.5 py-1 text-xs text-gray-400 hover:text-white'}">
				{env}
			</button>
		{/each}
		<button type="button" onclick={() => selectedEnv = 'shared'}
			class="{selectedEnv === 'shared' ? 'rounded px-2.5 py-1 text-xs bg-surface-600 text-white' : 'rounded px-2.5 py-1 text-xs text-gray-400 hover:text-white'}">
			Shared
		</button>
	</div>

	<!-- Mode toggle -->
	<div class="flex items-center justify-between border-b border-surface-600 px-3 py-2">
		<div class="flex gap-1">
			<button type="button" onclick={() => rawMode = false}
				class="{!rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
				<FileText class="inline h-3 w-3 mr-1" />Table
			</button>
			<button type="button" onclick={() => rawMode = true}
				class="{rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
				<Upload class="inline h-3 w-3 mr-1" />Raw / Import
			</button>
		</div>
		{#if editedKeys.size > 0 && !rawMode}
			<button type="button" onclick={saveEdited} disabled={saving}
				class="rounded-md bg-accent px-3 py-1 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
				{saving ? 'Saving...' : `Save ${editedKeys.size} change${editedKeys.size === 1 ? '' : 's'}`}
			</button>
		{/if}
	</div>

	{#if error}
		<div class="px-3 py-2 text-xs text-danger">{error}</div>
	{/if}

	<div class="flex-1 overflow-y-auto">
		{#if loading}
			<div class="flex items-center justify-center py-8">
				<div class="inline-block h-4 w-4 animate-spin rounded-full border-2 border-gray-500 border-t-transparent"></div>
			</div>
		{:else if rawMode}
			<!-- Raw / Import mode -->
			<div class="p-3 space-y-3">
				<p class="text-xs text-gray-500">
					Paste .env format below. Existing vars will be merged (not replaced).
				</p>
				<textarea bind:value={rawText} rows={12} placeholder="DATABASE_URL=postgres://...&#10;REDIS_URL=redis://..."
					class="w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent">
				</textarea>
				<div class="flex gap-2">
					<button type="button" onclick={importRaw} disabled={!rawText.trim() || saving}
						class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
						{saving ? 'Importing...' : 'Import'}
					</button>
					<button type="button" onclick={() => rawMode = false}
						class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
						Cancel
					</button>
				</div>
			</div>
		{:else}
			<!-- Table mode -->

			<!-- New var row -->
			{#if !showNewRow}
				<button type="button" onclick={() => showNewRow = true}
					class="flex w-full items-center gap-2 border-b border-surface-600 px-3 py-2.5 text-sm text-gray-500 hover:bg-surface-700 hover:text-white transition-colors">
					<Plus class="h-4 w-4" /> New variable
				</button>
			{:else}
				<div class="border-b border-surface-600 px-3 py-2.5 space-y-2 bg-surface-700/30">
					<div class="flex gap-2">
						<input type="text" bind:value={newKey} placeholder="VARIABLE_NAME"
							class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-3 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						<div class="relative flex-1">
							<input type="text" bind:value={newValue} placeholder="value or binding ref"
								class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent pr-8" />
							<button type="button" onclick={() => showPicker = !showPicker}
								class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-500 hover:text-accent" title="Insert reference">
								<Link class="h-3.5 w-3.5" />
							</button>
							{#if showPicker}
								<BindingsPicker
									{project}
									{app}
									onInsert={insertRef}
									onClose={() => showPicker = false}
								/>
							{/if}
						</div>
					</div>
					<div class="flex gap-2">
						<button type="button" onclick={addVar} disabled={!newKey.trim() || saving}
							class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
							{saving ? 'Adding...' : 'Add'}
						</button>
						<button type="button" onclick={() => { showNewRow = false; newKey = ''; newValue = ''; }}
							class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
							Cancel
						</button>
					</div>
				</div>
			{/if}

			<!-- Variables list -->
			{#if Object.keys(vars).length === 0}
				<div class="py-12 text-center text-xs text-gray-500">
					No variables set. Click "New variable" to add one.
				</div>
			{:else}
				{#each Object.entries(vars) as [key, value]}
					<div class="group flex items-center gap-2 border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
						<div class="flex-1 min-w-0">
							<div class="flex items-center gap-2">
								<span class="font-mono text-sm text-gray-200">{key}</span>
								{#if editedKeys.has(key)}
									<span class="inline-flex items-center rounded-full bg-accent/10 px-1.5 py-0.5 text-xs font-medium text-accent">edited</span>
								{/if}
								{#if isSecret(value)}
									<span class="inline-flex items-center rounded-full bg-surface-700 px-1.5 py-0.5 text-xs text-gray-400">secret ref</span>
								{/if}
							</div>
							<input type="text"
								value={value}
								oninput={(e) => handleValueEdit(key, (e.target as HTMLInputElement).value)}
								class="mt-1 w-full rounded border border-transparent bg-transparent px-1 py-0.5 font-mono text-xs text-gray-400 outline-none focus:border-surface-500 focus:bg-surface-700 hover:border-surface-600"
								placeholder="(empty)" />
						</div>
						<button type="button" onclick={() => deleteVar(key)}
							class="shrink-0 rounded p-1.5 text-gray-600 opacity-0 group-hover:opacity-100 hover:bg-surface-600 hover:text-danger transition-all">
							<Trash2 class="h-3.5 w-3.5" />
						</button>
					</div>
				{/each}
			{/if}
		{/if}
	</div>
</div>
