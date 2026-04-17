<script lang="ts">
	import { BarChart3 } from 'lucide-svelte';
	import type { App } from '$lib/types';

	let { app: _ }: { app: App } = $props();
</script>

<div class="flex flex-col items-center justify-center py-16 text-center">
	<BarChart3 class="mb-4 h-12 w-12 text-gray-600" />
	<p class="text-sm font-medium text-gray-400">CPU &amp; Memory metrics</p>
	<p class="mt-1 text-xs text-gray-500">
		Install metrics-server to enable. See Extensions for setup guide.
	</p>
	<a href="/extensions" class="mt-4 text-xs text-accent hover:underline">View Extensions &rarr;</a>
</div>
