<script lang="ts">
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { Project } from '$lib/types';

	const projectName = $derived(page.params.project ?? '');
	let project = $state<Project | null>(null);
	let loading = $state(true);
	let filterText = $state('');

	// General
	let editDesc = $state('');
	let savingGeneral = $state(false);

	// Danger
	let confirmDeleteText = $state('');
	let deleting = $state(false);

	onMount(async () => {
		try {
			project = await api.getProject(projectName);
			editDesc = project.description ?? '';
		} catch {
			// ignore
		} finally {
			loading = false;
		}
	});

	async function saveGeneral() {
		if (!project) return;
		savingGeneral = true;
		try {
			project = await api.updateProject(project.name, { description: editDesc });
		} catch {
			// ignore
		} finally {
			savingGeneral = false;
		}
	}

	async function deleteProject() {
		if (confirmDeleteText !== projectName) return;
		deleting = true;
		try {
			await api.deleteProject(projectName);
			await goto('/');
		} catch {
			deleting = false;
		}
	}
</script>

<div class="mx-auto max-w-3xl p-8">
	<div class="mb-6">
		<h1 class="text-xl font-semibold text-white">Project Settings</h1>
		<p class="mt-1 text-sm text-gray-500">{projectName}</p>
	</div>

	<!-- Filter -->
	<input
		type="text"
		bind:value={filterText}
		placeholder="Filter settings..."
		class="mb-6 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
	/>

	<!-- General section -->
	<section class="mb-8 space-y-4">
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">General</h2>
		<div>
			<label class="text-sm text-gray-400">Project name</label>
			<input
				type="text"
				value={projectName}
				disabled
				class="mt-1 w-full cursor-not-allowed rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-gray-500"
			/>
			<p class="mt-1 text-xs text-gray-500">Project names cannot be changed after creation.</p>
		</div>
		<div>
			<label class="text-sm text-gray-400">Description</label>
			<input
				type="text"
				bind:value={editDesc}
				placeholder="Optional description"
				class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
			/>
		</div>
		<div class="flex gap-2">
			<button
				type="button"
				onclick={saveGeneral}
				disabled={savingGeneral}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50"
			>
				{savingGeneral ? 'Saving...' : 'Save changes'}
			</button>
		</div>
	</section>

	<!-- PR Environments section -->
	<section class="mb-8 space-y-4">
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">PR Environments</h2>
		<div class="flex items-start justify-between rounded-md border border-surface-600 p-4">
			<div>
				<p class="text-sm font-medium text-white">Enable PR Environments</p>
				<p class="mt-1 text-xs text-gray-500">
					Automatically create preview deployments for pull requests. When enabled, all apps in this
					project participate.
				</p>
			</div>
			<label class="relative inline-flex cursor-pointer items-center">
				<input type="checkbox" class="peer sr-only" />
				<div
					class="peer h-5 w-9 rounded-full bg-surface-600 after:absolute after:left-0.5 after:top-0.5 after:h-4 after:w-4 after:rounded-full after:bg-white after:transition-all peer-checked:bg-accent peer-checked:after:translate-x-4"
				></div>
			</label>
		</div>
	</section>

	<!-- Members section -->
	<section class="mb-8 space-y-4">
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Members</h2>
		<div class="rounded-md border border-surface-600 p-4">
			<p class="text-sm text-gray-400">
				Manage members in <a href="/admin/settings#users" class="text-accent hover:underline"
					>Platform Settings → Users</a
				>.
			</p>
		</div>
	</section>

	<!-- Danger section -->
	<section class="mb-8">
		<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-danger">Danger Zone</h2>
		<div class="mt-4 space-y-3 rounded-md border border-danger/30 bg-danger/5 p-4">
			<div>
				<p class="text-sm font-medium text-white">Delete Project</p>
				<p class="mt-1 text-xs text-gray-500">
					This will permanently delete all apps and resources. Type the project name to confirm.
				</p>
			</div>
			<input
				type="text"
				bind:value={confirmDeleteText}
				placeholder={projectName}
				class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-danger"
			/>
			<button
				type="button"
				onclick={deleteProject}
				disabled={confirmDeleteText !== projectName || deleting}
				class="rounded-md bg-danger px-4 py-2 text-sm font-medium text-white hover:bg-danger/80 disabled:cursor-not-allowed disabled:opacity-50"
			>
				{deleting ? 'Deleting...' : 'Delete project'}
			</button>
		</div>
	</section>
</div>
