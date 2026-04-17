<script lang="ts">
	import { Bell, X, CheckCheck } from 'lucide-svelte';
	import type { Notification } from '$lib/types';

	let { onClose }: { onClose: () => void } = $props();

	let items = $state<Notification[]>([]);
	let unread = $derived(items.filter((n) => !n.read).length);
</script>

<div
	class="absolute right-0 top-full z-50 mt-1 w-80 overflow-hidden rounded-md border border-surface-600 bg-surface-800 shadow-xl"
>
	<div class="flex items-center justify-between border-b border-surface-600 px-3 py-2">
		<span class="text-sm font-semibold text-white">Notifications</span>
		<div class="flex items-center gap-2">
			{#if unread > 0}
				<button
					type="button"
					onclick={() => (items = items.map((n) => ({ ...n, read: true })))}
					class="text-xs text-gray-400 hover:text-white flex items-center gap-1"
				>
					<CheckCheck class="h-3.5 w-3.5" /> Mark all read
				</button>
			{/if}
			<button type="button" onclick={onClose} class="text-gray-500 hover:text-white">
				<X class="h-4 w-4" />
			</button>
		</div>
	</div>
	{#if items.length === 0}
		<div class="flex flex-col items-center justify-center py-10">
			<Bell class="h-8 w-8 text-gray-600 mb-2" />
			<p class="text-xs text-gray-500">No notifications yet</p>
			<p class="text-xs text-gray-600 mt-1">Deploy completions and failures appear here</p>
		</div>
	{:else}
		<div class="max-h-80 overflow-y-auto">
			{#each items as n}
				<div
					class="flex items-start gap-3 border-b border-surface-600 px-3 py-2.5 {n.read
						? ''
						: 'bg-accent/5'}"
				>
					<div
						class="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full {n.type ===
						'deploy_success'
							? 'bg-success/10 text-success'
							: 'bg-danger/10 text-danger'} text-xs"
					>
						{n.type === 'deploy_success' ? '✓' : '✕'}
					</div>
					<div class="min-w-0 flex-1">
						<p class="text-xs text-gray-300">{n.message}</p>
						<p class="mt-0.5 text-xs text-gray-500">{n.projectName} · {n.appName}</p>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>
