<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { App, EnvironmentStatus, SecretResponse } from '$lib/types';

	const appName = $derived(page.params.name);

	let app = $state<App | null>(null);
	let secrets = $state<SecretResponse[]>([]);
	let loading = $state(true);
	let error = $state('');
	let deployImage = $state('');
	let deploying = $state(false);
	let activeTab = $state(0);

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
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load app';
		} finally {
			loading = false;
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

	const phaseColors: Record<string, string> = {
		Ready: 'bg-success/20 text-success',
		Deploying: 'bg-accent/20 text-accent',
		Building: 'bg-accent/20 text-accent',
		Pending: 'bg-warning/20 text-warning',
		Failed: 'bg-danger/20 text-danger'
	};
</script>

{#if loading}
	<div class="text-sm text-gray-500">Loading...</div>
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
				<a href="/" class="text-gray-500 hover:text-white">&larr;</a>
				<h1 class="text-xl font-semibold text-white">{app.metadata.name}</h1>
				{#if app.status?.phase}
					<span class="rounded-full px-2 py-0.5 text-xs font-medium {phaseColors[app.status.phase] ?? ''}">
						{app.status.phase}
					</span>
				{/if}
			</div>
			<button
				onclick={handleDeleteApp}
				class="rounded-md border border-danger/30 px-3 py-1.5 text-xs text-danger transition-colors hover:bg-danger/10"
			>
				Delete App
			</button>
		</div>

		<!-- Deploy -->
		<section class="mb-6 rounded-lg border border-surface-600 bg-surface-800 p-5">
			<h2 class="mb-3 text-sm font-medium text-gray-300">Deploy</h2>
			<div class="flex gap-2">
				<input
					type="text"
					bind:value={deployImage}
					placeholder="registry.example.com/app:v2.0.0"
					class="flex-1 rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
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
		{#if app.spec.environments && app.spec.environments.length > 0}
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

			<div class="space-y-4">
				<!-- Info row -->
				<div class="flex gap-6 text-sm text-gray-400">
					{#if env.domain}
						<div>Domain: <span class="text-white">{env.domain}</span></div>
					{/if}
					<div>Replicas: <span class="text-white">{env.replicas ?? 1}</span></div>
					{#if envStatus}
						<div>Ready: <span class="text-white">{envStatus.readyReplicas ?? 0}</span></div>
					{/if}
				</div>

				<!-- Env vars (from spec, read-only display) -->
				{#if env.env && env.env.length > 0}
					<section class="rounded-lg border border-surface-600 bg-surface-800 p-4">
						<h3 class="mb-2 text-sm font-medium text-gray-300">Variables</h3>
						<div class="space-y-1">
							{#each env.env as envVar}
								<div class="flex text-sm">
									<span class="w-40 font-mono text-gray-400">{envVar.name}</span>
									<span class="font-mono text-white">{envVar.valueFrom ? '********' : envVar.value}</span>
								</div>
							{/each}
						</div>
					</section>
				{/if}

				<!-- Deploy history -->
				{#if envStatus?.deployHistory && envStatus.deployHistory.length > 0}
					<section class="rounded-lg border border-surface-600 bg-surface-800 p-4">
						<h3 class="mb-2 text-sm font-medium text-gray-300">Deploy History</h3>
						<div class="space-y-2">
							{#each envStatus.deployHistory.toReversed() as record, i}
								<div class="flex items-center justify-between text-sm">
									<div>
										<span class="font-mono text-white">{record.image}</span>
										{#if record.gitSHA}
											<span class="ml-2 text-gray-500">{record.gitSHA.slice(0, 7)}</span>
										{/if}
									</div>
									<div class="flex items-center gap-3">
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
												onclick={() => handleRollback(envStatus, envStatus.deployHistory!.length - 1 - i)}
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
						<div class="flex items-center justify-between rounded-md bg-surface-700 px-3 py-2 text-sm">
							<div>
								<span class="font-mono text-white">{secret.name}</span>
								<span class="ml-2 text-xs text-gray-500">{secret.keys.length} key{secret.keys.length !== 1 ? 's' : ''}</span>
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
