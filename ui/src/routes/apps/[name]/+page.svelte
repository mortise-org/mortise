<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import EnvVarEditor from '$lib/components/EnvVarEditor.svelte';
	import LogViewer from '$lib/components/LogViewer.svelte';
	import type { App, EnvironmentStatus, EnvVar, SecretResponse } from '$lib/types';

	const appName = $derived(page.params.name);

	let app = $state<App | null>(null);
	let secrets = $state<SecretResponse[]>([]);
	let loading = $state(true);
	let error = $state('');
	let deployImage = $state('');
	let deploying = $state(false);
	let activeTab = $state(0);
	let copied = $state<string | null>(null);

	// Env var editing
	let envVars = $state<EnvVar[]>([]);
	let envDirty = $state(false);
	let savingEnv = $state(false);

	// Secret form
	let newSecretKey = $state('');
	let newSecretValue = $state('');
	let savingSecret = $state(false);

	onMount(async () => {
		if (!localStorage.getItem('token')) {
			goto('/login');
			return;
		}
		await loadApp();
	});

	async function loadApp() {
		loading = true;
		error = '';
		try {
			app = await api.get<App>(`/apps/${appName}`);
			secrets = await api.get<SecretResponse[]>(`/apps/${appName}/secrets`);
			syncEnvVarsFromApp();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load app';
		} finally {
			loading = false;
		}
	}

	function syncEnvVarsFromApp() {
		const env = app?.spec.environments?.[activeTab]?.env ?? [];
		envVars = env.map((e) => ({ name: e.name, value: e.value ?? '', valueFrom: e.valueFrom }));
		envDirty = false;
	}

	$effect(() => {
		// When the active tab changes and we aren't dirty, resync from the app.
		activeTab;
		if (!envDirty && app) syncEnvVarsFromApp();
	});

	function onEnvChange() {
		// The EnvVarEditor writes to envVars via bind:value. Mark dirty whenever
		// the serialized form differs from what the server last returned.
		const current = (app?.spec.environments?.[activeTab]?.env ?? []).map((e) => ({
			name: e.name,
			value: e.value ?? ''
		}));
		const next = envVars.map((e) => ({ name: e.name, value: e.value ?? '' }));
		if (current.length !== next.length) {
			envDirty = true;
			return;
		}
		for (let i = 0; i < current.length; i++) {
			if (current[i].name !== next[i].name || current[i].value !== next[i].value) {
				envDirty = true;
				return;
			}
		}
		envDirty = false;
	}

	$effect(() => {
		// Re-run dirty check whenever envVars changes.
		envVars;
		if (app) onEnvChange();
	});

	async function saveEnvVars() {
		if (!app) return;
		savingEnv = true;
		try {
			const spec = structuredClone($state.snapshot(app.spec));
			const envs = spec.environments ?? [];
			if (envs[activeTab]) {
				envs[activeTab].env = envVars.filter((v) => v.name.trim());
				spec.environments = envs;
			}
			await api.put(`/apps/${appName}`, spec);
			await loadApp();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to save variables';
		} finally {
			savingEnv = false;
		}
	}

	async function handleDeploy() {
		if (!deployImage.trim()) return;
		deploying = true;
		try {
			await api.post('/deploy', { app: appName, image: deployImage });
			deployImage = '';
			await loadApp();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Deploy failed';
		} finally {
			deploying = false;
		}
	}

	async function handleRollback(envStatus: EnvironmentStatus, index: number) {
		const history = envStatus.deployHistory;
		if (!history || index < 0) return;
		const target = history[index];
		try {
			await api.post('/deploy', { app: appName, image: target.image });
			await loadApp();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Rollback failed';
		}
	}

	async function handleDeleteApp() {
		if (!confirm(`Delete ${appName}? This cannot be undone.`)) return;
		try {
			await api.del(`/apps/${appName}`);
			goto('/');
		} catch (err) {
			error = err instanceof Error ? err.message : 'Delete failed';
		}
	}

	async function handleAddSecret() {
		if (!newSecretKey.trim()) return;
		savingSecret = true;
		try {
			await api.post(`/apps/${appName}/secrets`, {
				name: newSecretKey,
				data: { [newSecretKey]: newSecretValue }
			});
			newSecretKey = '';
			newSecretValue = '';
			secrets = await api.get<SecretResponse[]>(`/apps/${appName}/secrets`);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to save secret';
		} finally {
			savingSecret = false;
		}
	}

	async function handleDeleteSecret(name: string) {
		try {
			await api.del(`/apps/${appName}/secrets/${name}`);
			secrets = await api.get<SecretResponse[]>(`/apps/${appName}/secrets`);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to delete secret';
		}
	}

	async function copy(text: string, key: string) {
		try {
			await navigator.clipboard.writeText(text);
			copied = key;
			setTimeout(() => {
				if (copied === key) copied = null;
			}, 1500);
		} catch {
			// ignore
		}
	}

	const phaseStyles: Record<string, { dot: string; pill: string }> = {
		Ready: { dot: 'bg-success', pill: 'bg-success/15 text-success' },
		Deploying: { dot: 'bg-accent animate-pulse', pill: 'bg-accent/15 text-accent' },
		Building: { dot: 'bg-accent animate-pulse', pill: 'bg-accent/15 text-accent' },
		Pending: { dot: 'bg-warning', pill: 'bg-warning/15 text-warning' },
		Failed: { dot: 'bg-danger', pill: 'bg-danger/15 text-danger' }
	};

	function phase() {
		return app?.status?.phase ?? 'Pending';
	}

	function currentImage(envName: string): string | undefined {
		const s = app?.status?.environments?.find((e) => e.name === envName);
		return s?.currentImage ?? app?.spec.source.image;
	}
