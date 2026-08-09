<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { App, DeployToken } from '$lib/types';
	import { Copy, Plus, Trash2 } from 'lucide-svelte';
	import { inputCls, labelCls, sectionCls, headingCls, btnPrimary, btnSecondary } from './styles';

	let {
		project,
		app,
		appIdentity,
		resetEpoch,
		selectedEnv,
		onDirty,
		onDraftCleared,
		onError
	}: {
		project: string;
		app: App;
		appIdentity: string;
		resetEpoch: number;
		selectedEnv: string;
		onDirty: () => void;
		onDraftCleared: () => void;
		onError: (msg: string) => void;
	} = $props();

	let tokens = $state<DeployToken[]>([]);
	let loadingTokens = $state(true);
	let showTokenForm = $state(false);
	let newTokenName = $state('');
	let createdToken = $state<string | null>(null);
	let copiedToken = $state(false);
	let saving = $state(false);

	$effect(() => {
		appIdentity;
		selectedEnv;
		resetEpoch;
		showTokenForm = false;
		newTokenName = '';
	});

	onMount(async () => {
		await loadTokens();
	});

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

	async function createToken() {
		if (!newTokenName.trim() || !selectedEnv) return;
		saving = true;
		try {
			const tok = await api.createToken(project, app.metadata.name, newTokenName.trim(), selectedEnv);
			tokens = [...tokens, tok];
			createdToken = tok.token ?? null;
			newTokenName = '';
			showTokenForm = false;
			onDraftCleared();
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to create token');
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
			onError(e instanceof Error ? e.message : 'Failed to revoke token');
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
</script>

<div class={sectionCls}>
	<h3 class={headingCls}>Deploy Tokens</h3>

	{#if createdToken}
		<div class="rounded-md border border-success/30 bg-success/10 p-3">
			<p class="mb-1.5 text-xs font-medium text-success">Token created - copy it now, it won't be shown again.</p>
			<div class="flex items-center gap-2">
				<code class="flex-1 truncate rounded bg-surface-800 px-2 py-1 font-mono text-xs text-gray-300">
					{createdToken}
				</code>
				<button type="button" onclick={() => copyText(createdToken!)} class="text-gray-400 hover:text-white" aria-label="Copy token">
					{#if copiedToken}
						<span class="text-xs text-success">Copied!</span>
					{:else}
						<Copy class="h-3.5 w-3.5" />
					{/if}
				</button>
			</div>
			<button type="button" onclick={() => (createdToken = null)} class="mt-2 text-xs text-gray-500 hover:text-white">Dismiss</button>
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
					<button type="button" onclick={() => revokeToken(tok.id)}
						class="flex items-center gap-1 text-xs text-gray-500 hover:text-danger">
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
				<input id="tok-name" type="text" bind:value={newTokenName} oninput={onDirty} placeholder="ci-deploy" class={inputCls} />
			</div>
			<div class="flex justify-end gap-2">
				<button type="button" onclick={() => { showTokenForm = false; newTokenName = ''; onDraftCleared(); }} class={btnSecondary}>Cancel</button>
				<button type="button" onclick={createToken} disabled={saving || !newTokenName.trim() || !selectedEnv} class={btnPrimary}>
					Create
				</button>
			</div>
		</div>
	{:else}
		<button type="button" onclick={() => (showTokenForm = true)} class="flex items-center gap-1 {btnSecondary}">
			<Plus class="h-3 w-3" /> Create token
		</button>
	{/if}
</div>
