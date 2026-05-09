<script lang="ts">
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { App, BindingEdge } from '$lib/types';
	import { inputCls, btnSecondary } from './styles';

	let {
		project,
		app,
		onAppDeleted,
		onError
	}: {
		project: string;
		app: App;
		onAppDeleted: () => void;
		onError: (msg: string) => void;
	} = $props();

	let confirmDelete = $state(false);
	let deleteConfirmText = $state('');
	let deleting = $state(false);
	let bindingConsumers = $state<string[]>([]);
	let loadingConsumers = $state(false);

	async function loadBindingConsumers() {
		loadingConsumers = true;
		try {
			const envs = store.projectEnvs[project] ?? [];
			const allEdges: BindingEdge[] = [];
			await Promise.all(
				envs.map(async (env) => {
					try {
						const edges = await api.listBindings(project, env.name);
						allEdges.push(...edges);
					} catch {
						// ignore per-env failures
					}
				})
			);
			const consumers = new Set<string>();
			for (const edge of allEdges) {
				if (edge.to === app.metadata.name) {
					consumers.add(edge.from);
				}
			}
			bindingConsumers = [...consumers].sort();
		} catch {
			bindingConsumers = [];
		} finally {
			loadingConsumers = false;
		}
	}

	async function startDelete() {
		confirmDelete = true;
		await loadBindingConsumers();
	}

	async function handleDelete() {
		if (deleteConfirmText !== app.metadata.name) return;
		deleting = true;
		try {
			await api.deleteApp(project, app.metadata.name);
			onAppDeleted();
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to delete app');
			deleting = false;
		}
	}
</script>

<div class="rounded-md border border-danger/30 bg-danger/5 p-4">
	<h3 class="mb-3 text-sm font-medium text-danger">Danger Zone</h3>
	{#if confirmDelete}
		<div class="space-y-2">
			{#if loadingConsumers}
				<div class="h-6 animate-pulse rounded bg-surface-700"></div>
			{:else if bindingConsumers.length > 0}
				<div class="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
					<strong>{app.metadata.name}</strong> is bound to by: <strong>{bindingConsumers.join(', ')}</strong>. Deleting it will remove binding variables from those apps and may cause them to crash.
				</div>
			{/if}
			<p class="text-xs text-gray-400">Type <strong class="text-white">{app.metadata.name}</strong> to confirm deletion.</p>
			<input
				type="text"
				bind:value={deleteConfirmText}
				placeholder={app.metadata.name}
				class="{inputCls} border-danger/50 focus:border-danger"
			/>
			<div class="flex gap-2">
				<button
					type="button"
					onclick={handleDelete}
					disabled={deleting || deleteConfirmText !== app.metadata.name}
					class="rounded-md bg-danger px-3 py-1.5 text-xs font-medium text-white hover:bg-danger/80 disabled:opacity-50"
				>
					{deleting ? 'Deleting…' : 'Delete App'}
				</button>
				<button
					type="button"
					onclick={() => { confirmDelete = false; deleteConfirmText = ''; bindingConsumers = []; }}
					class={btnSecondary}
				>
					Cancel
				</button>
			</div>
		</div>
	{:else}
		<div class="flex items-center justify-between">
			<div>
				<p class="text-sm text-gray-300">Delete App</p>
				<p class="text-xs text-gray-500">This will delete all resources. Cannot be undone.</p>
			</div>
			<button
				type="button"
				onclick={startDelete}
				class="rounded-md bg-danger px-3 py-1.5 text-sm font-medium text-white hover:bg-danger/80"
			>
				Delete
			</button>
		</div>
	{/if}
</div>
