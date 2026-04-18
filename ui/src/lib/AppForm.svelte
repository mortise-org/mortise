<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import EnvVarEditor from '$lib/components/EnvVarEditor.svelte';
	import { templateIcons, type Template } from '$lib/templates';
	import type { AppSource, AppSpec, Build, Credential, EnvVar, GitProviderSummary, SourceType, VolumeSpec } from '$lib/types';

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
	let sourceType = $state<SourceType>(initial.spec.source.type);
	let image = $state(initial.spec.source.image ?? '');
	let repo = $state(initial.spec.source.repo ?? '');
	let branch = $state(initial.spec.source.branch ?? 'main');
	let sourcePath = $state(initial.spec.source.path ?? '');
	let providerRef = $state(initial.spec.source.providerRef ?? '');
	let buildMode = $state<NonNullable<Build['mode']>>(initial.spec.source.build?.mode ?? 'auto');
	let dockerfilePath = $state(initial.spec.source.build?.dockerfilePath ?? '');
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

	let gitProviders = $state<GitProviderSummary[]>([]);
	let providersLoaded = $state(false);

	const hasDomainField = untrack(() => template.fields.some((f) => f.key === 'domain'));

	$effect(() => {
		if (sourceType !== 'git' || providersLoaded) {
			return;
		}
		api
			.listGitProviders()
			.then((list) => {
				gitProviders = list ?? [];
				if (!providerRef && gitProviders.length === 1) {
					providerRef = gitProviders[0].name;
				}
			})
			.catch(() => {
				gitProviders = [];
			})
			.finally(() => {
				providersLoaded = true;
			});
	});

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		submitting = true;

		try {
			const env: EnvVar[] = envVars.filter((v) => v.name.trim());

			let source: AppSource;
			if (sourceType === 'git') {
				const build: Build = { mode: buildMode };
				if (buildMode === 'dockerfile' && dockerfilePath.trim()) {
					build.dockerfilePath = dockerfilePath.trim();
				}
				source = {
					type: 'git',
					repo,
					branch: branch || 'main',
					providerRef,
					build
				};
				if (sourcePath.trim()) {
					source.path = sourcePath.trim();
				}
			} else {
				source = { type: 'image', image };
			}

			const spec: AppSpec = {
				source,
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
		{@const IconComp = templateIcons[template.icon]}
		{#if IconComp}<svelte:component this={IconComp} class="h-8 w-8 text-accent" />{/if}
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
				{sourceType === 'git' ? 'Git Repository' : 'Container Image'}
			</div>
		</div>

		{#if sourceType === 'image'}
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
		{:else}
			<div>
				<label for="repo" class="mb-1 block text-sm text-gray-400">Repo URL</label>
				<input
					id="repo"
					type="text"
					bind:value={repo}
					required
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					placeholder="https://github.com/you/your-repo"
				/>
			</div>

			<div>
				<label for="branch" class="mb-1 block text-sm text-gray-400">Branch</label>
				<input
					id="branch"
					type="text"
					bind:value={branch}
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					placeholder="main"
				/>
			</div>

			<div>
				<label for="source-path" class="mb-1 block text-sm text-gray-400">Path</label>
				<input
					id="source-path"
					type="text"
					bind:value={sourcePath}
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					placeholder="services/api"
				/>
				<p class="mt-1 text-xs text-gray-500">
					Leave empty for root. Use a subdirectory for monorepos.
				</p>
			</div>

			<div>
				<label for="provider-ref" class="mb-1 block text-sm text-gray-400">Git Provider</label>
				{#if providersLoaded && gitProviders.length === 0}
					<div
						class="rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-gray-400"
					>
						No git providers configured.
						<a href="/settings/git-providers" class="text-accent hover:underline">
							Go to Settings → Git Providers
						</a>
						to connect one.
					</div>
				{:else}
					<select
						id="provider-ref"
						bind:value={providerRef}
						required
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
					>
						<option value="" disabled>
							{providersLoaded ? 'Select a git provider' : 'Loading…'}
						</option>
						{#each gitProviders as provider}
							<option value={provider.name}>
								{provider.name} ({provider.type} · {provider.host})
							</option>
						{/each}
					</select>
				{/if}
			</div>

			<div>
				<label for="build-mode" class="mb-1 block text-sm text-gray-400">Build Mode</label>
				<select
					id="build-mode"
					bind:value={buildMode}
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
				>
					<option value="auto">Auto-detect</option>
					<option value="dockerfile">Dockerfile</option>
					<option value="railpack">Railpack</option>
				</select>
			</div>

			{#if buildMode === 'dockerfile'}
				<div>
					<label for="dockerfile-path" class="mb-1 block text-sm text-gray-400">
						Dockerfile Path
					</label>
					<input
						id="dockerfile-path"
						type="text"
						bind:value={dockerfilePath}
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						placeholder="Dockerfile"
					/>
				</div>
			{/if}
		{/if}

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
							{cred.name}
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
