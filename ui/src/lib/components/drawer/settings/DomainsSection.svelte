<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { App, AppSpec, DomainsResponse } from '$lib/types';
	import { inputCls, labelCls, sectionCls, headingCls, btnPrimary } from './styles';

	let {
		project,
		app,
		selectedEnv,
		cloneSpec,
		onSpecUpdate,
		onError
	}: {
		project: string;
		app: App;
		selectedEnv: string;
		cloneSpec: () => AppSpec;
		onSpecUpdate: (spec: AppSpec) => void;
		onError: (msg: string) => void;
	} = $props();

	let domains = $state<DomainsResponse | null>(null);
	let newDomain = $state('');
	let savingDomain = $state(false);
	let editingPrimary = $state(false);
	let primaryDomainInput = $state('');
	let primaryDomainError = $state('');
	let savingPrimary = $state(false);

	let tlsClusterIssuer = $state('');
	let tlsSecretName = $state('');
	let savingTls = $state(false);
	const certEnvStatus = $derived(app.status?.environments?.find(e => e.name === selectedEnv));

	$effect(() => {
		if (selectedEnv) void loadDomains();
	});

	$effect(() => {
		const envName = selectedEnv;
		void app.metadata.name;
		untrack(() => {
			const spec = cloneSpec();
			const env = spec.environments?.find((e: { name: string }) => e.name === envName) as
				| { tls?: { clusterIssuer?: string; secretName?: string } }
				| undefined;
			tlsClusterIssuer = env?.tls?.clusterIssuer ?? '';
			tlsSecretName = env?.tls?.secretName ?? '';
		});
	});

	async function loadDomains() {
		if (!selectedEnv) return;
		try {
			domains = await api.listDomains(project, app.metadata.name, selectedEnv);
		} catch {
			domains = null;
		}
	}

	async function handleAddDomain() {
		if (!newDomain.trim() || !selectedEnv) return;
		savingDomain = true;
		const domainToAdd = newDomain.trim();
		const prevDomains = domains;

		try {
			const result = await api.validateDomain(domainToAdd, app.metadata.name, project);
			if (!result.valid && result.conflict) {
				onError(`Already used by ${result.conflict.app} in ${result.conflict.project} (${result.conflict.environment})`);
				savingDomain = false;
				return;
			}
		} catch {
			onError('Failed to validate domain');
			savingDomain = false;
			return;
		}

		domains = domains ? { ...domains, custom: [...(domains.custom ?? []), domainToAdd] } : domains;
		newDomain = '';
		try {
			domains = await api.addDomain(project, app.metadata.name, selectedEnv, domainToAdd);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to add domain');
			domains = prevDomains;
			newDomain = domainToAdd;
		} finally {
			savingDomain = false;
		}
	}

	async function handleRemoveDomain(domain: string) {
		if (!selectedEnv) return;
		const prevDomains = domains;
		domains = domains ? { ...domains, custom: (domains.custom ?? []).filter(d => d !== domain) } : domains;
		try {
			domains = await api.removeDomain(project, app.metadata.name, selectedEnv, domain);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to remove domain');
			domains = prevDomains;
		}
	}

	function startEditPrimary() {
		editingPrimary = true;
		primaryDomainInput = domains?.primary ?? '';
		primaryDomainError = '';
	}

	async function savePrimaryDomain() {
		if (!selectedEnv) return;
		const domain = primaryDomainInput.trim();
		primaryDomainError = '';

		if (domain && domain !== domains?.primary) {
			try {
				const result = await api.validateDomain(domain, app.metadata.name, project);
				if (!result.valid && result.conflict) {
					primaryDomainError = `Already used by ${result.conflict.app} in ${result.conflict.project} (${result.conflict.environment})`;
					return;
				}
			} catch {
				primaryDomainError = 'Failed to validate domain';
				return;
			}
		}

		savingPrimary = true;
		try {
			const spec = cloneSpec();
			spec.environments = spec.environments ?? [];
			const idx = spec.environments.findIndex((e: { name: string }) => e.name === selectedEnv);
			if (idx >= 0) {
				spec.environments[idx] = { ...spec.environments[idx], domain };
			} else {
				spec.environments.push({ name: selectedEnv, domain });
			}
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result.spec);
			domains = domains ? { ...domains, primary: domain } : { primary: domain, custom: [] };
			editingPrimary = false;
		} catch (e) {
			primaryDomainError = e instanceof Error ? e.message : 'Failed to save';
		} finally {
			savingPrimary = false;
		}
	}

	async function removePrimaryDomain() {
		if (!selectedEnv) return;
		savingPrimary = true;
		try {
			const spec = cloneSpec();
			spec.environments = spec.environments ?? [];
			const idx = spec.environments.findIndex((e: { name: string }) => e.name === selectedEnv);
			if (idx >= 0) {
				spec.environments[idx] = { ...spec.environments[idx], domain: '' };
			}
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result.spec);
			domains = domains ? { ...domains, primary: '' } : { primary: '', custom: [] };
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to remove primary domain');
		} finally {
			savingPrimary = false;
		}
	}

	async function saveTlsOverride() {
		if (!selectedEnv) return;
		savingTls = true;
		try {
			const spec = cloneSpec();
			spec.environments = spec.environments ?? [];
			let envIdx = spec.environments.findIndex((e: { name: string }) => e.name === selectedEnv);
			if (envIdx < 0) {
				spec.environments.push({ name: selectedEnv });
				envIdx = spec.environments.length - 1;
			}
			(spec.environments[envIdx] as { tls?: { clusterIssuer?: string; secretName?: string } }).tls = {
				clusterIssuer: tlsClusterIssuer || undefined,
				secretName: tlsSecretName || undefined
			};
			const result = await api.updateApp(project, app.metadata.name, spec);
			onSpecUpdate(result.spec);
		} catch (e) {
			onError(e instanceof Error ? e.message : 'Failed to save TLS config');
		} finally {
			savingTls = false;
		}
	}
