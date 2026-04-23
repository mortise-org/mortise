<script lang="ts">
  import { api } from '$lib/api';
  import { store } from '$lib/store.svelte';
  import type { ActivityEvent } from '$lib/types';
  import { X, Activity } from 'lucide-svelte';

  let { project }: { project: string } = $props();

  let events = $state<ActivityEvent[]>([]);
  let loading = $state(false);
  let filter = $state<'all' | 'deploys' | 'changes' | 'members'>('all');

  $effect(() => {
    void project;
    void store.activityRailOpen;
    if (!store.activityRailOpen) return;
    void load();
  });

  async function load() {
    if (!project) return;
    loading = true;
    try {
      events = await api.listActivity(project);
    } catch {
      events = [];
    } finally {
      loading = false;
    }
  }

  const filteredEvents = $derived(
    filter === 'all' ? events :
    filter === 'deploys' ? events.filter(e => e.action === 'deploy' || e.action === 'rollback' || e.action === 'promote') :
    filter === 'changes' ? events.filter(e => e.action === 'update' || e.action === 'create' || e.action === 'delete') :
    events.filter(e => e.action === 'invite' || e.action === 'remove' || e.kind === 'member')
  );

  function relativeTime(ts: string): string {
    const diff = (Date.now() - new Date(ts).getTime()) / 1000;
    if (diff < 60) return 'just now';
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  }

  function initials(actor: string): string {
    return actor.slice(0, 2).toUpperCase();
  }

  function eventIcon(action: string): string {
    if (action === 'deploy' || action === 'rollback' || action === 'promote') return 'text-success';
    if (action === 'delete') return 'text-danger';
    if (action === 'build') return 'text-warning';
    return 'text-info';
  }
</script>

{#if store.activityRailOpen}
  <!-- Backdrop for mobile / click-away -->
  <div
    class="fixed inset-0 z-30"
    onclick={() => store.toggleActivityRail()}
    role="presentation"
  ></div>

  <!-- Rail panel -->
  <div class="fixed right-0 top-14 bottom-0 z-40 flex w-80 flex-col border-l border-surface-600 bg-surface-800 shadow-2xl transition-transform duration-200 ease-out">
    <!-- Header -->
    <div class="flex items-center justify-between border-b border-surface-600 px-4 py-3">
      <div class="flex items-center gap-2">
        <Activity class="h-4 w-4 text-gray-400" />
        <h2 class="text-sm font-semibold text-white">Activity</h2>
      </div>
      <button
        type="button"
        onclick={() => store.toggleActivityRail()}
        class="rounded-md p-1 text-gray-500 hover:bg-surface-700 hover:text-white"
        aria-label="Close activity rail"
      >
        <X class="h-4 w-4" />
      </button>
    </div>

    <!-- Filter chips -->
    <div class="flex items-center gap-1 border-b border-surface-600 px-4 overflow-x-auto">
      {#each (['all', 'deploys', 'changes', 'members'] as const) as f}
        <button
          type="button"
          onclick={() => (filter = f)}
          class="shrink-0 -mb-px border-b-2 px-3 py-1.5 text-xs font-medium capitalize transition-colors {filter === f ? 'border-accent text-white' : 'border-transparent text-gray-500 hover:text-white'}"
        >
          {f}
        </button>
      {/each}
      <button
        type="button"
        onclick={() => void load()}
        class="ml-auto shrink-0 rounded px-2 py-1 text-xs text-gray-500 hover:text-white"
        title="Refresh"
      >
        ↻
      </button>
    </div>

    <!-- Event list -->
    <div class="flex-1 overflow-y-auto">
      {#if loading}
        <div class="space-y-3 p-4">
          {#each Array(5) as _}
            <div class="flex gap-3 animate-pulse">
              <div class="h-7 w-7 rounded-full bg-surface-700 shrink-0"></div>
              <div class="flex-1 space-y-1.5">
                <div class="h-3 w-3/4 rounded bg-surface-700"></div>
                <div class="h-3 w-1/2 rounded bg-surface-700"></div>
              </div>
            </div>
          {/each}
        </div>
      {:else if filteredEvents.length === 0}
        <div class="flex flex-col items-center justify-center h-full text-center px-6">
          <Activity class="h-8 w-8 text-gray-600 mb-3" />
          <p class="text-sm text-gray-400">No activity yet</p>
          <p class="text-xs text-gray-500 mt-1">Deploy events, changes, and member actions will appear here.</p>
        </div>
      {:else}
        <div class="divide-y divide-surface-700">
          {#each filteredEvents as event}
            <div class="flex gap-3 px-4 py-3 hover:bg-surface-700/50">
              <!-- Actor avatar -->
              <div class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-accent/20 text-xs font-medium text-accent">
                {initials(event.actor)}
              </div>
              <!-- Content -->
              <div class="min-w-0 flex-1">
                <p class="text-xs text-gray-300 leading-relaxed">{event.msg}</p>
                <div class="mt-1 flex items-center gap-2">
                  <span class="text-xs {eventIcon(event.action)} font-medium capitalize">{event.action}</span>
                  <span class="text-xs text-gray-600">·</span>
                  <span class="text-xs text-gray-500">{relativeTime(event.ts)}</span>
                </div>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
{/if}
