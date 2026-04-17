<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { AppSpec, GitProviderSummary, Repository, Branch } from '$lib/types';
	import { X } from 'lucide-svelte';

	let {
		project,
		onClose,
		onCreated
	}: {
		project: string;
		onClose: () => void;
		onCreated: (appName: string) => void;
	} = $props();

	type AppType = 'git' | 'image' | 'database' | 'template' | 'external' | 'empty';

	let selectedType = $state<AppType | null>(null);
	let submitting = $state(false);
	let error = $state('');

	// Common fields
	let appName = $state('');
	let appKind = $state<'service' | 'cron'>('service');
	let schedule = $state('0 * * * *');

	// Git source
	let gitProvider = $state('');
	let repos = $state<Repository[]>([]);
	let reposLoading = $state(false);
	let selectedRepo = $state<Repository | null>(null);
	let branches = $state<Branch[]>([]);
	let gitBranch = $state('main');
	let gitPath = $state('');
	let repoSearch = $state('');

	const filteredRepos = $derived(
		repoSearch.trim()
			? repos.filter((r) => r.fullName.toLowerCase().includes(repoSearch.toLowerCase()))
			: repos
	);

	// Image source
	let imageRef = $state('');

	// External source
	let externalHost = $state('');
	let externalPort = $state(80);

	// Build config
	let buildMode = $state<'auto' | 'dockerfile' | 'railpack'>('auto');
	let buildCache = $state(false);
	let dockerfilePath = $state('Dockerfile');
	let buildArgs = $state<Record<string, string>>({});

	// Git-specific
	let watchPathsText = $state(''); // newline-separated
	let pullSecret = $state(''); // for image source

	// External credentials
	let externalCredentials = $state<Array<{ name: string; value: string }>>([]);

	// Database/Template presets
	type DbTemplate = { name: string; image: string; icon: string; description: string };
	const DB_TEMPLATES: DbTemplate[] = [
		{ name: 'Postgres', image: 'postgres:16', icon: '🐘', description: 'PostgreSQL 16' },
		{ name: 'Redis', image: 'redis:7', icon: '🔴', description: 'Redis 7 in-memory store' },
		{ name: 'MinIO', image: 'minio/minio:latest', icon: '🪣', description: 'S3-compatible object storage' },
		{ name: 'MySQL', image: 'mysql:8', icon: '🐬', description: 'MySQL 8' }
	];
	let selectedDbTemplate = $state<DbTemplate | null>(null);

	let providers = $state<GitProviderSummary[]>([]);

	const typeOptions: { type: AppType; icon: string; label: string; description: string }[] = [
		{ type: 'git', icon: '🔀', label: 'Git Repository', description: 'Deploy from a connected git provider' },
		{ type: 'database', icon: '🗄️', label: 'Database', description: 'Postgres, Redis, MinIO, MySQL' },
		{ type: 'template', icon: '📦', label: 'Template', description: 'Pre-configured app templates' },
		{ type: 'image', icon: '🐳', label: 'Docker Image', description: 'Deploy any container image' },
		{ type: 'external', icon: '🌐', label: 'External Service', description: 'Facade over an external API or DB' },
		{ type: 'empty', icon: '⬜', label: 'Empty App', description: 'Blank scaffold, configure later' }
	];

	function getTypeName(t: AppType): string {
		return typeOptions.find((o) => o.type === t)?.label ?? t;
	}

	function selectType(t: AppType) {
		selectedType = t;
		error = '';
		if (t === 'git' && providers.length > 0 && !gitProvider) {
			gitProvider = providers[0].name;
			loadRepos();
		}
	}

	async function loadRepos() {
		if (!gitProvider) return;
		reposLoading = true;
		repos = [];
		selectedRepo = null;
		branches = [];
		try {
			repos = await api.listRepos(gitProvider);
		} catch {
			repos = [];
		} finally {
			reposLoading = false;
		}
	}

	function selectRepo(repo: Repository) {
		selectedRepo = repo;
		gitBranch = repo.defaultBranch;
		if (!appName) {
			appName = repo.name.toLowerCase().replace(/[^a-z0-9-]/g, '-');
		}
		branches = [];
		const [owner, name] = repo.fullName.split('/');
		api
			.listBranches(owner, name, gitProvider)
			.then((list) => { branches = list ?? []; })
			.catch(() => { branches = [{ name: repo.defaultBranch, default: true }]; });
	}

	function buildSpec(): AppSpec {
		const baseEnv: AppSpec['environments'] = [{ name: 'production', replicas: 1 }];

		if (selectedType === 'git') {
			return {
				source: {
					type: 'git' as const,
					repo: selectedRepo?.cloneURL ?? '',
					branch: gitBranch,
					path: gitPath || undefined,
					providerRef: gitProvider || undefined,
					watchPaths: watchPathsText.trim() ? watchPathsText.split('\n').map(s => s.trim()).filter(Boolean) : undefined,
					build: {
						mode: buildMode,
						cache: buildCache || undefined,
						dockerfilePath: buildMode === 'dockerfile' ? dockerfilePath : undefined,
						args: Object.keys(buildArgs).length > 0 ? buildArgs : undefined
					}
				},
				network: { public: true },
				environments: appKind === 'cron'
					? [{ name: 'production', replicas: 0, annotations: { 'mortise.dev/schedule': schedule } }]
					: baseEnv,
				...(appKind === 'cron' ? { kind: 'cron' as const } : {})
			} as AppSpec;
		}
		if (selectedType === 'image' || selectedType === 'database' || selectedType === 'template') {
			return {
				source: {
					type: 'image' as const,
					image: imageRef || 'nginx:latest',
					pullSecretRef: pullSecret || undefined
				},
				network: { public: selectedType === 'image' },
				environments: appKind === 'cron'
					? [{ name: 'production', replicas: 0, annotations: { 'mortise.dev/schedule': schedule } }]
					: baseEnv,
				...(appKind === 'cron' ? { kind: 'cron' as const } : {})
			} as AppSpec;
		}
		if (selectedType === 'external') {
			return {
				source: {
					type: 'external' as const,
					host: externalHost,
					port: externalPort
				},
				network: { public: true },
				credentials: externalCredentials.filter(c => c.name),
				environments: baseEnv
			} as AppSpec;
		}
		// empty
		return {
			source: { type: 'image', image: '' },
			network: { public: true },
			environments: baseEnv
		};
	}

	async function handleCreate() {
		if (!appName) return;
		submitting = true;
		error = '';
		try {
			const spec = buildSpec();
			await api.createApp(project, appName, spec);
			onCreated(appName);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to create app';
		} finally {
			submitting = false;
		}
	}

	onMount(async () => {
		try {
			providers = await api.listGitProviders();
		} catch {
			providers = [];
		}
	});
