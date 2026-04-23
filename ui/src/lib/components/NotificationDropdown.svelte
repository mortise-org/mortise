<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import { store } from '$lib/store.svelte';
  import type { ActivityEvent } from '$lib/types';
  import { X, CheckCircle, AlertCircle, Bell } from 'lucide-svelte';

  let { onClose }: { onClose: () => void } = $props();

  let events = $state<ActivityEvent[]>([]);
  let loading = $state(false);

  onMount(() => {
    void load();
  });

  async function load() {
    loading = true;
    try {
      const all = await api.listPlatformActivity(10);
      events = all
        .filter((e: ActivityEvent) =>
          ['deploy', 'build', 'rollback', 'promote', 'invite', 'remove'].includes(e.action) ||
          (e.action === 'update' && e.kind === 'member')
        );
    } catch {
      events = [];
    } finally {
      loading = false;
    }
  }

  async function openNotification(event: ActivityEvent) {
    store.setProject(event.project);
    store.setActivityRailOpen(true);
    onClose();
    await goto(`/projects/${encodeURIComponent(event.project)}`);
  }

  function relativeTime(ts: string): string {
    const diff = (Date.now() - new Date(ts).getTime()) / 1000;
    if (diff < 60) return 'just now';
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  }
</script>

<div
  class="absolute right-0 top-full z-50 mt-1 w-80 overflow-hidden rounded-md border border-surface-600 bg-surface-800 shadow-xl"
>
  <div class="flex items-center justify-between border-b border-surface-600 px-4 py-3">
    <h3 class="text-sm font-semibold text-white">Notifications</h3>
    <button
      type="button"
      onclick={onClose}
      class="rounded-md p-1 text-gray-500 hover:bg-surface-700 hover:text-white"
      aria-label="Close notifications"
    >
      <X class="h-3.5 w-3.5" />
    </button>
  </div>

  {#if loading}
    <div class="space-y-2 p-4">
      {#each Array(3) as _}
        <div class="h-10 animate-pulse rounded bg-surface-700"></div>
      {/each}
    </div>
  {:else if events.length === 0}
    <div class="px-4 py-8 text-center">
      <Bell class="mx-auto mb-2 h-8 w-8 text-gray-600" />
      <p class="text-sm text-gray-400">No notifications</p>
      <p class="text-xs text-gray-500 mt-1">Deploy completions and build failures will appear here.</p>
    </div>
  {:else}
    <div class="max-h-80 divide-y divide-surface-700 overflow-y-auto">
      {#each events as event}
        <button
          type="button"
          onclick={() => void openNotification(event)}
          class="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-surface-700/50"
        >
          {#if event.action === 'deploy'}
            <CheckCircle class="mt-0.5 h-4 w-4 shrink-0 text-success" />
          {:else}
            <AlertCircle class="mt-0.5 h-4 w-4 shrink-0 text-warning" />
          {/if}
          <div class="min-w-0 flex-1">
            <p class="text-xs text-gray-300 leading-relaxed">{event.msg}</p>
            <p class="mt-0.5 text-xs text-gray-500">{event.project} · {relativeTime(event.ts)}</p>
          </div>
        </button>
      {/each}
    </div>
  {/if}
</div>
