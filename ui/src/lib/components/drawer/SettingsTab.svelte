<script lang="ts">
	import { untrack } from 'svelte';
	import { store } from '$lib/store.svelte';
	import { resolveAppEnvironment } from '$lib/types';
	import type { App, AppSpec } from '$lib/types';

	import SourceSection from './settings/SourceSection.svelte';
	import BuildSection from './settings/BuildSection.svelte';
	import NetworkingSection from './settings/NetworkingSection.svelte';
	import ScaleSection from './settings/ScaleSection.svelte';
	import StorageSection from './settings/StorageSection.svelte';
	import DomainsSection from './settings/DomainsSection.svelte';
	import DeployTokensSection from './settings/DeployTokensSection.svelte';
	import AdvancedSection from './settings/AdvancedSection.svelte';
	import DangerZoneSection from './settings/DangerZoneSection.svelte';
	import { sectionCls, headingCls, btnPrimary } from './settings/styles';

	let {
		project,
		app,
		onAppDeleted
	}: {
		project: string;
		app: App;
		onAppDeleted: () => void;
	} = $props();

	const selectedEnv = $derived(
		resolveAppEnvironment(app, store.currentEnv(project))
	);
	const appIdentity = $derived(`${project}/${app.metadata.name}`);

	type SettingsSectionId =
		| 'source' | 'registry' | 'build' | 'networking' | 'scale' | 'storage'
		| 'domains-primary' | 'domains-custom' | 'domains-tls' | 'deploy-tokens'
		| 'annotations' | 'mounts' | 'mount-form' | 'delete';
	const copySpec = (spec: AppSpec): AppSpec => JSON.parse(JSON.stringify(spec));
	let acceptedSpec = $state<AppSpec | null>(null);
	let acceptedGeneration = $state<number | undefined>();
	let dirtySections = $state<Set<SettingsSectionId>>(new Set());
	let newerSpecWhileDirty = $state(false);
	let showExternalChangeWarning = $state(false);
	let resetEpoch = $state(0);
	let lastIdentity = $state('');
	let lastEnvironment = $state('');

	$effect(() => {
		const identity = `${project}/${app.metadata.name}`;
		const environment = selectedEnv;
		const generation = app.metadata.generation;

		if (identity !== lastIdentity || environment !== lastEnvironment) {
			acceptedSpec = untrack(() => copySpec(app.spec));
			acceptedGeneration = generation;
			dirtySections = new Set();
			newerSpecWhileDirty = false;
			showExternalChangeWarning = false;
			lastIdentity = identity;
			lastEnvironment = environment;
			resetEpoch += 1;
			return;
		}

		if (generation === undefined || generation === acceptedGeneration) return;
		if (acceptedGeneration !== undefined && generation < acceptedGeneration) return;

		acceptedSpec = untrack(() => copySpec(app.spec));
		acceptedGeneration = generation;
		if (dirtySections.size === 0) {
			resetEpoch += 1;
			newerSpecWhileDirty = false;
			showExternalChangeWarning = false;
		} else {
			newerSpecWhileDirty = true;
			showExternalChangeWarning = true;
		}
	});

	function cloneSpec(): AppSpec {
		return copySpec(acceptedSpec ?? app.spec);
	}

	function markSectionDirty(section: SettingsSectionId) {
		if (dirtySections.has(section)) return;
		dirtySections = new Set([...dirtySections, section]);
	}

	function clearSectionDirty(section: SettingsSectionId) {
		if (!dirtySections.has(section)) return;
		const next = new Set(dirtySections);
		next.delete(section);
		dirtySections = next;
		if (dirtySections.size === 0) {
			newerSpecWhileDirty = false;
			showExternalChangeWarning = false;
		}
	}

	function handleSpecUpdate(updatedApp: App, section?: SettingsSectionId) {
		acceptedSpec = copySpec(updatedApp.spec);
		acceptedGeneration = updatedApp.metadata.generation ?? acceptedGeneration;
		if (section) clearSectionDirty(section);
	}

	function keepEditing() {
		showExternalChangeWarning = false;
	}

	function reloadLatest() {
		dirtySections = new Set();
		newerSpecWhileDirty = false;
		showExternalChangeWarning = false;
		resetEpoch += 1;
	}

	const envEntry = $derived((acceptedSpec ?? app.spec).environments?.find(e => e.name === selectedEnv));
	const envEnabled = $derived(envEntry?.enabled !== false);
	let togglingEnabled = $state(false);

	async function toggleEnvEnabled() {
		if (!selectedEnv) return;
		togglingEnabled = true;
		const next = !envEnabled;
		const spec = cloneSpec();
		spec.environments = spec.environments ?? [];
		const idx = spec.environments.findIndex((e: { name: string }) => e.name === selectedEnv);
		if (idx >= 0) {
			spec.environments[idx].enabled = next;
		} else {
			spec.environments.push({ name: selectedEnv, enabled: next });
		}
		try {
			const { api } = await import('$lib/api');
			const result = await api.updateApp(project, app.metadata.name, spec);
			handleSpecUpdate(result);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to toggle environment';
		} finally {
			togglingEnabled = false;
		}
	}

	let filterText = $state('');
	let errorMsg = $state('');

	function handleError(msg: string) {
		errorMsg = msg;
	}

	function sectionVisible(name: string): boolean {
		if (!filterText) return true;
		return name.toLowerCase().includes(filterText.toLowerCase());
	}
