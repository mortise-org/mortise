<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import NewAppModal from '$lib/components/NewAppModal.svelte';

	const projectName = $derived(page.params.project ?? '');

	function handleClose() {
		goto(`/projects/${encodeURIComponent(projectName)}`);
	}

	function handleCreated(appName: string) {
		goto(`/projects/${encodeURIComponent(projectName)}/apps/${encodeURIComponent(appName)}`);
	}
</script>

<NewAppModal project={projectName} onClose={handleClose} onCreated={handleCreated} />
