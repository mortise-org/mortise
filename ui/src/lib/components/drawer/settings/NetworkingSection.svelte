<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { App, AppSpec } from '$lib/types';
	import { inputCls, labelCls, sectionCls, headingCls, btnPrimary } from './styles';

	let {
		project,
		app,
		cloneSpec,
		onSpecUpdate,
		onError
	}: {
		project: string;
		app: App;
		cloneSpec: () => AppSpec;
		onSpecUpdate: (spec: AppSpec) => void;
		onError: (msg: string) => void;
	} = $props();

	let netPublic = $state(true);
	let netPort = $state('');
	let saving = $state(false);

	$effect(() => {
		void app.metadata.name;
		untrack(() => {
			const spec = cloneSpec();
			netPublic = spec.network?.public ?? true;
			netPort = String(spec.network?.port ?? '');
		});
	});

	async function saveNetworking() {
		saving = true;
		const spec = cloneSpec();
		spec.network = spec.network ?? {};
		spec.network.public = netPublic;
		if (netPort) spec.network.port = parseInt(netPort, 10);
		try {
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result.spec);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to save');
		} finally {
			saving = false;
		}
	}
</script>

<div class={sectionCls}>
	<h3 class={headingCls}>Networking</h3>
	<div class="space-y-3">
		<div class="flex items-center justify-between">
			<div>
				<p class="text-sm text-gray-300">Public</p>
				<p class="text-xs text-gray-500">Expose this app via ingress</p>
			</div>
			<button
				type="button"
				role="switch"
				aria-checked={netPublic}
				aria-label="Toggle public access"
				onclick={() => (netPublic = !netPublic)}
				class="relative inline-flex h-5 w-9 items-center rounded-full transition-colors {netPublic ? 'bg-accent' : 'bg-surface-600'}"
			>
				<span
					class="inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform {netPublic ? 'translate-x-4.5' : 'translate-x-0.5'}"
				></span>
			</button>
		</div>
		<div>
			<label class={labelCls} for="net-port">Port</label>
			<input id="net-port" type="number" bind:value={netPort} placeholder="8080" class={inputCls} />
		</div>
	</div>
	<div class="flex justify-end pt-1">
		<button type="button" onclick={saveNetworking} disabled={saving} class={btnPrimary}>
			{saving ? 'Saving…' : 'Update'}
		</button>
	</div>
</div>
