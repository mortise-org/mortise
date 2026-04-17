<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { PlatformResponse, CreateGitProviderRequest } from '$lib/types';

	let step = $state(1);
	let loading = $state(false);
	let error = $state('');

	// Step 1 — Platform domain
	let domain = $state('');

	// Step 2 — DNS provider
	let dnsProvider = $state('none');
	let dnsToken = $state('');

	// Step 3 — Git provider
	let gitProviderType = $state<'github' | 'gitlab' | 'gitea'>('github');
	let gitProviderName = $state('');
	let gitProviderHost = $state('https://github.com');
	let gitClientID = $state('');
	let gitClientSecret = $state('');
	let gitWebhookSecret = $state('');

	onMount(async () => {
		if (!localStorage.getItem('token')) {
			goto('/login');
			return;
		}
		// If PlatformConfig already exists with a domain, skip the wizard.
		try {
			const pc = await api.getPlatform();
			if (pc && pc.domain) {
				goto('/');
				return;
			}
		} catch {
			// PlatformConfig doesn't exist yet — continue with wizard.
		}
	});

	async function saveStep1() {
		if (!domain.trim()) {
			error = 'Please enter a domain';
			return;
		}
		loading = true;
		error = '';
		try {
			await api.patchPlatform({ domain: domain.trim() });
			step = 2;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to save domain';
		} finally {
			loading = false;
		}
	}

	async function saveStep2() {
		if (dnsProvider === 'none') {
			step = 3;
			return;
		}
		loading = true;
		error = '';
		try {
			await api.patchPlatform({
				dns: { provider: dnsProvider, apiTokenSecretRef: dnsToken || '' }
			});
			step = 3;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to save DNS config';
		} finally {
			loading = false;
		}
	}

	async function saveStep3() {
		if (!gitProviderName.trim() || !gitClientID.trim() || !gitClientSecret.trim()) {
			step = 4;
			return;
		}
		loading = true;
		error = '';
		try {
			const body: CreateGitProviderRequest = {
				name: gitProviderName.trim(),
				type: gitProviderType,
				host: gitProviderHost.trim(),
				oauth: { clientID: gitClientID.trim(), clientSecret: gitClientSecret.trim() },
				webhookSecret: gitWebhookSecret.trim() || 'change-me'
			};
			await api.createGitProvider(body);
			step = 4;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to create git provider';
		} finally {
			loading = false;
		}
	}

	const totalSteps = 4;
</script>

<div class="flex min-h-screen items-center justify-center bg-surface-900">
	<div class="w-full max-w-md">
		<!-- Progress -->
		<div class="mb-6 flex items-center justify-center gap-2">
			{#each Array(totalSteps) as _, i}
				<div
					class="h-1.5 w-12 rounded-full transition-colors {i + 1 <= step
						? 'bg-accent'
						: 'bg-surface-600'}"
				></div>
			{/each}
		</div>

		<div class="rounded-lg border border-surface-600 bg-surface-800 p-6">
			{#if error}
				<div class="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
			{/if}

			{#if step === 1}
				<h2 class="mb-1 text-lg font-semibold text-white">Platform Domain</h2>
				<p class="mb-4 text-sm text-gray-500">
					Apps will receive subdomains under this domain (e.g. myapp.yourdomain.com).
				</p>
				<input
					type="text"
					bind:value={domain}
					placeholder="yourdomain.com"
					class="mb-4 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<div class="flex items-center justify-between">
					<a href="/" class="text-xs text-gray-500 hover:text-gray-300">Skip for now</a>
					<button
						onclick={saveStep1}
						disabled={loading}
						class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
					>
						{loading ? 'Saving...' : 'Next'}
					</button>
				</div>

			{:else if step === 2}
				<h2 class="mb-1 text-lg font-semibold text-white">DNS Provider</h2>
				<p class="mb-4 text-sm text-gray-500">
					Optional. Mortise can create DNS records automatically via ExternalDNS.
				</p>
				<select
					bind:value={dnsProvider}
					class="mb-3 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white outline-none focus:border-accent"
				>
					<option value="none">None (manual DNS)</option>
					<option value="cloudflare">Cloudflare</option>
					<option value="route53">AWS Route 53</option>
				</select>
				{#if dnsProvider !== 'none'}
					<input
						type="password"
						bind:value={dnsToken}
						placeholder="API token"
						class="mb-4 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
				{/if}
				<div class="flex items-center justify-between">
					<button
						onclick={() => (step = 3)}
						class="text-xs text-gray-500 hover:text-gray-300"
					>
						Skip for now
					</button>
					<div class="flex gap-2">
						<button
							onclick={() => (step = 1)}
							class="rounded-md border border-surface-600 px-3 py-2 text-sm text-gray-400 hover:text-white"
						>
							Back
						</button>
						<button
							onclick={saveStep2}
							disabled={loading}
							class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
						>
							{loading ? 'Saving...' : 'Next'}
						</button>
					</div>
				</div>

			{:else if step === 3}
				<h2 class="mb-1 text-lg font-semibold text-white">Git Provider</h2>
				<p class="mb-4 text-sm text-gray-500">
					Optional. Connect a git forge to deploy from repositories.
				</p>
				<select
					bind:value={gitProviderType}
					class="mb-3 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white outline-none focus:border-accent"
				>
					<option value="github">GitHub</option>
					<option value="gitlab">GitLab</option>
					<option value="gitea">Gitea</option>
				</select>
				<input
					type="text"
					bind:value={gitProviderName}
					placeholder="Provider name (e.g. github-main)"
					class="mb-3 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<input
					type="text"
					bind:value={gitProviderHost}
					placeholder="Host URL (e.g. https://github.com)"
					class="mb-3 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<input
					type="text"
					bind:value={gitClientID}
					placeholder="OAuth Client ID"
					class="mb-3 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<input
					type="password"
					bind:value={gitClientSecret}
					placeholder="OAuth Client Secret"
					class="mb-3 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<input
					type="text"
					bind:value={gitWebhookSecret}
					placeholder="Webhook Secret"
					class="mb-4 w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<div class="flex items-center justify-between">
					<button
						onclick={() => (step = 4)}
						class="text-xs text-gray-500 hover:text-gray-300"
					>
						Skip for now
					</button>
					<div class="flex gap-2">
						<button
							onclick={() => (step = 2)}
							class="rounded-md border border-surface-600 px-3 py-2 text-sm text-gray-400 hover:text-white"
						>
							Back
						</button>
						<button
							onclick={saveStep3}
							disabled={loading}
							class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
						>
							{loading ? 'Saving...' : 'Next'}
						</button>
					</div>
				</div>

			{:else if step === 4}
				<div class="text-center">
					<h2 class="mb-2 text-lg font-semibold text-white">Your platform is ready!</h2>
					<p class="mb-6 text-sm text-gray-500">
						You can always change these settings later in the platform settings page.
					</p>
					<a
						href="/projects/default/apps/new"
						class="inline-block rounded-md bg-accent px-6 py-2.5 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
					>
						Deploy your first app
					</a>
					<div class="mt-4">
						<a href="/" class="text-xs text-gray-500 hover:text-gray-300">
							Go to dashboard
						</a>
					</div>
				</div>
			{/if}
		</div>
	</div>
</div>
