<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import AppForm from '$lib/AppForm.svelte';
	import { templates, getTemplate, type TemplateCategory } from '$lib/templates';

	type Filter = 'all' | TemplateCategory;

	let selectedId = $state<string | null>(
		untrack(() => page.url.searchParams.get('template'))
	);
	let filter = $state<Filter>('all');

	const selected = $derived(selectedId ? getTemplate(selectedId) : undefined);

	const filtered = $derived(
		filter === 'all' ? templates : templates.filter((t) => t.category === filter)
	);

	const filterTabs: { id: Filter; label: string }[] = [
		{ id: 'all', label: 'All' },
		{ id: 'database', label: 'Databases' },
		{ id: 'app', label: 'Apps' },
		{ id: 'blank', label: 'Blank' }
	];

	function pick(id: string) {
		selectedId = id;
	}

	function back() {
		selectedId = null;
	}
</script>

{#if selected}
	<AppForm template={selected} onBack={back} />
{:else}
	<div class="mx-auto max-w-5xl">
		<div class="mb-6">
			<h1 class="text-xl font-semibold text-white">Deploy from template</h1>
			<p class="mt-1 text-sm text-gray-500">
				Pick a pre-configured template or start from a blank app.
			</p>
		</div>

		<div class="mb-6 flex gap-1 border-b border-surface-600">
			{#each filterTabs as tab}
				<button
					type="button"
					onclick={() => (filter = tab.id)}
					class="px-4 py-2 text-sm transition-colors {filter === tab.id
						? 'border-b-2 border-accent text-white'
						: 'text-gray-500 hover:text-gray-300'}"
				>
					{tab.label}
				</button>
			{/each}
		</div>

		<div class="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
			{#each filtered as template}
				<button
					type="button"
					onclick={() => pick(template.id)}
					class="group flex flex-col items-start rounded-lg border border-surface-600 bg-surface-800 p-5 text-left transition-colors hover:border-accent/60 hover:bg-surface-700"
				>
					<div class="mb-3 flex items-center gap-3">
						<span class="text-2xl">{template.icon}</span>
						<span class="font-medium text-white group-hover:text-accent">{template.name}</span>
					</div>
					<p class="text-sm text-gray-400">{template.description}</p>
					<span
						class="mt-4 rounded-full bg-surface-700 px-2 py-0.5 text-xs uppercase tracking-wide text-gray-500 group-hover:bg-surface-600"
					>
						{template.category}
					</span>
				</button>
			{/each}
		</div>
	</div>
{/if}
