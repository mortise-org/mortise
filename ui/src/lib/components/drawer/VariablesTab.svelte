<script lang="ts">
	import { api } from '$lib/api';
	import type { App } from '$lib/types';
	import BindingsPicker from '$lib/components/BindingsPicker.svelte';
	import { Plus, Trash2, Link, Upload, FileText, ChevronDown, X } from 'lucide-svelte';

	let {
		project,
		app,
		onAppUpdated
	}: {
		project: string;
		app: App;
		onAppUpdated: (app: App) => void;
	} = $props();

	// Per-environment section state: vars, loading, saving, error, editedKeys, originalVars
	// Keyed by env name, plus 'shared' for the shared vars section.
	type SectionState = {
		vars: Record<string, string>;
		loading: boolean;
		saving: boolean;
		error: string;
		editedKeys: Set<string>;
		originalVars: Record<string, string>;
		expanded: boolean;
		showNewRow: boolean;
		newKey: string;
		newValue: string;
		showPicker: boolean;
		rawMode: boolean;
		rawText: string;
	};

	let restartTriggered = $state(false);
	let dismissTimer: ReturnType<typeof setTimeout> | null = null;

	function triggerRestartBanner() {
		restartTriggered = true;
		if (dismissTimer) clearTimeout(dismissTimer);
		dismissTimer = setTimeout(() => { restartTriggered = false; }, 4000);
	}

	const envNames = $derived(app.spec.environments?.map(e => e.name) ?? ['production']);

	function makeSection(expanded: boolean): SectionState {
		return {
			vars: {},
			loading: false,
			saving: false,
			error: '',
			editedKeys: new Set(),
			originalVars: {},
			expanded,
			showNewRow: false,
			newKey: '',
			newValue: '',
			showPicker: false,
			rawMode: false,
			rawText: ''
		};
	}

	// Build initial section map: first env expanded, rest collapsed; shared always expanded.
	let sections = $state<Record<string, SectionState>>(
		Object.fromEntries([
			...envNames.map((name, i) => [name, makeSection(i === 0)]),
			['shared', makeSection(true)]
		])
	);

	const shared = $derived(sections['shared']);

	// Load env vars for each env on mount.
	$effect(() => {
		for (const name of envNames) {
			if (!sections[name]) {
				sections[name] = makeSection(false);
			}
			void loadEnv(name);
		}
		loadShared();
	});

	async function loadEnv(envName: string) {
		sections[envName].loading = true;
		sections[envName].error = '';
		try {
			const vars = await api.getEnv(project, app.metadata.name, envName);
			sections[envName].vars = vars;
			sections[envName].originalVars = { ...vars };
			sections[envName].editedKeys = new Set();
		} catch (e) {
			sections[envName].error = e instanceof Error ? e.message : 'Failed to load';
			sections[envName].vars = {};
		} finally {
			sections[envName].loading = false;
		}
	}

	function loadShared() {
		// Read shared vars from app.spec.sharedVars (no API call needed).
		const raw = app.spec.sharedVars ?? [];
		const vars = Object.fromEntries(raw.map(v => [v.name, v.value]));
		if (!sections['shared']) {
			sections['shared'] = makeSection(true);
		}
		sections['shared'].vars = vars;
		sections['shared'].originalVars = { ...vars };
		sections['shared'].editedKeys = new Set();
	}

	// ---- Per-section actions ----

	function toggleExpanded(key: string) {
		sections[key].expanded = !sections[key].expanded;
	}

	function handleValueEdit(key: string, varKey: string, value: string) {
		sections[key].vars = { ...sections[key].vars, [varKey]: value };
		const next = new Set(sections[key].editedKeys);
		if (value !== sections[key].originalVars[varKey]) next.add(varKey);
		else next.delete(varKey);
		sections[key].editedKeys = next;
	}

	async function addVar(key: string) {
		const s = sections[key];
		if (!s.newKey.trim()) return;
		const updated = { ...s.vars, [s.newKey.trim()]: s.newValue };
		const prevVars = { ...s.vars };
		const prevOriginal = { ...s.originalVars };
		const prevKey = s.newKey;
		const prevValue = s.newValue;
		sections[key].vars = updated;
		sections[key].originalVars = { ...updated };
		sections[key].editedKeys = new Set();
		sections[key].newKey = '';
		sections[key].newValue = '';
		sections[key].showNewRow = false;
		sections[key].saving = true;
		try {
			if (key === 'shared') {
				await saveSharedVars(updated);
			} else {
				await api.setEnv(project, app.metadata.name, key, updated);
			}
			triggerRestartBanner();
		} catch (e) {
			sections[key].error = e instanceof Error ? e.message : 'Failed to save';
			sections[key].vars = prevVars;
			sections[key].originalVars = prevOriginal;
			sections[key].newKey = prevKey;
			sections[key].newValue = prevValue;
			sections[key].showNewRow = true;
		} finally {
			sections[key].saving = false;
		}
	}

	async function deleteVar(key: string, varKey: string) {
		const updated = Object.fromEntries(Object.entries(sections[key].vars).filter(([k]) => k !== varKey));
		const prevVars = { ...sections[key].vars };
		const prevOriginal = { ...sections[key].originalVars };
		const prevEdited = new Set(sections[key].editedKeys);
		sections[key].vars = updated;
		sections[key].originalVars = { ...updated };
		const next = new Set(sections[key].editedKeys);
		next.delete(varKey);
		sections[key].editedKeys = next;
		sections[key].saving = true;
		try {
			if (key === 'shared') {
				await saveSharedVars(updated);
			} else {
				await api.setEnv(project, app.metadata.name, key, updated);
			}
			triggerRestartBanner();
		} catch (e) {
			sections[key].error = e instanceof Error ? e.message : 'Failed to delete';
			sections[key].vars = prevVars;
			sections[key].originalVars = prevOriginal;
			sections[key].editedKeys = prevEdited;
		} finally {
			sections[key].saving = false;
		}
	}

	async function saveEdited(key: string) {
		const s = sections[key];
		if (s.editedKeys.size === 0) return;
		const prevOriginal = { ...s.originalVars };
		const prevEdited = new Set(s.editedKeys);
		sections[key].originalVars = { ...s.vars };
		sections[key].editedKeys = new Set();
		sections[key].saving = true;
		try {
			if (key === 'shared') {
				await saveSharedVars(s.vars);
			} else {
				await api.setEnv(project, app.metadata.name, key, s.vars);
			}
			triggerRestartBanner();
		} catch (e) {
			sections[key].error = e instanceof Error ? e.message : 'Failed to save';
			sections[key].originalVars = prevOriginal;
			sections[key].editedKeys = prevEdited;
		} finally {
			sections[key].saving = false;
		}
	}

	async function importRaw(key: string) {
		const s = sections[key];
		const parsed: Record<string, string> = {};
		for (const line of s.rawText.split('\n')) {
			const trimmed = line.trim();
			if (!trimmed || trimmed.startsWith('#')) continue;
			const idx = trimmed.indexOf('=');
			if (idx < 0) continue;
			parsed[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1).trim();
		}
		const merged = { ...s.vars, ...parsed };
		const prevVars = { ...s.vars };
		const prevOriginal = { ...s.originalVars };
		const prevRawText = s.rawText;
		sections[key].vars = merged;
		sections[key].originalVars = { ...merged };
		sections[key].editedKeys = new Set();
		sections[key].rawText = '';
		sections[key].rawMode = false;
		sections[key].saving = true;
		try {
			if (key === 'shared') {
				await saveSharedVars(merged);
			} else {
				await api.setEnv(project, app.metadata.name, key, merged);
			}
			triggerRestartBanner();
		} catch (e) {
			sections[key].error = e instanceof Error ? e.message : 'Import failed';
			sections[key].vars = prevVars;
			sections[key].originalVars = prevOriginal;
			sections[key].rawText = prevRawText;
			sections[key].rawMode = true;
		} finally {
			sections[key].saving = false;
		}
	}

	// Write shared vars via updateApp (spec patch).
	async function saveSharedVars(vars: Record<string, string>) {
		const plainSpec = JSON.parse(JSON.stringify(app.spec));
		plainSpec.sharedVars = Object.entries(vars).map(([name, value]) => ({ name, value }));
		const updated = await api.updateApp(project, app.metadata.name, plainSpec);
		onAppUpdated(updated);
		// Sync local state from updated app.
		sections['shared'].vars = vars;
		sections['shared'].originalVars = { ...vars };
		sections['shared'].editedKeys = new Set();
	}

	function insertRef(key: string, ref: string) {
		sections[key].newValue = sections[key].newValue + ref;
		sections[key].showPicker = false;
	}

	function isSecret(value: string): boolean {
		return value.startsWith('${{secrets.');
	}
