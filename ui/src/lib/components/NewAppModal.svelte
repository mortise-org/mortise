<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { AppSpec, GitProviderSummary, Repository, Branch } from '$lib/types';
	import { X, GitBranch as GitBranchIcon, Database, Package, Container, Globe, Square } from 'lucide-svelte';
	import type { ComponentType } from 'svelte';

	let {
		project,
		onClose,
		onCreated
	}: {
		project: string;
		onClose: () => void;
		onCreated: (appName: string) => void;
	} = $props();

	type AppType = 'git' | 'image' | 'database' | 'supabase' | 'compose' | 'template' | 'external' | 'empty';

	let selectedType = $state<AppType | null>(null);
	let submitting = $state(false);
	let error = $state('');

	// Common fields
	let appName = $state('');
	let appNameManuallyEdited = $state(false);
	let appKind = $state<'service' | 'cron'>('service');

	function generateAppName(repoName: string, path: string): string {
		const parts = [repoName];
		const trimmed = path.replace(/^\/+|\/+$/g, '');
		if (trimmed) {
			parts.push(...trimmed.split('/'));
		}
		return parts.join('-').toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').slice(0, 53);
	}
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
	let repoListOpen = $state(true);

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
	let buildContext = $state<'' | 'root' | 'subdir'>('');
	let buildArgs = $state<Record<string, string>>({});

	// Domain (optional, for git/image/database/empty)
	let domain = $state('');

	// Git-specific - watch paths picker
	let repoTree = $state<Array<{ name: string; type: string; path: string }>>([]);
	let treeLoading = $state(false);
	let selectedPaths = $state<Set<string>>(new Set());
	let customPath = $state('');
	let pullSecret = $state(''); // for image source

	// Root Directory autocomplete
	let rootDirFocused = $state(false);
	const repoDirs = $derived(
		repoTree.filter((e) => e.type === 'tree').map((e) => e.path)
	);
	const filteredDirs = $derived(
		repoDirs.filter((p) => {
			if (!gitPath.trim()) return true;
			return p.toLowerCase().startsWith(gitPath.toLowerCase().replace(/\/$/, ''));
		})
	);

	// Update app name when root directory changes (unless manually edited).
	$effect(() => {
		if (selectedRepo && !appNameManuallyEdited) {
			appName = generateAppName(selectedRepo.name, gitPath);
		}
	});

	// Advanced options / watch paths toggle
	let advancedOpen = $state(false);
	let watchPathsDiffer = $state(false);

	let userEnvVars = $state<Array<{ name: string; value: string }>>([]);
	let envVarsOpen = $state(false);

	// External credentials
	let externalCredentials = $state<Array<{ name: string; value: string }>>([]);

	// Database/Template presets
	type DbTemplate = {
		name: string;
		image: string;
		icon: ComponentType;
		description: string;
		port: number;
		env: Array<{ name: string; value: string }>;
	};

	function generatePassword(length = 24): string {
		const chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';
		const arr = new Uint8Array(length);
		crypto.getRandomValues(arr);
		return Array.from(arr, b => chars[b % chars.length]).join('');
	}

	const DB_TEMPLATES: DbTemplate[] = [
		{
			name: 'Postgres', image: 'postgres:16', icon: Database, description: 'PostgreSQL 16',
			port: 5432,
			env: [
				{ name: 'POSTGRES_PASSWORD', value: generatePassword() },
				{ name: 'POSTGRES_DB', value: 'app' }
			]
		},
		{
			name: 'Redis', image: 'redis:7', icon: Database, description: 'Redis 7 in-memory store',
			port: 6379,
			env: []
		},
		{
			name: 'MinIO', image: 'minio/minio:latest', icon: Package, description: 'S3-compatible object storage',
			port: 9000,
			env: [
				{ name: 'MINIO_ROOT_USER', value: 'admin' },
				{ name: 'MINIO_ROOT_PASSWORD', value: generatePassword() }
			]
		},
		{
			name: 'MySQL', image: 'mysql:8', icon: Database, description: 'MySQL 8',
			port: 3306,
			env: [
				{ name: 'MYSQL_ROOT_PASSWORD', value: generatePassword() },
				{ name: 'MYSQL_DATABASE', value: 'app' }
			]
		}
	];
	let selectedDbTemplate = $state<DbTemplate | null>(null);

	// Supabase stack
	let supabaseProgress = $state('');
	let supabaseCreating = $state(false);
	let supabaseServicesLoaded = $state(false);
	let supabaseServices = $state<Array<{ name: string; image: string; required: boolean; selected: boolean }>>([]);

	async function loadSupabaseServices() {
		try {
			const tpls = await api.listTemplates();
			const sb = tpls.find((t) => t.name === 'supabase');
			if (sb) {
				supabaseServices = sb.services.map((s) => ({ ...s, selected: true }));
			}
		} catch {
			// Fallback if endpoint not available
			supabaseServices = [
				{ name: 'postgres', image: 'supabase/postgres', required: true, selected: true },
				{ name: 'auth', image: 'supabase/gotrue', required: false, selected: true },
				{ name: 'rest', image: 'postgrest/postgrest', required: false, selected: true },
				{ name: 'storage', image: 'supabase/storage-api', required: false, selected: true },
				{ name: 'realtime', image: 'supabase/realtime', required: false, selected: true },
				{ name: 'studio', image: 'supabase/studio', required: false, selected: true }
			];
		} finally {
			supabaseServicesLoaded = true;
		}
	}

	async function createSupabaseStack() {
		supabaseCreating = true;
		supabaseProgress = 'Creating Supabase stack...';
		try {
			const selected = supabaseServices.filter((s) => s.selected).map((s) => s.name);
			await api.createStack(project, {
				template: 'supabase',
				services: selected.length < supabaseServices.length ? selected : undefined,
				vars: {}
			});
			onCreated('supabase-postgres');
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to create Supabase stack';
		} finally {
			supabaseCreating = false;
			supabaseProgress = '';
		}
	}

	// Docker Compose import
	let composeContent = $state('');
	let composeCreating = $state(false);

	async function deployCompose() {
		if (!composeContent.trim()) return;
		composeCreating = true;
		error = '';
		try {
			await api.createStack(project, { compose: composeContent });
			onCreated('compose-stack');
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to deploy compose stack';
		} finally {
			composeCreating = false;
		}
	}

	let providers = $state<GitProviderSummary[]>([]);

	const typeOptions: { type: AppType; icon: ComponentType; label: string; description: string }[] = [
		{ type: 'git', icon: GitBranchIcon, label: 'Git Repository', description: 'Deploy from a connected git provider' },
		{ type: 'database', icon: Database, label: 'Database', description: 'Postgres, Redis, MinIO, MySQL' },
		{ type: 'supabase', icon: Database, label: 'Supabase', description: 'Self-hosted Supabase (auth, database, storage, realtime)' },
		{ type: 'compose', icon: Package, label: 'Docker Compose', description: 'Import a docker-compose.yml to deploy a multi-service stack' },
		{ type: 'template', icon: Package, label: 'Template', description: 'Pre-configured app templates' },
		{ type: 'image', icon: Container, label: 'Docker Image', description: 'Deploy any container image' },
		{ type: 'external', icon: Globe, label: 'External Service', description: 'Facade over an external API or DB' },
		{ type: 'empty', icon: Square, label: 'Empty App', description: 'Blank scaffold, configure later' }
	];

	function getTypeName(t: AppType): string {
		return typeOptions.find((o) => o.type === t)?.label ?? t;
	}

	function selectType(t: AppType) {
		selectedType = t;
		error = '';
		if (t === 'supabase' && supabaseServices.length === 0) {
			loadSupabaseServices();
		}
		if (t === 'git') {
			// Auto-select provider if none chosen.
			if (!gitProvider && providers.length > 0) {
				gitProvider = providers[0].name;
			}
			if (gitProvider) {
				loadRepos();
			}
		}
	}

	async function loadRepos() {
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

	async function loadRepoTree(path = '') {
		if (!selectedRepo) return;
		treeLoading = true;
		const [owner, name] = selectedRepo.fullName.split('/');
		try {
			repoTree = await api.listRepoTree(owner, name, gitProvider, gitBranch, path);
		} catch {
			repoTree = [];
		} finally {
			treeLoading = false;
		}
	}

	function togglePath(p: string) {
		const next = new Set(selectedPaths);
		if (next.has(p)) {
			next.delete(p);
		} else {
			next.add(p);
		}
		selectedPaths = next;
	}

	function addCustomPath() {
		const trimmed = customPath.trim();
		if (!trimmed) return;
		selectedPaths = new Set([...selectedPaths, trimmed]);
		customPath = '';
	}

	function selectRepo(repo: Repository) {
		selectedRepo = repo;
		repoListOpen = false;
		gitBranch = repo.defaultBranch;
		selectedPaths = new Set();
		customPath = '';
		repoTree = [];
		if (!appNameManuallyEdited) {
			appName = generateAppName(repo.name, gitPath);
		}
		branches = [];
		const [owner, name] = repo.fullName.split('/');
		api
			.listBranches(owner, name, gitProvider)
			.then((list) => { branches = list ?? []; })
			.catch(() => { branches = [{ name: repo.defaultBranch, default: true }]; });
		void loadRepoTree();
	}

	function buildSpec(): AppSpec {
		const filteredUserEnv: Array<{ name: string; value: string }> = userEnvVars.filter(e => e.name.trim() !== '');
		// Env override name: use the currently-active env so any per-env overrides (domain, env vars)
		// land on the env the user is looking at. If none is set yet, fall back to the project's first
		// env, else "production".
		const overrideEnvName = store.currentEnv(project)
			?? store.projectEnvs[project]?.[0]?.name
			?? 'production';
		const hasOverride = !!domain || filteredUserEnv.length > 0 || appKind === 'cron';
		const baseOverride = hasOverride
			? { name: overrideEnvName, ...(domain ? { domain } : {}) }
			: null;

		if (selectedType === 'git') {
			const envs = appKind === 'cron'
				? [{ name: overrideEnvName, replicas: 0, annotations: { 'mortise.dev/schedule': schedule }, ...(domain ? { domain } : {}) }]
				: baseOverride
					? [{ ...baseOverride, ...(filteredUserEnv.length ? { env: filteredUserEnv } : {}) }]
					: undefined;
			return {
				source: {
					type: 'git' as const,
					repo: selectedRepo?.cloneURL ?? '',
					branch: gitBranch,
					path: gitPath || undefined,
					providerRef: gitProvider,
					watchPaths: watchPathsDiffer && [...selectedPaths, customPath.trim()].filter(Boolean).length > 0
						? [...selectedPaths, customPath.trim()].filter(Boolean)
						: gitPath ? [gitPath] : undefined,
					build: {
						mode: buildMode,
						cache: buildCache || undefined,
						dockerfilePath: buildMode === 'dockerfile' ? dockerfilePath : undefined,
						context: buildContext === '' ? undefined : buildContext,
						args: Object.keys(buildArgs).length > 0 ? buildArgs : undefined
					}
				},
				network: { public: true },
				...(envs ? { environments: envs } : {}),
				...(appKind === 'cron' ? { kind: 'cron' as const } : {})
			} as AppSpec;
		}
		if (selectedType === 'image' || selectedType === 'database' || selectedType === 'template') {
			const isDb = selectedType === 'database' && selectedDbTemplate;
			const combinedEnv = [
				...(isDb ? selectedDbTemplate!.env : []),
				...filteredUserEnv
			].filter(e => e.name) as Array<{ name: string; value: string }>;
			const hasImageOverride = !!domain || combinedEnv.length > 0 || appKind === 'cron';
			const envs = appKind === 'cron'
				? [{ name: overrideEnvName, replicas: 0, annotations: { 'mortise.dev/schedule': schedule }, ...(domain ? { domain } : {}) }]
				: hasImageOverride
					? [{
						name: overrideEnvName,
						...(domain ? { domain } : {}),
						...(combinedEnv.length ? { env: combinedEnv } : {})
					}]
					: undefined;
			return {
				source: {
					type: 'image' as const,
					image: imageRef || 'nginx:latest',
					pullSecretRef: pullSecret || undefined
				},
				network: {
					public: selectedType === 'image',
					...(isDb ? { port: selectedDbTemplate!.port } : {})
				},
				...(envs ? { environments: envs } : {}),
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
				...(baseOverride ? { environments: [baseOverride] } : {})
			} as AppSpec;
		}
		// empty
		return {
			source: { type: 'image', image: '' },
			network: { public: true },
			...(baseOverride ? { environments: [baseOverride] } : {})
		};
	}

	async function handleCreate() {
		if (selectedType === 'supabase') {
			await createSupabaseStack();
			return;
		}
		if (selectedType === 'compose') {
			await deployCompose();
			return;
		}
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
	<div class="w-full max-w-lg max-h-[calc(100vh-2rem)] overflow-y-auto rounded-lg border border-surface-600 bg-surface-800 p-6 shadow-xl">
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
						class="group flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left transition-colors hover:bg-surface-700"
					>
						<span class="flex h-8 w-8 items-center justify-center rounded-md bg-surface-800 transition-colors group-hover:bg-surface-700 text-white"
							><svelte:component this={opt.icon} class="h-4 w-4" /></span
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
						<!-- Provider selector (optional - GitHub uses per-user token) -->
						{#if providers.length > 0}
						<div>
							<label class="text-sm text-gray-400">Git Provider</label>
							<select
								bind:value={gitProvider}
								onchange={loadRepos}
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
							>
								<option value="">GitHub (your account)</option>
								{#each providers as p}
									<option value={p.name}>{p.name} ({p.type})</option>
								{/each}
							</select>
						</div>
						{/if}

						<!-- Repo selector -->
						<div>
						{#if reposLoading}
							<div class="flex items-center justify-center h-[14rem] text-sm text-gray-500">Loading repositories...</div>
						{:else if repos.length > 0}
							<div>
								<label class="text-sm text-gray-400">Repository</label>
								{#if selectedRepo && !repoListOpen}
									<button
										type="button"
										onclick={() => repoListOpen = true}
										class="mt-1 flex w-full items-center justify-between rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white hover:border-accent transition-colors"
									>
										<span class="truncate font-medium">{selectedRepo.fullName}</span>
										<span class="text-xs text-gray-400">change</span>
									</button>
								{:else}
									<input
										type="text"
										bind:value={repoSearch}
										placeholder="Search repos..."
										class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
									/>
									<div class="mt-1 max-h-48 overflow-y-auto rounded-md border border-surface-600">
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
								{/if}
							</div>
						{/if}
						</div>

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

						<!-- Root Directory (autocomplete) -->
						<div class="relative">
							<label class="text-sm text-gray-400">Root Directory</label>
							<input
								type="text"
								bind:value={gitPath}
								placeholder="/"
								onfocus={() => (rootDirFocused = true)}
								onblur={() => setTimeout(() => (rootDirFocused = false), 150)}
								onkeydown={(e) => {
									if (e.key === 'Tab' && rootDirFocused && filteredDirs.length > 0) {
										e.preventDefault();
										gitPath = filteredDirs[0] + '/';
										void loadRepoTree(filteredDirs[0]);
									}
								}}
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white font-mono placeholder-gray-500 outline-none focus:border-accent"
							/>
							{#if rootDirFocused && filteredDirs.length > 0}
								<div class="absolute z-10 mt-1 max-h-48 w-full overflow-y-auto rounded-md border border-surface-600 bg-surface-800">
									{#each filteredDirs as dir}
										<button
											type="button"
											onmousedown={() => { gitPath = dir + '/'; rootDirFocused = false; void loadRepoTree(dir); }}
											class="flex w-full px-3 py-2 text-sm font-mono hover:bg-surface-700 {gitPath === dir ? 'bg-surface-600 text-white' : 'text-gray-300'}"
										>
											{dir}/
										</button>
									{/each}
								</div>
							{/if}
							<p class="mt-0.5 text-xs text-gray-500">Where your app's code lives in the repo. Leave as / for the repo root.</p>
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
						{#if buildMode !== 'railpack' && gitPath}
							<div>
								<label class="text-sm text-gray-400">Build context</label>
								<select bind:value={buildContext}
									class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
									<option value="">Auto-detect</option>
									<option value="subdir">Subdirectory (self-contained)</option>
									<option value="root">Repo root (monorepo Dockerfile)</option>
								</select>
								<p class="mt-1 text-xs text-gray-500">Override when the Dockerfile references sibling directories.</p>
							</div>
						{/if}

						<!-- Build cache -->
						<label class="flex items-center gap-2 cursor-pointer">
							<input type="checkbox" bind:checked={buildCache} class="rounded border-surface-600 bg-surface-800 text-accent" />
							<span class="text-sm text-gray-400">Enable build cache</span>
						</label>

						<!-- Advanced options -->
						<div class="border-t border-surface-600 pt-3">
							<button
								type="button"
								onclick={() => (advancedOpen = !advancedOpen)}
								class="flex items-center gap-1.5 text-sm text-gray-400 hover:text-white transition-colors"
							>
								<span class="text-xs">{advancedOpen ? '▾' : '▸'}</span>
								Advanced options
							</button>

							{#if advancedOpen}
								<div class="mt-3 space-y-3">
									<!-- Watch paths toggle -->
									<label class="flex items-center gap-2 cursor-pointer">
										<input type="checkbox" bind:checked={watchPathsDiffer} class="rounded border-surface-600 bg-surface-800 text-accent" />
										<span class="text-sm text-gray-400">Watch path differs from source path</span>
									</label>

									{#if !watchPathsDiffer}
										<p class="text-xs text-gray-500 font-mono">Watching: {gitPath || '/'}</p>
									{:else}
										<!-- Watch paths picker -->
										<div>
											{#if treeLoading}
												<div class="rounded-md border border-surface-600 bg-surface-700 px-3 py-3 text-xs text-gray-500">
													Loading repository tree…
												</div>
											{:else if repoTree.length > 0}
												<div class="max-h-36 overflow-y-auto rounded-md border border-surface-600 bg-surface-700">
													{#each repoTree.slice().sort((a, b) => {
														if (a.type === b.type) return a.name.localeCompare(b.name);
														return a.type === 'tree' ? -1 : 1;
													}) as entry}
														<label class="flex cursor-pointer items-center gap-2 px-3 py-1.5 hover:bg-surface-600">
															<input
																type="checkbox"
																checked={selectedPaths.has(entry.path)}
																onchange={() => togglePath(entry.path)}
																class="rounded border-surface-600 bg-surface-800 text-accent"
															/>
															<span class="font-mono text-xs text-gray-300">{entry.name}{entry.type === 'tree' ? '/' : ''}</span>
														</label>
													{/each}
												</div>
											{:else if selectedRepo}
												<div class="rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-xs text-gray-500">
													No paths found. Enter paths manually below.
												</div>
											{/if}

											<!-- Custom path input -->
											<div class="mt-1.5 flex gap-2">
												<input
													type="text"
													bind:value={customPath}
													placeholder="Add custom path (e.g. src/)"
													onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addCustomPath(); } }}
													class="flex-1 rounded-md border border-surface-600 bg-surface-700 px-3 py-1.5 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
												/>
												<button type="button" onclick={addCustomPath}
													class="rounded-md border border-surface-600 px-2.5 py-1.5 text-xs text-gray-400 hover:bg-surface-600 hover:text-white">
													Add
												</button>
											</div>

											<!-- Selected paths display -->
											{#if selectedPaths.size > 0}
												<div class="mt-1.5 flex flex-wrap gap-1">
													{#each [...selectedPaths] as p}
														<span class="flex items-center gap-1 rounded-full bg-accent/10 px-2 py-0.5 font-mono text-xs text-accent">
															{p}
															<button type="button" onclick={() => togglePath(p)} class="ml-0.5 opacity-60 hover:opacity-100">✕</button>
														</span>
													{/each}
												</div>
											{/if}
										</div>
									{/if}
								</div>
							{:else}
								<p class="mt-1 text-xs text-gray-500 font-mono">Watching: {gitPath || '/'}</p>
							{/if}
						</div>
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
								<div class="mb-1 text-accent"><svelte:component this={tpl.icon} class="h-5 w-5" /></div>
								<div class="text-sm font-medium text-white">{tpl.name}</div>
								<div class="text-xs text-gray-500">{tpl.description}</div>
							</button>
						{/each}
					</div>
				{:else if selectedType === 'supabase'}
					<div class="space-y-3">
						<p class="text-sm text-gray-400">Select which Supabase services to deploy. Secrets and configuration are auto-generated.</p>
						{#if supabaseServices.length > 0}
							<div class="space-y-1.5">
								{#each supabaseServices as svc, i}
									<label class="flex items-center gap-3 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 cursor-pointer hover:border-surface-500 transition-colors" class:opacity-60={svc.required && svc.selected}>
										<input
											type="checkbox"
											checked={svc.selected}
											onchange={(e) => { supabaseServices[i].selected = e.currentTarget.checked; supabaseServices = supabaseServices; }}
											disabled={svc.required}
											class="rounded border-surface-500 bg-surface-700 text-accent focus:ring-accent"
										/>
										<div class="flex-1 min-w-0">
											<div class="flex items-center gap-2">
												<span class="text-sm font-medium text-white">{svc.name}</span>
												{#if svc.required}
													<span class="text-[10px] px-1.5 py-0.5 rounded bg-surface-600 text-gray-400">required</span>
												{/if}
											</div>
											<div class="text-xs text-gray-500">{svc.image}</div>
										</div>
									</label>
								{/each}
							</div>
						{:else}
							<div class="text-sm text-gray-500">Loading services...</div>
						{/if}
						{#if supabaseProgress}
							<p class="text-sm text-accent">{supabaseProgress}</p>
						{/if}
					</div>
				{:else if selectedType === 'compose'}
					<div class="space-y-3">
						<div>
							<label class="text-sm text-gray-400">docker-compose.yml</label>
							<textarea
								bind:value={composeContent}
								placeholder="Paste your docker-compose.yml content here..."
								rows="10"
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent resize-y"
							></textarea>
						</div>
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
					<!-- App name (not shown for supabase/compose — names are auto-generated) -->
					{#if selectedType !== 'supabase' && selectedType !== 'compose'}
					<div>
						<label class="text-sm text-gray-400">App name</label>
						<input
							type="text"
							bind:value={appName}
							oninput={() => { appNameManuallyEdited = true; }}
							placeholder="my-app"
							class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
						/>
					</div>
					{/if}

					<!-- Domain (optional, not shown for external service or supabase) -->
					{#if selectedType !== 'external' && selectedType !== 'supabase' && selectedType !== 'compose'}
						<div>
							<label class="text-sm text-gray-400">Domain <span class="text-gray-600">(optional)</span></label>
							<input
								type="text"
								bind:value={domain}
								placeholder="app.yourdomain.com"
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
							/>
							<p class="mt-0.5 text-xs text-gray-500">Leave blank to auto-assign a subdomain.</p>
						</div>
					{/if}

					<!-- Environment variables -->
					{#if selectedType !== 'external' && selectedType !== 'supabase' && selectedType !== 'compose'}
					<div class="border-t border-surface-600 pt-3">
						<button
							type="button"
							onclick={() => (envVarsOpen = !envVarsOpen)}
							class="flex items-center gap-1.5 text-sm text-gray-400 hover:text-white transition-colors"
						>
							<span class="text-xs">{envVarsOpen ? '▾' : '▸'}</span>
							Environment variables
							{#if userEnvVars.filter(e => e.name.trim()).length > 0}
								<span class="rounded-full bg-accent/20 px-1.5 py-0.5 text-[10px] text-accent">
									{userEnvVars.filter(e => e.name.trim()).length}
								</span>
							{/if}
						</button>
						{#if envVarsOpen}
							<div class="mt-3 space-y-1.5">
								{#each userEnvVars as _, i}
									<div class="flex gap-2">
										<input
											type="text"
											bind:value={userEnvVars[i].name}
											placeholder="VARIABLE_NAME"
											class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2 py-1.5 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent"
										/>
										<input
											type="text"
											bind:value={userEnvVars[i].value}
											placeholder="value"
											class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-2 py-1.5 font-mono text-xs text-white placeholder-gray-500 outline-none focus:border-accent"
										/>
										<button
											type="button"
											onclick={() => (userEnvVars = userEnvVars.filter((_, idx) => idx !== i))}
											class="rounded px-1.5 py-1 text-xs text-gray-500 hover:text-danger"
										>✕</button>
									</div>
								{/each}
								<button
									type="button"
									onclick={() => (userEnvVars = [...userEnvVars, { name: '', value: '' }])}
									class="text-xs text-accent hover:text-accent-hover"
								>+ Add variable</button>
							</div>
						{/if}
					</div>
					{/if}

					<!-- Kind selector (git + image only) -->
					{#if (selectedType === 'git' || selectedType === 'image')}
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
							disabled={submitting || supabaseCreating || composeCreating || (selectedType === 'supabase' && !supabaseServicesLoaded) || (selectedType !== 'supabase' && selectedType !== 'compose' && !appName) || (selectedType === 'compose' && !composeContent.trim())}
							class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
						>
							{#if selectedType === 'supabase'}
								{#if supabaseCreating}
									{supabaseProgress || 'Creating...'}
								{:else if !supabaseServicesLoaded}
									Loading services...
								{:else}
									Create Supabase Stack
								{/if}
							{:else if selectedType === 'compose'}
								{composeCreating ? 'Deploying...' : 'Deploy Stack'}
							{:else}
								{submitting ? 'Creating...' : 'Create app'}
							{/if}
						</button>
					</div>
				</div>
			</div>
		{/if}
	</div>
</div>