</script>

<!-- Backdrop -->
<div class="fixed inset-0 z-40 bg-black/60" onclick={onClose} role="presentation"></div>

<!-- Modal panel -->
<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
	<div class="w-full max-w-lg rounded-lg border border-surface-600 bg-surface-800 p-6 shadow-xl">
		<div class="mb-5 flex items-center justify-between">
			<h2 class="text-lg font-semibold text-white">
				{selectedType ? getTypeName(selectedType) : 'What would you like to create?'}
			</h2>
			<button
				onclick={onClose}
				class="rounded-md p-1.5 text-gray-500 hover:bg-surface-700 hover:text-white"
			>
				<X class="h-4 w-4" />
			</button>
		</div>

		{#if !selectedType}
			<!-- Type picker -->
			<div class="space-y-1">
				{#each typeOptions as opt}
					<button
						type="button"
						onclick={() => selectType(opt.type)}
						class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left transition-colors hover:bg-surface-700"
					>
						<span class="flex h-8 w-8 items-center justify-center rounded-md bg-surface-700 text-sm"
							>{opt.icon}</span
						>
						<div>
							<p class="text-sm font-medium text-white">{opt.label}</p>
							<p class="text-xs text-gray-500">{opt.description}</p>
						</div>
					</button>
				{/each}
			</div>
		{:else}
			<!-- Configure pane -->
			<div>
				<button
					type="button"
					onclick={() => { selectedType = null; error = ''; }}
					class="mb-4 text-sm text-gray-500 transition-colors hover:text-white"
				>
					&larr; Back
				</button>

				<!-- Source-specific config -->
				{#if selectedType === 'git'}
					<div class="space-y-4">
						<!-- Provider selector -->
						<div>
							<label class="text-sm text-gray-400">Git Provider</label>
							{#if providers.length === 0}
								<p class="mt-1 text-sm text-gray-500">
									No git providers connected.
									<a href="/admin/settings#git-providers" class="text-accent hover:underline">Configure one</a>.
								</p>
							{:else}
								<select
									bind:value={gitProvider}
									onchange={loadRepos}
									class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
								>
									{#each providers as p}
										<option value={p.name}>{p.name} ({p.type})</option>
									{/each}
								</select>
							{/if}
						</div>

						<!-- Repo search -->
						{#if reposLoading}
							<div class="text-sm text-gray-500">Loading repositories...</div>
						{:else if repos.length > 0}
							<div>
								<label class="text-sm text-gray-400">Repository</label>
								<input
									type="text"
									bind:value={repoSearch}
									placeholder="Search repos..."
									class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
								/>
								<div class="mt-1 max-h-40 overflow-y-auto rounded-md border border-surface-600">
									{#each filteredRepos as repo}
										<button
											type="button"
											onclick={() => selectRepo(repo)}
											class="flex w-full items-center justify-between px-3 py-2 text-sm hover:bg-surface-700 {selectedRepo?.fullName === repo.fullName ? 'bg-surface-600 text-white' : 'text-gray-300'}"
										>
											<span class="truncate">{repo.fullName}</span>
											{#if repo.private}<span class="text-xs text-gray-500">private</span>{/if}
										</button>
									{/each}
								</div>
							</div>
						{/if}

						<!-- Branch -->
						<div>
							<label class="text-sm text-gray-400">Branch</label>
							<select
								bind:value={gitBranch}
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
							>
								{#each branches as b}
									<option value={b.name}>{b.name}{b.default ? ' (default)' : ''}</option>
								{/each}
								{#if !branches.length}
									<option value="main">main</option>
								{/if}
							</select>
						</div>

						<!-- Watch paths -->
						<div>
							<label class="text-sm text-gray-400">Watch paths <span class="text-gray-600">(optional)</span></label>
							<textarea
								bind:value={watchPathsText}
								placeholder="src/&#10;package.json"
								rows="3"
								class="mt-1 w-full resize-y rounded-md border border-surface-600 bg-surface-700 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
							></textarea>
							<p class="mt-0.5 text-xs text-gray-500">One path prefix per line. Leave empty to rebuild on any push.</p>
						</div>

						<!-- Build mode -->
						<div>
							<label class="text-sm text-gray-400">Build mode</label>
							<select bind:value={buildMode}
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
								<option value="auto">Auto-detect</option>
								<option value="dockerfile">Dockerfile</option>
								<option value="railpack">Railpack / Nixpacks</option>
							</select>
						</div>
						{#if buildMode === 'dockerfile'}
							<div>
								<label class="text-sm text-gray-400">Dockerfile path</label>
								<input type="text" bind:value={dockerfilePath} placeholder="Dockerfile"
									class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
							</div>
						{/if}

						<!-- Build cache -->
						<label class="flex items-center gap-2 cursor-pointer">
							<input type="checkbox" bind:checked={buildCache} class="rounded border-surface-600 bg-surface-800 text-accent" />
							<span class="text-sm text-gray-400">Enable build cache</span>
						</label>
					</div>
				{:else if selectedType === 'image'}
					<div class="space-y-4">
						<div>
							<label class="text-sm text-gray-400">Image Reference</label>
							<input
								type="text"
								bind:value={imageRef}
								placeholder="nginx:1.27 or ghcr.io/org/app:latest"
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
							/>
						</div>
						<div>
							<label class="text-sm text-gray-400">Pull secret name <span class="text-gray-600">(optional)</span></label>
							<input type="text" bind:value={pullSecret} placeholder="my-registry-secret"
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
							<p class="mt-0.5 text-xs text-gray-500">Name of a k8s Secret in the project namespace for private registries.</p>
						</div>
					</div>
				{:else if selectedType === 'database'}
					<div class="grid grid-cols-2 gap-2">
						{#each DB_TEMPLATES as tpl}
							<button
								type="button"
								onclick={() => {
									selectedDbTemplate = tpl;
									appName = tpl.name.toLowerCase();
									imageRef = tpl.image;
								}}
								class="rounded-lg border p-3 text-left {selectedDbTemplate?.name === tpl.name ? 'border-accent bg-accent/5' : 'border-surface-600 bg-surface-700 hover:border-surface-500'}"
							>
								<div class="mb-1 text-xl">{tpl.icon}</div>
								<div class="text-sm font-medium text-white">{tpl.name}</div>
								<div class="text-xs text-gray-500">{tpl.description}</div>
							</button>
						{/each}
					</div>
				{:else if selectedType === 'template'}
					<div class="rounded-md border border-surface-600 bg-surface-700 p-4">
						<p class="text-sm text-gray-400">Custom templates are not yet configured. Start from a Docker Image or Database preset.</p>
					</div>
				{:else if selectedType === 'external'}
					<div class="space-y-4">
						<div>
							<label class="text-sm text-gray-400">Host</label>
							<input
								type="text"
								bind:value={externalHost}
								placeholder="db.internal.example.com"
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
							/>
						</div>
						<div>
							<label class="text-sm text-gray-400">Port</label>
							<input
								type="number"
								bind:value={externalPort}
								min="1"
								max="65535"
								class="mt-1 w-24 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
							/>
						</div>
						<div>
							<label class="text-sm text-gray-400">Credentials (binding contract)</label>
							<p class="mt-0.5 text-xs text-gray-500 mb-2">Declare which credential keys this external service exposes for other apps to bind.</p>
							{#each externalCredentials as cred, i}
								<div class="flex gap-2 mb-1.5">
									<input type="text" bind:value={cred.name} placeholder="DATABASE_URL"
										class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2 py-1.5 text-xs text-white placeholder-gray-500 outline-none focus:border-accent" />
									<input type="text" bind:value={cred.value} placeholder="value or leave empty"
										class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2 py-1.5 text-xs text-white placeholder-gray-500 outline-none focus:border-accent" />
									<button type="button" onclick={() => externalCredentials = externalCredentials.filter((_, idx) => idx !== i)}
										class="rounded px-1.5 py-1 text-gray-500 hover:text-danger text-xs">✕</button>
								</div>
							{/each}
							<button type="button" onclick={() => externalCredentials = [...externalCredentials, { name: '', value: '' }]}
								class="text-xs text-accent hover:text-accent-hover">+ Add credential key</button>
						</div>
					</div>
				{/if}

				<!-- Common footer -->
				<div class="mt-5 space-y-3 border-t border-surface-600 pt-4">
					<!-- App name -->
					<div>
						<label class="text-sm text-gray-400">App name</label>
						<input
							type="text"
							bind:value={appName}
							placeholder="my-app"
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
					</div>

					<!-- Kind selector (git + image only) -->
					{#if selectedType === 'git' || selectedType === 'image'}
						<div>
							<label class="text-sm text-gray-400">Kind</label>
							<div class="mt-1 flex gap-2">
								<button
									type="button"
									onclick={() => (appKind = 'service')}
									class="rounded-md border px-3 py-1.5 text-sm {appKind === 'service' ? 'border-accent bg-accent/10 text-accent' : 'border-surface-600 text-gray-400 hover:border-surface-500'}"
								>
									Service
								</button>
								<button
									type="button"
									onclick={() => (appKind = 'cron')}
									class="rounded-md border px-3 py-1.5 text-sm {appKind === 'cron' ? 'border-accent bg-accent/10 text-accent' : 'border-surface-600 text-gray-400 hover:border-surface-500'}"
								>
									Cron
								</button>
							</div>
						</div>
						{#if appKind === 'cron'}
							<div>
								<label class="text-sm text-gray-400">Schedule</label>
								<input
									type="text"
									bind:value={schedule}
									placeholder="0 * * * *"
									class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
								/>
								<p class="mt-1 text-xs text-gray-500">Cron expression (UTC)</p>
							</div>
						{/if}
					{/if}

					{#if error}
						<p class="text-sm text-danger">{error}</p>
					{/if}

					<!-- Submit -->
					<div class="flex justify-end gap-2">
						<button
							type="button"
							onclick={onClose}
							class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 hover:bg-surface-700 hover:text-white"
						>
							Cancel
						</button>
						<button
							type="button"
							onclick={handleCreate}
							disabled={submitting || !appName}
							class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
						>
							{submitting ? 'Creating...' : 'Create app'}
						</button>
					</div>
				</div>
			</div>
		{/if}
	</div>
</div>
