<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api';
  import type { ActivityEvent, DashboardResponse } from '$lib/types';
  import { LayoutDashboard, RefreshCw } from 'lucide-svelte';

  let data = $state<DashboardResponse | null>(null);
  let activity = $state<ActivityEvent[]>([]);
  let loading = $state(true);
  let error = $state('');

  async function load() {
    loading = data === null;
    error = '';
    try {
      const [dash, act] = await Promise.all([api.getDashboard(), api.listPlatformActivity(30)]);
      data = dash;
      activity = act ?? [];
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to load dashboard';
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
    const t = setInterval(() => void load(), 30_000);
    return () => clearInterval(t);
  });

  const apps = $derived((data?.apps ?? []).slice().sort((a, b) => b.cpu - a.cpu));

  function fmtCores(v: number): string {
    return v >= 10 ? v.toFixed(1) : v.toFixed(2);
  }

  function fmtBytes(v: number): string {
    if (v >= 1 << 30) return (v / (1 << 30)).toFixed(1) + ' GiB';
    if (v >= 1 << 20) return (v / (1 << 20)).toFixed(0) + ' MiB';
    if (v >= 1 << 10) return (v / (1 << 10)).toFixed(0) + ' KiB';
    return v + ' B';
  }

  function phaseColor(phase: string): string {
    if (phase === 'Ready') return 'text-success';
    if (phase === 'Failed' || phase === 'CrashLooping') return 'text-danger';
    return 'text-warning';
  }

  function healthDot(health: string): string {
    if (health === 'healthy') return 'bg-success';
    if (health === 'danger') return 'bg-danger';
    if (health === 'warning') return 'bg-warning';
    return 'bg-surface-500';
  }

  function relativeTime(ts: string): string {
    const diff = (Date.now() - new Date(ts).getTime()) / 1000;
    if (diff < 60) return 'just now';
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  }

  const observerDown = $derived((data?.cluster.observer ?? []).filter((c) => !c.ok));
</script>

<svelte:head><title>Dashboard · Mortise</title></svelte:head>