</script>

{#if loading}
	<!-- Loading skeleton -->
	<div class="animate-pulse">
		<div class="mb-6 flex items-center gap-3">
			<div class="h-6 w-6 rounded bg-surface-700"></div>
			<div class="h-6 w-40 rounded bg-surface-700"></div>
			<div class="h-5 w-16 rounded-full bg-surface-700"></div>
		</div>
		<div class="mb-4 h-24 rounded-lg bg-surface-800"></div>
		<div class="mb-4 h-10 rounded-lg bg-surface-800"></div>
		<div class="h-64 rounded-lg bg-surface-800"></div>
	</div>
{:else if error && !app}
	<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
{:else if app}
	<div>
		{#if error}
			<div class="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
		{/if}

		<!-- Header -->
		<div class="mb-6 flex items-center justify-between">
			<div class="flex items-center gap-3">
				<a href="/" class="text-gray-500 hover:text-white" aria-label="Back to apps">&larr;</a>
				<h1 class="text-xl font-semibold text-white">{app.metadata.name}</h1>
				<span
					class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium {phaseStyles[
						phase()
					]?.pill ?? 'bg-surface-700 text-gray-400'}"
				>
					<span class="h-1.5 w-1.5 rounded-full {phaseStyles[phase()]?.dot ?? 'bg-gray-500'}"
					></span>
					{phase()}
				</span>
			</div>
			<button
				onclick={handleDeleteApp}
				class="rounded-md border border-danger/30 px-3 py-1.5 text-xs text-danger transition-colors hover:bg-danger/10"
			>
				Delete App
			</button>
		</div>

		<!-- Overview -->
		<section class="mb-6 grid grid-cols-1 gap-4 md:grid-cols-3">
			<div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
				<div class="text-xs uppercase tracking-wide text-gray-500">Source</div>
				<div class="mt-1 text-sm text-white">
					{app.spec.source.type === 'image' ? 'Container Image' : 'Git Repository'}
				</div>
				{#if app.spec.source.image}
					<div class="mt-1 break-all font-mono text-xs text-gray-400">
						{currentImage(app.spec.environments?.[activeTab]?.name ?? '') ?? app.spec.source.image}
					</div>
				{/if}
			</div>

			<div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
				<div class="text-xs uppercase tracking-wide text-gray-500">Replicas</div>
				{#if app.spec.environments?.[activeTab]}
					{@const env = app.spec.environments[activeTab]}
					{@const envStatus = app.status?.environments?.find((e) => e.name === env.name)}
					<div class="mt-1 text-2xl font-semibold text-white">
						{envStatus?.readyReplicas ?? 0}
						<span class="text-sm font-normal text-gray-500">/ {env.replicas ?? 1}</span>
					</div>
					<div class="mt-1 text-xs text-gray-500">ready / desired</div>
				{/if}
			</div>

			<div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
				<div class="text-xs uppercase tracking-wide text-gray-500">Domain</div>
				{#if app.spec.environments?.[activeTab]?.domain}
					{@const d = app.spec.environments[activeTab].domain!}
					<div class="mt-1 flex items-center gap-2">
						<a
							href="https://{d}"
							target="_blank"
							rel="noopener noreferrer"
							class="truncate text-sm text-accent hover:text-accent-hover"
						>
							{d}
						</a>
						<button
							type="button"
							onclick={() => copy(d, 'domain')}
							class="shrink-0 text-xs text-gray-500 transition-colors hover:text-white"
							aria-label="Copy domain"
						>
							{copied === 'domain' ? 'Copied!' : 'Copy'}
						</button>
					</div>
				{:else}
					<div class="mt-1 text-sm text-gray-500">Private</div>
				{/if}
			</div>
		</section>

		<!-- Deploy -->
		<section class="mb-6 rounded-lg border border-surface-600 bg-surface-800 p-5">
			<h2 class="mb-3 text-sm font-medium text-gray-300">Deploy new image</h2>
			<div class="flex gap-2">
				<input
					type="text"
					bind:value={deployImage}
					placeholder="registry.example.com/app:v2.0.0"
					class="flex-1 rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<button
					onclick={handleDeploy}
					disabled={deploying || !deployImage.trim()}
					class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
				>
					{deploying ? 'Deploying...' : 'Deploy'}
				</button>
			</div>
		</section>

		<!-- Environment tabs -->
		{#if app.spec.environments && app.spec.environments.length > 1}
			<div class="mb-4 flex gap-1 border-b border-surface-600">
				{#each app.spec.environments as env, i}
					<button
						onclick={() => (activeTab = i)}
						class="px-4 py-2 text-sm transition-colors {activeTab === i
							? 'border-b-2 border-accent text-white'
							: 'text-gray-500 hover:text-gray-300'}"
					>
						{env.name}
					</button>
				{/each}
			</div>
		{/if}

		<!-- Environment details -->
		{#if app.spec.environments && app.spec.environments[activeTab]}
			{@const env = app.spec.environments[activeTab]}
			{@const envStatus = app.status?.environments?.find((e) => e.name === env.name)}

			<div class="space-y-6">
				<!-- Env vars editor -->
				<section>
					<div class="mb-2 flex items-center justify-between">
						<h2 class="text-sm font-medium text-gray-300">Environment Variables</h2>
						{#if envDirty}
							<div class="flex items-center gap-2">
								<button
									type="button"
									onclick={syncEnvVarsFromApp}
									disabled={savingEnv}
									class="rounded-md border border-surface-600 px-3 py-1.5 text-xs text-gray-400 transition-colors hover:text-white"
								>
									Discard
								</button>
								<button
									type="button"
									onclick={saveEnvVars}
									disabled={savingEnv}
									class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
								>
									{savingEnv ? 'Saving...' : 'Save changes'}
								</button>
							</div>
						{/if}
					</div>
					<EnvVarEditor bind:value={envVars} />
				</section>

				<!-- Logs -->
				<LogViewer appName={appName ?? ''} env={env.name} />

				<!-- Deploy history -->
				{#if envStatus?.deployHistory && envStatus.deployHistory.length > 0}
					<section class="rounded-lg border border-surface-600 bg-surface-800 p-5">
						<h2 class="mb-3 text-sm font-medium text-gray-300">Deploy History</h2>
						<div class="space-y-2">
							{#each envStatus.deployHistory.toReversed() as record, i}
								<div class="flex items-center justify-between text-sm">
									<div class="min-w-0 flex-1">
										<span class="font-mono text-white">{record.image}</span>
										{#if record.gitSHA}
											<span class="ml-2 text-gray-500">{record.gitSHA.slice(0, 7)}</span>
										{/if}
									</div>
									<div class="flex shrink-0 items-center gap-3">
										<span class="text-xs text-gray-500">
											{new Date(record.timestamp).toLocaleDateString('en-US', {
												month: 'short',
												day: 'numeric',
												hour: '2-digit',
												minute: '2-digit'
											})}
										</span>
										{#if i > 0}
											<button
												onclick={() =>
													handleRollback(envStatus, envStatus.deployHistory!.length - 1 - i)}
												class="text-xs text-accent hover:text-accent-hover"
											>
												Rollback
											</button>
										{/if}
									</div>
								</div>
							{/each}
						</div>
					</section>
				{/if}
			</div>
		{/if}

		<!-- Secrets -->
		<section class="mt-6 rounded-lg border border-surface-600 bg-surface-800 p-5">
			<h2 class="mb-3 text-sm font-medium text-gray-300">Secrets</h2>

			{#if secrets.length > 0}
				<div class="mb-4 space-y-2">
					{#each secrets as secret}
						<div
							class="flex items-center justify-between rounded-md bg-surface-700 px-3 py-2 text-sm"
						>
							<div>
								<span class="font-mono text-white">{secret.name}</span>
								<span class="ml-2 text-xs text-gray-500"
									>{secret.keys.length} key{secret.keys.length !== 1 ? 's' : ''}</span
								>
							</div>
							<button
								onclick={() => handleDeleteSecret(secret.name)}
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
					bind:value={newSecretKey}
					placeholder="SECRET_NAME"
					class="w-40 rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<input
					type="password"
					bind:value={newSecretValue}
					placeholder="value (write-only)"
					class="flex-1 rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
				<button
					onclick={handleAddSecret}
					disabled={savingSecret || !newSecretKey.trim()}
					class="rounded-md bg-surface-600 px-3 py-2 text-sm text-white transition-colors hover:bg-surface-500 disabled:opacity-50"
				>
					{savingSecret ? 'Saving...' : 'Add'}
				</button>
			</div>
		</section>
	</div>
{/if}
