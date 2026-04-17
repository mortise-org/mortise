<script lang="ts">
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type {
		CreateGitProviderRequest,
		GitProviderPhase,
		GitProviderSummary,
		GitProviderType
	} from '$lib/types';

	let providers = $state<GitProviderSummary[]>([]);
	let loading = $state(true);
	let error = $state('');

	// Create-form state (OAuth manual form).
	let showForm = $state(false);
	let formName = $state('');
	let formType = $state<GitProviderType>('github');
	let formHost = $state('https://github.com');
	let formClientID = $state('');
	let formClientSecret = $state('');
	let formWebhookSecret = $state('');
	let formError = $state('');
	let submitting = $state(false);
	// Tracks whether the user has manually edited host so we don't clobber their value.
	let hostDirty = $state(false);

	// GitHub App flow state.
	let creatingGitHubApp = $state(false);
	let githubAppError = $state('');

	const DEFAULT_HOSTS: Record<GitProviderType, string> = {
		github: 'https://github.com',
		gitlab: 'https://gitlab.com',
		gitea: 'https://gitea.example.com'
	};

	// Show a success banner when ?connected=<name> is present (set by the
	// OAuth callback redirect).
	const connectedName = $derived(page.url.searchParams.get('connected'));
	const githubAppCreated = $derived(page.url.searchParams.get('github-app-created') === 'true');
	const githubAppInstalled = $derived(page.url.searchParams.get('github-app-installed') === 'true');

	// Derived: does a github-app provider already exist?
	const githubAppProvider = $derived(providers.find((p) => p.mode === 'github-app'));
	const hasGitHubProvider = $derived(providers.some((p) => p.type === 'github'));

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

	function openForm() {
		showForm = true;
		formError = '';
		formName = '';
		formType = 'github';
		formHost = DEFAULT_HOSTS.github;
		formClientID = '';
		formClientSecret = '';
		formWebhookSecret = '';
		hostDirty = false;
	}

	function closeForm() {
		showForm = false;
	}

	function onTypeChange() {
		if (!hostDirty) {
			formHost = DEFAULT_HOSTS[formType];
		}
	}

	function generateWebhookSecret() {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		formWebhookSecret = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	async function submitCreate(e: SubmitEvent) {
		e.preventDefault();
		formError = '';
		submitting = true;
		const body: CreateGitProviderRequest = {
			name: formName.trim(),
			type: formType,
			host: formHost.trim(),
			oauth: { clientID: formClientID, clientSecret: formClientSecret },
			webhookSecret: formWebhookSecret
		};
		try {
			await api.createGitProvider(body);
			closeForm();
			await load();
		} catch (err) {
			formError = err instanceof Error ? err.message : 'Failed to create git provider';
		} finally {
			submitting = false;
		}
	}

	async function handleDelete(name: string) {
		const ok = confirm(
			`Delete git provider '${name}'? This will remove OAuth credentials and any stored access token.`
		);
		if (!ok) {
			return;
		}
		try {
			await api.deleteGitProvider(name);
			await load();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to delete git provider';
		}
	}

	async function createGitHubApp() {
		creatingGitHubApp = true;
		githubAppError = '';
		try {
			const resp = await api.githubAppManifest();
			// Create a hidden form and POST the manifest to GitHub.
			const form = document.createElement('form');
			form.method = 'POST';
			form.action = resp.redirectUrl;
			const input = document.createElement('input');
			input.type = 'hidden';
			input.name = 'manifest';
			input.value = JSON.stringify(resp.manifest);
			form.appendChild(input);
			document.body.appendChild(form);
			form.submit();
		} catch (err) {
			githubAppError = err instanceof Error ? err.message : 'Failed to start GitHub App creation';
			creatingGitHubApp = false;
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
			<div class="flex items-center gap-2">
				<button
					type="button"
					onclick={load}
					class="rounded-md border border-surface-600 px-3 py-1.5 text-sm text-gray-400 transition-colors hover:bg-surface-700 hover:text-white"
				>
					Refresh
				</button>
				{#if providers.length > 0}
					<button
						type="button"
						onclick={openForm}
						class="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
					>
						Add provider
					</button>
				{/if}
			</div>
		</div>

		<!-- Success banners -->
		{#if connectedName}
			<div class="mb-4 rounded-md bg-success/10 px-4 py-3 text-sm text-success">
				<strong>{connectedName}</strong> connected successfully.
			</div>
		{/if}
		{#if githubAppCreated}
			<div class="mb-4 rounded-md bg-success/10 px-4 py-3 text-sm text-success">
				GitHub App created successfully. Install it on your repos to start deploying.
				{#if githubAppProvider?.githubAppSlug}
					<a
						href="https://github.com/apps/{githubAppProvider.githubAppSlug}/installations/new"
						target="_blank"
						rel="noopener noreferrer"
						class="ml-1 font-medium underline"
					>
						Install on repos
					</a>
				{/if}
			</div>
		{/if}
		{#if githubAppInstalled}
			<div class="mb-4 rounded-md bg-success/10 px-4 py-3 text-sm text-success">
				GitHub App installed on your repos.
			</div>
		{/if}

		<!-- Error banners -->
		{#if error}
			<div class="mb-4 rounded-md bg-danger/10 px-4 py-3 text-sm text-danger">{error}</div>
		{/if}
		{#if githubAppError}
			<div class="mb-4 rounded-md bg-danger/10 px-4 py-3 text-sm text-danger">{githubAppError}</div>
		{/if}

		{#if showForm}
			<!-- Create-provider form (inline) -->
			<form
				onsubmit={submitCreate}
				class="mb-6 space-y-4 rounded-lg border border-surface-600 bg-surface-800/60 p-6"
			>
				<div class="flex items-start justify-between">
					<h2 class="text-base font-medium text-white">Create git provider</h2>
					<button
						type="button"
						onclick={closeForm}
						class="text-xs text-gray-500 transition-colors hover:text-white"
						aria-label="Close"
					>
						Cancel
					</button>
				</div>

				{#if formError}
					<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{formError}</div>
				{/if}

				<div class="grid gap-4 sm:grid-cols-2">
					<div>
						<label for="gp-name" class="mb-1 block text-sm text-gray-400">Name</label>
						<input
							id="gp-name"
							type="text"
							bind:value={formName}
							required
							pattern="[a-z0-9]([a-z0-9-]{'{'}0,61{'}'}[a-z0-9])?"
							maxlength="63"
							autocomplete="off"
							placeholder="github-main"
							class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
					</div>

					<div>
						<label for="gp-type" class="mb-1 block text-sm text-gray-400">Type</label>
						<select
							id="gp-type"
							bind:value={formType}
							onchange={onTypeChange}
							class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 text-sm text-white outline-none focus:border-accent"
						>
							<option value="github">GitHub</option>
							<option value="gitlab">GitLab</option>
							<option value="gitea">Gitea</option>
						</select>
					</div>
				</div>

				<div>
					<label for="gp-host" class="mb-1 block text-sm text-gray-400">Host</label>
					<input
						id="gp-host"
						type="url"
						bind:value={formHost}
						oninput={() => (hostDirty = true)}
						required
						placeholder="https://github.com"
						class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
					<p class="mt-1 text-xs text-gray-500">
						Override for self-hosted instances (e.g. <span class="font-mono"
							>https://gitea.internal.example</span
						>).
					</p>
				</div>

				<div>
					<label for="gp-client-id" class="mb-1 block text-sm text-gray-400"
						>OAuth Client ID</label
					>
					<input
						id="gp-client-id"
						type="text"
						bind:value={formClientID}
						required
						autocomplete="off"
						class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
				</div>

				<div>
					<label for="gp-client-secret" class="mb-1 block text-sm text-gray-400"
						>OAuth Client Secret</label
					>
					<input
						id="gp-client-secret"
						type="password"
						bind:value={formClientSecret}
						required
						autocomplete="new-password"
						class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
				</div>

				<div>
					<div class="mb-1 flex items-center justify-between">
						<label for="gp-webhook-secret" class="block text-sm text-gray-400"
							>Webhook Secret</label
						>
						<button
							type="button"
							onclick={generateWebhookSecret}
							class="text-xs text-accent transition-colors hover:underline"
						>
							Generate
						</button>
					</div>
					<input
						id="gp-webhook-secret"
						type="password"
						bind:value={formWebhookSecret}
						required
						autocomplete="new-password"
						class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
					<p class="mt-1 text-xs text-gray-500">
						Used to verify inbound webhook HMAC signatures. Paste the value you configured on the
						forge, or Generate a new one here and copy it into the forge's webhook settings.
					</p>
				</div>

				<div class="flex gap-2 pt-2">
					<button
						type="submit"
						disabled={submitting}
						class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
					>
						{submitting ? 'Creating...' : 'Create provider'}
					</button>
					<button
						type="button"
						onclick={closeForm}
						class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 transition-colors hover:text-white"
					>
						Cancel
					</button>
				</div>
			</form>
		{/if}

		{#if providers.length === 0 && !error && !showForm}
			<!-- Empty state: two-option layout -->
			<div class="space-y-4">
				<!-- Recommended: GitHub App -->
				{#if !hasGitHubProvider}
					<div class="rounded-lg border border-accent/30 bg-surface-800/60 p-6">
						<div class="mb-1 text-xs font-medium uppercase tracking-wide text-accent">Recommended</div>
						<h2 class="text-base font-medium text-white">Create a GitHub App (2 clicks)</h2>
						<p class="mt-1 text-sm text-gray-500">
							Mortise generates the app for you. Granular permissions, per-repo access.
						</p>
						<button
							type="button"
							onclick={createGitHubApp}
							disabled={creatingGitHubApp}
							class="mt-4 rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
						>
							{creatingGitHubApp ? 'Redirecting to GitHub...' : 'Create GitHub App'}
						</button>
					</div>
				{/if}

				<!-- Advanced: OAuth manual form -->
				<div class="rounded-lg border border-surface-600 bg-surface-800/60 p-6">
					<div class="mb-1 text-xs font-medium uppercase tracking-wide text-gray-500">Advanced</div>
					<h2 class="text-base font-medium text-white">Use an existing OAuth App</h2>
					<p class="mt-1 text-sm text-gray-500">
						Paste client ID + secret from your own OAuth app on GitHub, GitLab, or Gitea.
					</p>
					<button
						type="button"
						onclick={openForm}
						class="mt-4 rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 transition-colors hover:bg-surface-700 hover:text-white"
					>
						Show form
					</button>
				</div>
			</div>
		{:else if !error && providers.length > 0}
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
							<th class="px-4 py-3 text-left font-medium">Connect</th>
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
									{#if provider.mode === 'github-app'}
										{#if provider.githubAppInstallationID}
											<span class="inline-flex items-center gap-1.5 text-xs font-medium text-success">
												<span class="h-1.5 w-1.5 rounded-full bg-success"></span>
												Installed
											</span>
										{:else}
											<span class="inline-flex items-center gap-1.5 text-xs font-medium text-warning">
												<span class="h-1.5 w-1.5 rounded-full bg-warning"></span>
												App created
											</span>
										{/if}
									{:else if provider.hasToken}
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
									{#if provider.mode === 'github-app'}
										{#if provider.githubAppSlug}
											<a
												href="https://github.com/apps/{provider.githubAppSlug}/installations/new"
												target="_blank"
												rel="noopener noreferrer"
												class="inline-flex items-center rounded-md border border-accent/50 px-3 py-1 text-xs font-medium text-accent transition-colors hover:bg-accent/10"
											>
												{provider.githubAppInstallationID ? 'Manage installs' : 'Install on repos'}
											</a>
										{/if}
									{:else}
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
									{/if}
								</td>
								<td class="px-4 py-3">
									<button
										type="button"
										onclick={() => handleDelete(provider.name)}
										class="text-xs text-danger transition-colors hover:underline"
									>
										Delete
									</button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</div>
{/if}