<div class="mx-auto max-w-7xl px-6 py-6">
  <div class="mb-6 flex items-center justify-between">
    <div class="flex items-center gap-2">
      <LayoutDashboard class="h-5 w-5 text-gray-400" />
      <h1 class="text-lg font-semibold text-white">Dashboard</h1>
    </div>
    <button
      type="button"
      onclick={() => void load()}
      class="flex items-center gap-1.5 rounded-md border border-surface-600 px-2.5 py-1.5 text-xs text-gray-400 hover:bg-surface-700 hover:text-white"
    >
      <RefreshCw class="h-3.5 w-3.5" />
      Refresh
    </button>
  </div>

  {#if error}
    <div class="rounded-md border border-danger/40 bg-danger/10 px-4 py-3 text-sm text-danger">{error}</div>
  {:else if loading}
    <div class="grid grid-cols-2 gap-4 md:grid-cols-3 lg:grid-cols-6">
      {#each Array(6) as _}
        <div class="h-20 animate-pulse rounded-lg bg-surface-700"></div>
      {/each}
    </div>
  {:else if data}
    <!-- Cluster strip -->
    <div class="grid grid-cols-2 gap-4 md:grid-cols-3 lg:grid-cols-6">
      <div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
        <p class="text-xs text-gray-500">Projects</p>
        <p class="mt-1 text-2xl font-semibold text-white">{data.cluster.projects}</p>
      </div>
      <div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
        <p class="text-xs text-gray-500">Apps</p>
        <p class="mt-1 text-2xl font-semibold text-white">{data.cluster.apps}</p>
        <p class="mt-0.5 truncate text-xs text-gray-500">
          {#each Object.entries(data.cluster.appsByPhase) as [phase, n], i}{i > 0 ? ' · ' : ''}{n} {phase}{/each}
        </p>
      </div>
      <div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
        <p class="text-xs text-gray-500">CPU</p>
        {#if data.cluster.metricsAvailable}
          <p class="mt-1 text-2xl font-semibold text-white">{fmtCores(data.cluster.cpuUsed)}</p>
          <p class="mt-0.5 text-xs text-gray-500">
            cores{data.cluster.cpuAllocatable ? ` of ${fmtCores(data.cluster.cpuAllocatable)}` : ''}
          </p>
        {:else}
          <p class="mt-1 text-2xl font-semibold text-gray-600">—</p>
          <p class="mt-0.5 text-xs text-gray-500">metrics unavailable</p>
        {/if}
      </div>
      <div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
        <p class="text-xs text-gray-500">Memory</p>
        {#if data.cluster.metricsAvailable}
          <p class="mt-1 text-2xl font-semibold text-white">{fmtBytes(data.cluster.memoryUsed)}</p>
          <p class="mt-0.5 text-xs text-gray-500">
            {data.cluster.memoryAllocatable ? `of ${fmtBytes(data.cluster.memoryAllocatable)}` : 'used'}
          </p>
        {:else}
          <p class="mt-1 text-2xl font-semibold text-gray-600">—</p>
          <p class="mt-0.5 text-xs text-gray-500">metrics unavailable</p>
        {/if}
      </div>
      <div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
        <p class="text-xs text-gray-500">Builds</p>
        <p class="mt-1 text-2xl font-semibold text-white">{data.cluster.buildsRunning}</p>
        <p class="mt-0.5 text-xs text-gray-500">running · {data.cluster.buildsQueued} queued</p>
      </div>
      <div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
        <p class="text-xs text-gray-500">Observer</p>
        {#if data.cluster.observer && data.cluster.observer.length > 0}
          {#if observerDown.length === 0}
            <p class="mt-1 text-2xl font-semibold text-success">OK</p>
            <p class="mt-0.5 text-xs text-gray-500">{data.cluster.observer.length} collectors</p>
          {:else}
            <p class="mt-1 text-2xl font-semibold text-danger">Degraded</p>
            <p class="mt-0.5 truncate text-xs text-gray-500">{observerDown.map((c) => c.collector).join(', ')} stale</p>
          {/if}
        {:else}
          <p class="mt-1 text-2xl font-semibold text-gray-600">—</p>
          <p class="mt-0.5 text-xs text-gray-500">n/a</p>
        {/if}
      </div>
    </div>

    <div class="mt-6 grid gap-6 lg:grid-cols-3">
      <!-- Apps table -->
      <div class="lg:col-span-2">
        <h2 class="mb-2 text-sm font-semibold text-white">Apps</h2>
        <div class="overflow-x-auto rounded-lg border border-surface-600">
          <table class="w-full text-left text-sm">
            <thead class="bg-surface-800 text-xs text-gray-500">
              <tr>
                <th class="px-4 py-2 font-medium">App</th>
                <th class="px-4 py-2 font-medium">Phase</th>
                <th class="px-4 py-2 font-medium">Environments</th>
                <th class="px-4 py-2 text-right font-medium">CPU</th>
                <th class="px-4 py-2 text-right font-medium">Memory</th>
                <th class="px-4 py-2 text-right font-medium">Restarts</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-surface-700">
              {#if apps.length === 0}
                <tr><td colspan="6" class="px-4 py-8 text-center text-gray-500">No apps yet</td></tr>
              {/if}
              {#each apps as app}
                <tr class="hover:bg-surface-700/40">
                  <td class="px-4 py-2">
                    <a href={`/projects/${app.project}`} class="text-white hover:text-accent">
                      <span class="text-gray-500">{app.project} /</span>
                      {app.name}
                    </a>
                  </td>
                  <td class="px-4 py-2 {phaseColor(app.phase)}">{app.phase}</td>
                  <td class="px-4 py-2">
                    <div class="flex flex-wrap gap-1">
                      {#each app.envs ?? [] as env}
                        <span class="rounded bg-surface-700 px-1.5 py-0.5 text-xs {phaseColor(env.phase)}" title={env.phase}>
                          {env.name}
                        </span>
                      {/each}
                    </div>
                  </td>
                  <td class="px-4 py-2 text-right tabular-nums text-gray-300">
                    {app.metricsAvailable ? fmtCores(app.cpu) : '—'}
                  </td>
                  <td class="px-4 py-2 text-right tabular-nums text-gray-300">
                    {app.metricsAvailable ? fmtBytes(app.memory) : '—'}
                  </td>
                  <td class="px-4 py-2 text-right tabular-nums {app.restarts > 0 ? 'text-warning' : 'text-gray-300'}">
                    {app.restarts}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        <!-- Project health -->
        <h2 class="mt-6 mb-2 text-sm font-semibold text-white">Project environments</h2>
        <div class="grid gap-3 sm:grid-cols-2">
          {#each data.projects ?? [] as project}
            <a
              href={`/projects/${project.name}`}
              class="rounded-lg border border-surface-600 bg-surface-800 p-3 hover:border-surface-500"
            >
              <div class="flex items-center justify-between">
                <span class="text-sm text-white">{project.name}</span>
                <span class="text-xs text-gray-500">{project.appCount} apps</span>
              </div>
              <div class="mt-2 flex flex-wrap gap-2">
                {#each Object.entries(project.envHealth) as [env, health]}
                  <span class="flex items-center gap-1 text-xs text-gray-400" title={`${env}: ${health}`}>
                    <span class="h-2 w-2 rounded-full {healthDot(health)}"></span>
                    {env}
                  </span>
                {/each}
              </div>
            </a>
          {/each}
        </div>
      </div>

      <!-- Activity feed -->
      <div>
        <h2 class="mb-2 text-sm font-semibold text-white">Recent activity</h2>
        <div class="rounded-lg border border-surface-600 bg-surface-800">
          {#if activity.length === 0}
            <p class="px-4 py-8 text-center text-sm text-gray-500">No activity yet</p>
          {:else}
            <div class="max-h-[36rem] divide-y divide-surface-700 overflow-y-auto">
              {#each activity as event}
                <div class="px-4 py-2.5">
                  <p class="text-xs leading-relaxed text-gray-300">{event.msg}</p>
                  <p class="mt-0.5 text-xs text-gray-500">
                    {event.project} · {event.actor} · {relativeTime(event.ts)}
                  </p>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    </div>
  {/if}
</div>
