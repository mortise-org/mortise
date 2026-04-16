<script lang="ts">
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { GitProviderSummary, GitProviderPhase } from '$lib/types';

	let providers = $state<GitProviderSummary[]>([]);
	let loading = $state(true);
	let error = $state('');

	// Show a success banner when ?connected=<name> is present (set by the
	// OAuth callback redirect).
	const connectedName = $derived(page.url.searchParams.get('connected'));

	onMount(async () => {
		if (!localStorage.getItem('token')) {
			goto('/login');
			return;
		}
		await load();
	});

	async function load() {
		loading = true;
		error = '';
		try {
			providers = await api.listGitProviders();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load git providers';
		} finally {
			loading = false;
		}
	}

	const phaseStyles: Record<GitProviderPhase, { dot: string; text: string }> = {
		Ready: { dot: 'bg-success', text: 'text-success' },
		Pending: { dot: 'bg-warning', text: 'text-warning' },
		Failed: { dot: 'bg-danger', text: 'text-danger' }
	};
</script>

{#if loading}
	<div class="animate-pulse">
		<div class="mb-6 h-6 w-48 rounded bg-surface-700"></div>
		<div class="h-40 rounded-lg bg-surface-800"></div>
	</div>
{:else}
	<div>
		<!-- Page header -->
		<div class="mb-6 flex items-start justify-between gap-4">
			<div>
				<div class="flex items-center gap-2 text-xs text-gray-500">
					<span>Settings</span>
					<span>/</span>
					<span class="text-gray-400">Git Providers</span>
				</div>
				<h1 class="mt-1 text-xl font-semibold text-white">Git Providers</h1>
				<p class="mt-1 max-w-2xl text-sm text-gray-500">
					Connect git forges so Mortise can clone repositories and register webhooks.
				</p>
			</div>
			<button
				type="button"
				onclick={load}
				class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 transition-colors hover:bg-surface-700 hover:text-white"
			>
				Refresh
			</button>
		</div>

		<!-- Success banner from OAuth callback redirect -->
		{#if connectedName}
			<div class="mb-4 rounded-md bg-success/10 px-4 py-3 text-sm text-success">
				<strong>{connectedName}</strong> connected successfully.
			</div>
		{/if}

		<!-- Error banner -->
		{#if error}
			<div class="mb-4 rounded-md bg-danger/10 px-4 py-3 text-sm text-danger">{error}</div>
		{/if}

		{#if providers.length === 0 && !error}
			<!-- Empty state -->
			<div
				class="rounded-lg border border-dashed border-surface-600 bg-surface-800/60 p-12 text-center"
			>
				<div
					class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-surface-700 text-accent"
					aria-hidden="true"
				>
					<svg
						xmlns="http://www.w3.org/2000/svg"
						fill="none"
						viewBox="0 0 24 24"
						stroke-width="1.5"
						stroke="currentColor"
						class="h-7 w-7"
					>
						<path
							stroke-linecap="round"
							stroke-linejoin="round"
							d="M17.25 6.75L22.5 12l-5.25 5.25m-10.5 0L1.5 12l5.25-5.25m7.5-3l-4.5 16.5"
						/>
					</svg>
				</div>
				<h2 class="text-base font-medium text-white">No git providers configured</h2>
				<p class="mx-auto mt-2 max-w-sm text-sm text-gray-500">
					A git provider must be created as a CRD before it appears here. Use the CLI or
					<code class="rounded bg-surface-700 px-1 py-0.5 font-mono text-xs text-gray-300"
						>kubectl</code
					>:
				</p>
				<pre
					class="mx-auto mt-4 max-w-lg rounded-md bg-surface-900 px-4 py-3 text-left text-xs text-gray-300"
				><code>kubectl apply -f - &lt;&lt;EOF
apiVersion: mortise.dev/v1alpha1
kind: GitProvider
metadata:
  name: github-main
spec:
  type: github
  host: https://github.com
  oauth:
    clientIDSecretRef:
      namespace: mortise-system
      name: github-oauth
      key: clientID
    clientSecretSecretRef:
      namespace: mortise-system
      name: github-oauth
      key: clientSecret
  webhookSecretRef:
    namespace: mortise-system
    name: github-webhook
    key: secret
EOF</code></pre>
			</div>
		{:else if !error}
			<!-- Provider table -->
			<div class="overflow-hidden rounded-lg border border-surface-600">
				<table class="w-full text-sm">
					<thead class="bg-surface-800 text-xs uppercase tracking-wide text-gray-500">
						<tr>
							<th class="px-4 py-3 text-left font-medium">Name</th>
							<th class="px-4 py-3 text-left font-medium">Type</th>
							<th class="px-4 py-3 text-left font-medium">Host</th>
							<th class="px-4 py-3 text-left font-medium">Phase</th>
							<th class="px-4 py-3 text-left font-medium">Token</th>
							<th class="px-4 py-3 text-left font-medium">Action</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-surface-700 bg-surface-800/40">
						{#each providers as provider}
							{@const phase = provider.phase}
							{@const style = phase ? phaseStyles[phase] : undefined}
							<tr class="transition-colors hover:bg-surface-800">
								<td class="px-4 py-3 font-mono text-white">{provider.name}</td>
								<td class="px-4 py-3 text-gray-300">{provider.type}</td>
								<td class="px-4 py-3 text-gray-400">{provider.host}</td>
								<td class="px-4 py-3">
									{#if phase}
										<span
											class="inline-flex items-center gap-1.5 text-xs font-medium {style?.text ?? 'text-gray-400'}"
										>
											<span
												class="h-1.5 w-1.5 rounded-full {style?.dot ?? 'bg-gray-500'}"
											></span>
											{phase}
										</span>
									{:else}
										<span class="text-gray-500">—</span>
									{/if}
								</td>
								<td class="px-4 py-3">
									{#if provider.hasToken}
										<span
											class="inline-flex items-center gap-1.5 text-xs font-medium text-success"
										>
											<span class="h-1.5 w-1.5 rounded-full bg-success"></span>
											Connected
										</span>
									{:else}
										<span class="text-xs text-gray-500">Not connected</span>
									{/if}
								</td>
								<td class="px-4 py-3">
									<!--
										Full-page navigation (not fetch) — the OAuth authorize endpoint
										issues a browser-level redirect to the forge consent page.
									-->
									<a
										href="/api/oauth/{provider.name}/authorize"
										class="inline-flex items-center rounded-md border border-accent/50 px-3 py-1 text-xs font-medium text-accent transition-colors hover:bg-accent/10"
									>
										{provider.hasToken ? 'Reconnect' : 'Connect'}
									</a>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</div>
{/if}
