<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';

	let name = $state('');
	let description = $state('');
	let error = $state('');
	let submitting = $state(false);

	// RFC 1123 DNS label: lowercase letters, digits, and hyphens;
	// must start and end with an alphanumeric; max 63 chars.
	const DNS_LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		error = '';

		const trimmed = name.trim();
		if (!DNS_LABEL.test(trimmed)) {
			error =
				'Project name must be 1-63 lowercase letters, digits, or hyphens, starting and ending with alphanumeric.';
			return;
		}

		submitting = true;
		try {
			const p = await api.createProject(trimmed, description.trim() || undefined);
			await goto(`/projects/${encodeURIComponent(p.name)}`);
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to create project';
		} finally {
			submitting = false;
		}
	}
</script>

<div class="mx-auto max-w-lg">
	<a
		href="/"
		class="mb-4 inline-block text-sm text-gray-500 transition-colors hover:text-white"
	>
		&larr; Back to projects
	</a>

	<div class="mb-6">
		<h1 class="text-xl font-semibold text-white">New project</h1>
		<p class="mt-1 text-sm text-gray-500">
			Projects group related apps under a shared namespace.
		</p>
	</div>

	<form onsubmit={handleSubmit} class="space-y-5">
		{#if error}
			<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
		{/if}

		<div>
			<label for="name" class="mb-1 block text-sm text-gray-400">Name</label>
			<input
				id="name"
				type="text"
				bind:value={name}
				required
				pattern="[a-z0-9]([a-z0-9-]{'{'}0,61{'}'}[a-z0-9])?"
				maxlength="63"
				autocomplete="off"
				class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 font-mono text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				placeholder="my-saas"
			/>
			<p class="mt-1 text-xs text-gray-500">
				Used as a DNS label. Apps in this project run in namespace <span class="font-mono"
					>project-{name || '&lt;name&gt;'}</span
				>.
			</p>
		</div>

		<div>
			<label for="description" class="mb-1 block text-sm text-gray-400">
				Description <span class="text-gray-600">(optional)</span>
			</label>
			<textarea
				id="description"
				bind:value={description}
				rows="3"
				maxlength="300"
				class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				placeholder="What lives in this project?"
			></textarea>
		</div>

		<div class="flex gap-3 pt-2">
			<button
				type="submit"
				disabled={submitting}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
			>
				{submitting ? 'Creating...' : 'Create project'}
			</button>
			<a
				href="/"
				class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 transition-colors hover:text-white"
			>
				Cancel
			</a>
		</div>
	</form>
</div>
