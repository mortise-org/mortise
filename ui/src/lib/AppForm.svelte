<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import EnvVarEditor from '$lib/components/EnvVarEditor.svelte';
	import type { Template } from '$lib/templates';
	import type { AppSpec, EnvVar, VolumeSpec } from '$lib/types';

	let {
		project,
		template,
		onBack
	}: { project: string; template: Template; onBack: () => void } = $props();

	// Initialize state from the template defaults. The parent remounts this
	// component when the template changes, so reading the initial value once
	// is intentional. Structured clone keeps the template data immutable.
	const initial = untrack(() => structuredClone(template.defaults));

	let name = $state(initial.name);
	let image = $state(initial.spec.source.image ?? '');
	let publicNet = $state(initial.spec.network?.public ?? true);

	const firstEnv = initial.spec.environments?.[0];
	let replicas = $state(firstEnv?.replicas ?? 1);
	let domain = $state(firstEnv?.domain ?? '');
	let envVars = $state<EnvVar[]>(
		(firstEnv?.env ?? []).map((e: EnvVar) => ({ name: e.name, value: e.value ?? '' }))
	);

	let storage = $state<VolumeSpec[]>(
		(initial.spec.storage ?? []).map((v) => ({ ...v }))
	);
	const credentials = initial.spec.credentials ?? [];

	let error = $state('');
	let submitting = $state(false);

	const hasDomainField = untrack(() => template.fields.some((f) => f.key === 'domain'));

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		submitting = true;

		try {
			const env: EnvVar[] = envVars.filter((v) => v.name.trim());

			const spec: AppSpec = {
				source: { type: 'image', image },
				network: { public: publicNet },
				environments: [
					{
						name: 'production',
						replicas,
						env,
						...(domain ? { domain } : {})
					}
				]
			};

			if (storage.length > 0) {
				spec.storage = storage;
			}
			if (credentials.length > 0) {
				spec.credentials = credentials;
			}

			await api.createApp(project, name, spec);
			goto(`/projects/${encodeURIComponent(project)}`);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to create app';
		} finally {
			submitting = false;
		}
	}
</script>

<div class="mx-auto max-w-lg">
	<button
		type="button"
		onclick={onBack}
		class="mb-4 text-sm text-gray-500 transition-colors hover:text-white"
	>
		&larr; Back to templates
	</button>

	<div class="mb-6 flex items-center gap-3">
		<span class="text-3xl">{template.icon}</span>
		<div>
			<h1 class="text-xl font-semibold text-white">{template.name}</h1>
			<p class="text-sm text-gray-500">{template.description}</p>
		</div>
	</div>

	<form onsubmit={handleSubmit} class="space-y-5">
		{#if error}
			<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
		{/if}

		<div>
			<label for="name" class="mb-1 block text-sm text-gray-400">App Name</label>
			<input
				id="name"
				type="text"
				bind:value={name}
				required
				pattern="[a-z0-9-]+"
				class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				placeholder={template.fields.find((f) => f.key === 'name')?.placeholder ?? 'my-app'}
			/>
		</div>

		<div>
			<span class="mb-1 block text-sm text-gray-400">Source</span>
			<div
				class="rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-gray-300"
			>
				Container Image
			</div>
		</div>

		<div>
			<label for="image" class="mb-1 block text-sm text-gray-400">Image Reference</label>
			<input
				id="image"
				type="text"
				bind:value={image}
				required
				class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				placeholder="registry.example.com/app:v1.0.0"
			/>
		</div>

		{#if hasDomainField}
			<div>
				<label for="domain" class="mb-1 block text-sm text-gray-400">
					Domain {publicNet ? '' : '(private, optional)'}
				</label>
				<input
					id="domain"
					type="text"
					bind:value={domain}
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					placeholder={template.fields.find((f) => f.key === 'domain')?.placeholder ??
						'app.example.com'}
				/>
			</div>
		{/if}

		<div>
			<label for="replicas" class="mb-1 block text-sm text-gray-400">Replicas</label>
			<input
				id="replicas"
				type="number"
				bind:value={replicas}
				min="1"
				max="20"
				class="w-24 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
			/>
		</div>

		{#if storage.length > 0}
			<div>
				<span class="mb-1 block text-sm text-gray-400">Persistent Storage</span>
				<div class="space-y-2">
					{#each storage as vol, i}
						<div class="flex gap-2 text-sm">
							<input
								type="text"
								bind:value={vol.name}
								placeholder="name"
								class="w-28 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-white outline-none focus:border-accent"
							/>
							<input
								type="text"
								bind:value={vol.mountPath}
								placeholder="/mount/path"
								class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-white outline-none focus:border-accent"
							/>
							<input
								type="text"
								bind:value={vol.size}
								placeholder="10Gi"
								class="w-20 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-white outline-none focus:border-accent"
							/>
							<button
								type="button"
								onclick={() => (storage = storage.filter((_, j) => j !== i))}
								class="px-2 text-gray-500 hover:text-danger"
							>
								&times;
							</button>
						</div>
					{/each}
				</div>
			</div>
		{/if}

		{#if credentials.length > 0}
			<div>
				<span class="mb-1 block text-sm text-gray-400">Exposed Credentials</span>
				<div class="flex flex-wrap gap-2">
					{#each credentials as cred}
						<span
							class="rounded-md bg-surface-700 px-2 py-1 font-mono text-xs text-gray-300"
						>
							{cred}
						</span>
					{/each}
				</div>
				<p class="mt-2 text-xs text-gray-500">
					Other apps can bind to this service and receive these values as env vars.
				</p>
			</div>
		{/if}

		<div>
			<span class="mb-1 block text-sm text-gray-400">Environment Variables</span>
			<EnvVarEditor bind:value={envVars} />
		</div>

		<div class="flex gap-3 pt-2">
			<button
				type="submit"
				disabled={submitting}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
			>
				{submitting ? 'Creating...' : template.submitLabel}
			</button>
			<a
				href="/projects/{project}"
				class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 transition-colors hover:text-white"
			>
				Cancel
			</a>
		</div>
	</form>
</div>
