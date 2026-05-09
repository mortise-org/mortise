<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { App, AppSpec } from '$lib/types';
	import { inputCls, labelCls, sectionCls, headingCls, btnPrimary } from './styles';

	let {
		project,
		app,
		selectedEnv,
		cloneSpec,
		onSpecUpdate,
		onError
	}: {
		project: string;
		app: App;
		selectedEnv: string;
		cloneSpec: () => AppSpec;
		onSpecUpdate: (spec: AppSpec) => void;
		onError: (msg: string) => void;
	} = $props();

	let scaleReplicas = $state('1');
	let scaleCpu = $state('');
	let scaleMemory = $state('');
	let saving = $state(false);

	$effect(() => {
		const envName = selectedEnv;
		void app.metadata.name;
		untrack(() => {
			const spec = cloneSpec();
			const env = spec.environments?.find((e: { name: string }) => e.name === envName);
			scaleReplicas = String(env?.replicas ?? 1);
			scaleCpu = env?.resources?.cpu ?? '';
			scaleMemory = env?.resources?.memory ?? '';
		});
	});

	async function saveScale() {
		if (!selectedEnv) return;
		saving = true;
		const spec = cloneSpec();
		spec.environments = spec.environments ?? [];
		let envIdx = spec.environments.findIndex((e: { name: string }) => e.name === selectedEnv);
		if (envIdx < 0) {
			spec.environments.push({ name: selectedEnv });
			envIdx = spec.environments.length - 1;
		}
		const envSpec = spec.environments[envIdx];
		envSpec.replicas = parseInt(scaleReplicas, 10) || 1;
		envSpec.resources = envSpec.resources ?? {};
		if (scaleCpu) envSpec.resources.cpu = scaleCpu;
		if (scaleMemory) envSpec.resources.memory = scaleMemory;
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
	<h3 class={headingCls}>Scale</h3>
	<div class="space-y-2">
		<div>
			<label class={labelCls} for="scale-replicas">Replicas</label>
			<input id="scale-replicas" type="number" min="0" bind:value={scaleReplicas} class={inputCls} />
		</div>
		<div class="grid grid-cols-2 gap-2">
			<div>
				<label class={labelCls} for="scale-cpu">CPU</label>
				<input id="scale-cpu" type="text" bind:value={scaleCpu} placeholder="500m" class={inputCls} />
			</div>
			<div>
				<label class={labelCls} for="scale-mem">Memory</label>
				<input id="scale-mem" type="text" bind:value={scaleMemory} placeholder="256Mi" class={inputCls} />
			</div>
		</div>
	</div>
	<div class="flex justify-end pt-1">
		<button type="button" onclick={saveScale} disabled={saving} class={btnPrimary}>
			{saving ? 'Saving…' : 'Update'}
		</button>
	</div>
</div>