</script>

<div class={sectionCls}>
	<h3 class={headingCls}>Domains</h3>

	{#if certEnvStatus?.certificateStatus === 'Pending'}
		<div class="rounded-md border border-warning/30 bg-warning/5 px-3 py-2 text-xs text-warning">
			Certificate pending{certEnvStatus.certificateMessage ? ` — ${certEnvStatus.certificateMessage}` : ''}
		</div>
	{:else if certEnvStatus?.certificateStatus === 'Failed'}
		<div class="rounded-md border border-danger/30 bg-danger/5 px-3 py-2 text-xs text-danger">
			Certificate failed{certEnvStatus.certificateMessage ? ` — ${certEnvStatus.certificateMessage}` : ''}
		</div>
	{:else if certEnvStatus?.certificateStatus === 'Ready'}
		<div class="rounded-md border border-success/30 bg-success/5 px-3 py-2 text-xs text-success">
			TLS certificate active
		</div>
	{/if}

	{#if !domains?.primary && !domains?.auto && (!domains?.custom || domains.custom.length === 0)}
		<p class="text-xs text-gray-500">No domains configured. Add a domain to make this app reachable.</p>
	{/if}

	{#if domains?.auto}
		<div class="flex items-center justify-between rounded-md bg-surface-700 px-3 py-2">
			<div>
				<div class="flex items-center gap-2">
					<p class="text-xs text-gray-500">Auto-generated</p>
					<span class="rounded bg-surface-600 px-1.5 py-0.5 text-[10px] font-medium text-gray-400">Generated</span>
				</div>
				<p class="font-mono text-xs text-gray-200">{domains.auto}</p>
			</div>
		</div>
	{/if}

	{#if domains?.primary && !editingPrimary}
		<div class="flex items-center justify-between rounded-md bg-surface-700 px-3 py-2">
			<div>
				<p class="text-xs text-gray-500">Primary</p>
				<p class="font-mono text-xs text-gray-200">{domains.primary}</p>
			</div>
			<div class="flex items-center gap-2">
				<button type="button" onclick={startEditPrimary} class="text-xs text-gray-500 hover:text-white">Edit</button>
				<button type="button" onclick={removePrimaryDomain} disabled={savingPrimary} class="text-xs text-gray-500 hover:text-danger">Remove</button>
			</div>
		</div>
	{:else if editingPrimary}
		<div class="space-y-2">
			<div class="flex gap-2">
				<input type="text" bind:value={primaryDomainInput} placeholder="app.example.com" class="{inputCls} flex-1" />
				<button type="button" onclick={savePrimaryDomain} disabled={savingPrimary} class={btnPrimary}>
					{savingPrimary ? 'Saving…' : 'Save'}
				</button>
				<button type="button" onclick={() => { editingPrimary = false; primaryDomainError = ''; }}
					class="rounded-md px-2 py-1 text-xs text-gray-400 hover:bg-surface-700 hover:text-white">Cancel</button>
			</div>
			{#if primaryDomainError}
				<p class="text-xs text-danger">{primaryDomainError}</p>
			{/if}
		</div>
	{:else if !domains?.primary}
		<div class="space-y-2">
			<div class="flex gap-2">
				<input type="text" bind:value={primaryDomainInput} placeholder="app.example.com" class="{inputCls} flex-1" />
				<button type="button" onclick={savePrimaryDomain} disabled={savingPrimary || !primaryDomainInput.trim()} class={btnPrimary}>
					{savingPrimary ? 'Saving…' : 'Set primary'}
				</button>
			</div>
			{#if primaryDomainError}
				<p class="text-xs text-danger">{primaryDomainError}</p>
			{/if}
		</div>
	{/if}

	{#if domains?.custom && domains.custom.length > 0}
		<div class="space-y-1.5">
			{#each domains.custom as d}
				<div class="flex items-center justify-between rounded-md bg-surface-700 px-3 py-2">
					<span class="font-mono text-xs text-gray-200">{d}</span>
					<button type="button" onclick={() => handleRemoveDomain(d)} class="text-xs text-gray-500 hover:text-danger">Remove</button>
				</div>
			{/each}
		</div>
	{/if}

	<div class="flex gap-2">
		<input type="text" bind:value={newDomain} placeholder="custom.example.com" class="{inputCls} flex-1" />
		<button type="button" onclick={handleAddDomain} disabled={savingDomain || !newDomain.trim()} class={btnPrimary}>
			{savingDomain ? 'Adding…' : 'Add'}
		</button>
	</div>

	<!-- TLS overrides (advanced) -->
	<details class="mt-3">
		<summary class="cursor-pointer text-xs text-gray-500 hover:text-gray-300">TLS overrides (advanced)</summary>
		<div class="mt-2 space-y-2 rounded-md border border-surface-600 bg-surface-700/50 p-3">
			<div class="rounded-md border border-warning/20 bg-warning/5 p-2 text-xs text-warning">
				These override the platform-wide cert-manager issuer for this app only.
			</div>
			<div>
				<label class={labelCls} for="tls-issuer-ovr">Cluster issuer override</label>
				<input id="tls-issuer-ovr" type="text" bind:value={tlsClusterIssuer} placeholder="letsencrypt-staging" class={inputCls} />
			</div>
			<div>
				<label class={labelCls} for="tls-secret-ovr">TLS secret name override</label>
				<input id="tls-secret-ovr" type="text" bind:value={tlsSecretName} placeholder="my-tls-secret" class={inputCls} />
				<p class="text-xs text-gray-500 mt-0.5">Mutually exclusive with cluster issuer</p>
			</div>
			<button type="button" onclick={saveTlsOverride} disabled={savingTls} class={btnPrimary}>
				{savingTls ? 'Saving…' : 'Save TLS overrides'}
			</button>
		</div>
	</details>
</div>
