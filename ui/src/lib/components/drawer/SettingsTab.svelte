<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { App, AppSpec, DeployToken, DomainsResponse } from '$lib/types';
	import { Copy, Plus, Trash2 } from 'lucide-svelte';

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
	let srcHost = $state('');
	let srcPort = $state('');

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

	// --- Deploy tokens ---
	let tokens = $state<DeployToken[]>([]);
	let loadingTokens = $state(true);
	let showTokenForm = $state(false);
	let newTokenName = $state('');
	let newTokenEnv = $state(app.spec.environments?.[0]?.name ?? '');
	let createdToken = $state<string | null>(null);
	let copiedToken = $state(false);

	// --- Danger ---
	let confirmDelete = $state(false);
	let deleteConfirmText = $state('');
	let deleting = $state(false);

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
		const spec = structuredClone(app.spec);

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
		try {
			const updated = await api.updateApp(project, app.metadata.name, buildUpdatedSpec());
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			saving = false;
		}
	}

	async function saveNetworking() {
		saving = true;
		errorMsg = '';
		try {
			const updated = await api.updateApp(project, app.metadata.name, buildUpdatedSpec());
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			saving = false;
		}
	}

	async function saveScale() {
		saving = true;
		errorMsg = '';
		try {
			const spec = structuredClone(app.spec);
			const envIdx = (spec.environments ?? []).findIndex((e) => e.name === scaleEnv);
			if (envIdx >= 0 && spec.environments) {
				spec.environments[envIdx].replicas = parseInt(scaleReplicas, 10) || 1;
				spec.environments[envIdx].resources = spec.environments[envIdx].resources ?? {};
				if (scaleCpu) spec.environments[envIdx].resources!.cpu = scaleCpu;
				if (scaleMemory) spec.environments[envIdx].resources!.memory = scaleMemory;
			}
			const updated = await api.updateApp(project, app.metadata.name, spec);
			onAppUpdated(updated);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			saving = false;
		}
	}

	async function handleAddDomain() {
		if (!newDomain.trim() || !domainsEnv) return;
		savingDomain = true;
		try {
			domains = await api.addDomain(project, app.metadata.name, domainsEnv, newDomain.trim());
			newDomain = '';
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to add domain';
		} finally {
			savingDomain = false;
		}
	}

	async function handleRemoveDomain(domain: string) {
		try {
			domains = await api.removeDomain(project, app.metadata.name, domainsEnv, domain);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to remove domain';
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
		try {
			await api.revokeToken(project, app.metadata.name, id);
			tokens = tokens.filter((t) => t.id !== id);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to revoke token';
		}
	}

	async function handleDelete() {
		if (deleteConfirmText !== app.metadata.name) return;
		deleting = true;
		try {
			await api.deleteApp(project, app.metadata.name);
			onAppDeleted();
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Failed to delete app';
			deleting = false;
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
		</div>
	{/if}

	<!-- Deploy Tokens -->
	{#if sectionVisible('deploy tokens')}
		<div class={sectionCls}>
			<h3 class={headingCls}>Deploy Tokens</h3>

			{#if createdToken}
				<div class="rounded-md border border-success/30 bg-success/10 p-3">
					<p class="mb-1.5 text-xs font-medium text-success">Token created — copy it now, it won't be shown again.</p>
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
