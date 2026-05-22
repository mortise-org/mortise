<script lang="ts">
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import { appNeedsRedeploy, resolveAppEnvironment } from '$lib/types';
	import type { App, EnvVar } from '$lib/types';
	import { Loader2, X } from 'lucide-svelte';

	import VarTable from './variables/VarTable.svelte';
	import BindingsSection from './variables/BindingsSection.svelte';
	import CredentialsSection from './variables/CredentialsSection.svelte';

	let {
		project,
		app,
		autoRedeploy = false
	}: {
		project: string;
		app: App;
		autoRedeploy?: boolean;
	} = $props();

	type EnvEntry = {
		name: string;
		value: string;
		source?: string;
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
		if (!autoRedeploy) localStale = true;
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
		resolveAppEnvironment(app, store.currentEnv(project))
	);
	const isGitSource = $derived(app.spec.source.type === 'git');

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
	let buildSection = $state<SectionState>(makeSection());
	let lastLoadedEnv = $state('');
	let lastLoadedApp = $state('');

	$effect(() => {
		const env = activeEnv;
		const appName = app.metadata.name;
		if (!env) return;
		if (env === lastLoadedEnv && appName === lastLoadedApp) return;
		lastLoadedEnv = env;
		lastLoadedApp = appName;
		// Reset sections so stale data from the previous env doesn't linger.
		envSection = makeSection();
		sharedSection = makeSection();
		buildSection = makeSection();
		void loadEnv(env);
		void loadShared();
		void loadBuildArgs();
	});

	// --- Load functions ---

	async function loadEnv(envName: string) {
		envSection.loading = true;
		envSection.error = '';
		try {
			const rows = await api.getEnv(project, app.metadata.name, envName);
			const entries: EnvEntry[] = (rows ?? []).map(r => ({
				name: r.name, value: r.value, source: r.source ?? 'user', revealed: false
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

	async function loadBuildArgs() {
		if (!isGitSource || !activeEnv) return;
		buildSection.loading = true;
		buildSection.error = '';
		try {
			const rows = await api.getBuildArgs(project, app.metadata.name, activeEnv);
			const entries: EnvEntry[] = (rows ?? []).map(r => ({
				name: r.name, value: r.value ?? '', source: 'user', revealed: false
			}));
			buildSection.entries = entries;
			buildSection.originalEntries = entries.map(e => ({ ...e }));
			buildSection.editedKeys = new Set();
		} catch (e) {
			buildSection.error = e instanceof Error ? e.message : 'Failed to load build args';
			buildSection.entries = [];
		} finally {
			buildSection.loading = false;
		}
	}

	// --- Shared helpers ---

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
		const lines = text.split('\n').filter(l => l.trim() && !l.trim().startsWith('#') && l.includes('='));
		if (lines.length > 1) {
			e.preventDefault();
			for (const line of lines) {
				const idx = line.indexOf('=');
				if (idx < 1) continue;
				const key = line.slice(0, idx).trim();
				let val = line.slice(idx + 1).trim();
				if (val.length >= 2 && ((val[0] === '"' && val[val.length - 1] === '"') || (val[0] === "'" && val[val.length - 1] === "'"))) {
					val = val.slice(1, -1);
				}
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

	function toggleReveal(section: SectionState, idx: number) {
		section.entries[idx] = { ...section.entries[idx], revealed: !section.entries[idx].revealed };
	}

	function insertRef(ref: string) {
		envSection.newValue = envSection.newValue + ref;
		envSection.showPicker = false;
	}

	function parseRaw(text: string): Record<string, string> {
		const parsed: Record<string, string> = {};
		for (const line of text.split('\n')) {
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
		return parsed;
	}

	function ensureUniqueKey(section: SectionState, key: string): string {
		const existingKeys = new Set(section.entries.map(e => e.name));
		if (!existingKeys.has(key)) return key;
		let suffix = 2;
		while (existingKeys.has(`${key}_${suffix}`)) suffix++;
		return `${key}_${suffix}`;
	}

	// --- Env section actions ---

	async function saveEnvSection() {
		envSection.saving = true;
		envSection.error = '';
		try {
			const seen = new Set<string>();
			const dupes: string[] = [];
			for (const e of envSection.entries) {
				if (seen.has(e.name)) dupes.push(e.name);
				seen.add(e.name);
			}
			if (dupes.length > 0) {
				envSection.error = `Duplicate keys: ${[...new Set(dupes)].join(', ')}. Rename or remove duplicates before saving.`;
				envSection.saving = false;
				return;
			}
			const vars: Record<string, string> = {};
			for (const e of envSection.entries) vars[e.name] = e.value;
			await api.setEnv(project, app.metadata.name, activeEnv, vars);
			envSection.originalEntries = envSection.entries.map(e => ({ ...e }));
			envSection.editedKeys = new Set();
			markStale();
		} catch (e) {
			envSection.error = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			envSection.saving = false;
		}
	}

	async function addEnvVar() {
		if (!envSection.newKey.trim()) return;
		const key = ensureUniqueKey(envSection, envSection.newKey.trim());
		envSection.entries = [...envSection.entries, { name: key, value: envSection.newValue, source: 'user', revealed: false }];
		envSection.showNewRow = false;
		envSection.newKey = '';
		envSection.newValue = '';
		envSection.editedKeys = new Set([...envSection.editedKeys, key]);
		await saveEnvSection();
	}

	async function deleteEnvVar(idx: number) {
		const key = envSection.entries[idx].name;
		envSection.entries = envSection.entries.filter((_, i) => i !== idx);
		const next = new Set(envSection.editedKeys);
		next.delete(key);
		envSection.editedKeys = next;
		await saveEnvSection();
	}

	async function importRawEnv() {
		const parsed = parseRaw(envSection.rawText);
		envSection.entries = Object.entries(parsed).map(([key, val]) => ({
			name: key, value: val, source: 'user', revealed: false
		}));
		envSection.rawMode = false;
		envSection.rawText = '';
		envSection.editedKeys = new Set(Object.keys(parsed));
		await saveEnvSection();
	}

	// --- Build section actions ---

	async function saveBuildSection() {
		buildSection.saving = true;
		buildSection.error = '';
		try {
			const seen = new Set<string>();
			const dupes: string[] = [];
			for (const e of buildSection.entries) {
				if (seen.has(e.name)) dupes.push(e.name);
				seen.add(e.name);
			}
			if (dupes.length > 0) {
				buildSection.error = `Duplicate keys: ${[...new Set(dupes)].join(', ')}. Rename or remove duplicates before saving.`;
				buildSection.saving = false;
				return;
			}
			const filtered = buildSection.entries
				.filter(e => e.name.trim() !== '')
				.map(e => ({ name: e.name, value: e.value }));
			const result = await api.putBuildArgs(project, app.metadata.name, activeEnv, filtered);
			buildSection.entries = (result ?? []).map(r => ({
				name: r.name, value: r.value ?? '', source: 'user', revealed: false
			}));
			buildSection.originalEntries = buildSection.entries.map(e => ({ ...e }));
			buildSection.editedKeys = new Set();
			markStale();
		} catch (e) {
			buildSection.error = e instanceof Error ? e.message : 'Failed to save build args';
		} finally {
			buildSection.saving = false;
		}
	}

	async function addBuildVar() {
		if (!buildSection.newKey.trim()) return;
		const key = ensureUniqueKey(buildSection, buildSection.newKey.trim());
		buildSection.entries = [...buildSection.entries, { name: key, value: buildSection.newValue, source: 'user', revealed: false }];
		buildSection.showNewRow = false;
		buildSection.newKey = '';
		buildSection.newValue = '';
		buildSection.editedKeys = new Set([...buildSection.editedKeys, key]);
		await saveBuildSection();
	}

	async function deleteBuildVar(idx: number) {
		const key = buildSection.entries[idx].name;
		buildSection.entries = buildSection.entries.filter((_, i) => i !== idx);
		const next = new Set(buildSection.editedKeys);
		next.delete(key);
		buildSection.editedKeys = next;
		await saveBuildSection();
	}

	async function importRawBuild() {
		const parsed = parseRaw(buildSection.rawText);
		buildSection.entries = Object.entries(parsed).map(([key, val]) => ({
			name: key, value: val, source: 'user', revealed: false
		}));
		buildSection.rawMode = false;
		buildSection.rawText = '';
		buildSection.editedKeys = new Set(Object.keys(parsed));
		await saveBuildSection();
	}

	// --- Shared section actions ---

	async function saveSharedSection() {
		sharedSection.saving = true;
		sharedSection.error = '';
		try {
			const seen = new Set<string>();
			const dupes: string[] = [];
			for (const e of sharedSection.entries) {
				if (seen.has(e.name)) dupes.push(e.name);
				seen.add(e.name);
			}
			if (dupes.length > 0) {
				sharedSection.error = `Duplicate keys: ${[...new Set(dupes)].join(', ')}. Rename or remove duplicates before saving.`;
				sharedSection.saving = false;
				return;
			}
			const entries = sharedSection.entries.map(e => ({ name: e.name, value: e.value }));
			await api.setSharedVars(project, entries);
			sharedSection.originalEntries = sharedSection.entries.map(e => ({ ...e }));
			sharedSection.editedKeys = new Set();
			markStale();
		} catch (e) {
			sharedSection.error = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			sharedSection.saving = false;
		}
	}

	async function addSharedVar() {
		if (!sharedSection.newKey.trim()) return;
		const key = ensureUniqueKey(sharedSection, sharedSection.newKey.trim());
		sharedSection.entries = [...sharedSection.entries, { name: key, value: sharedSection.newValue, source: 'shared', revealed: false }];
		sharedSection.showNewRow = false;
		sharedSection.newKey = '';
		sharedSection.newValue = '';
		sharedSection.editedKeys = new Set([...sharedSection.editedKeys, key]);
		await saveSharedSection();
	}

	async function deleteSharedVar(idx: number) {
		const key = sharedSection.entries[idx].name;
		sharedSection.entries = sharedSection.entries.filter((_, i) => i !== idx);
		const next = new Set(sharedSection.editedKeys);
		next.delete(key);
		sharedSection.editedKeys = next;
		await saveSharedSection();
	}

	async function importRawShared() {
		const parsed = parseRaw(sharedSection.rawText);
		sharedSection.entries = Object.entries(parsed).map(([key, val]) => ({
			name: key, value: val, source: 'shared', revealed: false
		}));
		sharedSection.rawMode = false;
		sharedSection.rawText = '';
		sharedSection.editedKeys = new Set(Object.keys(parsed));
		await saveSharedSection();
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

	{#if activeEnv}
		<VarTable
			section={envSection}
			title="Runtime - {activeEnv}"
			{project}
			{app}
			showBindingPicker={true}
			onSave={saveEnvSection}
			onAdd={addEnvVar}
			onDelete={deleteEnvVar}
			onImportRaw={importRawEnv}
			onValueEdit={(idx, val) => handleValueEdit(envSection, idx, val)}
			onKeyPaste={(e) => handleKeyPaste(envSection, e)}
			onToggleReveal={(idx) => toggleReveal(envSection, idx)}
			onInsertRef={insertRef}
			onSetRawMode={(v) => { envSection.rawMode = v; }}
			onSetRawText={(v) => { envSection.rawText = v; }}
			onSetShowNewRow={(v) => { envSection.showNewRow = v; }}
			onSetNewKey={(v) => { envSection.newKey = v; }}
			onSetNewValue={(v) => { envSection.newValue = v; }}
			onSetShowPicker={(v) => { envSection.showPicker = v; }}
		/>
	{/if}

	{#if isGitSource && activeEnv}
		<VarTable
			section={buildSection}
			title="Build - {activeEnv}"
			showSourceBadge={false}
			onSave={saveBuildSection}
			onAdd={addBuildVar}
			onDelete={deleteBuildVar}
			onImportRaw={importRawBuild}
			onValueEdit={(idx, val) => handleValueEdit(buildSection, idx, val)}
			onKeyPaste={(e) => handleKeyPaste(buildSection, e)}
			onToggleReveal={(idx) => toggleReveal(buildSection, idx)}
			onSetRawMode={(v) => { buildSection.rawMode = v; }}
			onSetRawText={(v) => { buildSection.rawText = v; }}
			onSetShowNewRow={(v) => { buildSection.showNewRow = v; }}
			onSetNewKey={(v) => { buildSection.newKey = v; }}
			onSetNewValue={(v) => { buildSection.newValue = v; }}
		/>
	{/if}

	<VarTable
		section={sharedSection}
		title="Project"
		subtitle="all apps & environments"
		onSave={saveSharedSection}
		onAdd={addSharedVar}
		onDelete={deleteSharedVar}
		onImportRaw={importRawShared}
		onValueEdit={(idx, val) => handleValueEdit(sharedSection, idx, val)}
		onKeyPaste={(e) => handleKeyPaste(sharedSection, e)}
		onToggleReveal={(idx) => toggleReveal(sharedSection, idx)}
		onSetRawMode={(v) => { sharedSection.rawMode = v; }}
		onSetRawText={(v) => { sharedSection.rawText = v; }}
		onSetShowNewRow={(v) => { sharedSection.showNewRow = v; }}
		onSetNewKey={(v) => { sharedSection.newKey = v; }}
		onSetNewValue={(v) => { sharedSection.newValue = v; }}
	/>

	<BindingsSection {project} {app} {activeEnv} onStale={markStale} />

	<CredentialsSection {project} {app} {activeEnv} />
</div>
