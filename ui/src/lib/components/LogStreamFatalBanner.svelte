<script lang="ts">
	let { code }: { code?: string } = $props();
</script>

<div
	class="mb-1 rounded-md border border-danger/30 bg-danger/10 px-2 py-1 text-[11px] font-medium text-danger"
	role="alert"
>
	Fatal log stream error{code ? ` · ${code}` : ''}
</div>
