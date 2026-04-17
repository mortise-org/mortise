<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import AppForm from '$lib/AppForm.svelte';
	import { templates, getTemplate } from '$lib/templates';
	import type { GitProviderSummary, Repository, Branch, AppSpec } from '$lib/types';

	const projectName = $derived(page.params.project ?? '');

	// --- provider + repo state ---
	let providers = $state<GitProviderSummary[]>([]);
	let providersLoaded = $state(false);
	const connectedProvider = $derived(providers.find((p) => p.hasToken && p.phase === 'Ready'));

	let repos = $state<Repository[]>([]);
	let reposLoading = $state(false);
	let reposError = $state('');
	let searchQuery = $state('');

	const filteredRepos = $derived(
		searchQuery.trim()
			? repos.filter((r) =>
					r.fullName.toLowerCase().includes(searchQuery.toLowerCase()) ||
					(r.description ?? '').toLowerCase().includes(searchQuery.toLowerCase())
				)
			: repos
	);

	// --- selected repo config state ---
	let selectedRepo = $state<Repository | null>(null);
	let branches = $state<Branch[]>([]);
	let branchesLoading = $state(false);
	let selectedBranch = $state('');
	let rootDir = $state('');
	let appName = $state('');
	let appDomain = $state('');
	let deploying = $state(false);
	let deployError = $state('');

	// --- docker image state ---
	let dockerImage = $state('');
	let dockerDeploying = $state(false);
	let dockerError = $state('');

	// --- template state ---
	let selectedTemplateId = $state<string | null>(null);
	const selectedTemplate = $derived(selectedTemplateId ? getTemplate(selectedTemplateId) : undefined);
	const compactTemplates = templates.filter((t) => t.category !== 'blank');

	// --- language color map ---
	const langColors: Record<string, string> = {
		JavaScript: '#f1e05a',
		TypeScript: '#3178c6',
		Python: '#3572a5',
		Go: '#00add8',
		Rust: '#dea584',
		Ruby: '#701516',
		Java: '#b07219',
		PHP: '#4f5d95',
		'C#': '#178600',
		Swift: '#f05138'
	};

	// --- relative time helper ---
	function relativeTime(iso: string): string {
		const now = Date.now();
		const then = new Date(iso).getTime();
		const diff = now - then;
		const seconds = Math.floor(diff / 1000);
		if (seconds < 60) return 'just now';
		const minutes = Math.floor(seconds / 60);
		if (minutes < 60) return `${minutes}m ago`;
		const hours = Math.floor(minutes / 60);
		if (hours < 24) return `${hours}h ago`;
		const days = Math.floor(hours / 24);
		if (days < 30) return `${days}d ago`;
		const months = Math.floor(days / 30);
		return `${months}mo ago`;
	}

	// --- lifecycle ---
	onMount(async () => {
		try {
			const list = await api.listGitProviders();
			providers = list ?? [];
		} catch {
			providers = [];
		} finally {
			providersLoaded = true;
		}
	});

	$effect(() => {
		const p = connectedProvider;
		if (!p) return;
		reposLoading = true;
		reposError = '';
		api
			.listRepos(p.name)
			.then((list) => {
				repos = (list ?? []).sort(
					(a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime()
				);
			})
			.catch((err) => {
				reposError = err instanceof Error ? err.message : 'Failed to load repositories';
			})
			.finally(() => {
				reposLoading = false;
			});
	});

	function selectRepo(repo: Repository) {
		selectedRepo = repo;
		selectedBranch = repo.defaultBranch;
		rootDir = '';
		appName = repo.name.toLowerCase().replace(/[^a-z0-9-]/g, '-');
		appDomain = '';
		deployError = '';
		branches = [];
		branchesLoading = true;

		const [owner, name] = repo.fullName.split('/');
		api
			.listBranches(owner, name, connectedProvider!.name)
			.then((list) => {
				branches = list ?? [];
			})
			.catch(() => {
				branches = [{ name: repo.defaultBranch, default: true }];
			})
			.finally(() => {
				branchesLoading = false;
			});
	}

	function backToRepoList() {
		selectedRepo = null;
		branches = [];
		deployError = '';
	}

	async function deployFromRepo() {
		if (!selectedRepo || !connectedProvider) return;
		deploying = true;
		deployError = '';

		try {
			const spec: AppSpec = {
				source: {
					type: 'git',
					repo: selectedRepo.cloneURL,
					branch: selectedBranch || selectedRepo.defaultBranch,
					providerRef: connectedProvider.name,
					build: { mode: 'auto' },
					...(rootDir.trim() ? { path: rootDir.trim() } : {})
				},
				network: { public: true },
				environments: [
					{
						name: 'production',
						replicas: 1,
						...(appDomain.trim() ? { domain: appDomain.trim() } : {})
					}
				]
			};

			await api.createApp(projectName, appName, spec);
			goto(`/projects/${encodeURIComponent(projectName)}`);
		} catch (err) {
			deployError = err instanceof Error ? err.message : 'Failed to deploy';
		} finally {
			deploying = false;
		}
	}

	async function deployDockerImage() {
		if (!dockerImage.trim()) return;
		dockerDeploying = true;
		dockerError = '';

		try {
			const imageName = dockerImage.trim().split('/').pop()?.split(':')[0] ?? 'app';
			const safeName = imageName.toLowerCase().replace(/[^a-z0-9-]/g, '-');

			const spec: AppSpec = {
				source: { type: 'image', image: dockerImage.trim() },
				network: { public: true },
				environments: [{ name: 'production', replicas: 1 }]
			};

			await api.createApp(projectName, safeName, spec);
			goto(`/projects/${encodeURIComponent(projectName)}`);
		} catch (err) {
			dockerError = err instanceof Error ? err.message : 'Failed to deploy';
		} finally {
			dockerDeploying = false;
		}
	}

	function templateBack() {
		selectedTemplateId = null;
	}
</script>

{#if selectedTemplate}
	<AppForm project={projectName} template={selectedTemplate} onBack={templateBack} />
{:else}
	<div class="mx-auto max-w-3xl">
		<a
			href="/projects/{encodeURIComponent(projectName)}"
			class="mb-4 inline-block text-sm text-gray-500 transition-colors hover:text-white"
		>
			&larr; Back to {projectName}
		</a>

		<h1 class="mb-6 text-xl font-semibold text-white">Deploy a new service</h1>

		<!-- Section 1: GitHub Repo -->
		<section class="rounded-lg border border-surface-600 bg-surface-800 p-5">
			<h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-gray-400">
				Deploy from a Git repo
			</h2>

			{#if !providersLoaded}
				<div class="flex items-center gap-2 py-8 text-sm text-gray-500">
					<svg class="h-4 w-4 animate-spin" viewBox="0 0 24 24" fill="none">
						<circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" class="opacity-25" />
						<path d="M4 12a8 8 0 018-8" stroke="currentColor" stroke-width="3" stroke-linecap="round" class="opacity-75" />
					</svg>
					Loading providers...
				</div>
			{:else if !connectedProvider}
				<!-- No provider connected -->
				<div class="flex flex-col items-center gap-3 rounded-md border border-dashed border-surface-500 bg-surface-900/50 px-6 py-10 text-center">
					<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="h-8 w-8 text-gray-500">
						<path stroke-linecap="round" stroke-linejoin="round" d="M13.19 8.688a4.5 4.5 0 0 1 1.242 7.244l-4.5 4.5a4.5 4.5 0 0 1-6.364-6.364l1.757-1.757m13.35-.622 1.757-1.757a4.5 4.5 0 0 0-6.364-6.364l-4.5 4.5a4.5 4.5 0 0 0 1.242 7.244" />
					</svg>
					<p class="text-sm text-gray-400">Connect a git provider to deploy from a repository</p>
					<a
						href="/settings/git-providers"
						class="mt-1 inline-flex items-center gap-1 rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
					>
						Connect GitHub
						<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" class="h-3.5 w-3.5">
							<path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5 21 12m0 0-7.5 7.5M21 12H3" />
						</svg>
					</a>
				</div>
			{:else if selectedRepo}
				<!-- Repo config panel -->
				<div>
					<button
						type="button"
						onclick={backToRepoList}
						class="mb-4 text-sm text-gray-500 transition-colors hover:text-white"
					>
						&larr; Back
					</button>

					<div class="mb-5">
						<div class="flex items-center gap-2">
							{#if selectedRepo.private}
								<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="h-4 w-4 text-gray-500">
									<path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
								</svg>
							{:else}
								<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="h-4 w-4 text-gray-500">
									<path stroke-linecap="round" stroke-linejoin="round" d="M12 21a9.004 9.004 0 0 0 8.716-6.747M12 21a9.004 9.004 0 0 1-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 0 1 7.843 4.582M12 3a8.997 8.997 0 0 0-7.843 4.582m15.686 0A11.953 11.953 0 0 1 12 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0 1 21 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0 1 12 16.5a17.92 17.92 0 0 1-8.716-2.247m0 0A8.966 8.966 0 0 1 3 12c0-1.97.633-3.794 1.708-5.282" />
								</svg>
							{/if}
							<h3 class="text-base font-medium text-white">{selectedRepo.fullName}</h3>
						</div>
						<p class="mt-1 text-sm text-gray-500">
							{selectedRepo.description || 'No description'}
							{#if selectedRepo.language}
								<span class="mx-1">·</span>
								<span class="inline-flex items-center gap-1">
									<span
										class="inline-block h-2.5 w-2.5 rounded-full"
										style="background-color: {langColors[selectedRepo.language] ?? '#888'}"
									></span>
									{selectedRepo.language}
								</span>
							{/if}
							<span class="mx-1">·</span>
							Last push: {relativeTime(selectedRepo.updatedAt)}
						</p>
					</div>

					{#if deployError}
						<div class="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{deployError}</div>
					{/if}

					<form onsubmit={(e) => { e.preventDefault(); deployFromRepo(); }} class="space-y-4">
						<div>
							<label for="branch" class="mb-1 block text-sm text-gray-400">Branch</label>
							{#if branchesLoading}
								<div class="rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-gray-500">Loading branches...</div>
							{:else}
								<select
									id="branch"
									bind:value={selectedBranch}
									class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 text-sm text-white outline-none focus:border-accent"
								>
									{#each branches as b}
										<option value={b.name}>{b.name}{b.default ? ' (default)' : ''}</option>
									{/each}
								</select>
							{/if}
						</div>

						<div>
							<label for="root-dir" class="mb-1 block text-sm text-gray-400">Root directory</label>
							<input
								id="root-dir"
								type="text"
								bind:value={rootDir}
								class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
								placeholder="/"
							/>
							<p class="mt-1 text-xs text-gray-500">For monorepos. Leave as / for the repo root.</p>
						</div>

						<div>
							<label for="app-name" class="mb-1 block text-sm text-gray-400">App name</label>
							<input
								id="app-name"
								type="text"
								bind:value={appName}
								required
								pattern="[a-z0-9-]+"
								class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
								placeholder="my-app"
							/>
						</div>

						<div>
							<label for="app-domain" class="mb-1 block text-sm text-gray-400">Domain <span class="text-gray-600">(optional)</span></label>
							<input
								id="app-domain"
								type="text"
								bind:value={appDomain}
								class="w-full rounded-md border border-surface-600 bg-surface-900 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
								placeholder="{appName}.yourdomain.com"
							/>
						</div>

						<button
							type="submit"
							disabled={deploying || !appName.trim()}
							class="w-full rounded-md bg-accent px-4 py-2.5 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
						>
							{deploying ? 'Deploying...' : 'Deploy Now'}
						</button>
					</form>
				</div>
			{:else}
				<!-- Repo list -->
				<div>
					<div class="relative mb-3">
						<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-gray-500">
							<path stroke-linecap="round" stroke-linejoin="round" d="m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z" />
						</svg>
						<!-- svelte-ignore a11y_autofocus -->
						<input
							type="text"
							bind:value={searchQuery}
							autofocus
							class="w-full rounded-md border border-surface-600 bg-surface-900 py-2 pl-9 pr-3 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
							placeholder="Search repositories..."
						/>
					</div>

					{#if reposLoading}
						<div class="space-y-2">
							{#each Array(4) as _}
								<div class="h-14 animate-pulse rounded-md bg-surface-700"></div>
							{/each}
						</div>
					{:else if reposError}
						<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{reposError}</div>
					{:else if filteredRepos.length === 0}
						<p class="py-6 text-center text-sm text-gray-500">
							{searchQuery ? 'No repositories match your search.' : 'No repositories found.'}
						</p>
					{:else}
						<div class="max-h-[420px] overflow-y-auto rounded-md border border-surface-600">
							{#each filteredRepos as repo, i}
								<button
									type="button"
									onclick={() => selectRepo(repo)}
									class="flex w-full items-start gap-3 px-4 py-3 text-left transition-colors hover:bg-surface-700 {i > 0 ? 'border-t border-surface-600' : ''}"
								>
									{#if repo.private}
										<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="mt-0.5 h-4 w-4 shrink-0 text-gray-500">
											<path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z" />
										</svg>
									{:else}
										<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="mt-0.5 h-4 w-4 shrink-0 text-gray-500">
											<path stroke-linecap="round" stroke-linejoin="round" d="M12 21a9.004 9.004 0 0 0 8.716-6.747M12 21a9.004 9.004 0 0 1-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 0 1 7.843 4.582M12 3a8.997 8.997 0 0 0-7.843 4.582m15.686 0A11.953 11.953 0 0 1 12 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0 1 21 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0 1 12 16.5a17.92 17.92 0 0 1-8.716-2.247m0 0A8.966 8.966 0 0 1 3 12c0-1.97.633-3.794 1.708-5.282" />
										</svg>
									{/if}
									<div class="min-w-0 flex-1">
										<div class="truncate text-sm font-medium text-white">{repo.fullName}</div>
										<div class="mt-0.5 flex items-center gap-2 text-xs text-gray-500">
											{#if repo.description}
												<span class="truncate">{repo.description}</span>
												<span class="shrink-0">·</span>
											{/if}
											{#if repo.language}
												<span class="inline-flex shrink-0 items-center gap-1">
													<span
														class="inline-block h-2 w-2 rounded-full"
														style="background-color: {langColors[repo.language] ?? '#888'}"
													></span>
													{repo.language}
												</span>
												<span class="shrink-0">·</span>
											{/if}
											<span class="shrink-0">{relativeTime(repo.updatedAt)}</span>
										</div>
									</div>
								</button>
							{/each}
						</div>
					{/if}

					<div class="mt-3 flex items-center justify-between text-xs text-gray-500">
						<span>
							Connected: {connectedProvider.type}
							({connectedProvider.name})
						</span>
						<a href="/settings/git-providers" class="text-accent hover:underline">Manage</a>
					</div>
				</div>
			{/if}
		</section>

		<!-- Divider -->
		<div class="my-5 flex items-center gap-3">
			<div class="h-px flex-1 bg-surface-600"></div>
			<span class="text-xs text-gray-500">or</span>
			<div class="h-px flex-1 bg-surface-600"></div>
		</div>

		<!-- Section 2: Docker Image -->
		<section class="rounded-lg border border-surface-600 bg-surface-800 p-5">
			<h2 class="mb-3 text-sm font-medium uppercase tracking-wide text-gray-400">
				Deploy a Docker image
			</h2>

			{#if dockerError}
				<div class="mb-3 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{dockerError}</div>
			{/if}

			<form onsubmit={(e) => { e.preventDefault(); deployDockerImage(); }} class="flex gap-2">
				<input
					type="text"
					bind:value={dockerImage}
					class="flex-1 rounded-md border border-surface-600 bg-surface-900 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					placeholder="ghcr.io/org/image:tag"
				/>
				<button
					type="submit"
					disabled={dockerDeploying || !dockerImage.trim()}
					class="shrink-0 rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
				>
					{dockerDeploying ? 'Deploying...' : 'Deploy'}
				</button>
			</form>
		</section>

		<!-- Divider -->
		<div class="my-5 flex items-center gap-3">
			<div class="h-px flex-1 bg-surface-600"></div>
			<span class="text-xs text-gray-500">or</span>
			<div class="h-px flex-1 bg-surface-600"></div>
		</div>

		<!-- Section 3: Templates (compact) -->
		<section class="rounded-lg border border-surface-600 bg-surface-800 p-5">
			<h2 class="mb-3 text-sm font-medium uppercase tracking-wide text-gray-400">
				Deploy from a template
			</h2>
			<div class="flex flex-wrap gap-2">
				{#each compactTemplates as tmpl}
					<button
						type="button"
						onclick={() => (selectedTemplateId = tmpl.id)}
						class="inline-flex items-center gap-1.5 rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white transition-colors hover:border-accent/60 hover:bg-surface-600"
					>
						<span>{tmpl.icon}</span>
						<span>{tmpl.name}</span>
					</button>
				{/each}
			</div>
		</section>
	</div>
{/if}
