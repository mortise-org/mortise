<script lang="ts">
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { App } from '$lib/types';
	import BindingsPicker from '$lib/components/BindingsPicker.svelte';
	import { Plus, Trash2, Link, Upload, FileText, X, Eye, EyeOff } from 'lucide-svelte';

	let {
		project,
		app
	}: {
		project: string;
		app: App;
	} = $props();

	type EnvEntry = {
		name: string;
		value: string;
		source?: string; // "user" | "binding" | "generated" | "shared"
		revealed?: boolean;
	};

	type SectionState = {
		entries: EnvEntry[];
		loading: boolean;
		saving: boolean;
		error: string;
		editedKeys: Set<string>;
		originalEntries: EnvEntry[];
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

	const activeEnv = $derived(
		store.currentEnv(project) || app.spec.environments?.[0]?.name || 'production'
	);

	function makeSection(): SectionState {
		return {
			entries: [],
			loading: false,
			saving: false,
			error: '',
			editedKeys: new Set(),
			originalEntries: [],
			showNewRow: false,
			newKey: '',
			newValue: '',
			showPicker: false,
			rawMode: false,
			rawText: ''
		};
	}

	let envSection = $state<SectionState>(makeSection());
	let sharedSection = $state<SectionState>(makeSection());

	$effect(() => {
		const env = activeEnv;
		if (!env) return;
		void loadEnv(env);
		void loadShared();
	});

	async function loadEnv(envName: string) {
		envSection.loading = true;
		envSection.error = '';
		try {
			const rows = await api.getEnv(project, app.metadata.name, envName);
			// API now returns {name, value, source}
			const entries: EnvEntry[] = Array.isArray(rows)
				? (typeof rows[0] === 'object' && 'source' in (rows[0] ?? {})
					? (rows as Array<{name: string; value: string; source?: string}>).map(r => ({
						name: r.name,
						value: r.value,
						source: r.source ?? 'user',
						revealed: false
					}))
					: Object.entries(rows as Record<string, string>).map(([name, value]) => ({
						name, value, source: 'user', revealed: false
					})))
				: [];
			envSection.entries = entries;
			envSection.originalEntries = entries.map(e => ({ ...e }));
			envSection.editedKeys = new Set();
		} catch (e) {
			envSection.error = e instanceof Error ? e.message : 'Failed to load';
			envSection.entries = [];
		} finally {
			envSection.loading = false;
		}
	}

	async function loadShared() {
		// Read shared vars from app.spec.sharedVars (no dedicated API yet).
		const raw = app.spec.sharedVars ?? [];
		sharedSection.entries = raw.map((v: {name: string; value: string}) => ({
			name: v.name, value: v.value, source: 'shared', revealed: false
		}));
		sharedSection.originalEntries = sharedSection.entries.map(e => ({ ...e }));
		sharedSection.editedKeys = new Set();
	}

	// ---- Actions ----

	function handleValueEdit(section: SectionState, idx: number, value: string) {
		section.entries[idx] = { ...section.entries[idx], value };
		const key = section.entries[idx].name;
		const orig = section.originalEntries.find(e => e.name === key);
		const next = new Set(section.editedKeys);
		if (!orig || value !== orig.value) next.add(key);
		else next.delete(key);
		section.editedKeys = next;
	}

	function handleKeyPaste(section: SectionState, e: ClipboardEvent) {
		const text = e.clipboardData?.getData('text') ?? '';
		// Detect multi-line paste (KEY=VALUE format).
		const lines = text.split('\n').filter(l => l.trim() && !l.trim().startsWith('#') && l.includes('='));
		if (lines.length > 1) {
			e.preventDefault();
			for (const line of lines) {
				const idx = line.indexOf('=');
				if (idx < 1) continue;
				const key = line.slice(0, idx).trim();
				let val = line.slice(idx + 1).trim();
				// Strip quotes
				if (val.length >= 2 && ((val[0] === '"' && val[val.length - 1] === '"') || (val[0] === "'" && val[val.length - 1] === "'"))) {
					val = val.slice(1, -1);
				}
				// Add or update
				const existing = section.entries.findIndex(e => e.name === key);
				if (existing >= 0) {
					section.entries[existing] = { ...section.entries[existing], value: val };
				} else {
					section.entries = [...section.entries, { name: key, value: val, source: 'user', revealed: false }];
				}
				section.editedKeys = new Set([...section.editedKeys, key]);
			}
			section.showNewRow = false;
			section.newKey = '';
			section.newValue = '';
		}
	}

	async function addVar(section: SectionState, isShared: boolean) {
		if (!section.newKey.trim()) return;
		section.entries = [...section.entries, {
			name: section.newKey.trim(),
			value: section.newValue,
			source: isShared ? 'shared' : 'user',
			revealed: false
		}];
		section.showNewRow = false;
		const key = section.newKey.trim();
		section.newKey = '';
		section.newValue = '';

		section.editedKeys = new Set([...section.editedKeys, key]);
		await saveAll(section, isShared);
	}

	async function deleteVar(section: SectionState, idx: number, isShared: boolean) {
		const key = section.entries[idx].name;
		section.entries = section.entries.filter((_, i) => i !== idx);
		const next = new Set(section.editedKeys);
		next.delete(key);
		section.editedKeys = next;
		await saveAll(section, isShared);
	}

	async function saveAll(section: SectionState, isShared: boolean) {
		section.saving = true;
		section.error = '';
		try {
			const vars: Record<string, string> = {};
			for (const e of section.entries) {
				vars[e.name] = e.value;
			}
			if (isShared) {
				const plainSpec = JSON.parse(JSON.stringify(app.spec));
				plainSpec.sharedVars = Object.entries(vars).map(([name, value]) => ({ name, value }));
				await api.updateApp(project, app.metadata.name, plainSpec);
			} else {
				await api.setEnv(project, app.metadata.name, activeEnv, vars);
			}
			section.originalEntries = section.entries.map(e => ({ ...e }));
			section.editedKeys = new Set();
			triggerRestartBanner();
		} catch (e) {
			section.error = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			section.saving = false;
		}
	}

	async function importRaw(section: SectionState, isShared: boolean) {
		const parsed: Record<string, string> = {};
		for (const line of section.rawText.split('\n')) {
			const trimmed = line.trim();
			if (!trimmed || trimmed.startsWith('#')) continue;
			const idx = trimmed.indexOf('=');
			if (idx < 0) continue;
			const key = trimmed.slice(0, idx).trim();
			let val = trimmed.slice(idx + 1).trim();
			if (val.length >= 2 && ((val[0] === '"' && val[val.length - 1] === '"') || (val[0] === "'" && val[val.length - 1] === "'"))) {
				val = val.slice(1, -1);
			}
			parsed[key] = val;
		}

		// Merge into existing
		for (const [key, val] of Object.entries(parsed)) {
			const existing = section.entries.findIndex(e => e.name === key);
			if (existing >= 0) {
				section.entries[existing] = { ...section.entries[existing], value: val };
			} else {
				section.entries = [...section.entries, { name: key, value: val, source: 'user', revealed: false }];
			}
		}
		section.rawMode = false;
		section.rawText = '';
		section.editedKeys = new Set(Object.keys(parsed));
		await saveAll(section, isShared);
	}

	function toggleReveal(section: SectionState, idx: number) {
		section.entries[idx] = { ...section.entries[idx], revealed: !section.entries[idx].revealed };
	}

	function insertRef(section: SectionState, ref: string) {
		section.newValue = section.newValue + ref;
		section.showPicker = false;
	}

	function sourceBadge(source?: string): { label: string; classes: string } | null {
		switch (source) {
			case 'binding': return { label: 'binding', classes: 'bg-info/10 text-info' };
			case 'generated': return { label: 'generated', classes: 'bg-warning/10 text-warning' };
			case 'shared': return { label: 'shared', classes: 'bg-accent/10 text-accent' };
			default: return null;
		}
	}

	function entriesToRaw(entries: EnvEntry[]): string {
		return entries.map(e => `${e.name}=${e.value}`).join('\n');
	}
</script>

<div class="flex h-full flex-col gap-3 overflow-y-auto p-1">
{#if restartTriggered}
	<div class="flex items-center justify-between rounded-md border border-blue-500/30 bg-blue-500/10 px-3 py-2 text-xs text-blue-300">
		<span>Changes saved — rolling restart in progress</span>
		<button type="button" onclick={() => { restartTriggered = false; if (dismissTimer) clearTimeout(dismissTimer); }} class="ml-2 text-blue-400 hover:text-blue-200">
			<X class="h-3.5 w-3.5" />
		</button>
	</div>
{/if}

	<!-- Environment variables section -->
	{#if activeEnv}
		{@const s = envSection}
		<div class="rounded-lg border border-surface-600 bg-surface-900">
			<div class="flex items-center justify-between px-3 py-2.5">
				<span class="text-sm font-medium text-white">{activeEnv}</span>
				<div class="flex items-center gap-2">
					<div class="flex gap-1">
						<button type="button" onclick={() => { envSection.rawMode = false; }}
							class="{!s.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
							<FileText class="inline h-3 w-3 mr-1" />Table
						</button>
						<button type="button" onclick={() => { envSection.rawMode = true; envSection.rawText = entriesToRaw(s.entries); }}
							class="{s.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
							<Upload class="inline h-3 w-3 mr-1" />Raw
						</button>
					</div>
					{#if s.editedKeys.size > 0 && !s.rawMode}
						<button type="button" onclick={() => saveAll(envSection, false)} disabled={s.saving}
							class="rounded-md bg-accent px-3 py-1 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
							{s.saving ? 'Saving...' : `Save ${s.editedKeys.size} change${s.editedKeys.size === 1 ? '' : 's'}`}
						</button>
					{/if}
					{#if !s.rawMode}
						<button type="button" onclick={() => { envSection.showNewRow = true; }}
							class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
							<Plus class="h-3.5 w-3.5" />
						</button>
					{/if}
				</div>
			</div>

			<div class="border-t border-surface-600">
				{#if s.error}
					<div class="px-3 py-2 text-xs text-danger">{s.error}</div>
				{/if}

				{#if s.loading}
					<div class="flex items-center justify-center py-6">
						<div class="inline-block h-4 w-4 animate-spin rounded-full border-2 border-gray-500 border-t-transparent"></div>
					</div>
				{:else if s.rawMode}
					<div class="p-3 space-y-3">
						<p class="text-xs text-gray-500">Edit as .env format. Save replaces all variables.</p>
						<textarea bind:value={envSection.rawText} rows={10}
							placeholder="DATABASE_URL=postgres://...&#10;REDIS_URL=redis://..."
							class="w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent">
						</textarea>
						<div class="flex gap-2">
							<button type="button" onclick={() => importRaw(envSection, false)} disabled={!s.rawText.trim() || s.saving}
								class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
								{s.saving ? 'Saving...' : 'Save'}
							</button>
							<button type="button" onclick={() => { envSection.rawMode = false; }}
								class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
								Cancel
							</button>
						</div>
					</div>
				{:else}
					<!-- New variable row (inline, Vercel-style) -->
					{#if s.showNewRow}
						<div class="flex items-center gap-2 border-b border-surface-600 px-3 py-2 bg-surface-700/30">
							<input type="text" bind:value={envSection.newKey} placeholder="VARIABLE_NAME"
								onpaste={(e) => handleKeyPaste(envSection, e)}
								class="w-[40%] rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
							<div class="relative flex-1">
								<input type="text" bind:value={envSection.newValue} placeholder="value"
									class="w-full rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent pr-8" />
								<button type="button" onclick={() => { envSection.showPicker = !envSection.showPicker; }}
									class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-500 hover:text-accent" title="Insert reference">
									<Link class="h-3.5 w-3.5" />
								</button>
								{#if s.showPicker}
									<BindingsPicker {project} {app}
										onInsert={(ref) => insertRef(envSection, ref)}
										onClose={() => { envSection.showPicker = false; }} />
								{/if}
							</div>
							<button type="button" onclick={() => addVar(envSection, false)} disabled={!s.newKey.trim() || s.saving}
								class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">Add</button>
							<button type="button" onclick={() => { envSection.showNewRow = false; envSection.newKey = ''; envSection.newValue = ''; }}
								class="rounded p-1.5 text-gray-500 hover:text-white"><X class="h-3.5 w-3.5" /></button>
						</div>
					{/if}

					{#if s.entries.length === 0 && !s.showNewRow}
						<div class="py-8 text-center text-xs text-gray-500">
							No variables set. Click + to add one, or paste a .env file.
						</div>
					{:else}
						{#each s.entries as entry, idx}
							<div class="group flex items-center gap-2 border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
								<div class="flex items-center gap-2 w-[40%] min-w-0">
									<span class="font-mono text-sm text-gray-200 truncate">{entry.name}</span>
									{#if sourceBadge(entry.source)}
										<span class="shrink-0 inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium {sourceBadge(entry.source)?.classes}">{sourceBadge(entry.source)?.label}</span>
									{/if}
									{#if s.editedKeys.has(entry.name)}
										<span class="shrink-0 inline-flex items-center rounded-full bg-accent/10 px-1.5 py-0.5 text-[10px] font-medium text-accent">edited</span>
									{/if}
								</div>
								<div class="flex-1 flex items-center gap-1 min-w-0">
									{#if entry.revealed}
										<input type="text" value={entry.value}
											oninput={(e) => handleValueEdit(envSection, idx, (e.target as HTMLInputElement).value)}
											class="w-full rounded border border-transparent bg-transparent px-1 py-0.5 font-mono text-xs text-gray-400 outline-none focus:border-surface-500 focus:bg-surface-700 hover:border-surface-600" />
									{:else}
										<button type="button" onclick={() => toggleReveal(envSection, idx)}
											class="w-full text-left px-1 py-0.5 font-mono text-xs text-gray-500 hover:text-gray-400 truncate">
											{'*'.repeat(Math.min(entry.value.length || 7, 20))}
										</button>
									{/if}
									<button type="button" onclick={() => toggleReveal(envSection, idx)}
										class="shrink-0 p-1 text-gray-600 hover:text-gray-400" title={entry.revealed ? 'Hide' : 'Reveal'}>
										{#if entry.revealed}
											<EyeOff class="h-3.5 w-3.5" />
										{:else}
											<Eye class="h-3.5 w-3.5" />
										{/if}
									</button>
								</div>
								<button type="button" onclick={() => deleteVar(envSection, idx, false)}
									class="shrink-0 rounded p-1 text-gray-600 opacity-0 group-hover:opacity-100 hover:text-danger transition-all">
									<Trash2 class="h-3.5 w-3.5" />
								</button>
							</div>
						{/each}
					{/if}
				{/if}
			</div>
		</div>
	{/if}

	<!-- Shared variables section -->
	<div class="rounded-lg border border-surface-600 bg-surface-900">
		<div class="flex items-center justify-between px-3 py-2.5">
			<div class="flex items-center gap-2">
				<span class="text-sm font-medium text-white">Shared</span>
				<span class="text-xs text-gray-500">all environments</span>
			</div>
			<div class="flex items-center gap-2">
				{#if sharedSection.editedKeys.size > 0 && !sharedSection.rawMode}
					<button type="button" onclick={() => saveAll(sharedSection, true)} disabled={sharedSection.saving}
						class="rounded-md bg-accent px-3 py-1 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
						{sharedSection.saving ? 'Saving...' : `Save ${sharedSection.editedKeys.size}`}
					</button>
				{/if}
				<div class="flex gap-1">
					<button type="button" onclick={() => { sharedSection.rawMode = false; }}
						class="{!sharedSection.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
						<FileText class="inline h-3 w-3 mr-1" />Table
					</button>
					<button type="button" onclick={() => { sharedSection.rawMode = true; sharedSection.rawText = entriesToRaw(sharedSection.entries); }}
						class="{sharedSection.rawMode ? 'text-white bg-surface-700' : 'text-gray-500 hover:text-white'} text-xs px-2 py-1 rounded">
						<Upload class="inline h-3 w-3 mr-1" />Raw
					</button>
				</div>
				{#if !sharedSection.rawMode}
					<button type="button" onclick={() => { sharedSection.showNewRow = true; }}
						class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
						<Plus class="h-3.5 w-3.5" />
					</button>
				{/if}
			</div>
		</div>

		<div class="border-t border-surface-600">
			{#if sharedSection.error}
				<div class="px-3 py-2 text-xs text-danger">{sharedSection.error}</div>
			{/if}

			{#if sharedSection.rawMode}
				<div class="p-3 space-y-3">
					<p class="text-xs text-gray-500">Edit as .env format. Save replaces all shared variables.</p>
					<textarea bind:value={sharedSection.rawText} rows={6}
						placeholder="JWT_SECRET=...&#10;DB_PASSWORD=..."
						class="w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent">
					</textarea>
					<div class="flex gap-2">
						<button type="button" onclick={() => importRaw(sharedSection, true)} disabled={!sharedSection.rawText.trim() || sharedSection.saving}
							class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
							{sharedSection.saving ? 'Saving...' : 'Save'}
						</button>
						<button type="button" onclick={() => { sharedSection.rawMode = false; }}
							class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
							Cancel
						</button>
					</div>
				</div>
			{:else}
				{#if sharedSection.showNewRow}
					<div class="flex items-center gap-2 border-b border-surface-600 px-3 py-2 bg-surface-700/30">
						<input type="text" bind:value={sharedSection.newKey} placeholder="VARIABLE_NAME"
							onpaste={(e) => handleKeyPaste(sharedSection, e)}
							class="w-[40%] rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						<input type="text" bind:value={sharedSection.newValue} placeholder="value"
							class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						<button type="button" onclick={() => addVar(sharedSection, true)} disabled={!sharedSection.newKey.trim() || sharedSection.saving}
							class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">Add</button>
						<button type="button" onclick={() => { sharedSection.showNewRow = false; sharedSection.newKey = ''; sharedSection.newValue = ''; }}
							class="rounded p-1.5 text-gray-500 hover:text-white"><X class="h-3.5 w-3.5" /></button>
					</div>
				{/if}

				{#if sharedSection.entries.length === 0 && !sharedSection.showNewRow}
					<div class="py-6 text-center text-xs text-gray-500">
						No shared variables. These are available to all environments.
					</div>
				{:else}
					{#each sharedSection.entries as entry, idx}
						<div class="group flex items-center gap-2 border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
							<div class="flex items-center gap-2 w-[40%] min-w-0">
								<span class="font-mono text-sm text-gray-200 truncate">{entry.name}</span>
								{#if sourceBadge(entry.source)}
									<span class="shrink-0 inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium {sourceBadge(entry.source)?.classes}">{sourceBadge(entry.source)?.label}</span>
								{/if}
							</div>
							<div class="flex-1 flex items-center gap-1 min-w-0">
								{#if entry.revealed}
									<input type="text" value={entry.value}
										oninput={(e) => handleValueEdit(sharedSection, idx, (e.target as HTMLInputElement).value)}
										class="w-full rounded border border-transparent bg-transparent px-1 py-0.5 font-mono text-xs text-gray-400 outline-none focus:border-surface-500 focus:bg-surface-700 hover:border-surface-600" />
								{:else}
									<button type="button" onclick={() => toggleReveal(sharedSection, idx)}
										class="w-full text-left px-1 py-0.5 font-mono text-xs text-gray-500 hover:text-gray-400 truncate">
										{'*'.repeat(Math.min(entry.value.length || 7, 20))}
									</button>
								{/if}
								<button type="button" onclick={() => toggleReveal(sharedSection, idx)}
									class="shrink-0 p-1 text-gray-600 hover:text-gray-400">
									{#if entry.revealed}
										<EyeOff class="h-3.5 w-3.5" />
									{:else}
										<Eye class="h-3.5 w-3.5" />
									{/if}
								</button>
							</div>
							<button type="button" onclick={() => deleteVar(sharedSection, idx, true)}
								class="shrink-0 rounded p-1 text-gray-600 opacity-0 group-hover:opacity-100 hover:text-danger transition-all">
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						</div>
					{/each}
				{/if}
			{/if}
		</div>
	</div>
</div>
