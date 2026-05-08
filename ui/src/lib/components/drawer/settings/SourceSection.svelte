<script lang="ts">
	import { api } from '$lib/api';
	import type { App, AppSpec } from '$lib/types';
	import { ChevronDown } from 'lucide-svelte';
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

	let srcRepo = $state('');
	let srcBranch = $state('');
	let srcPath = $state('');
	let srcImage = $state('');
	let srcPullSecretRef = $state('');
	let saving = $state(false);

	let pullRegistry = $state('');
	let pullUsername = $state('');
	let pullPassword = $state('');
	let pullHasPassword = $state(false);
	let pullConnected = $state(false);
	let savingPull = $state(false);
	let deletingPull = $state(false);
	let showAdvancedPull = $state(false);

	$effect(() => {
		srcRepo = app.spec.source.repo ?? '';
		srcBranch = app.spec.source.branch ?? '';
		srcPath = app.spec.source.path ?? '';
		srcImage = app.spec.source.image ?? '';
		srcPullSecretRef = app.spec.source.pullSecretRef ?? '';
	});

	import { onMount } from 'svelte';
	onMount(async () => {
		await loadPullCredentials();
	});

	async function loadPullCredentials() {
		if (app.spec.source.type !== 'image') return;
		try {
			const creds = await api.getPullCredentials(project, app.metadata.name);
			pullRegistry = creds.registry ?? '';
			pullUsername = creds.username ?? '';
			pullHasPassword = creds.hasPassword ?? false;
			pullConnected = !!creds.registry;
		} catch {
			// ignore
		}
	}

	async function savePullCredentials() {
		if (!pullRegistry || !pullUsername || !pullPassword) {
			onError('Registry, username, and password are all required');
			return;
		}
		savingPull = true;
		try {
			const creds = await api.setPullCredentials(project, app.metadata.name, pullRegistry, pullUsername, pullPassword);
			pullRegistry = creds.registry;
			pullUsername = creds.username;
			pullHasPassword = creds.hasPassword;
			pullConnected = true;
			pullPassword = '';
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to save pull credentials');
		} finally {
			savingPull = false;
		}
	}

	async function deletePullCredentials() {
		deletingPull = true;
		try {
			await api.deletePullCredentials(project, app.metadata.name);
			pullRegistry = '';
			pullUsername = '';
			pullPassword = '';
			pullHasPassword = false;
			pullConnected = false;
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to remove pull credentials');
		} finally {
			deletingPull = false;
		}
	}

	function buildUpdatedSpec(): AppSpec {
		const spec = cloneSpec();
		if (spec.source.type === 'git') {
			spec.source.repo = srcRepo;
			spec.source.branch = srcBranch;
			spec.source.path = srcPath;
		} else if (spec.source.type === 'image') {
			spec.source.image = srcImage;
			spec.source.pullSecretRef = srcPullSecretRef || undefined;
		}
		return spec;
	}

	async function saveSource() {
		saving = true;
		try {
			const result = await api.updateApp(project, app.metadata.name, buildUpdatedSpec());
			onSpecUpdate(result.spec);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to save');
		} finally {
			saving = false;
		}
	}
</script>

<div class={sectionCls}>
	<h3 class={headingCls}>Source</h3>
	<div class="space-y-2">
		<p class="text-xs text-gray-500">Type: <span class="text-gray-300">{app.spec.source.type}</span></p>
		{#if app.spec.source.type === 'git'}
			<div>
				<label class={labelCls} for="src-repo">Repository</label>
				<input id="src-repo" type="text" bind:value={srcRepo} placeholder="https://github.com/org/repo" class={inputCls} />
			</div>
			<div class="grid grid-cols-2 gap-2">
				<div>
					<label class={labelCls} for="src-branch">Branch</label>
					<input id="src-branch" type="text" bind:value={srcBranch} placeholder="main" class={inputCls} />
				</div>
				<div>
					<label class={labelCls} for="src-path">Path</label>
					<input id="src-path" type="text" bind:value={srcPath} placeholder="/" class={inputCls} />
				</div>
			</div>
		{:else if app.spec.source.type === 'image'}
			<div>
				<label class={labelCls} for="src-image">Image</label>
				<input id="src-image" type="text" bind:value={srcImage} placeholder="registry.example.com/app:latest" class={inputCls} />
			</div>

			<!-- Pull credentials -->
			<div class="rounded-md border border-surface-600 bg-surface-800 p-3 space-y-2">
				<h4 class="text-xs font-medium text-gray-400">Registry credentials</h4>
				{#if pullConnected}
					<div class="flex items-center justify-between">
						<p class="text-xs text-gray-300">
							Connected to <span class="font-medium text-white">{pullRegistry}</span> as <span class="font-medium text-white">{pullUsername}</span>
						</p>
						<button type="button" onclick={deletePullCredentials} disabled={deletingPull} class="rounded-md border border-danger/50 px-2 py-1 text-xs text-danger hover:bg-danger/10 transition-colors disabled:opacity-50">
							{deletingPull ? 'Removing…' : 'Remove'}
						</button>
					</div>
				{:else}
					<div>
						<label class={labelCls} for="pull-registry">Registry URL</label>
						<input id="pull-registry" type="text" bind:value={pullRegistry} placeholder="ghcr.io" class={inputCls} />
					</div>
					<div>
						<label class={labelCls} for="pull-username">Username</label>
						<input id="pull-username" type="text" bind:value={pullUsername} placeholder="username" class={inputCls} />
					</div>
					<div>
						<label class={labelCls} for="pull-password">Password / token</label>
						<input id="pull-password" type="password" bind:value={pullPassword} placeholder="••••••••" class={inputCls} />
					</div>
					<div class="flex justify-end">
						<button type="button" onclick={savePullCredentials} disabled={savingPull} class={btnPrimary}>
							{savingPull ? 'Saving…' : 'Save credentials'}
						</button>
					</div>
				{/if}
			</div>

			<!-- Advanced: manual k8s secret ref -->
			<div>
				<button type="button" onclick={() => showAdvancedPull = !showAdvancedPull}
					class="flex items-center gap-1 text-xs text-gray-500 hover:text-gray-300 transition-colors">
					<ChevronDown class="h-3 w-3 transition-transform {showAdvancedPull ? 'rotate-180' : ''}" />
					Advanced
				</button>
				{#if showAdvancedPull}
					<div class="mt-2">
						<label class={labelCls} for="src-pull-secret">Manual pull secret <span class="text-gray-600">(k8s Secret name)</span></label>
						<input id="src-pull-secret" type="text" bind:value={srcPullSecretRef} placeholder="my-registry-secret" class={inputCls} />
						<p class="mt-0.5 text-xs text-gray-500">Name of an existing k8s Secret you manage yourself. Overrides credentials above.</p>
					</div>
				{/if}
			</div>
		{/if}
	</div>
	<div class="flex justify-end pt-1">
		<button type="button" onclick={saveSource} disabled={saving} class={btnPrimary}>
			{saving ? 'Saving…' : 'Update'}
		</button>
	</div>
</div>
