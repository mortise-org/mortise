<script lang="ts">
	import { api } from '$lib/api';
	import type { App, AppSpec, SecretMount } from '$lib/types';
	import { Plus, Trash2, ChevronDown } from 'lucide-svelte';
	import { inputCls, sectionCls, btnPrimary } from './styles';

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

	let showAdvanced = $state(false);
	let annotations = $state<Record<string, string>>({});
	let savingAnnotations = $state(false);
	let secretMounts = $state<SecretMount[]>([]);
	let showAddMount = $state(false);
	let newMount = $state<{ secretName: string; mountPath: string }>({ secretName: '', mountPath: '' });
	let savingMounts = $state(false);

	$effect(() => {
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		const env = (app.spec.environments?.find(e => e.name === selectedEnv) as any) ?? {};
		annotations = Object.fromEntries(Object.entries((env.annotations ?? {}) as Record<string, string>));
		secretMounts = (env.secretMounts ?? []) as SecretMount[];
	});

	function addAnnotation() {
		annotations = { ...annotations, '': '' };
	}

	function updateAnnotationKey(i: number, _oldKey: string, newKey: string) {
		const entries = Object.entries(annotations);
		entries[i] = [newKey, entries[i][1]];
		annotations = Object.fromEntries(entries);
	}

	function updateAnnotationValue(key: string, val: string) {
		annotations = { ...annotations, [key]: val };
	}

	function removeAnnotation(key: string) {
		const { [key]: _, ...rest } = annotations;
		annotations = rest;
	}

	async function saveAnnotations() {
		if (!selectedEnv) return;
		savingAnnotations = true;
		try {
			const spec = cloneSpec();
			spec.environments = spec.environments ?? [];
			let envIdx = spec.environments.findIndex((e: { name: string }) => e.name === selectedEnv);
			if (envIdx < 0) {
				spec.environments.push({ name: selectedEnv });
				envIdx = spec.environments.length - 1;
			}
			spec.environments[envIdx] = { ...spec.environments[envIdx], annotations };
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result.spec);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to save annotations');
		} finally {
			savingAnnotations = false;
		}
	}

	function addSecretMount() {
		if (!newMount.secretName || !newMount.mountPath) return;
		secretMounts = [...secretMounts, { name: newMount.secretName, secret: newMount.secretName, path: newMount.mountPath }];
		newMount = { secretName: '', mountPath: '' };
		showAddMount = false;
		void saveSecretMounts();
	}

	function removeSecretMount(i: number) {
		secretMounts = secretMounts.filter((_, idx) => idx !== i);
		void saveSecretMounts();
	}

	async function saveSecretMounts() {
		if (!selectedEnv) return;
		savingMounts = true;
		try {
			const spec = cloneSpec();
			spec.environments = spec.environments ?? [];
			let envIdx = spec.environments.findIndex((e: { name: string }) => e.name === selectedEnv);
			if (envIdx < 0) {
				spec.environments.push({ name: selectedEnv });
				envIdx = spec.environments.length - 1;
			}
			spec.environments[envIdx] = { ...spec.environments[envIdx], secretMounts };
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result.spec);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to save mounts');
		} finally {
			savingMounts = false;
		}
	}
</script>

<div class={sectionCls}>
	<button type="button" onclick={() => showAdvanced = !showAdvanced}
		class="flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-gray-400 hover:text-white w-full">
		<ChevronDown class="h-3.5 w-3.5 transition-transform {showAdvanced ? 'rotate-0' : '-rotate-90'}" />
		Advanced
	</button>
	{#if showAdvanced}
		<div class="rounded-md border border-warning/30 bg-warning/5 p-2 mt-2">
			<p class="text-xs text-warning">Warning: incorrect annotations may break your deployment.</p>
		</div>

		<!-- Environment Annotations -->
		<div class="mt-3">
			<p class="text-sm text-gray-400 mb-1">Environment Annotations</p>
			<p class="text-xs text-gray-500 mb-2">Arbitrary Kubernetes annotations on the Deployment (Linkerd injection, IRSA, rate limits, etc.)</p>
			{#each Object.entries(annotations) as [k, v], i}
				<div class="flex gap-2 mb-2">
					<input type="text" value={k} oninput={(e) => updateAnnotationKey(i, k, (e.target as HTMLInputElement).value)}
						placeholder="annotation.example.com/key"
						class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2 py-1.5 font-mono text-xs text-white placeholder-gray-600 outline-none focus:border-accent" />
					<input type="text" value={v} oninput={(e) => updateAnnotationValue(k, (e.target as HTMLInputElement).value)}
						placeholder="value"
						class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2 py-1.5 font-mono text-xs text-white placeholder-gray-600 outline-none focus:border-accent" />
					<button type="button" onclick={() => removeAnnotation(k)}
						class="rounded p-1 text-gray-500 hover:text-danger">
						<Trash2 class="h-3.5 w-3.5" />
					</button>
				</div>
			{/each}
			<button type="button" onclick={addAnnotation}
				class="text-xs text-accent hover:text-accent-hover flex items-center gap-1">
				<Plus class="h-3 w-3" /> Add annotation
			</button>
			{#if Object.keys(annotations).length > 0}
				<button type="button" onclick={saveAnnotations} disabled={savingAnnotations}
					class="mt-2 {btnPrimary} block">
					{savingAnnotations ? 'Saving...' : 'Save annotations'}
				</button>
			{/if}
		</div>

		<!-- Secret Mounts -->
		<div class="mt-4">
			<p class="text-sm text-gray-400 mb-1">Secret Mounts</p>
			<p class="text-xs text-gray-500 mb-2">Mount k8s Secrets as files (Java keystores, mTLS certs, config files)</p>
			{#each secretMounts as mount, i}
				<div class="mb-2 rounded-md border border-surface-600 bg-surface-700 p-2 text-xs space-y-1.5">
					<div class="flex justify-between">
						<span class="font-mono text-gray-300">{mount.path}</span>
						<button type="button" onclick={() => removeSecretMount(i)}
							class="text-gray-500 hover:text-danger"><Trash2 class="h-3 w-3" /></button>
					</div>
					<p class="text-gray-500">Secret: <span class="font-mono">{mount.secret}</span></p>
				</div>
			{/each}
			{#if showAddMount}
				<div class="rounded-md border border-surface-600 p-2 space-y-2 bg-surface-700">
					<input type="text" bind:value={newMount.secretName} placeholder="k8s-secret-name"
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-2 py-1.5 text-xs text-white placeholder-gray-500 outline-none focus:border-accent" />
					<input type="text" bind:value={newMount.mountPath} placeholder="/etc/certs"
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-2 py-1.5 text-xs text-white placeholder-gray-500 outline-none focus:border-accent" />
					<div class="flex gap-2">
						<button type="button" onclick={addSecretMount} disabled={!newMount.secretName || !newMount.mountPath}
							class="rounded-md bg-accent px-2 py-1 text-xs text-white hover:bg-accent-hover disabled:opacity-50">Add</button>
						<button type="button" onclick={() => showAddMount = false}
							class="rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-600">Cancel</button>
					</div>
				</div>
			{:else}
				<button type="button" onclick={() => showAddMount = true}
					class="text-xs text-accent hover:text-accent-hover flex items-center gap-1">
					<Plus class="h-3 w-3" /> Add secret mount
				</button>
			{/if}
			{#if secretMounts.length > 0}
				<button type="button" onclick={saveSecretMounts} disabled={savingMounts}
					class="mt-2 {btnPrimary} block">
					{savingMounts ? 'Saving...' : 'Save mounts'}
				</button>
			{/if}
		</div>
	{/if}
</div>
