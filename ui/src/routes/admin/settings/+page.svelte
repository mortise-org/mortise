<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import { goto } from '$app/navigation';
	import type { GitProviderSummary, PlatformResponse } from '$lib/types';
	import { GitBranch, Plus, Trash2 } from 'lucide-svelte';

	let platform = $state<PlatformResponse | null>(null);
	let providers = $state<GitProviderSummary[]>([]);
	let loading = $state(true);
	let filterText = $state('');
	let saving = $state(false);
	let error = $state('');

	// Platform form
	let domain = $state('');
	let dnsProvider = $state('cloudflare');
	let dnsToken = $state('');
	let clusterIssuer = $state('');

	// --- Registry ---
	let registryUrl = $state('');
	let registryNamespace = $state('');
	let registryUsername = $state('');
	let savingRegistry = $state(false);

	// --- Build ---
	let buildkitAddress = $state('');
	let buildkitPlatform = $state('linux/amd64');
	let savingBuild = $state(false);

	// --- Storage ---
	let defaultStorageClass = $state('');
	let savingStorage = $state(false);

	// --- TLS ---
	let tlsClusterIssuer = $state('');
	let savingTls = $state(false);

	// New git provider form (GitLab/Gitea only - GitHub is per-user)
	let showProviderForm = $state(false);
	let newProviderName = $state('');
	let newProviderType = $state<'github' | 'gitlab' | 'gitea'>('github');
	let newProviderHost = $state('');
	let newProviderClientID = $state('');
	let newProviderClientSecret = $state('');
	let newProviderWebhookSecret = $state('');
	let creatingProvider = $state(false);

	onMount(async () => {
		if (!store.isAdmin) {
			goto('/');
			return;
		}
		try {
			[platform, providers] = await Promise.all([
				api.getPlatform().catch(() => null),
				api.listGitProviders().catch(() => [])
			]);
			if (platform) {
				domain = platform.domain ?? '';
				dnsProvider = platform.dns?.provider ?? 'cloudflare';
				clusterIssuer = platform.tls?.certManagerClusterIssuer ?? '';
				tlsClusterIssuer = platform.tls?.certManagerClusterIssuer ?? '';
				defaultStorageClass = platform.storage?.defaultStorageClass ?? '';
			}
		} finally {
			loading = false;
		}
	});

	async function savePlatform() {
		saving = true;
		error = '';
		try {
			await api.patchPlatform({
				domain,
				dns: dnsToken
					? { provider: dnsProvider, apiTokenSecretRef: dnsToken }
					: { provider: dnsProvider, apiTokenSecretRef: '' }
			});
		} catch (e) {
			error = e instanceof Error ? e.message : 'Save failed';
		} finally {
			saving = false;
		}
	}

	async function createProvider() {
		creatingProvider = true;
		error = '';
		try {
			await api.createGitProvider({
				name: newProviderName,
				type: newProviderType,
				host: newProviderHost,
				oauth: { clientID: newProviderClientID, clientSecret: newProviderClientSecret },
				webhookSecret: newProviderWebhookSecret
			});
			providers = await api.listGitProviders();
			showProviderForm = false;
			newProviderName = '';
			newProviderClientID = '';
			newProviderClientSecret = '';
			newProviderWebhookSecret = '';
			newProviderHost = '';
		} catch (e) {
			error = e instanceof Error ? e.message : 'Create failed';
		} finally {
			creatingProvider = false;
		}
	}

	async function deleteProvider(name: string) {
		if (!confirm(`Delete git provider "${name}"?`)) return;
		try {
			await api.deleteGitProvider(name);
			providers = providers.filter((p) => p.name !== name);
		} catch {
			// ignore
		}
	}

	async function saveRegistry() {
		savingRegistry = true;
		try {
			await api.patchPlatform({});
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to save registry config';
		} finally {
			savingRegistry = false;
		}
	}

	async function saveBuildConfig() {
		savingBuild = true;
		try {
			await api.patchPlatform({});
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to save build config';
		} finally {
			savingBuild = false;
		}
	}

	async function saveStorage() {
		savingStorage = true;
		try {
			await api.patchPlatform({ storage: { defaultStorageClass } });
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to save storage config';
		} finally {
			savingStorage = false;
		}
	}

	async function saveTls() {
		savingTls = true;
		try {
			await api.patchPlatform({ tls: { certManagerClusterIssuer: tlsClusterIssuer } });
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to save TLS config';
		} finally {
			savingTls = false;
		}
	}

	// Section visibility based on filter text
	const sectionKeywords: Record<string, string[]> = {
		general: ['general', 'domain', 'platform'],
		dns: ['dns', 'cloudflare', 'route53'],
		registry: ['registry', 'oci', 'zot', 'image'],
		build: ['build', 'buildkit', 'container'],
		storage: ['storage', 'storageclass', 'pvc', 'volume'],
		tls: ['tls', 'cert', 'issuer', 'ssl'],
		'git-providers': ['git', 'provider', 'github', 'gitlab', 'gitea', 'oauth'],
		users: ['users', 'invite', 'admin']
	};
	function sectionVisible(id: string): boolean {
		if (!filterText) return true;
		const q = filterText.toLowerCase();
		return (sectionKeywords[id] ?? [id]).some((k) => k.includes(q));
	}
