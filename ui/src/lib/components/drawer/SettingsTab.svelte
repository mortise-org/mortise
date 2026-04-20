<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { App, AppSpec, DeployToken, DomainsResponse, SecretMount } from '$lib/types';
	import { Copy, Plus, Trash2, Link, ChevronDown } from 'lucide-svelte';

	let {
		project,
		app,
		onAppUpdated,
		onAppDeleted
	}: {
		project: string;
		app: App;
		onAppUpdated: (app: App) => void;
		onAppDeleted: () => void;
	} = $props();

	let filterText = $state('');
	let errorMsg = $state('');
	let saving = $state(false);

	// --- Source ---
	let srcRepo = $state(app.spec.source.repo ?? '');
	let srcBranch = $state(app.spec.source.branch ?? '');
	let srcPath = $state(app.spec.source.path ?? '');
	let srcImage = $state(app.spec.source.image ?? '');

	// --- Build ---
	let buildMode = $state<'auto' | 'dockerfile' | 'railpack'>(app.spec.source.build?.mode ?? 'auto');
	let dockerfilePath = $state(app.spec.source.build?.dockerfilePath ?? '');
	let savingBuild = $state(false);

	// --- Networking ---
	let netPublic = $state(app.spec.network?.public ?? true);
	let netPort = $state(String(app.spec.network?.port ?? ''));

	// --- Scale ---
	let scaleEnv = $state(app.spec.environments?.[0]?.name ?? '');
	let scaleReplicas = $state(String(app.spec.environments?.[0]?.replicas ?? 1));
	let scaleCpu = $state(app.spec.environments?.[0]?.resources?.cpu ?? '');
	let scaleMemory = $state(app.spec.environments?.[0]?.resources?.memory ?? '');

	// --- Domains ---
	let domains = $state<DomainsResponse | null>(null);
	let domainsEnv = $state(app.spec.environments?.[0]?.name ?? '');
	let newDomain = $state('');
	let savingDomain = $state(false);

	// --- TLS overrides ---
	const env0Tls = (app.spec.environments?.[0] as { tls?: { clusterIssuer?: string; secretName?: string } })?.tls;
	let tlsClusterIssuer = $state(env0Tls?.clusterIssuer ?? '');
	let tlsSecretName = $state(env0Tls?.secretName ?? '');
	let savingTls = $state(false);

	// --- Deploy tokens ---
	let tokens = $state<DeployToken[]>([]);
	let loadingTokens = $state(true);
	let showTokenForm = $state(false);
	let newTokenName = $state('');
	let newTokenEnv = $state(app.spec.environments?.[0]?.name ?? '');
	let createdToken = $state<string | null>(null);
	let copiedToken = $state(false);

	// --- Bindings ---
	let showAddBinding = $state(false);
	let newBindingRef = $state('');
	let savingBinding = $state(false);
	let allApps = $state<App[]>([]);
	$effect(() => {
		api.listApps(project).then(a => allApps = a).catch(() => {});
	});
	const currentBindings = $derived(app.spec.environments?.[0]?.bindings ?? []);
	const bindableApps = $derived(allApps.filter(a =>
		a.metadata.name !== app.metadata.name &&
		a.spec.credentials && a.spec.credentials.length > 0
	));

	// --- Advanced ---
	let showAdvanced = $state(false);
	// Environment type doesn't include annotations/secretMounts at the base type;
	// cast to access the extended fields that the API may return.
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	const env0Ext = (app.spec.environments?.[0] as any) ?? {};
	let annotations = $state<Record<string, string>>(
		Object.fromEntries(Object.entries((env0Ext.annotations ?? {}) as Record<string, string>))
	);
	let savingAnnotations = $state(false);
	let secretMounts = $state<SecretMount[]>((env0Ext.secretMounts ?? []) as SecretMount[]);
	let showAddMount = $state(false);
	let newMount = $state<{ secretName: string; mountPath: string }>({ secretName: '', mountPath: '' });
	let savingMounts = $state(false);

	// --- Danger ---
	let confirmDelete = $state(false);
	let deleteConfirmText = $state('');
	let deleting = $state(false);

	// --- Storage ---
	let showAddVolume = $state(false);
	let newVol = $state({ name: '', mountPath: '', size: '', storageClass: '' });
	let savingVolume = $state(false);

	async function addVolume() {
		if (!newVol.name || !newVol.mountPath) return;
		savingVolume = true;
		const prevApp = app;
		const prevVol = { ...newVol };
		const optimisticSpec = JSON.parse(JSON.stringify(app.spec));
		optimisticSpec.storage = [...(optimisticSpec.storage ?? []), {
			name: newVol.name,
			mountPath: newVol.mountPath,
			size: newVol.size || undefined,
			storageClass: newVol.storageClass || undefined
		}];
		onAppUpdated({ ...app, spec: optimisticSpec });
		showAddVolume = false;
		newVol = { name: '', mountPath: '', size: '', storageClass: '' };
		try {
			const updated = await api.updateApp(project, prevApp.metadata.name, optimisticSpec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to add volume';
			onAppUpdated(prevApp);
			showAddVolume = true;
			newVol = prevVol;
		} finally {
			savingVolume = false;
		}
	}

	async function removeVolume(idx: number) {
		const prevApp = app;
		const optimisticSpec = JSON.parse(JSON.stringify(app.spec));
		optimisticSpec.storage = (optimisticSpec.storage ?? []).filter((_: unknown, i: number) => i !== idx);
		onAppUpdated({ ...app, spec: optimisticSpec });
		try {
			const updated = await api.updateApp(project, prevApp.metadata.name, optimisticSpec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to remove volume';
			onAppUpdated(prevApp);
		}
	}

	onMount(async () => {
		await Promise.all([loadDomains(), loadTokens()]);
	});

	async function loadDomains() {
		if (!domainsEnv) return;
		try {
			domains = await api.listDomains(project, app.metadata.name, domainsEnv);
		} catch {
			domains = null;
		}
	}

	async function loadTokens() {
		loadingTokens = true;
		try {
			tokens = await api.listTokens(project, app.metadata.name);
		} catch {
			tokens = [];
		} finally {
			loadingTokens = false;
		}
	}

	function buildUpdatedSpec(): AppSpec {
		const spec = JSON.parse(JSON.stringify(app.spec));

		// Source
		if (spec.source.type === 'git') {
			spec.source.repo = srcRepo;
			spec.source.branch = srcBranch;
			spec.source.path = srcPath;
		} else if (spec.source.type === 'image') {
			spec.source.image = srcImage;
		}

		// Networking
		spec.network = spec.network ?? {};
		spec.network.public = netPublic;
		if (netPort) spec.network.port = parseInt(netPort, 10);

		return spec;
	}

	async function saveSource() {
		saving = true;
		errorMsg = '';
		const prevApp = app;
		const optimisticSpec = buildUpdatedSpec();
		onAppUpdated({ ...app, spec: optimisticSpec });
		try {
			const updated = await api.updateApp(project, prevApp.metadata.name, optimisticSpec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save';
			onAppUpdated(prevApp);
		} finally {
			saving = false;
		}
	}

	async function saveBuild() {
		savingBuild = true;
		const prevApp = app;
		const optimisticSpec = JSON.parse(JSON.stringify(app.spec));
		optimisticSpec.source = {
			...optimisticSpec.source,
			build: {
				mode: buildMode,
				dockerfilePath: buildMode === 'dockerfile' ? dockerfilePath : undefined
			}
		};
		onAppUpdated({ ...app, spec: optimisticSpec });
		try {
			const updated = await api.updateApp(project, prevApp.metadata.name, optimisticSpec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save build config';
			onAppUpdated(prevApp);
		} finally {
			savingBuild = false;
		}
	}

	async function saveNetworking() {
		saving = true;
		errorMsg = '';
		const prevApp = app;
		const optimisticSpec = buildUpdatedSpec();
		onAppUpdated({ ...app, spec: optimisticSpec });
		try {
			const updated = await api.updateApp(project, prevApp.metadata.name, optimisticSpec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save';
			onAppUpdated(prevApp);
		} finally {
			saving = false;
		}
	}

	async function saveScale() {
		saving = true;
		errorMsg = '';
		const prevApp = app;
		const optimisticSpec = JSON.parse(JSON.stringify(app.spec));
		const envIdx = (optimisticSpec.environments ?? []).findIndex((e: { name: string }) => e.name === scaleEnv);
		if (envIdx >= 0 && optimisticSpec.environments) {
			optimisticSpec.environments[envIdx].replicas = parseInt(scaleReplicas, 10) || 1;
			optimisticSpec.environments[envIdx].resources = optimisticSpec.environments[envIdx].resources ?? {};
			if (scaleCpu) optimisticSpec.environments[envIdx].resources!.cpu = scaleCpu;
			if (scaleMemory) optimisticSpec.environments[envIdx].resources!.memory = scaleMemory;
		}
		onAppUpdated({ ...app, spec: optimisticSpec });
		try {
			const updated = await api.updateApp(project, prevApp.metadata.name, optimisticSpec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save';
			onAppUpdated(prevApp);
		} finally {
			saving = false;
		}
	}

	async function handleAddDomain() {
		if (!newDomain.trim() || !domainsEnv) return;
		savingDomain = true;
		const domainToAdd = newDomain.trim();
		const prevDomains = domains;
		domains = domains ? { ...domains, custom: [...(domains.custom ?? []), domainToAdd] } : domains;
		newDomain = '';
		try {
			domains = await api.addDomain(project, app.metadata.name, domainsEnv, domainToAdd);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to add domain';
			domains = prevDomains;
			newDomain = domainToAdd;
		} finally {
			savingDomain = false;
		}
	}

	async function handleRemoveDomain(domain: string) {
		const prevDomains = domains;
		domains = domains ? { ...domains, custom: (domains.custom ?? []).filter(d => d !== domain) } : domains;
		try {
			domains = await api.removeDomain(project, app.metadata.name, domainsEnv, domain);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to remove domain';
			domains = prevDomains;
		}
	}

	async function saveTlsOverride() {
		savingTls = true;
		try {
			const spec = JSON.parse(JSON.stringify(app.spec));
			if (spec.environments?.[0]) {
				(spec.environments[0] as { tls?: { clusterIssuer?: string; secretName?: string } }).tls = {
					clusterIssuer: tlsClusterIssuer || undefined,
					secretName: tlsSecretName || undefined
				};
			}
			const updated = await api.updateApp(project, app.metadata.name, spec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save TLS config';
		} finally {
			savingTls = false;
		}
	}

	async function createToken() {
		if (!newTokenName.trim() || !newTokenEnv) return;
		saving = true;
		try {
			const tok = await api.createToken(project, app.metadata.name, newTokenName.trim(), newTokenEnv);
			tokens = [...tokens, tok];
			createdToken = tok.token ?? null;
			newTokenName = '';
			showTokenForm = false;
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to create token';
		} finally {
			saving = false;
		}
	}

	async function revokeToken(id: string) {
		const prev = tokens;
		tokens = tokens.filter((t) => t.id !== id);
		try {
			await api.revokeToken(project, app.metadata.name, id);
		} catch (e) {
			tokens = prev;
			errorMsg = e instanceof Error ? e.message : 'Failed to revoke token';
		}
	}

	async function addBinding() {
		if (!newBindingRef) return;
		savingBinding = true;
		const prevApp = app;
		const savedRef = newBindingRef;
		const optimisticSpec = JSON.parse(JSON.stringify(app.spec));
		optimisticSpec.environments = (optimisticSpec.environments ?? []).map((e: typeof optimisticSpec.environments[0]) => ({
			...e,
			bindings: [...(e.bindings ?? []), { ref: savedRef }]
		}));
		onAppUpdated({ ...app, spec: optimisticSpec });
		showAddBinding = false;
		newBindingRef = '';
		try {
			const updated = await api.updateApp(project, prevApp.metadata.name, optimisticSpec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to add binding';
			onAppUpdated(prevApp);
			showAddBinding = true;
			newBindingRef = savedRef;
		} finally {
			savingBinding = false;
		}
	}

	async function removeBinding(ref: string) {
		const prevApp = app;
		const optimisticSpec = JSON.parse(JSON.stringify(app.spec));
		optimisticSpec.environments = (optimisticSpec.environments ?? []).map((e: typeof optimisticSpec.environments[0]) => ({
			...e,
			bindings: (e.bindings ?? []).filter((b: { ref: string }) => b.ref !== ref)
		}));
		onAppUpdated({ ...app, spec: optimisticSpec });
		try {
			const updated = await api.updateApp(project, prevApp.metadata.name, optimisticSpec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to remove binding';
			onAppUpdated(prevApp);
		}
	}

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
		savingAnnotations = true;
		try {
			const spec = JSON.parse(JSON.stringify(app.spec));
			spec.environments = (spec.environments ?? []).map((e: typeof spec.environments[0], i: number) =>
				i === 0 ? { ...e, annotations } : e
			);
			const updated = await api.updateApp(project, app.metadata.name, spec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save annotations';
		} finally {
			savingAnnotations = false;
		}
	}

	function addSecretMount() {
		if (!newMount.secretName || !newMount.mountPath) return;
		secretMounts = [...secretMounts, { name: newMount.secretName, secretName: newMount.secretName, mountPath: newMount.mountPath }];
		newMount = { secretName: '', mountPath: '' };
		showAddMount = false;
		void saveSecretMounts();
	}

	function removeSecretMount(i: number) {
		secretMounts = secretMounts.filter((_, idx) => idx !== i);
		void saveSecretMounts();
	}

	async function saveSecretMounts() {
		savingMounts = true;
		try {
			const spec = JSON.parse(JSON.stringify(app.spec));
			spec.environments = (spec.environments ?? []).map((e: typeof spec.environments[0], i: number) =>
				i === 0 ? { ...e, secretMounts } : e
			);
			const updated = await api.updateApp(project, app.metadata.name, spec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save mounts';
		} finally {
			savingMounts = false;
		}
	}

	async function handleDelete() {
		if (deleteConfirmText !== app.metadata.name) return;
		deleting = true;
		onAppDeleted();
		try {
			await api.deleteApp(project, app.metadata.name);
		} catch {
			// drawer is already closed; error is unrecoverable from the user's perspective
		}
	}

	async function copyText(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			copiedToken = true;
			setTimeout(() => (copiedToken = false), 1500);
		} catch {
			// ignore
		}
	}

	function sectionVisible(name: string): boolean {
		if (!filterText) return true;
		return name.toLowerCase().includes(filterText.toLowerCase());
	}

	const inputCls =
		'w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent';
	const labelCls = 'block text-xs text-gray-500 mb-1';
	const sectionCls = 'rounded-lg border border-surface-600 bg-surface-900 p-4 space-y-3';
	const headingCls = 'text-xs font-semibold uppercase tracking-wide text-gray-400 mb-3';
	const btnPrimary =
		'rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50 transition-colors';
	const btnSecondary =
		'rounded-md border border-surface-600 px-3 py-1.5 text-xs text-gray-400 hover:text-white transition-colors';
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

	<!-- Source -->
	{#if sectionVisible('source')}
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
				{/if}
			</div>
			<div class="flex justify-end pt-1">
				<button type="button" onclick={saveSource} disabled={saving} class={btnPrimary}>
					{saving ? 'Saving…' : 'Update'}
				</button>
			</div>
		</div>
	{/if}

	<!-- Build -->
	{#if sectionVisible('build')}
		<div class={sectionCls}>
			<h3 class={headingCls}>Build</h3>
			<div class="space-y-3">
				<div>
					<label class={labelCls} for="build-mode">Build mode</label>
					<select id="build-mode" bind:value={buildMode}
						class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
						<option value="auto">Auto-detect</option>
						<option value="dockerfile">Dockerfile</option>
						<option value="railpack">Railpack / Nixpacks</option>
					</select>
				</div>
				{#if buildMode === 'dockerfile'}
					<div>
						<label class={labelCls} for="dockerfile-path">Dockerfile path</label>
						<input id="dockerfile-path" type="text" bind:value={dockerfilePath} placeholder="Dockerfile"
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
					</div>
				{/if}
			</div>
			<button type="button" onclick={saveBuild} disabled={savingBuild}
				class={btnPrimary}>
				{savingBuild ? 'Saving...' : 'Save build config'}
			</button>
		</div>
	{/if}

	<!-- Networking -->
	{#if sectionVisible('networking')}
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
				{#if app.spec.environments?.[0]?.domain}
					<div>
						<label class={labelCls}>Primary domain</label>
						<div class="flex items-center gap-2">
							<p class="flex-1 rounded-md bg-surface-700 px-3 py-1.5 font-mono text-xs text-gray-300">
								{app.spec.environments[0].domain}
							</p>
							<button
								type="button"
								onclick={() => copyText(app.spec.environments![0].domain!)}
								class="text-gray-500 hover:text-white"
								aria-label="Copy domain"
							>
								<Copy class="h-3.5 w-3.5" />
							</button>
						</div>
					</div>
				{/if}
			</div>
			<div class="flex justify-end pt-1">
				<button type="button" onclick={saveNetworking} disabled={saving} class={btnPrimary}>
					{saving ? 'Saving…' : 'Update'}
				</button>
			</div>
		</div>
	{/if}

	<!-- Bindings -->
	{#if sectionVisible('bindings')}
		<div class={sectionCls}>
			<div class="flex items-center justify-between">
				<h3 class={headingCls} style="margin-bottom:0">Bindings</h3>
				<button type="button" onclick={() => showAddBinding = true}
					class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
					<Plus class="h-3.5 w-3.5" /> Add binding
				</button>
			</div>

			{#each currentBindings as binding}
				<div class="flex items-center justify-between rounded-md border border-surface-600 px-3 py-2">
					<div class="flex items-center gap-2">
						<Link class="h-4 w-4 text-gray-400" />
						<span class="text-sm text-white">{binding.ref}</span>
						{#if binding.project}
							<span class="rounded-full bg-info/10 px-1.5 py-0.5 text-xs text-info">{binding.project}</span>
						{/if}
					</div>
					<button type="button" onclick={() => removeBinding(binding.ref)}
						class="rounded p-1 text-gray-500 hover:bg-surface-600 hover:text-danger">
						<Trash2 class="h-3.5 w-3.5" />
					</button>
				</div>
			{/each}

			{#if currentBindings.length === 0 && !showAddBinding}
				<p class="text-xs text-gray-500">No bindings. Add a binding to inject credentials from another app.</p>
			{/if}

			{#if showAddBinding}
				<div class="rounded-md border border-surface-600 bg-surface-700 p-3 space-y-2">
					<p class="text-xs font-medium text-gray-300">Add binding</p>
					<div>
						<label class={labelCls} for="binding-ref">App to bind</label>
						<select id="binding-ref" bind:value={newBindingRef}
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
							<option value="">Select an app...</option>
							{#each bindableApps as a}
								<option value={a.metadata.name}>{a.metadata.name}</option>
							{/each}
						</select>
					</div>
					{#if newBindingRef}
						<div class="rounded-md bg-surface-800 p-2 text-xs text-gray-400">
							<p class="font-medium text-gray-300 mb-1">Will inject:</p>
							{#each (allApps.find(a => a.metadata.name === newBindingRef)?.spec.credentials ?? []) as cred}
								<span class="mr-2 font-mono text-gray-300">{cred.name}</span>
							{/each}
						</div>
					{/if}
					<div class="flex gap-2">
						<button type="button" onclick={addBinding} disabled={!newBindingRef || savingBinding}
							class={btnPrimary}>
							{savingBinding ? 'Adding...' : 'Add'}
						</button>
						<button type="button" onclick={() => { showAddBinding = false; newBindingRef = ''; }}
							class={btnSecondary}>
							Cancel
						</button>
					</div>
				</div>
			{/if}
		</div>
	{/if}

	<!-- Scale -->
	{#if sectionVisible('scale')}
		<div class={sectionCls}>
			<h3 class={headingCls}>Scale</h3>
			<div class="space-y-2">
				{#if app.spec.environments && app.spec.environments.length > 1}
					<div>
						<label class={labelCls} for="scale-env">Environment</label>
						<select id="scale-env" bind:value={scaleEnv} class="{inputCls} bg-surface-700">
							{#each app.spec.environments as env}
								<option value={env.name}>{env.name}</option>
							{/each}
						</select>
					</div>
				{/if}
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
	{/if}

	<!-- Storage -->
	{#if sectionVisible('storage volumes')}
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
							<input id="vol-name" type="text" bind:value={newVol.name} placeholder="data" class={inputCls} />
						</div>
						<div>
							<label class={labelCls} for="vol-mount">Mount path</label>
							<input id="vol-mount" type="text" bind:value={newVol.mountPath} placeholder="/data" class={inputCls} />
						</div>
						<div>
							<label class={labelCls} for="vol-size">Size</label>
							<input id="vol-size" type="text" bind:value={newVol.size} placeholder="5Gi" class={inputCls} />
						</div>
						<div>
							<label class={labelCls} for="vol-class">Storage class</label>
							<input id="vol-class" type="text" bind:value={newVol.storageClass} placeholder="standard" class={inputCls} />
						</div>
					</div>
					<div class="flex gap-2">
						<button type="button" onclick={addVolume} disabled={!newVol.name || !newVol.mountPath || savingVolume}
							class={btnPrimary}>
							{savingVolume ? 'Adding...' : 'Add'}
						</button>
						<button type="button" onclick={() => { showAddVolume = false; newVol = { name: '', mountPath: '', size: '', storageClass: '' }; }}
							class={btnSecondary}>
							Cancel
						</button>
					</div>
				</div>
			{/if}
		</div>
	{/if}

	<!-- Domains -->
	{#if sectionVisible('domains')}
		<div class={sectionCls}>
			<h3 class={headingCls}>Domains</h3>

			{#if app.spec.environments && app.spec.environments.length > 1}
				<div class="flex gap-1 border-b border-surface-700 pb-2">
					{#each app.spec.environments as env}
						<button
							type="button"
							onclick={async () => { domainsEnv = env.name; await loadDomains(); }}
							class="rounded px-2.5 py-1 text-xs {domainsEnv === env.name ? 'bg-surface-600 text-white' : 'text-gray-400 hover:text-white'}"
						>
							{env.name}
						</button>
					{/each}
				</div>
			{/if}

			{#if domains?.primary}
				<div class="rounded-md bg-surface-700 px-3 py-2">
					<p class="text-xs text-gray-500">Primary</p>
					<p class="font-mono text-xs text-gray-200">{domains.primary}</p>
				</div>
			{/if}

			{#if domains?.custom && domains.custom.length > 0}
				<div class="space-y-1.5">
					{#each domains.custom as d}
						<div class="flex items-center justify-between rounded-md bg-surface-700 px-3 py-2">
							<span class="font-mono text-xs text-gray-200">{d}</span>
							<button
								type="button"
								onclick={() => handleRemoveDomain(d)}
								class="text-xs text-gray-500 hover:text-danger"
							>
								Remove
							</button>
						</div>
					{/each}
				</div>
			{/if}

			<div class="flex gap-2">
				<input
					type="text"
					bind:value={newDomain}
					placeholder="custom.example.com"
					class="{inputCls} flex-1"
				/>
				<button
					type="button"
					onclick={handleAddDomain}
					disabled={savingDomain || !newDomain.trim()}
					class={btnPrimary}
				>
					{savingDomain ? 'Adding…' : 'Add'}
				</button>
			</div>

			<!-- TLS overrides (advanced) -->
			<details class="mt-3">
				<summary class="cursor-pointer text-xs text-gray-500 hover:text-gray-300">TLS overrides (advanced)</summary>
				<div class="mt-2 space-y-2 rounded-md border border-surface-600 bg-surface-700/50 p-3">
					<div class="rounded-md border border-warning/20 bg-warning/5 p-2 text-xs text-warning">
						These override the platform-wide cert-manager issuer for this app only.
					</div>
					<div>
						<label class={labelCls} for="tls-issuer-ovr">Cluster issuer override</label>
						<input id="tls-issuer-ovr" type="text" bind:value={tlsClusterIssuer}
							placeholder="letsencrypt-staging"
							class={inputCls} />
					</div>
					<div>
						<label class={labelCls} for="tls-secret-ovr">TLS secret name override</label>
						<input id="tls-secret-ovr" type="text" bind:value={tlsSecretName}
							placeholder="my-tls-secret"
							class={inputCls} />
						<p class="text-xs text-gray-500 mt-0.5">Mutually exclusive with cluster issuer</p>
					</div>
					<button type="button" onclick={saveTlsOverride} disabled={savingTls}
						class={btnPrimary}>
						{savingTls ? 'Saving…' : 'Save TLS overrides'}
					</button>
				</div>
			</details>
		</div>
	{/if}

	<!-- Deploy Tokens -->
	{#if sectionVisible('deploy tokens')}
		<div class={sectionCls}>
			<h3 class={headingCls}>Deploy Tokens</h3>

			{#if createdToken}
				<div class="rounded-md border border-success/30 bg-success/10 p-3">
					<p class="mb-1.5 text-xs font-medium text-success">Token created - copy it now, it won't be shown again.</p>
					<div class="flex items-center gap-2">
						<code class="flex-1 truncate rounded bg-surface-800 px-2 py-1 font-mono text-xs text-gray-300">
							{createdToken}
						</code>
						<button
							type="button"
							onclick={() => copyText(createdToken!)}
							class="text-gray-400 hover:text-white"
							aria-label="Copy token"
						>
							{#if copiedToken}
								<span class="text-xs text-success">Copied!</span>
							{:else}
								<Copy class="h-3.5 w-3.5" />
							{/if}
						</button>
					</div>
					<button
						type="button"
						onclick={() => (createdToken = null)}
						class="mt-2 text-xs text-gray-500 hover:text-white"
					>
						Dismiss
					</button>
				</div>
			{/if}

			{#if loadingTokens}
				<div class="h-8 animate-pulse rounded bg-surface-700"></div>
			{:else if tokens.length > 0}
				<div class="space-y-1.5">
					{#each tokens as tok}
						<div class="flex items-center justify-between rounded-md bg-surface-700 px-3 py-2">
							<div>
								<p class="text-xs font-medium text-white">{tok.name}</p>
								<p class="text-xs text-gray-500">{tok.environment} · created {new Date(tok.createdAt).toLocaleDateString()}</p>
							</div>
							<button
								type="button"
								onclick={() => revokeToken(tok.id)}
								class="flex items-center gap-1 text-xs text-gray-500 hover:text-danger"
							>
								<Trash2 class="h-3 w-3" /> Revoke
							</button>
						</div>
					{/each}
				</div>
			{/if}

			{#if showTokenForm}
				<div class="space-y-2 rounded-md border border-surface-600 p-3">
					<div>
						<label class={labelCls} for="tok-name">Token name</label>
						<input id="tok-name" type="text" bind:value={newTokenName} placeholder="ci-deploy" class={inputCls} />
					</div>
					<div>
						<label class={labelCls} for="tok-env">Environment</label>
						<select id="tok-env" bind:value={newTokenEnv} class="{inputCls} bg-surface-700">
							{#each app.spec.environments ?? [] as env}
								<option value={env.name}>{env.name}</option>
							{/each}
						</select>
					</div>
					<div class="flex justify-end gap-2">
						<button type="button" onclick={() => (showTokenForm = false)} class={btnSecondary}>Cancel</button>
						<button type="button" onclick={createToken} disabled={saving || !newTokenName.trim()} class={btnPrimary}>
							Create
						</button>
					</div>
				</div>
			{:else}
				<button
					type="button"
					onclick={() => (showTokenForm = true)}
					class="flex items-center gap-1 {btnSecondary}"
				>
					<Plus class="h-3 w-3" /> Create token
				</button>
			{/if}
		</div>
	{/if}

	<!-- Config-as-code -->
	{#if sectionVisible('gitops config')}
		<div class={sectionCls}>
			<h3 class={headingCls}>Config-as-code (GitOps)</h3>
			<div class="rounded-md border border-surface-600 bg-surface-700/50 p-3 text-xs text-gray-400">
				<p>Managing this App via Argo CD or Flux? Add <code class="font-mono bg-surface-700 px-1 rounded">ignoreDifferences</code> for <code class="font-mono bg-surface-700 px-1 rounded">env</code> and <code class="font-mono bg-surface-700 px-1 rounded">sharedVars</code> fields to prevent drift conflicts.</p>
				<a href="https://docs.mortise.dev/gitops" target="_blank" rel="noopener noreferrer" class="mt-2 inline-block text-accent hover:underline">View GitOps guide →</a>
			</div>
		</div>
	{/if}

	<!-- Advanced -->
	{#if sectionVisible('advanced annotations mounts')}
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
								<span class="font-mono text-gray-300">{mount.mountPath}</span>
								<button type="button" onclick={() => removeSecretMount(i)}
									class="text-gray-500 hover:text-danger"><Trash2 class="h-3 w-3" /></button>
							</div>
							<p class="text-gray-500">Secret: <span class="font-mono">{mount.secretName}</span></p>
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
	{/if}

	<!-- Danger Zone -->
	{#if sectionVisible('danger delete')}
		<div class="rounded-md border border-danger/30 bg-danger/5 p-4">
			<h3 class="mb-3 text-sm font-medium text-danger">Danger Zone</h3>
			{#if confirmDelete}
				<div class="space-y-2">
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
							onclick={() => { confirmDelete = false; deleteConfirmText = ''; }}
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
						onclick={() => (confirmDelete = true)}
						class="rounded-md bg-danger px-3 py-1.5 text-sm font-medium text-white hover:bg-danger/80"
					>
						Delete
					</button>
				</div>
			{/if}
		</div>
	{/if}
</div>
