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

	// New git provider form
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
	<section class="mb-8 space-y-4" id="general">
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
	<section class="mb-8 space-y-4" id="dns">
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

	<!-- Git Providers -->
	<section class="mb-8 space-y-4" id="git-providers">
		<div class="flex items-center justify-between border-b border-surface-600 pb-2">
			<h2 class="text-sm font-medium text-gray-300">Git Providers</h2>
			<button
				type="button"
				onclick={() => (showProviderForm = !showProviderForm)}
				class="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
			>
				<Plus class="h-3.5 w-3.5" /> Add Provider
			</button>
		</div>

		{#if showProviderForm}
			<div class="space-y-3 rounded-lg border border-surface-600 bg-surface-700 p-4">
				<h3 class="text-sm font-medium text-white">New Git Provider</h3>
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
						<label class="text-xs text-gray-400">OAuth Client ID</label>
						<input
							type="text"
							bind:value={newProviderClientID}
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
					</div>
					<div>
						<label class="text-xs text-gray-400">OAuth Client Secret</label>
						<input
							type="password"
							bind:value={newProviderClientSecret}
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
					</div>
				</div>
				<div>
					<label class="text-xs text-gray-400">Webhook Secret</label>
					<input
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
	<section class="mb-8" id="users">
		<h2 class="mb-4 border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">
			Users & Invites
		</h2>
		<div class="rounded-md border border-surface-600 p-4">
			<p class="text-sm text-gray-400">
				User management is handled via the CLI or directly via the API. Admin account created during
				first-run setup.
			</p>
		</div>
	</section>
</div>
