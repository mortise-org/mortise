<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';

	let step = $state(1);
	let error = $state('');
	let saving = $state(false);

	// Step 1: Domain
	let domain = $state('');
	// Step 2: DNS
	let dnsProvider = $state('cloudflare');
	let dnsToken = $state('');

	const steps = ['Domain', 'DNS', 'Git Provider', 'Done'];

	async function next() {
		error = '';
		if (step === 1) {
			if (!domain) { error = 'Domain is required'; return; }
			step = 2;
		} else if (step === 2) {
			saving = true;
			try {
				await api.patchPlatform({ domain, dns: { provider: dnsProvider, apiTokenSecretRef: dnsToken || 'placeholder' } });
				step = 3;
			} catch(e) {
				error = e instanceof Error ? e.message : 'Failed to save';
			} finally { saving = false; }
		} else if (step === 3) {
			step = 4;
		}
	}

	async function finish() {
		await goto('/');
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-surface-900">
	<div class="w-full max-w-md">
		<div class="mb-8 text-center">
			<h1 class="text-2xl font-bold text-white">Platform Setup</h1>
			<p class="mt-2 text-sm text-gray-500">Configure your Mortise installation</p>
		</div>

		<!-- Step indicators -->
		<div class="mb-6 flex items-center justify-center gap-2">
			{#each steps as s, i}
				<div class="flex items-center gap-2">
					<div class="flex h-6 w-6 items-center justify-center rounded-full text-xs font-medium {i + 1 < step ? 'bg-success text-white' : i + 1 === step ? 'bg-accent text-white' : 'bg-surface-700 text-gray-400'}">
						{i + 1 < step ? '✓' : i + 1}
					</div>
					<span class="text-xs {i + 1 === step ? 'text-white' : 'text-gray-500'}">{s}</span>
					{#if i < steps.length - 1}
						<div class="h-px w-6 bg-surface-600"></div>
					{/if}
				</div>
			{/each}
		</div>

		<div class="rounded-lg border border-surface-600 bg-surface-800 p-6">
			{#if step === 1}
				<h2 class="mb-4 text-base font-semibold text-white">Platform Domain</h2>
				<p class="mb-4 text-sm text-gray-400">The base domain for all apps deployed to this platform (e.g. <span class="font-mono text-gray-300">apps.example.com</span>).</p>
				<input type="text" bind:value={domain} placeholder="apps.example.com"
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />

			{:else if step === 2}
				<h2 class="mb-4 text-base font-semibold text-white">DNS Provider</h2>
				<div class="space-y-3">
					<div>
						<label for="dns-provider" class="text-sm text-gray-400">Provider</label>
						<select id="dns-provider" bind:value={dnsProvider}
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
							<option value="cloudflare">Cloudflare</option>
							<option value="route53">Route 53</option>
							<option value="externaldns-noop">ExternalDNS (skip DNS management)</option>
						</select>
					</div>
					{#if dnsProvider !== 'externaldns-noop'}
						<div>
							<label for="dns-token" class="text-sm text-gray-400">API Token</label>
							<input id="dns-token" type="password" bind:value={dnsToken} placeholder="API token for DNS management"
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						</div>
					{/if}
				</div>

			{:else if step === 3}
				<h2 class="mb-4 text-base font-semibold text-white">Connect Git Provider</h2>
				<p class="mb-4 text-sm text-gray-400">Connect GitHub, GitLab, or Gitea to enable git-source apps. You can skip this and add providers later in Platform Settings.</p>
				<div class="space-y-2">
					<a href="/admin/settings#git-providers"
						class="flex items-center gap-3 rounded-md border border-surface-600 p-3 hover:border-accent hover:bg-surface-700 transition-colors">
						<span class="text-xl">🔀</span>
						<div>
							<p class="text-sm font-medium text-white">Configure Git Provider</p>
							<p class="text-xs text-gray-500">Go to Platform Settings to add GitHub, GitLab, or Gitea</p>
						</div>
					</a>
				</div>

			{:else if step === 4}
				<div class="py-4 text-center">
					<div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-success/10 text-2xl text-success">✓</div>
					<h2 class="mb-2 text-base font-semibold text-white">Platform Ready!</h2>
					<p class="text-sm text-gray-400">Your Mortise platform is configured and ready to use.</p>
				</div>
			{/if}

			{#if error}
				<p class="mt-3 text-sm text-danger">{error}</p>
			{/if}

			<div class="mt-6 flex justify-between">
				{#if step > 1 && step < 4}
					<button type="button" onclick={() => step--}
						class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
						Back
					</button>
				{:else}
					<div></div>
				{/if}

				{#if step < 4}
					<button type="button" onclick={next} disabled={saving}
						class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
						{saving ? 'Saving...' : step === 3 ? 'Skip for now' : 'Continue'}
					</button>
				{:else}
					<button type="button" onclick={finish}
						class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover">
						Go to Dashboard →
					</button>
				{/if}
			</div>
		</div>
	</div>
</div>
