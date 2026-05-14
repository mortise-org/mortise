<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';

	const projectName = $derived(page.params.project ?? '');

	onMount(() => {
		goto(`/projects/${encodeURIComponent(projectName)}/settings`, { replaceState: true });
	});
</script>

<div class="flex items-center justify-center p-8">
	<p class="text-sm text-gray-500">Redirecting to project settings…</p>
</div>
