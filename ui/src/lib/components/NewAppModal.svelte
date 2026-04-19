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

	type AppType = 'git' | 'image' | 'database' | 'supabase' | 'template' | 'external' | 'empty';

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
	type SupabaseService = {
		id: string;
		name: string;
		image: string;
		port: number;
		required: boolean;
		description: string;
		env: (shared: { jwtSecret: string; pgPassword: string; projectRef: string }) => Array<{ name: string; value: string }>;
	};

	const SUPABASE_SERVICES: SupabaseService[] = [
		{
			id: 'postgres', name: 'PostgreSQL', image: 'supabase/postgres:15.6.1.143', port: 5432, required: true,
			description: 'Core database (with Supabase extensions, schemas, and roles)',
			env: (s) => [
				{ name: 'POSTGRES_PASSWORD', value: s.pgPassword },
				{ name: 'POSTGRES_DB', value: 'supabase' },
				{ name: 'JWT_SECRET', value: s.jwtSecret },
				{ name: 'SUPABASE_AUTH_ADMIN_PASSWORD', value: s.pgPassword },
				{ name: 'SUPABASE_STORAGE_ADMIN_PASSWORD', value: s.pgPassword }
			]
		},
		{
			id: 'auth', name: 'GoTrue (Auth)', image: 'supabase/gotrue:v2.164.0', port: 9999, required: false,
			description: 'Authentication & user management',
			env: (s) => [
				{ name: 'GOTRUE_DB_DRIVER', value: 'postgres' },
				{ name: 'GOTRUE_DB_DATABASE_URL', value: `postgres://supabase_admin:${s.pgPassword}@supabase-postgres-production:5432/supabase?sslmode=disable` },
				{ name: 'DATABASE_URL', value: `postgres://supabase_admin:${s.pgPassword}@supabase-postgres-production:5432/supabase?sslmode=disable` },
				{ name: 'GOTRUE_JWT_SECRET', value: s.jwtSecret },
				{ name: 'GOTRUE_JWT_EXP', value: '3600' },
				{ name: 'GOTRUE_SITE_URL', value: 'http://localhost' },
				{ name: 'API_EXTERNAL_URL', value: 'http://localhost' },
				{ name: 'GOTRUE_MAILER_AUTOCONFIRM', value: 'true' },
				{ name: 'GOTRUE_EXTERNAL_EMAIL_ENABLED', value: 'true' },
				{ name: 'GOTRUE_DISABLE_SIGNUP', value: 'false' },
				{ name: 'GOTRUE_API_HOST', value: '0.0.0.0' },
				{ name: 'PORT', value: '9999' }
			]
		},
		{
			id: 'rest', name: 'PostgREST (REST API)', image: 'postgrest/postgrest:v12.2.3', port: 3000, required: false,
			description: 'Auto-generated REST API from your Postgres schema',
			env: (s) => [
				{ name: 'PGRST_DB_URI', value: `postgres://supabase_admin:${s.pgPassword}@supabase-postgres-production:5432/supabase` },
				{ name: 'PGRST_DB_SCHEMA', value: 'public,storage' },
				{ name: 'PGRST_DB_ANON_ROLE', value: 'anon' },
				{ name: 'PGRST_JWT_SECRET', value: s.jwtSecret },
				{ name: 'PGRST_DB_USE_LEGACY_GUCS', value: 'false' }
			]
		},
		{
			id: 'realtime', name: 'Realtime', image: 'supabase/realtime:v2.33.58', port: 4000, required: false,
			description: 'WebSocket-based realtime subscriptions',
			env: (s) => [
				{ name: 'DB_HOST', value: 'supabase-postgres-production' },
				{ name: 'DB_PORT', value: '5432' },
				{ name: 'DB_USER', value: 'supabase_admin' },
				{ name: 'DB_PASSWORD', value: s.pgPassword },
				{ name: 'DB_NAME', value: 'supabase' },
				{ name: 'DB_AFTER_CONNECT_QUERY', value: 'SET search_path TO realtime' },
				{ name: 'PORT', value: '4000' },
				{ name: 'JWT_SECRET', value: s.jwtSecret },
				{ name: 'SECURE_CHANNELS', value: 'true' },
				{ name: 'SECRET_KEY_BASE', value: generatePassword(64) },
				{ name: 'ERL_AFLAGS', value: '-proto_dist inet_tcp' },
				{ name: 'DNS_NODES', value: '' },
				{ name: 'RLIMIT_NOFILE', value: '10000' }
			]
		},
		{
			id: 'storage', name: 'Storage', image: 'supabase/storage-api:v1.11.13', port: 5000, required: false,
			description: 'S3-compatible file storage',
			env: (s) => [
				{ name: 'DATABASE_URL', value: `postgres://supabase_admin:${s.pgPassword}@supabase-postgres-production:5432/supabase` },
				{ name: 'PGRST_JWT_SECRET', value: s.jwtSecret },
				{ name: 'ANON_KEY', value: s.jwtSecret },
				{ name: 'SERVICE_KEY', value: s.jwtSecret },
				{ name: 'STORAGE_BACKEND', value: 'file' },
				{ name: 'FILE_STORAGE_BACKEND_PATH', value: '/var/lib/storage' }
			]
		}
	];

	let supabaseSelected = $state<Set<string>>(new Set(SUPABASE_SERVICES.map(s => s.id)));
	let supabaseProgress = $state('');
	let supabaseCreating = $state(false);

	function toggleSupabaseService(id: string) {
		const svc = SUPABASE_SERVICES.find(s => s.id === id);
		if (svc?.required) return;
		const next = new Set(supabaseSelected);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		supabaseSelected = next;
	}

	function toggleAllSupabaseOptional() {
		const optionalIds = SUPABASE_SERVICES.filter(s => !s.required).map(s => s.id);
		const allSelected = optionalIds.every(id => supabaseSelected.has(id));
		const next = new Set(supabaseSelected);
		if (allSelected) {
			optionalIds.forEach(id => next.delete(id));
		} else {
			optionalIds.forEach(id => next.add(id));
		}
		supabaseSelected = next;
	}

	async function createSupabaseStack() {
		supabaseCreating = true;
		error = '';
		const shared = {
			jwtSecret: generatePassword(48),
			pgPassword: generatePassword(24),
			projectRef: generatePassword(20)
		};
		const selected = SUPABASE_SERVICES.filter(s => supabaseSelected.has(s.id));
		const total = selected.length;

		try {
			for (let i = 0; i < selected.length; i++) {
				const svc = selected[i];
				supabaseProgress = `Creating ${svc.name} (${i + 1}/${total})...`;
				const svcName = `supabase-${svc.id}`;
				const isPublic = svc.id === 'kong';
				const spec: AppSpec = {
					source: { type: 'image', image: svc.image },
					network: { public: isPublic, port: svc.port },
					environments: [{ name: 'production', replicas: 1, env: svc.env(shared) }]
				};
				await api.createApp(project, svcName, spec);
			}
			onCreated('supabase-postgres');
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to create Supabase stack';
		} finally {
			supabaseCreating = false;
			supabaseProgress = '';
		}
	}

	let providers = $state<GitProviderSummary[]>([]);

	const typeOptions: { type: AppType; icon: ComponentType; label: string; description: string }[] = [
		{ type: 'git', icon: GitBranchIcon, label: 'Git Repository', description: 'Deploy from a connected git provider' },
		{ type: 'database', icon: Database, label: 'Database', description: 'Postgres, Redis, MinIO, MySQL' },
		{ type: 'supabase', icon: Database, label: 'Supabase', description: 'Self-hosted Supabase (auth, database, storage, realtime)' },
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
		const baseEnv: AppSpec['environments'] = [{ name: 'production', replicas: 1, ...(domain ? { domain } : {}) }];

		if (selectedType === 'git') {
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
						args: Object.keys(buildArgs).length > 0 ? buildArgs : undefined
					}
				},
				network: { public: true },
				environments: appKind === 'cron'
					? [{ name: 'production', replicas: 0, annotations: { 'mortise.dev/schedule': schedule }, ...(domain ? { domain } : {}) }]
					: baseEnv,
				...(appKind === 'cron' ? { kind: 'cron' as const } : {})
			} as AppSpec;
		}
		if (selectedType === 'image' || selectedType === 'database' || selectedType === 'template') {
			const isDb = selectedType === 'database' && selectedDbTemplate;
			const envs = appKind === 'cron'
				? [{ name: 'production', replicas: 0, annotations: { 'mortise.dev/schedule': schedule }, ...(domain ? { domain } : {}) }]
				: [{
					...baseEnv[0],
					...(isDb ? { env: selectedDbTemplate!.env } : {})
				}];
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
				environments: envs,
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
		if (selectedType === 'supabase') {
			submitting = true;
			await createSupabaseStack();
			submitting = false;
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
						class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left transition-colors hover:bg-surface-700"
					>
						<span class="flex h-8 w-8 items-center justify-center rounded-md bg-surface-700 text-accent"
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
						<div class="flex items-center justify-between">
							<label class="text-sm text-gray-400">Services</label>
							<button
								type="button"
								onclick={toggleAllSupabaseOptional}
								class="text-xs text-accent hover:text-accent-hover"
							>
								{SUPABASE_SERVICES.filter(s => !s.required).every(s => supabaseSelected.has(s.id)) ? 'Deselect Optional' : 'Select All'}
							</button>
						</div>
						<div class="space-y-1 rounded-md border border-surface-600 bg-surface-700 p-2">
							{#each SUPABASE_SERVICES as svc}
								<label class="flex cursor-pointer items-center gap-2.5 rounded px-2 py-1.5 hover:bg-surface-600 {svc.required ? 'opacity-80' : ''}">
									<input
										type="checkbox"
										checked={supabaseSelected.has(svc.id)}
										disabled={svc.required}
										onchange={() => toggleSupabaseService(svc.id)}
										class="rounded border-surface-600 bg-surface-800 text-accent"
									/>
									<div class="flex-1 min-w-0">
										<span class="text-sm text-white">{svc.name}</span>
										{#if svc.required}<span class="ml-1 text-xs text-gray-500">(required)</span>{/if}
										<p class="text-xs text-gray-500 truncate">{svc.description}</p>
									</div>
								</label>
							{/each}
						</div>
						<p class="text-xs text-gray-500">A shared JWT secret will be auto-generated across all services.</p>
						{#if supabaseProgress}
							<p class="text-sm text-accent">{supabaseProgress}</p>
						{/if}
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
					<!-- App name (not shown for supabase — names are auto-generated) -->
					{#if selectedType !== 'supabase'}
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
					{#if selectedType !== 'external' && selectedType !== 'supabase'}
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
							disabled={submitting || (selectedType !== 'supabase' && !appName)}
							class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
						>
							{#if selectedType === 'supabase'}
								{submitting ? supabaseProgress || 'Creating...' : 'Create Supabase Stack'}
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
