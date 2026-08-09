<script lang="ts">
	import { api } from '$lib/api';
	import type { App, AppSpec } from '$lib/types';
	import { Plus, Trash2 } from 'lucide-svelte';
	import { inputCls, labelCls, sectionCls, headingCls, btnPrimary, btnSecondary } from './styles';

	let {
		project,
		app,
		appIdentity,
		resetEpoch,
		cloneSpec,
		onDirty,
		onDraftCleared,
		onSpecUpdate,
		onError
	}: {
		project: string;
		app: App;
		appIdentity: string;
		resetEpoch: number;
		cloneSpec: () => AppSpec;
		onDirty: () => void;
		onDraftCleared: () => void;
		onSpecUpdate: (app: App) => void;
		onError: (msg: string) => void;
	} = $props();

	let showAddVolume = $state(false);
	let newVol = $state({ name: '', mountPath: '', size: '', storageClass: '' });
	let savingVolume = $state(false);

	$effect(() => {
		appIdentity;
		resetEpoch;
		showAddVolume = false;
		newVol = { name: '', mountPath: '', size: '', storageClass: '' };
	});

	async function addVolume() {
		if (!newVol.name || !newVol.mountPath) return;
		savingVolume = true;
		const prevVol = { ...newVol };
		const spec = cloneSpec();
		spec.storage = [...(spec.storage ?? []), {
			name: newVol.name,
			mountPath: newVol.mountPath,
			size: newVol.size || undefined,
			storageClass: newVol.storageClass || undefined
		}];
		showAddVolume = false;
		newVol = { name: '', mountPath: '', size: '', storageClass: '' };
		try {
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result);
			onDraftCleared();
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to add volume');
			showAddVolume = true;
			newVol = prevVol;
		} finally {
			savingVolume = false;
		}
	}

	async function removeVolume(idx: number) {
		const spec = cloneSpec();
		spec.storage = (spec.storage ?? []).filter((_: unknown, i: number) => i !== idx);
		try {
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to remove volume');
		}
	}
</script>

<div class={sectionCls}>
	<div class="flex items-center justify-between">
		<h3 class={headingCls} style="margin-bottom:0">Storage</h3>
		<button type="button" onclick={() => showAddVolume = true}
			class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
			<Plus class="h-3.5 w-3.5" /> Add volume
		</button>
	</div>

	{#each app.spec.storage ?? [] as vol, i}
		<div class="mt-3 rounded-md border border-surface-600 bg-surface-700/50 p-3 text-xs">
			<div class="flex items-center justify-between">
				<span class="font-medium text-white">{vol.name}</span>
				<button type="button" onclick={() => removeVolume(i)}
					class="rounded p-1 text-gray-500 hover:bg-surface-600 hover:text-danger">
					<Trash2 class="h-3.5 w-3.5" />
				</button>
			</div>
			<div class="mt-1.5 grid grid-cols-2 gap-1 text-gray-500">
				<span>Mount: <span class="font-mono text-gray-300">{vol.mountPath}</span></span>
				{#if vol.size}<span>Size: <span class="text-gray-300">{vol.size}</span></span>{/if}
				{#if vol.storageClass}<span>Class: <span class="text-gray-300">{vol.storageClass}</span></span>{/if}
				{#if vol.accessMode}<span>Mode: <span class="text-gray-300">{vol.accessMode}</span></span>{/if}
			</div>
		</div>
	{/each}

	{#if !app.spec.storage?.length && !showAddVolume}
		<p class="mt-2 text-xs text-gray-500">No volumes. Add a persistent volume to store data across deploys.</p>
	{/if}

	{#if showAddVolume}
		<div class="mt-3 rounded-md border border-surface-600 bg-surface-700 p-3 space-y-2">
			<p class="text-xs font-medium text-gray-300">New volume</p>
			<div class="grid grid-cols-2 gap-2">
				<div>
					<label class={labelCls} for="vol-name">Name</label>
					<input id="vol-name" type="text" bind:value={newVol.name} oninput={onDirty} placeholder="data" class={inputCls} />
				</div>
				<div>
					<label class={labelCls} for="vol-mount">Mount path</label>
					<input id="vol-mount" type="text" bind:value={newVol.mountPath} oninput={onDirty} placeholder="/data" class={inputCls} />
				</div>
				<div>
					<label class={labelCls} for="vol-size">Size</label>
					<input id="vol-size" type="text" bind:value={newVol.size} oninput={onDirty} placeholder="5Gi" class={inputCls} />
				</div>
				<div>
					<label class={labelCls} for="vol-class">Storage class</label>
					<input id="vol-class" type="text" bind:value={newVol.storageClass} oninput={onDirty} placeholder="standard" class={inputCls} />
				</div>
			</div>
			<div class="flex gap-2">
				<button type="button" onclick={addVolume} disabled={!newVol.name || !newVol.mountPath || savingVolume}
					class={btnPrimary}>
					{savingVolume ? 'Adding...' : 'Add'}
				</button>
				<button type="button" onclick={() => { showAddVolume = false; newVol = { name: '', mountPath: '', size: '', storageClass: '' }; onDraftCleared(); }}
					class={btnSecondary}>
					Cancel
				</button>
			</div>
		</div>
	{/if}
</div>
