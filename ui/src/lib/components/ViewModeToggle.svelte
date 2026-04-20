<script lang="ts">
	import { LayoutDashboard, List } from 'lucide-svelte';
	import { store } from '$lib/store.svelte';
</script>

<div class="flex overflow-hidden rounded-md border border-surface-600 bg-surface-800/90 backdrop-blur-sm">
	<button
		type="button"
		onclick={() => store.setViewMode('canvas')}
		class="px-2 py-1.5 {store.viewMode === 'canvas' ? 'bg-surface-600 text-white' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
		title="Canvas view"
	>
		<LayoutDashboard class="h-4 w-4" />
	</button>
	<button
		type="button"
		onclick={() => store.setViewMode('list')}
		class="px-2 py-1.5 {store.viewMode === 'list' ? 'bg-surface-600 text-white' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
		title="List view"
	>
		<List class="h-4 w-4" />
	</button>
</div>