</script>

<div class="space-y-4">
	{#if errorMsg}
		<div class="rounded-md bg-danger/10 px-3 py-2 text-xs text-danger">{errorMsg}</div>
	{/if}
	{#if newerSpecWhileDirty && showExternalChangeWarning}
		<div class="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning" role="alert">
			<p>This app changed elsewhere. Your unsaved edits are preserved.</p>
			<div class="mt-2 flex gap-2">
				<button type="button" onclick={keepEditing} class="rounded-md border border-warning/40 px-2 py-1 hover:bg-warning/10">Keep editing</button>
				<button type="button" onclick={reloadLatest} class="rounded-md bg-warning px-2 py-1 font-medium text-surface-900 hover:opacity-90">Reload latest</button>
			</div>
		</div>
	{/if}

	<!-- Filter -->
	<input
		type="text"
		placeholder="Filter settings…"
		bind:value={filterText}
		class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
	/>

	<div class:hidden={!sectionVisible('source')}>
		<SourceSection {project} {app} {appIdentity} {resetEpoch} {cloneSpec}
			onDirty={(scope) => markSectionDirty(scope === 'spec' ? 'source' : 'registry')}
			onDraftCleared={(scope) => clearSectionDirty(scope === 'spec' ? 'source' : 'registry')}
			onSpecUpdate={(result) => handleSpecUpdate(result, 'source')} onError={handleError} />
	</div>

	<div class:hidden={!sectionVisible('build')}>
		<BuildSection {project} {app} {appIdentity} {resetEpoch} {cloneSpec} onDirty={() => markSectionDirty('build')} onSpecUpdate={(result) => handleSpecUpdate(result, 'build')} onError={handleError} />
	</div>

	<div class:hidden={!sectionVisible('networking')}>
		<NetworkingSection {project} {app} {appIdentity} {resetEpoch} {cloneSpec} onDirty={() => markSectionDirty('networking')} onSpecUpdate={(result) => handleSpecUpdate(result, 'networking')} onError={handleError} />
	</div>

	<!-- Environment enabled toggle (inline — too small to extract) -->
	{#if selectedEnv}
		<div class:hidden={!sectionVisible('environment enabled')}>
		<div class={sectionCls}>
			<h3 class={headingCls}>Environment</h3>
			<div class="flex items-center justify-between">
				<div>
					<p class="text-sm text-gray-300">Enabled</p>
					<p class="text-xs text-gray-500">Disabling stops reconciliation and garbage-collects resources for this env.</p>
				</div>
				<button
					type="button"
					role="switch"
					aria-checked={envEnabled}
					aria-label="Toggle environment enabled"
					disabled={togglingEnabled}
					onclick={toggleEnvEnabled}
					class="relative inline-flex h-5 w-9 items-center rounded-full transition-colors {envEnabled ? 'bg-accent' : 'bg-surface-600'} disabled:opacity-50"
				>
					<span
						class="inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform {envEnabled ? 'translate-x-4.5' : 'translate-x-0.5'}"
					></span>
				</button>
			</div>
		</div>
		</div>
	{/if}

	<div class:hidden={!sectionVisible('scale')}>
		<ScaleSection {project} {app} {appIdentity} {selectedEnv} {resetEpoch} {cloneSpec} onDirty={() => markSectionDirty('scale')} onSpecUpdate={(result) => handleSpecUpdate(result, 'scale')} onError={handleError} />
	</div>

	<div class:hidden={!sectionVisible('storage volumes')}>
		<StorageSection {project} {app} {appIdentity} {resetEpoch} {cloneSpec}
			onDirty={() => markSectionDirty('storage')} onDraftCleared={() => clearSectionDirty('storage')}
			onSpecUpdate={handleSpecUpdate} onError={handleError} />
	</div>

	<div class:hidden={!sectionVisible('domains')}>
		<DomainsSection {project} {app} {appIdentity} {selectedEnv} {resetEpoch} {cloneSpec}
			onDirty={(scope) => markSectionDirty(`domains-${scope}` as SettingsSectionId)}
			onDraftCleared={(scope) => clearSectionDirty(`domains-${scope}` as SettingsSectionId)}
			onSpecUpdate={(result, scope) => handleSpecUpdate(result, `domains-${scope}` as SettingsSectionId)} onError={handleError} />
	</div>

	<div class:hidden={!sectionVisible('deploy tokens')}>
		<DeployTokensSection {project} {app} {appIdentity} {resetEpoch} {selectedEnv}
			onDirty={() => markSectionDirty('deploy-tokens')} onDraftCleared={() => clearSectionDirty('deploy-tokens')}
			onError={handleError} />
	</div>

	<!-- Config-as-code (inline — informational only) -->
	<div class:hidden={!sectionVisible('gitops config')}>
		<div class={sectionCls}>
			<h3 class={headingCls}>Config-as-code (GitOps)</h3>
			<div class="rounded-md border border-surface-600 bg-surface-700/50 p-3 text-xs text-gray-400">
				<p>Managing this App via Argo CD or Flux? Add <code class="font-mono bg-surface-700 px-1 rounded">ignoreDifferences</code> for <code class="font-mono bg-surface-700 px-1 rounded">env</code> and <code class="font-mono bg-surface-700 px-1 rounded">sharedVars</code> fields to prevent drift conflicts.</p>
				<a href="https://docs.mortise.dev/gitops" target="_blank" rel="noopener noreferrer" class="mt-2 inline-block text-accent hover:underline">View GitOps guide →</a>
			</div>
		</div>
	</div>

	<div class:hidden={!sectionVisible('advanced annotations mounts')}>
		<AdvancedSection {project} {app} {appIdentity} {selectedEnv} {resetEpoch} {cloneSpec}
			onDirty={(scope) => markSectionDirty(scope)}
			onDraftCleared={(scope) => clearSectionDirty(scope)}
			onSpecUpdate={(result, scope) => handleSpecUpdate(result, scope)} onError={handleError} />
	</div>

	<div class:hidden={!sectionVisible('danger delete')}>
		<DangerZoneSection {project} {app} {appIdentity} {resetEpoch} {onAppDeleted}
			onDirty={() => markSectionDirty('delete')} onDraftCleared={() => clearSectionDirty('delete')}
			onError={handleError} />
	</div>
</div>
