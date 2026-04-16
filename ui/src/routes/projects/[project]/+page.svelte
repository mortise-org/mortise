<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { App, AppPhase, Project } from '$lib/types';

	const projectName = $derived(page.params.project ?? '');

	let project = $state<Project | null>(null);
	let apps = $state<App[]>([]);
	let loading = $state(true);
	let error = $state('');
	let deletingProject = $state(false);

	onMount(async () => {
		if (!localStorage.getItem('token')) {
			goto('/login');
			return;
		}
		await load();
	});

	$effect(() => {
		// Refetch if the user navigates between projects without remount.
		void projectName;
		if (!loading && projectName && projectName !== project?.name) {
			void load();
		}
	});

	async function load() {
		loading = true;
		error = '';
		try {
			[project, apps] = await Promise.all([
				api.getProject(projectName),
				api.listApps(projectName)
			]);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load project';
		} finally {
			loading = false;
		}
	}

	async function handleDeleteProject() {
		if (!project) return;
		const confirmation = prompt(
			`Deleting "${project.name}" will remove all ${project.appCount} app${
				project.appCount === 1 ? '' : 's'
			} inside it. This cannot be undone.\n\nType the project name to confirm:`
		);
		if (confirmation !== project.name) {
			return;
		}
		deletingProject = true;
		try {
			await api.deleteProject(project.name);
			await goto('/');
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to delete project';
			deletingProject = false;
		}
	}

	const phaseStyles: Record<AppPhase, { dot: string; text: string }> = {
		Ready: { dot: 'bg-success', text: 'text-success' },
		Deploying: { dot: 'bg-accent animate-pulse', text: 'text-accent' },
		Building: { dot: 'bg-accent animate-pulse', text: 'text-accent' },
		Pending: { dot: 'bg-warning', text: 'text-warning' },
		Failed: { dot: 'bg-danger', text: 'text-danger' }
	};

	function lastDeploy(app: App): string {
		const envs = app.status?.environments;
		if (!envs?.length) return 'Never';
		const history = envs[0].deployHistory;
		if (!history?.length) return 'Never';
		const ts = history[history.length - 1].timestamp;
		return new Date(ts).toLocaleDateString('en-US', {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	function replicaSummary(app: App): string {
		const env = app.spec.environments?.[0];
		const envStatus = app.status?.environments?.find((e) => e.name === env?.name);
		const ready = envStatus?.readyReplicas ?? 0;
		const desired = env?.replicas ?? 1;
		return `${ready} / ${desired}`;
	}
</script>

{#if loading}
	<div class="animate-pulse">
		<div class="mb-6 h-6 w-48 rounded bg-surface-700"></div>
		<div class="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
			{#each Array(3) as _}
				<div class="h-28 rounded-lg bg-surface-800"></div>
			{/each}
		</div>
	</div>
{:else if error && !project}
	<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
{:else if project}
	<div>
		{#if error}
			<div class="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
		{/if}

		<!-- Header -->
		<div class="mb-6 flex items-start justify-between gap-4">
			<div class="min-w-0">
				<div class="flex items-center gap-2 text-xs text-gray-500">
					<a href="/" class="hover:text-white">Projects</a>
					<span>/</span>
					<span class="font-mono text-gray-400">{project.namespace}</span>
				</div>
				<h1 class="mt-1 truncate text-xl font-semibold text-white">{project.name}</h1>
				{#if project.description}
					<p class="mt-1 max-w-2xl text-sm text-gray-500">{project.description}</p>
				{/if}
			</div>
			<div class="flex shrink-0 items-center gap-2">
				<a
					href="/projects/{encodeURIComponent(project.name)}/apps/new"
					class="inline-flex items-center gap-2 rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
				>
					<span aria-hidden="true">+</span>
					Deploy app
				</a>
				<button
					type="button"
					onclick={handleDeleteProject}
					disabled={deletingProject}
					class="rounded-md border border-danger/30 px-3 py-2 text-xs text-danger transition-colors hover:bg-danger/10 disabled:opacity-50"
				>
					{deletingProject ? 'Deleting...' : 'Delete project'}
				</button>
			</div>
		</div>

		{#if apps.length === 0}
			<div
				class="rounded-lg border border-dashed border-surface-600 bg-surface-800/60 p-12 text-center"
			>
				<div
					class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-surface-700 text-accent"
					aria-hidden="true"
				>
					<svg
						xmlns="http://www.w3.org/2000/svg"
						fill="none"
						viewBox="0 0 24 24"
						stroke-width="1.5"
						stroke="currentColor"
						class="h-7 w-7"
					>
						<path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
					</svg>
				</div>
				<h2 class="text-base font-medium text-white">No apps in this project</h2>
				<p class="mx-auto mt-1 max-w-sm text-sm text-gray-500">
					Deploy your first app — pick a template or start from a container image.
				</p>
				<a
					href="/projects/{encodeURIComponent(project.name)}/apps/new"
					class="mt-5 inline-block rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
				>
					Deploy an app
				</a>
			</div>
		{:else}
			<div class="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
				{#each apps as app}
					{@const phase = app.status?.phase}
					{@const style = phase ? phaseStyles[phase] : undefined}
					<a
						href="/projects/{encodeURIComponent(project.name)}/apps/{encodeURIComponent(
							app.metadata.name
						)}"
						class="group block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-all duration-150 hover:-translate-y-0.5 hover:border-surface-500 hover:shadow-lg hover:shadow-black/20"
					>
						<div class="flex items-start justify-between gap-3">
							<h2 class="truncate font-medium text-white group-hover:text-accent">
								{app.metadata.name}
							</h2>
							{#if phase}
								<span
									class="inline-flex shrink-0 items-center gap-1.5 text-xs font-medium {style?.text ??
										'text-gray-400'}"
								>
									<span class="h-1.5 w-1.5 rounded-full {style?.dot ?? 'bg-gray-500'}"></span>
									{phase}
								</span>
							{/if}
						</div>
						<dl class="mt-4 space-y-1.5 text-xs">
							<div class="flex justify-between">
								<dt class="text-gray-500">Source</dt>
								<dd class="text-gray-300">{app.spec.source.type}</dd>
							</div>
							<div class="flex justify-between">
								<dt class="text-gray-500">Replicas</dt>
								<dd class="text-gray-300">{replicaSummary(app)}</dd>
							</div>
							<div class="flex justify-between">
								<dt class="text-gray-500">Last deploy</dt>
								<dd class="text-gray-300">{lastDeploy(app)}</dd>
							</div>
						</dl>
					</a>
				{/each}
			</div>
		{/if}
	</div>
{/if}
