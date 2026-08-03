<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { App, AppSpec } from '$lib/types';
	import { inputCls, labelCls, sectionCls, headingCls, btnPrimary } from './styles';

	let {
		project,
		app,
		appIdentity,
		resetEpoch,
		cloneSpec,
		onDirty,
		onSpecUpdate,
		onError
	}: {
		project: string;
		app: App;
		appIdentity: string;
		resetEpoch: number;
		cloneSpec: () => AppSpec;
		onDirty: () => void;
		onSpecUpdate: (app: App) => void;
		onError: (msg: string) => void;
	} = $props();

	let buildMode = $state<'auto' | 'dockerfile' | 'railpack'>('auto');
	let dockerfilePath = $state('');
	let buildContext = $state<'' | 'root' | 'subdir'>('');
	let savingBuild = $state(false);

	$effect(() => {
		appIdentity;
		resetEpoch;
		untrack(() => {
			const spec = cloneSpec();
			buildMode = spec.source.build?.mode ?? 'auto';
			dockerfilePath = spec.source.build?.dockerfilePath ?? '';
			buildContext = spec.source.build?.context ?? '';
		});
	});

	const srcPath = $derived(app.spec.source.path ?? '');

	async function saveBuild() {
		savingBuild = true;
		const spec = cloneSpec();
		spec.source = {
			...spec.source,
			build: {
				...spec.source.build,
				mode: buildMode,
				dockerfilePath: buildMode === 'dockerfile' ? dockerfilePath : undefined,
				context: buildContext === '' ? undefined : buildContext,
			}
		};
		try {
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to save build config');
		} finally {
			savingBuild = false;
		}
	}
</script>

<div class={sectionCls}>
	<h3 class={headingCls}>Build</h3>
	<div class="space-y-3">
		<div>
			<label class={labelCls} for="build-mode">Build mode</label>
			<select id="build-mode" bind:value={buildMode} onchange={onDirty}
				class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
				<option value="auto">Auto-detect</option>
				<option value="dockerfile">Dockerfile</option>
				<option value="railpack">Railpack / Nixpacks</option>
			</select>
		</div>
		{#if buildMode === 'dockerfile'}
			<div>
				<label class={labelCls} for="dockerfile-path">Dockerfile path</label>
				<input id="dockerfile-path" type="text" bind:value={dockerfilePath} oninput={onDirty} placeholder="Dockerfile"
					class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
			</div>
		{/if}
		{#if buildMode !== 'railpack' && srcPath}
			<div>
				<label class={labelCls} for="build-context">Build context</label>
				<select id="build-context" bind:value={buildContext} onchange={onDirty}
					class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
					<option value="">Auto-detect</option>
					<option value="subdir">Subdirectory (self-contained)</option>
					<option value="root">Repo root (monorepo Dockerfile)</option>
				</select>
				<p class="mt-1 text-xs text-gray-500">Override the build context root when the Dockerfile references sibling directories.</p>
			</div>
		{/if}

	</div>
	<button type="button" onclick={saveBuild} disabled={savingBuild}
		class={btnPrimary}>
		{savingBuild ? 'Saving...' : 'Save build config'}
	</button>
</div>
