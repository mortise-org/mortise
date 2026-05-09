<script lang="ts">
	import { api } from '$lib/api';
	import type { App } from '$lib/types';
	import { Plus, Trash2, X, ChevronDown } from 'lucide-svelte';

	let {
		project,
		app,
		activeEnv
	}: {
		project: string;
		app: App;
		activeEnv: string;
	} = $props();

	let showAddCredential = $state(false);
	let newCredName = $state('');
	let newCredValue = $state('');
	let savingCredentials = $state(false);
	let credentialError = $state('');
	let credentialsOpen = $state(true);

	let pendingCredentials = $state<Array<{name: string; value?: string; valueFrom?: unknown}> | null>(null);
	let pendingClearTimer = $state<ReturnType<typeof setTimeout> | null>(null);

	function setPendingCredentials(creds: Array<{name: string; value?: string; valueFrom?: unknown}>) {
		pendingCredentials = creds;
		if (pendingClearTimer) clearTimeout(pendingClearTimer);
		pendingClearTimer = setTimeout(() => { pendingCredentials = null; }, 3000);
	}

	$effect(() => {
		void activeEnv;
		void app.metadata.name;
		showAddCredential = false;
		newCredName = '';
		newCredValue = '';
		credentialError = '';
		pendingCredentials = null;
	});

	const currentCredentials = $derived(pendingCredentials ?? (app.spec.credentials ?? []));

	async function addCredential() {
		if (!newCredName.trim()) return;
		savingCredentials = true;
		credentialError = '';
		const spec = JSON.parse(JSON.stringify(app.spec));
		spec.credentials = [
			...(spec.credentials ?? []),
			{ name: newCredName.trim(), ...(newCredValue ? { value: newCredValue } : {}) }
		];
		const savedName = newCredName;
		const savedValue = newCredValue;
		showAddCredential = false;
		newCredName = '';
		newCredValue = '';
		try {
			await api.updateApp(project, app.metadata.name, spec);
			setPendingCredentials(spec.credentials);
		} catch (e) {
			credentialError = e instanceof Error ? e.message : 'Failed to add credential';
			showAddCredential = true;
			newCredName = savedName;
			newCredValue = savedValue;
		} finally {
			savingCredentials = false;
		}
	}

	async function removeCredential(name: string) {
		credentialError = '';
		const spec = JSON.parse(JSON.stringify(app.spec));
		spec.credentials = (spec.credentials ?? []).filter(
			(c: { name: string }) => c.name !== name
		);
		try {
			await api.updateApp(project, app.metadata.name, spec);
			setPendingCredentials(spec.credentials);
		} catch (e) {
			credentialError = e instanceof Error ? e.message : 'Failed to remove credential';
		}
	}
</script>

<div class="rounded-lg border border-surface-600 bg-surface-900">
	<div
		role="button"
		tabindex="0"
		onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') credentialsOpen = !credentialsOpen; }}
		onclick={() => credentialsOpen = !credentialsOpen}
		class="flex w-full cursor-pointer items-center justify-between px-3 py-2.5">
		<div class="flex items-center gap-2">
			<span class="text-sm font-medium text-white">Exposed Credentials</span>
			{#if currentCredentials.length > 0}
				<span class="rounded-full bg-surface-700 px-1.5 py-0.5 text-[10px] font-medium text-gray-400">{currentCredentials.length}</span>
			{/if}
		</div>
		<div class="flex items-center gap-2">
			{#if credentialsOpen}
				<button type="button" onclick={(e) => { e.stopPropagation(); showAddCredential = true; }}
					class="flex items-center gap-1 rounded-md border border-surface-600 px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">
					<Plus class="h-3 w-3" />
				</button>
			{/if}
			<ChevronDown class="h-4 w-4 text-gray-500 transition-transform {credentialsOpen ? 'rotate-180' : ''}" />
		</div>
	</div>

	{#if credentialsOpen}
		<div class="border-t border-surface-600">
			{#if credentialError}
				<div class="px-3 py-2 text-xs text-danger">{credentialError}</div>
			{/if}

			{#if showAddCredential}
				<div class="border-b border-surface-600 bg-surface-700/30 px-3 py-2.5">
					<div class="flex items-center gap-2">
						<input id="cred-name" type="text" bind:value={newCredName} placeholder="key"
							class="w-[40%] rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						<input id="cred-value" type="text" bind:value={newCredValue} placeholder="value (optional)"
							onkeydown={(e) => { if (e.key === 'Enter' && newCredName.trim()) addCredential(); }}
							class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2.5 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						<button type="button" onclick={addCredential} disabled={!newCredName.trim() || savingCredentials}
							class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
							{savingCredentials ? 'Adding…' : 'Add'}
						</button>
						<button type="button" onclick={() => { showAddCredential = false; newCredName = ''; newCredValue = ''; }}
							class="rounded p-1.5 text-gray-500 hover:text-white"><X class="h-3.5 w-3.5" /></button>
					</div>
				</div>
			{/if}

			{#if currentCredentials.length === 0 && !showAddCredential}
				<div class="py-6 text-center text-xs text-gray-500">
					No credentials. Declare what this app exposes so other apps can bind to it.
				</div>
			{:else}
				{#each currentCredentials as cred}
					<div class="group flex items-center justify-between border-b border-surface-600 px-3 py-2 hover:bg-surface-700/30">
						<div class="flex items-center gap-2">
							<span class="font-mono text-sm text-gray-200">{cred.name}</span>
							{#if cred.value}
								<span class="text-[10px] text-gray-500">= ••••••</span>
							{:else if cred.valueFrom}
								<span class="inline-flex items-center rounded-full bg-accent/10 px-1.5 py-0.5 text-[10px] font-medium text-accent">from secret</span>
							{/if}
						</div>
						<button type="button" onclick={() => removeCredential(cred.name)}
							class="shrink-0 rounded p-1 text-gray-500 hover:text-danger transition-colors">
							<Trash2 class="h-3.5 w-3.5" />
						</button>
					</div>
				{/each}
			{/if}
		</div>
	{/if}
</div>