</script>

<div class="flex h-full flex-col gap-3 overflow-y-auto p-1">
{#if restartTriggered}
	<div class="flex items-center justify-between rounded-md border border-blue-500/30 bg-blue-500/10 px-3 py-2 text-xs text-blue-300">
		<span>Changes saved - rolling restart in progress</span>
		<button type="button" onclick={() => { restartTriggered = false; if (dismissTimer) clearTimeout(dismissTimer); }} class="ml-2 text-blue-400 hover:text-blue-200">
			<X class="h-3.5 w-3.5" />
		</button>
	</div>
{/if}
	<!-- Environment sections -->
	{#each envNames as envName}
		{@const s = sections[envName] ?? makeSection(false)}
		<div class="rounded-lg border border-surface-600 bg-surface-900">
			<!-- Section header -->
			<div class="flex items-center justify-between px-3 py-2.5">
				<button
					type="button"
					onclick={() => toggleExpanded(envName)}
					class="flex items-center gap-2 text-sm font-medium text-white hover:text-gray-300 flex-1 text-left"
				>
					<ChevronDown class="h-4 w-4 shrink-0 transition-transform {s.expanded ? 'rotate-0' : '-rotate-90'}" />
					{envName}
				</button>
				{#if s.expanded}
					<div class="flex items-center gap-2">
						<!-- Mode toggle -->
						<div class="flex gap-1">
							<button type="button" onclick={() => { sections[envName].rawMode = false; }}
								class="{!s.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
								<FileText class="inline h-3 w-3 mr-1" />Table
							</button>
							<button type="button" onclick={() => { sections[envName].rawMode = true; }}
								class="{s.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
								<Upload class="inline h-3 w-3 mr-1" />Raw
							</button>
						</div>
						{#if s.editedKeys.size > 0 && !s.rawMode}
							<button type="button" onclick={() => saveEdited(envName)} disabled={s.saving}
								class="rounded-md bg-accent px-3 py-1 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
								{s.saving ? 'Saving...' : `Save ${s.editedKeys.size} change${s.editedKeys.size === 1 ? '' : 's'}`}
							</button>
						{/if}
						{#if !s.rawMode}
							<button type="button" onclick={() => { sections[envName].showNewRow = true; }}
								class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
								<Plus class="h-3.5 w-3.5" /> New variable
							</button>
						{/if}
					</div>
				{/if}
			</div>

			{#if s.expanded}
				<div class="border-t border-surface-600">
					{#if s.error}
						<div class="px-3 py-2 text-xs text-danger">{s.error}</div>
					{/if}

					{#if s.loading}
						<div class="flex items-center justify-center py-6">
							<div class="inline-block h-4 w-4 animate-spin rounded-full border-2 border-gray-500 border-t-transparent"></div>
						</div>
					{:else if s.rawMode}
						<!-- Raw / Import mode -->
						<div class="p-3 space-y-3">
							<p class="text-xs text-gray-500">Paste .env format below. Existing vars will be merged.</p>
							<textarea bind:value={sections[envName].rawText} rows={8}
								placeholder="DATABASE_URL=postgres://...&#10;REDIS_URL=redis://..."
								class="w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent">
							</textarea>
							<div class="flex gap-2">
								<button type="button" onclick={() => importRaw(envName)} disabled={!s.rawText.trim() || s.saving}
									class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
									{s.saving ? 'Importing...' : 'Import'}
								</button>
								<button type="button" onclick={() => { sections[envName].rawMode = false; }}
									class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
									Cancel
								</button>
							</div>
						</div>
					{:else}
						<!-- New var row -->
						{#if s.showNewRow}
							<div class="border-b border-surface-600 px-3 py-2.5 space-y-2 bg-surface-700/30">
								<div class="flex gap-2">
									<input type="text" bind:value={sections[envName].newKey} placeholder="VARIABLE_NAME"
										class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-3 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
									<div class="relative flex-1">
										<input type="text" bind:value={sections[envName].newValue} placeholder="value or binding ref"
											class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent pr-8" />
										<button type="button" onclick={() => { sections[envName].showPicker = !sections[envName].showPicker; }}
											class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-500 hover:text-accent" title="Insert reference">
											<Link class="h-3.5 w-3.5" />
										</button>
										{#if s.showPicker}
											<BindingsPicker
												{project}
												{app}
												onInsert={(ref) => insertRef(envName, ref)}
												onClose={() => { sections[envName].showPicker = false; }}
											/>
										{/if}
									</div>
								</div>
								<div class="flex gap-2">
									<button type="button" onclick={() => addVar(envName)} disabled={!s.newKey.trim() || s.saving}
										class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
										{s.saving ? 'Adding...' : 'Add'}
									</button>
									<button type="button" onclick={() => { sections[envName].showNewRow = false; sections[envName].newKey = ''; sections[envName].newValue = ''; }}
										class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
										Cancel
									</button>
								</div>
							</div>
						{/if}

						<!-- Variable rows -->
						{#if Object.keys(s.vars).length === 0 && !s.showNewRow}
							<div class="py-8 text-center text-xs text-gray-500">
								No variables set. Click "New variable" to add one.
							</div>
						{:else}
							{#each Object.entries(s.vars) as [varKey, value]}
								<div class="group flex items-center gap-2 border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
									<div class="flex-1 min-w-0">
										<div class="flex items-center gap-2">
											<span class="font-mono text-sm text-gray-200">{varKey}</span>
											{#if s.editedKeys.has(varKey)}
												<span class="inline-flex items-center rounded-full bg-accent/10 px-1.5 py-0.5 text-xs font-medium text-accent">edited</span>
											{/if}
											{#if isSecret(value)}
												<span class="inline-flex items-center rounded-full bg-surface-700 px-1.5 py-0.5 text-xs text-gray-400">secret ref</span>
											{/if}
										</div>
										<input type="text"
											value={value}
											oninput={(e) => handleValueEdit(envName, varKey, (e.target as HTMLInputElement).value)}
											class="mt-1 w-full rounded border border-transparent bg-transparent px-1 py-0.5 font-mono text-xs text-gray-400 outline-none focus:border-surface-500 focus:bg-surface-700 hover:border-surface-600"
											placeholder="(empty)" />
									</div>
									<button type="button" onclick={() => deleteVar(envName, varKey)}
										class="shrink-0 rounded p-1.5 text-gray-600 opacity-0 group-hover:opacity-100 hover:bg-surface-600 hover:text-danger transition-all">
										<Trash2 class="h-3.5 w-3.5" />
									</button>
								</div>
							{/each}
						{/if}
					{/if}
				</div>
			{/if}
		</div>
	{/each}

	<!-- Shared variables section -->
	<div class="rounded-lg border border-surface-600 bg-surface-900">
		<!-- Section header -->
		<div class="flex items-center justify-between px-3 py-2.5">
			<div class="flex items-center gap-2 flex-1">
				<span class="text-sm font-medium text-white">Shared variables</span>
				<span class="text-xs text-gray-500">- available to all environments of this app</span>
			</div>
			<div class="flex items-center gap-2">
				{#if shared.editedKeys.size > 0 && !shared.rawMode}
					<button type="button" onclick={() => saveEdited('shared')} disabled={shared.saving}
						class="rounded-md bg-accent px-3 py-1 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
						{shared.saving ? 'Saving...' : `Save ${shared.editedKeys.size} change${shared.editedKeys.size === 1 ? '' : 's'}`}
					</button>
				{/if}
				<div class="flex gap-1">
					<button type="button" onclick={() => { sections['shared'].rawMode = false; }}
						class="{!shared.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
						<FileText class="inline h-3 w-3 mr-1" />Table
					</button>
					<button type="button" onclick={() => { sections['shared'].rawMode = true; }}
						class="{shared.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
						<Upload class="inline h-3 w-3 mr-1" />Raw
					</button>
				</div>
				{#if !shared.rawMode}
					<button type="button" onclick={() => { sections['shared'].showNewRow = true; }}
						class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
						<Plus class="h-3.5 w-3.5" /> New shared variable
					</button>
				{/if}
			</div>
		</div>

		<div class="border-t border-surface-600">
			{#if shared.error}
				<div class="px-3 py-2 text-xs text-danger">{shared.error}</div>
			{/if}

			{#if shared.rawMode}
				<div class="p-3 space-y-3">
					<p class="text-xs text-gray-500">Paste .env format below. Existing vars will be merged.</p>
					<textarea bind:value={sections['shared'].rawText} rows={8}
						placeholder="DATABASE_URL=postgres://...&#10;REDIS_URL=redis://..."
						class="w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent">
					</textarea>
					<div class="flex gap-2">
						<button type="button" onclick={() => importRaw('shared')} disabled={!shared.rawText.trim() || shared.saving}
							class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
							{shared.saving ? 'Importing...' : 'Import'}
						</button>
						<button type="button" onclick={() => { sections['shared'].rawMode = false; }}
							class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
							Cancel
						</button>
					</div>
				</div>
			{:else}
				<!-- New var row for shared -->
				{#if shared.showNewRow}
					<div class="border-b border-surface-600 px-3 py-2.5 space-y-2 bg-surface-700/30">
						<div class="flex gap-2">
							<input type="text" bind:value={sections['shared'].newKey} placeholder="VARIABLE_NAME"
								class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-3 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
							<div class="relative flex-1">
								<input type="text" bind:value={sections['shared'].newValue} placeholder="value or binding ref"
									class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent pr-8" />
								<button type="button" onclick={() => { sections['shared'].showPicker = !sections['shared'].showPicker; }}
									class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-500 hover:text-accent" title="Insert reference">
									<Link class="h-3.5 w-3.5" />
								</button>
								{#if shared.showPicker}
									<BindingsPicker
										{project}
										{app}
										onInsert={(ref) => insertRef('shared', ref)}
										onClose={() => { sections['shared'].showPicker = false; }}
									/>
								{/if}
							</div>
						</div>
						<div class="flex gap-2">
							<button type="button" onclick={() => addVar('shared')} disabled={!shared.newKey.trim() || shared.saving}
								class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
								{shared.saving ? 'Adding...' : 'Add'}
							</button>
							<button type="button" onclick={() => { sections['shared'].showNewRow = false; sections['shared'].newKey = ''; sections['shared'].newValue = ''; }}
								class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
								Cancel
							</button>
						</div>
					</div>
				{/if}

				<!-- Shared variable rows -->
				{#if Object.keys(shared.vars).length === 0 && !shared.showNewRow}
					<div class="py-8 text-center text-xs text-gray-500">
						No vars set here yet. Click "New shared variable" to add one.
					</div>
				{:else}
					{#each Object.entries(shared.vars) as [varKey, value]}
						<div class="group flex items-center gap-2 border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
							<div class="flex-1 min-w-0">
								<div class="flex items-center gap-2">
									<span class="font-mono text-sm text-gray-200">{varKey}</span>
									{#if shared.editedKeys.has(varKey)}
										<span class="inline-flex items-center rounded-full bg-accent/10 px-1.5 py-0.5 text-xs font-medium text-accent">edited</span>
									{/if}
									{#if isSecret(value)}
										<span class="inline-flex items-center rounded-full bg-surface-700 px-1.5 py-0.5 text-xs text-gray-400">secret ref</span>
									{/if}
								</div>
								<input type="text"
									value={value}
									oninput={(e) => handleValueEdit('shared', varKey, (e.target as HTMLInputElement).value)}
									class="mt-1 w-full rounded border border-transparent bg-transparent px-1 py-0.5 font-mono text-xs text-gray-400 outline-none focus:border-surface-500 focus:bg-surface-700 hover:border-surface-600"
									placeholder="(empty)" />
							</div>
							<button type="button" onclick={() => deleteVar('shared', varKey)}
								class="shrink-0 rounded p-1.5 text-gray-600 opacity-0 group-hover:opacity-100 hover:bg-surface-600 hover:text-danger transition-all">
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						</div>
					{/each}
				{/if}
			{/if}
		</div>
	</div>
</div>
