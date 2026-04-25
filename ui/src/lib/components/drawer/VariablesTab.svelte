<script lang="ts">
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import { appNeedsRedeploy } from '$lib/types';
	import type { App } from '$lib/types';
	import BindingsPicker from '$lib/components/BindingsPicker.svelte';
	import { Plus, Trash2, Link, Upload, FileText, X, Eye, EyeOff, Loader2, ChevronDown } from 'lucide-svelte';

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

	const serverNeedsRedeploy = $derived(appNeedsRedeploy(app));
	let localStale = $state(false);
	$effect(() => {
		if (!serverNeedsRedeploy) localStale = false;
	});
	const needsRedeploy = $derived(localStale || serverNeedsRedeploy);
	let redeploying = $state(false);

	function markStale() {
		localStale = true;
	}

	async function handleRedeploy() {
		redeploying = true;
		try {
			await api.redeploy(project, app.metadata.name, activeEnv);
			localStale = false;
		} finally {
			redeploying = false;
		}
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
	let lastLoadedEnv = $state('');
	let lastLoadedApp = $state('');

	$effect(() => {
		const env = activeEnv;
		const appName = app.metadata.name;
		if (!env) return;
		// Only reload when the env or app actually changes, not on every re-render.
		if (env === lastLoadedEnv && appName === lastLoadedApp) return;
		lastLoadedEnv = env;
		lastLoadedApp = appName;
		// Reset transient UI state when switching apps/envs.
		showAddBinding = false;
		newBindingRef = '';
		bindingError = '';
		showAddCredential = false;
		newCredName = '';
		newCredValue = '';
		credentialError = '';
		pendingBindings = null;
		pendingCredentials = null;
		void loadEnv(env);
		void loadShared();
	});

	async function loadEnv(envName: string) {
		envSection.loading = true;
		envSection.error = '';
		try {
			const rows = await api.getEnv(project, app.metadata.name, envName);
			const entries: EnvEntry[] = (rows ?? []).map(r => ({
				name: r.name,
				value: r.value,
				source: r.source ?? 'user',
				revealed: false
			}));
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
		sharedSection.loading = true;
		sharedSection.error = '';
		try {
			const rows = await api.getSharedVars(project);
			sharedSection.entries = (rows ?? []).map(r => ({
				name: r.name, value: r.value, source: r.source ?? 'shared', revealed: false
			}));
			sharedSection.originalEntries = sharedSection.entries.map(e => ({ ...e }));
			sharedSection.editedKeys = new Set();
		} catch (e) {
			sharedSection.error = e instanceof Error ? e.message : 'Failed to load shared vars';
			sharedSection.entries = [];
		} finally {
			sharedSection.loading = false;
		}
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
		let key = section.newKey.trim();
		// Ensure the key is unique within the section
		const existingKeys = new Set(section.entries.map(e => e.name));
		if (existingKeys.has(key)) {
			let suffix = 2;
			while (existingKeys.has(`${key}_${suffix}`)) suffix++;
			key = `${key}_${suffix}`;
		}
		section.entries = [...section.entries, {
			name: key,
			value: section.newValue,
			source: isShared ? 'shared' : 'user',
			revealed: false
		}];
		section.showNewRow = false;
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
			// Check for duplicate keys
			const seen = new Set<string>();
			const dupes: string[] = [];
			for (const e of section.entries) {
				if (seen.has(e.name)) dupes.push(e.name);
				seen.add(e.name);
			}
			if (dupes.length > 0) {
				section.error = `Duplicate keys: ${[...new Set(dupes)].join(', ')}. Rename or remove duplicates before saving.`;
				section.saving = false;
				return;
			}
			const vars: Record<string, string> = {};
			for (const e of section.entries) {
				vars[e.name] = e.value;
			}
			if (isShared) {
				const entries = Object.entries(vars).map(([name, value]) => ({ name, value }));
				await api.setSharedVars(project, entries);
			} else {
				await api.setEnv(project, app.metadata.name, activeEnv, vars);
			}
			section.originalEntries = section.entries.map(e => ({ ...e }));
			section.editedKeys = new Set();
			markStale();
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

		// Replace all entries with parsed result (raw mode is sole source of truth)
		section.entries = Object.entries(parsed).map(([key, val]) => ({
			name: key,
			value: val,
			source: isShared ? 'shared' : 'user',
			revealed: false
		}));
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
			case 'shared': return { label: 'project', classes: 'bg-accent/10 text-accent' };
			default: return null;
		}
	}

	function entriesToRaw(entries: EnvEntry[]): string {
		return entries.map(e => `${e.name}=${e.value}`).join('\n');
	}

	// --- Bindings ---
	let showAddBinding = $state(false);
	let newBindingRef = $state('');
	let savingBinding = $state(false);
	let bindingError = $state('');
	let allApps = $state<App[]>([]);
	let bindingsOpen = $state(true);
	let credentialsOpen = $state(true);

	let pendingBindings = $state<Array<{ref: string}> | null>(null);
	let pendingCredentials = $state<Array<{name: string; value?: string; valueFrom?: unknown}> | null>(null);
	let pendingClearTimer = $state<ReturnType<typeof setTimeout> | null>(null);

	function setPendingBindings(bindings: Array<{ref: string}>) {
		pendingBindings = bindings;
		if (pendingClearTimer) clearTimeout(pendingClearTimer);
		pendingClearTimer = setTimeout(() => { pendingBindings = null; }, 3000);
	}

	function setPendingCredentials(creds: Array<{name: string; value?: string; valueFrom?: unknown}>) {
		pendingCredentials = creds;
		if (pendingClearTimer) clearTimeout(pendingClearTimer);
		pendingClearTimer = setTimeout(() => { pendingCredentials = null; }, 3000);
	}

	$effect(() => {
		void app.metadata.name;
		api.listApps(project).then(a => allApps = a).catch(() => {});
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
			markStale();
			setTimeout(() => void loadEnv(activeEnv), 1500);
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
			markStale();
			setTimeout(() => void loadEnv(activeEnv), 1500);
		} catch (e) {
			bindingError = e instanceof Error ? e.message : 'Failed to remove binding';
		}
	}

	// --- Credentials ---
	let showAddCredential = $state(false);
	let newCredName = $state('');
	let newCredValue = $state('');
	let savingCredentials = $state(false);
	let credentialError = $state('');
	const currentCredentials = $derived(pendingCredentials ?? (app.spec.credentials ?? []));

	async function addCredential() {
		if (!newCredName.trim()) return;
		savingCredentials = true;
		credentialError = '';
		const spec = JSON.parse(JSON.stringify(app.spec));
		spec.credentials = [
			...(spec.credentials ?? []),
			{ name: newCredName.trim(), ...(newCredValue ? { value: newCredValue } : {}) }
		];
		const savedName = newCredName;
		const savedValue = newCredValue;
		showAddCredential = false;
		newCredName = '';
		newCredValue = '';
		try {
			await api.updateApp(project, app.metadata.name, spec);
			setPendingCredentials(spec.credentials);
		} catch (e) {
			credentialError = e instanceof Error ? e.message : 'Failed to add credential';
			showAddCredential = true;
			newCredName = savedName;
			newCredValue = savedValue;
		} finally {
			savingCredentials = false;
		}
	}

	async function removeCredential(name: string) {
		credentialError = '';
		const spec = JSON.parse(JSON.stringify(app.spec));
		spec.credentials = (spec.credentials ?? []).filter(
			(c: { name: string }) => c.name !== name
		);
		try {
			await api.updateApp(project, app.metadata.name, spec);
			setPendingCredentials(spec.credentials);
		} catch (e) {
			credentialError = e instanceof Error ? e.message : 'Failed to remove credential';
		}
	}
</script>

<div class="flex h-full flex-col gap-3 overflow-y-auto p-1">
{#if needsRedeploy}
	<div class="flex items-center justify-between rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
		<span>Changes saved — redeploy to apply</span>
		<div class="flex items-center gap-1.5">
			<button type="button" onclick={handleRedeploy} disabled={redeploying}
				class="rounded bg-warning/20 px-2 py-0.5 font-medium text-warning hover:bg-warning/30 disabled:opacity-50">
				{#if redeploying}
					<Loader2 class="inline h-3 w-3 animate-spin" />
				{:else}
					Redeploy
				{/if}
			</button>
			<button type="button" onclick={() => { localStale = false; }}
				class="text-warning/60 hover:text-warning">
				<X class="h-3.5 w-3.5" />
			</button>
		</div>
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
								onkeydown={(e) => { if (e.key === 'Enter' && envSection.newKey.trim()) addVar(envSection, false); }}
								class="w-[40%] rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
							<div class="relative flex-1">
								<input type="text" bind:value={envSection.newValue} placeholder="value"
									onkeydown={(e) => { if (e.key === 'Enter' && envSection.newKey.trim()) addVar(envSection, false); }}
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

	<!-- Project variables section -->
	<div class="rounded-lg border border-surface-600 bg-surface-900">
		<div class="flex items-center justify-between px-3 py-2.5">
			<div class="flex items-center gap-2">
				<span class="text-sm font-medium text-white">Project</span>
				<span class="text-xs text-gray-500">all apps &amp; environments</span>
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
					<p class="text-xs text-gray-500">Edit as .env format. Save replaces all project variables.</p>
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
							onkeydown={(e) => { if (e.key === 'Enter' && sharedSection.newKey.trim()) addVar(sharedSection, true); }}
							class="w-[40%] rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						<input type="text" bind:value={sharedSection.newValue} placeholder="value"
							onkeydown={(e) => { if (e.key === 'Enter' && sharedSection.newKey.trim()) addVar(sharedSection, true); }}
							class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						<button type="button" onclick={() => addVar(sharedSection, true)} disabled={!sharedSection.newKey.trim() || sharedSection.saving}
							class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">Add</button>
						<button type="button" onclick={() => { sharedSection.showNewRow = false; sharedSection.newKey = ''; sharedSection.newValue = ''; }}
							class="rounded p-1.5 text-gray-500 hover:text-white"><X class="h-3.5 w-3.5" /></button>
					</div>
				{/if}

				{#if sharedSection.entries.length === 0 && !sharedSection.showNewRow}
					<div class="py-6 text-center text-xs text-gray-500">
						No project variables. These are injected into every app in this project.
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

	<!-- Bindings section -->
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
								class="shrink-0 rounded p-1 text-gray-600 opacity-0 group-hover:opacity-100 hover:text-danger transition-all">
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						</div>
					{/each}
				{/if}
			</div>
		{/if}
	</div>

	<!-- Credentials section (what this app exposes) -->
	<div class="rounded-lg border border-surface-600 bg-surface-900">
		<div
			role="button"
			tabindex="0"
			onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') credentialsOpen = !credentialsOpen; }}
			onclick={() => credentialsOpen = !credentialsOpen}
			class="flex w-full cursor-pointer items-center justify-between px-3 py-2.5">
			<div class="flex items-center gap-2">
				<span class="text-sm font-medium text-white">Exposed Credentials</span>
				{#if currentCredentials.length > 0}
					<span class="rounded-full bg-surface-700 px-1.5 py-0.5 text-[10px] font-medium text-gray-400">{currentCredentials.length}</span>
				{/if}
			</div>
			<div class="flex items-center gap-2">
				{#if credentialsOpen}
					<button type="button" onclick={(e) => { e.stopPropagation(); showAddCredential = true; }}
						class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
						<Plus class="h-3 w-3" />
					</button>
				{/if}
				<ChevronDown class="h-4 w-4 text-gray-500 transition-transform {credentialsOpen ? 'rotate-180' : ''}" />
			</div>
		</div>

		{#if credentialsOpen}
			<div class="border-t border-surface-600">
				{#if credentialError}
					<div class="px-3 py-2 text-xs text-danger">{credentialError}</div>
				{/if}

				{#if showAddCredential}
					<div class="border-b border-surface-600 bg-surface-700/30 px-3 py-2.5">
						<div class="flex items-center gap-2">
							<input id="cred-name" type="text" bind:value={newCredName} placeholder="key"
								class="w-[40%] rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
							<input id="cred-value" type="text" bind:value={newCredValue} placeholder="value (optional)"
								onkeydown={(e) => { if (e.key === 'Enter' && newCredName.trim()) addCredential(); }}
								class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
							<button type="button" onclick={addCredential} disabled={!newCredName.trim() || savingCredentials}
								class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
								{savingCredentials ? 'Adding…' : 'Add'}
							</button>
							<button type="button" onclick={() => { showAddCredential = false; newCredName = ''; newCredValue = ''; }}
								class="rounded p-1.5 text-gray-500 hover:text-white"><X class="h-3.5 w-3.5" /></button>
						</div>
					</div>
				{/if}

				{#if currentCredentials.length === 0 && !showAddCredential}
					<div class="py-6 text-center text-xs text-gray-500">
						No credentials. Declare what this app exposes so other apps can bind to it.
					</div>
				{:else}
					{#each currentCredentials as cred}
						<div class="group flex items-center justify-between border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
							<div class="flex items-center gap-2">
								<span class="font-mono text-sm text-gray-200">{cred.name}</span>
								{#if cred.value}
									<span class="text-[10px] text-gray-500">= ••••••</span>
								{:else if cred.valueFrom}
									<span class="inline-flex items-center rounded-full bg-accent/10 px-1.5 py-0.5 text-[10px] font-medium text-accent">from secret</span>
								{/if}
							</div>
							<button type="button" onclick={() => removeCredential(cred.name)}
								class="shrink-0 rounded p-1 text-gray-600 opacity-0 group-hover:opacity-100 hover:text-danger transition-all">
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						</div>
					{/each}
				{/if}
			</div>
		{/if}
	</div>
</div>