</script>

<div class="mx-auto max-w-3xl p-8">
	<div class="mb-6">
		<h1 class="text-xl font-semibold text-white">Platform Settings</h1>
		<p class="mt-1 text-sm text-gray-500">Admin-only platform configuration</p>
	</div>

	<input
		type="text"
		bind:value={filterText}
		placeholder="Filter settings..."
		class="mb-6 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
	/>

	{#if error}
		<div class="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
	{/if}

	<!-- General -->
	<section class="mb-8 space-y-4" id="general" style:display={sectionVisible('general') ? '' : 'none'}>
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">General</h2>
		<div>
			<label class="text-sm text-gray-400">Platform Domain</label>
			<input
				type="text"
				bind:value={domain}
				placeholder="yourdomain.com"
				class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
			/>
		</div>
		<button
			type="button"
			onclick={savePlatform}
			disabled={saving}
			class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50"
		>
			{saving ? 'Saving...' : 'Save'}
		</button>
	</section>

	<!-- DNS -->
	<section class="mb-8 space-y-4" id="dns" style:display={sectionVisible('dns') ? '' : 'none'}>
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">DNS</h2>
		<div>
			<label class="text-sm text-gray-400">Provider</label>
			<select
				bind:value={dnsProvider}
				class="mt-1 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
			>
				<option value="cloudflare">Cloudflare</option>
				<option value="route53">Route 53</option>
				<option value="externaldns-noop">ExternalDNS (noop)</option>
			</select>
		</div>
	</section>

	<!-- Registry -->
	<section id="registry" class="mb-8 space-y-4" style:display={sectionVisible('registry') ? '' : 'none'}>
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Registry</h2>
		<p class="text-xs text-gray-500">OCI registry for storing built images. Defaults to the bundled Zot registry.</p>
		<div class="space-y-3 rounded-md border border-surface-600 bg-surface-800/50 p-4">
			<div>
				<label class="block text-xs text-gray-500 mb-1" for="reg-url">Registry URL</label>
				<input id="reg-url" type="text" bind:value={registryUrl}
					placeholder="registry.example.com (leave empty for built-in Zot)"
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
			</div>
			<div class="grid grid-cols-2 gap-3">
				<div>
					<label class="block text-xs text-gray-500 mb-1" for="reg-ns">Namespace / prefix</label>
					<input id="reg-ns" type="text" bind:value={registryNamespace}
						placeholder="mortise"
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
				</div>
				<div>
					<label class="block text-xs text-gray-500 mb-1" for="reg-user">Username</label>
					<input id="reg-user" type="text" bind:value={registryUsername}
						placeholder="admin"
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
				</div>
			</div>
			<div class="rounded-md border border-info/20 bg-info/5 p-3 text-xs text-info">
				Registry credentials are managed via Kubernetes Secrets. See the <a href="/extensions" class="underline">Extensions</a> page for ESO integration.
			</div>
			<button type="button" onclick={saveRegistry} disabled={savingRegistry}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
				{savingRegistry ? 'Saving…' : 'Save registry config'}
			</button>
		</div>
	</section>

	<!-- Build -->
	<section id="build" class="mb-8 space-y-4" style:display={sectionVisible('build') ? '' : 'none'}>
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Build</h2>
		<p class="text-xs text-gray-500">BuildKit configuration for building container images from source.</p>
		<div class="space-y-3 rounded-md border border-surface-600 bg-surface-800/50 p-4">
			<div>
				<label class="block text-xs text-gray-500 mb-1" for="bk-addr">BuildKit address</label>
				<input id="bk-addr" type="text" bind:value={buildkitAddress}
					placeholder="tcp://buildkitd.mortise-system:1234 (leave empty for built-in)"
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none font-mono focus:border-accent" />
			</div>
			<div>
				<label class="block text-xs text-gray-500 mb-1" for="bk-platform">Default platform</label>
				<select id="bk-platform" bind:value={buildkitPlatform}
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
					<option value="linux/amd64">linux/amd64</option>
					<option value="linux/arm64">linux/arm64</option>
					<option value="linux/amd64,linux/arm64">linux/amd64 + linux/arm64 (multi)</option>
				</select>
			</div>
			<button type="button" onclick={saveBuildConfig} disabled={savingBuild}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
				{savingBuild ? 'Saving…' : 'Save build config'}
			</button>
		</div>
	</section>

	<!-- Storage -->
	<section id="storage" class="mb-8 space-y-4" style:display={sectionVisible('storage') ? '' : 'none'}>
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Storage</h2>
		<p class="text-xs text-gray-500">Default Kubernetes StorageClass for persistent volumes created by Mortise. Leave empty to use the cluster default.</p>
		<div class="space-y-3 rounded-md border border-surface-600 bg-surface-800/50 p-4">
			<div>
				<label class="block text-xs text-gray-500 mb-1" for="storage-class">Default storage class</label>
				<input id="storage-class" type="text" bind:value={defaultStorageClass}
					placeholder="local-path"
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none font-mono focus:border-accent" />
				<p class="mt-0.5 text-xs text-gray-500">Maps to <code class="font-mono bg-surface-700 px-1 rounded">spec.storage.defaultStorageClass</code> in PlatformConfig. Apps can override per-volume via <code class="font-mono bg-surface-700 px-1 rounded">storageClass</code>.</p>
			</div>
			<button type="button" onclick={saveStorage} disabled={savingStorage}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
				{savingStorage ? 'Saving…' : 'Save storage config'}
			</button>
		</div>
	</section>

	<!-- TLS -->
	<section id="tls" class="mb-8 space-y-4" style:display={sectionVisible('tls') ? '' : 'none'}>
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">TLS</h2>
		<p class="text-xs text-gray-500">cert-manager configuration for automatic TLS certificate provisioning.</p>
		<div class="space-y-3 rounded-md border border-surface-600 bg-surface-800/50 p-4">
			<div>
				<label class="block text-xs text-gray-500 mb-1" for="tls-issuer">Default cluster issuer</label>
				<input id="tls-issuer" type="text" bind:value={tlsClusterIssuer}
					placeholder="letsencrypt-prod"
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none font-mono focus:border-accent" />
				<p class="mt-0.5 text-xs text-gray-500">Name of the cert-manager ClusterIssuer to use for all Ingress TLS. Apps can override per-environment.</p>
			</div>
			<button type="button" onclick={saveTls} disabled={savingTls}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
				{savingTls ? 'Saving…' : 'Save TLS config'}
			</button>
		</div>
	</section>

	<!-- Git Providers -->
	<section class="mb-8 space-y-4" id="git-providers" style:display={sectionVisible('git-providers') ? '' : 'none'}>
		<div class="flex items-center justify-between border-b border-surface-600 pb-2">
			<h2 class="text-sm font-medium text-gray-300">Git Providers</h2>
			<button
				type="button"
				onclick={() => (showProviderForm = !showProviderForm)}
				class="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
			>
				<Plus class="h-3.5 w-3.5" /> Add Manually
			</button>
		</div>

		<p class="text-xs text-gray-500">GitHub is connected per-user from the user menu. Use the form below for GitLab or Gitea.</p>

		<!-- Manual provider form (for GitLab or Gitea) -->
		{#if showProviderForm}
			<div class="space-y-3 rounded-lg border border-surface-600 bg-surface-700 p-4">
				<h3 class="text-sm font-medium text-white">Add Provider Manually</h3>
				<div class="grid grid-cols-2 gap-3">
					<div>
						<label class="text-xs text-gray-400">Name</label>
						<input
							type="text"
							bind:value={newProviderName}
							placeholder="github-main"
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
					</div>
					<div>
						<label class="text-xs text-gray-400">Type</label>
						<select
							bind:value={newProviderType}
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
						>
							<option value="github">GitHub</option>
							<option value="gitlab">GitLab</option>
							<option value="gitea">Gitea</option>
						</select>
					</div>
				</div>
				<div>
					<label class="text-xs text-gray-400">Host URL</label>
					<input
						type="text"
						bind:value={newProviderHost}
						placeholder="https://github.com"
						class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
				</div>
				<div class="grid grid-cols-2 gap-3">
					<div>
						<label class="text-xs text-gray-400" for="new-provider-client-id">OAuth Client ID</label>
						<input
							id="new-provider-client-id"
							type="text"
							bind:value={newProviderClientID}
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
					</div>
					<div>
						<label class="text-xs text-gray-400" for="new-provider-client-secret">OAuth Client Secret</label>
						<input
							id="new-provider-client-secret"
							type="password"
							bind:value={newProviderClientSecret}
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
					</div>
				</div>
				<div>
					<label class="text-xs text-gray-400" for="new-provider-webhook-secret">Webhook Secret</label>
					<input
						id="new-provider-webhook-secret"
						type="text"
						bind:value={newProviderWebhookSecret}
						class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
				</div>
				<div class="flex gap-2">
					<button
						type="button"
						onclick={createProvider}
						disabled={creatingProvider}
						class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50"
					>
						{creatingProvider ? 'Creating...' : 'Create'}
					</button>
					<button
						type="button"
						onclick={() => (showProviderForm = false)}
						class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 hover:bg-surface-700 hover:text-white"
					>
						Cancel
					</button>
				</div>
			</div>
		{/if}

		{#if providers.length === 0 && !showProviderForm}
			<div class="rounded-md border border-surface-600 p-4 text-center">
				<p class="text-sm text-gray-500">No git providers configured.</p>
			</div>
		{:else if providers.length > 0}
			<div class="space-y-2">
				{#each providers as provider}
					<div
						class="flex items-center justify-between rounded-md border border-surface-600 bg-surface-700 px-4 py-3"
					>
						<div class="flex items-center gap-3">
							<GitBranch class="h-4 w-4 text-gray-400" />
							<div>
								<p class="text-sm font-medium text-white">{provider.name}</p>
								<p class="text-xs text-gray-500">{provider.type} · {provider.host}</p>
							</div>
							<span
								class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium {provider.phase === 'Ready' ? 'bg-success/10 text-success' : provider.phase === 'Failed' ? 'bg-danger/10 text-danger' : 'bg-info/10 text-info'}"
							>
								{provider.phase}
							</span>
						</div>
						<div class="flex items-center gap-2">
							{#if !provider.hasToken}
								<a
									href="/api/oauth/{provider.name}/authorize"
									class="rounded-md border border-surface-600 px-3 py-1 text-xs text-gray-400 hover:bg-surface-600 hover:text-white"
								>
									Connect
								</a>
							{:else}
								<span class="text-xs text-success">Connected</span>
							{/if}
							<button
								type="button"
								onclick={() => deleteProvider(provider.name)}
								class="rounded-md p-1.5 text-gray-500 hover:bg-surface-600 hover:text-danger"
							>
								<Trash2 class="h-4 w-4" />
							</button>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</section>

	<!-- Users section -->
	<section id="users" class="mb-8 space-y-4" style:display={sectionVisible('users') ? '' : 'none'}>
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Users &amp; Invites</h2>
		<div class="rounded-md border border-surface-600 bg-surface-800/50 p-4 text-sm text-gray-400">
			<p>User management is handled per-project. To invite users to a project, go to <strong class="text-white">Project Settings → Members</strong>.</p>
			<p class="mt-2 text-xs text-gray-500">Platform-level admin accounts are created during first-run setup or via the CLI: <code class="font-mono bg-surface-700 px-1 rounded">mrt admin create-user</code>.</p>
		</div>
	</section>
</div>
