<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';

	let name = $state('');
	let description = $state('');
	let loading = $state(false);
	let error = $state('');

	// RFC 1123 DNS label: lowercase letters, digits, and hyphens;
	// must start and end with an alphanumeric; max 63 chars.
	const DNS_LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

	async function handleCreate() {
		if (!name) return;
		error = '';
		const trimmed = name.trim();
		if (!DNS_LABEL.test(trimmed)) {
			error = 'Project name must be 1-63 lowercase letters, digits, or hyphens, starting and ending with alphanumeric.';
			return;
		}
		loading = true;
		try {
			const project = await api.createProject(trimmed, description.trim() || undefined);
			await goto(`/projects/${encodeURIComponent(project.name)}`);
		} catch(e) {
			error = e instanceof Error ? e.message : 'Failed to create project';
		} finally {
			loading = false;
		}
	}
</script>

<div class="p-8 max-w-lg mx-auto">
	<div class="mb-6">
		<a href="/" class="text-sm text-gray-500 hover:text-white">← Back to Projects</a>
		<h1 class="mt-4 text-xl font-semibold text-white">New Project</h1>
		<p class="mt-1 text-sm text-gray-400">A project is an isolated workspace where your apps run together — your frontend, backend, and database all in one place. Each project gets its own namespace on the cluster.</p>
	</div>

	<div class="rounded-lg border border-surface-600 bg-surface-800 p-6">
		<form onsubmit={(e) => { e.preventDefault(); handleCreate(); }} class="space-y-4">
			<div>
				<label for="name" class="block text-sm text-gray-400">Project name <span class="text-danger">*</span></label>
				<input id="name" type="text" bind:value={name}
					placeholder="my-project"
					pattern="[a-z0-9][a-z0-9-]*[a-z0-9]"
					maxlength="63"
					autocomplete="off"
					required
					class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
				<p class="mt-1 text-xs text-gray-500">
					Lowercase letters, numbers, and hyphens only. Apps run in namespace <span class="font-mono">project-{name || '<name>'}</span>.
				</p>
			</div>
			<div>
				<label for="desc" class="block text-sm text-gray-400">Description <span class="text-gray-600">(optional)</span></label>
				<textarea id="desc" bind:value={description}
					rows="3"
					maxlength="300"
					placeholder="What lives in this project?"
					class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"></textarea>
			</div>

			{#if error}
				<p class="text-sm text-danger">{error}</p>
			{/if}

			<div class="flex gap-3 pt-2">
				<button type="submit" disabled={loading || !name}
					class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50 disabled:cursor-not-allowed transition-colors">
					{loading ? 'Creating...' : 'Create project'}
				</button>
				<a href="/"
					class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 hover:bg-surface-700 hover:text-white transition-colors">
					Cancel
				</a>
			</div>
		</form>
	</div>
</div>
