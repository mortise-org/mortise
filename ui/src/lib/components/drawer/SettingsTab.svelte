<script lang="ts">
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
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

	let specOverride = $state<AppSpec | null>(null);
	let lastSeenSpec = $state<AppSpec | null>(null);
	$effect(() => {
		if (lastSeenSpec !== null && app.spec !== lastSeenSpec) {
			specOverride = null;
		}
		lastSeenSpec = app.spec;
	});
	function cloneSpec(): AppSpec { return JSON.parse(JSON.stringify(specOverride ?? app.spec)); }

	function handleSpecUpdate(spec: AppSpec) {
		specOverride = spec;
	}

	const selectedEnv = $derived(store.currentEnv(project) ?? '');
	const envEntry = $derived(app.spec.environments?.find(e => e.name === selectedEnv));
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
			const result = await api.updateApp(project, app.metadata.name, spec);
			specOverride = result.spec;
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

	<!-- Filter -->
	<input
		type="text"
		placeholder="Filter settings…"
		bind:value={filterText}
		class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
	/>

	{#if sectionVisible('source')}
		<SourceSection {project} {app} {cloneSpec} onSpecUpdate={handleSpecUpdate} onError={handleError} />
	{/if}

	{#if sectionVisible('build')}
		<BuildSection {project} {app} {cloneSpec} onSpecUpdate={handleSpecUpdate} onError={handleError} />
	{/if}

	{#if sectionVisible('networking')}
		<NetworkingSection {project} {app} {cloneSpec} onSpecUpdate={handleSpecUpdate} onError={handleError} />
	{/if}

	<!-- Environment enabled toggle (inline — too small to extract) -->
	{#if selectedEnv && sectionVisible('environment enabled')}
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
	{/if}

	{#if sectionVisible('scale')}
		<ScaleSection {project} {app} {selectedEnv} {cloneSpec} onSpecUpdate={handleSpecUpdate} onError={handleError} />
	{/if}

	{#if sectionVisible('storage volumes')}
		<StorageSection {project} {app} {cloneSpec} onSpecUpdate={handleSpecUpdate} onError={handleError} />
	{/if}

	{#if sectionVisible('domains')}
		<DomainsSection {project} {app} {selectedEnv} onSpecUpdate={handleSpecUpdate} onError={handleError} />
	{/if}

	{#if sectionVisible('deploy tokens')}
		<DeployTokensSection {project} {app} {selectedEnv} onError={handleError} />
	{/if}

	<!-- Config-as-code (inline — informational only) -->
	{#if sectionVisible('gitops config')}
		<div class={sectionCls}>
			<h3 class={headingCls}>Config-as-code (GitOps)</h3>
			<div class="rounded-md border border-surface-600 bg-surface-700/50 p-3 text-xs text-gray-400">
				<p>Managing this App via Argo CD or Flux? Add <code class="font-mono bg-surface-700 px-1 rounded">ignoreDifferences</code> for <code class="font-mono bg-surface-700 px-1 rounded">env</code> and <code class="font-mono bg-surface-700 px-1 rounded">sharedVars</code> fields to prevent drift conflicts.</p>
				<a href="https://docs.mortise.dev/gitops" target="_blank" rel="noopener noreferrer" class="mt-2 inline-block text-accent hover:underline">View GitOps guide →</a>
			</div>
		</div>
	{/if}

	{#if sectionVisible('advanced annotations mounts')}
		<AdvancedSection {project} {app} {selectedEnv} {cloneSpec} onSpecUpdate={handleSpecUpdate} onError={handleError} />
	{/if}

	{#if sectionVisible('danger delete')}
		<DangerZoneSection {project} {app} {onAppDeleted} onError={handleError} />
	{/if}
</div>
