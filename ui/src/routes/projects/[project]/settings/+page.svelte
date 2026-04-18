<script lang="ts">
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import type { Project, ProjectMember, InviteResponse } from '$lib/types';
  import { Plus, Trash2, Copy, Check } from 'lucide-svelte';

  const projectName = $derived(page.params.project ?? '');
  let project = $state<Project | null>(null);
  let loading = $state(true);
  let activeTab = $state<'general' | 'environments' | 'shared-vars' | 'members' | 'tokens' | 'webhooks' | 'integrations' | 'danger'>('general');
  let filterText = $state('');

  // --- General ---
  let editDesc = $state('');
  let savingGeneral = $state(false);
  let generalError = $state('');

  // --- PR Environments ---
  let prEnabled = $state(false);
  let prDomainTemplate = $state('');
  let prTtl = $state('72h');
  let savingPR = $state(false);

  // --- Environments ---
  let envList = $state<string[]>([]);
  let showAddEnv = $state(false);
  let newEnvName = $state('');
  let savingEnv = $state(false);
  let envError = $state('');

  // --- Members ---
  let members = $state<ProjectMember[]>([]);
  let loadingMembers = $state(false);
  let inviteEmail = $state('');
  let inviteRole = $state<'admin' | 'member'>('member');
  let inviting = $state(false);
  let inviteLink = $state('');
  let copiedLink = $state(false);
  let membersError = $state('');

  // --- Danger ---
  let confirmDeleteText = $state('');
  let deleting = $state(false);
  let projectApps = $state<Array<{ name: string }>>([]);
  let loadingApps = $state(false);
  let deletingApp = $state('');
  let appDeleteError = $state('');

  onMount(async () => {
    try {
      project = await api.getProject(projectName);
      editDesc = project.description ?? '';
    } catch {
      // ignore
    } finally {
      loading = false;
    }
  });

  // --- Shared vars (lazy load) ---
  let sharedVars = $state<Record<string, string>>({});
  let loadingShared = $state(false);
  async function loadSharedVars() {
    loadingShared = true;
    try {
      // shared vars are per-app; nothing to load at project level
      sharedVars = {};
    } finally {
      loadingShared = false;
    }
  }

  async function loadAppsForDanger() {
    loadingApps = true;
    try {
      const apps = await api.listApps(projectName);
      projectApps = apps.map(a => ({ name: a.metadata.name }));
    } catch {
      projectApps = [];
    } finally {
      loadingApps = false;
    }
  }

  async function removeApp(appName: string) {
    if (deletingApp) return;
    deletingApp = appName;
    appDeleteError = '';
    try {
      await api.deleteApp(projectName, appName);
      projectApps = projectApps.filter(a => a.name !== appName);
    } catch (e) {
      appDeleteError = e instanceof Error ? e.message : 'Failed to remove app';
    } finally {
      deletingApp = '';
    }
  }

  async function switchTab(tab: typeof activeTab) {
    activeTab = tab;
    if (tab === 'members' && members.length === 0 && !loadingMembers) await loadMembers();
    if (tab === 'shared-vars' && Object.keys(sharedVars).length === 0 && !loadingShared) await loadSharedVars();
    if (tab === 'danger' && projectApps.length === 0 && !loadingApps) await loadAppsForDanger();
  }

  async function saveGeneral() {
    if (!project) return;
    savingGeneral = true;
    generalError = '';
    try {
      project = await api.updateProject(project.name, { description: editDesc });
    } catch (e) {
      generalError = e instanceof Error ? e.message : 'Failed to save';
    } finally {
      savingGeneral = false;
    }
  }

  async function savePR() {
    savingPR = true;
    try {
      await api.setProjectPreview(projectName, prEnabled, prDomainTemplate || undefined, prTtl || undefined);
    } catch {
      // ignore
    } finally {
      savingPR = false;
    }
  }

  async function loadMembers() {
    loadingMembers = true;
    try {
      members = await api.listMembers(projectName);
    } catch {
      members = [];
    } finally {
      loadingMembers = false;
    }
  }

  async function handleInvite() {
    if (!inviteEmail.trim()) return;
    inviting = true;
    membersError = '';
    inviteLink = '';
    try {
      const resp: InviteResponse = await api.inviteMember(projectName, inviteEmail.trim(), inviteRole);
      inviteLink = resp.link;
      inviteEmail = '';
      await loadMembers();
    } catch (e) {
      membersError = e instanceof Error ? e.message : 'Failed to invite';
    } finally {
      inviting = false;
    }
  }

  async function handleRemoveMember(email: string) {
    try {
      await api.removeMember(projectName, email);
      members = members.filter(m => m.email !== email);
    } catch (e) {
      membersError = e instanceof Error ? e.message : 'Failed to remove member';
    }
  }

  async function deleteProject() {
    if (confirmDeleteText !== projectName) return;
    deleting = true;
    try {
      await api.deleteProject(projectName);
      await goto('/');
    } catch {
      deleting = false;
    }
  }

  async function copyLink(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      copiedLink = true;
      setTimeout(() => (copiedLink = false), 1500);
    } catch { /* ignore */ }
  }

  const tabCls = (t: string) =>
    `px-3 py-2 text-sm transition-colors cursor-pointer ${activeTab === t ? 'border-b-2 border-accent text-white' : 'text-gray-400 hover:text-white'}`;
  const inputCls = 'w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent';
  const labelCls = 'block text-xs text-gray-500 mb-1';
  const btnPrimary = 'rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50';
  const btnSecondary = 'rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 hover:bg-surface-700 hover:text-white';
</script>

<div class="flex h-full flex-col">
  <!-- Header -->
  <div class="border-b border-surface-600 bg-surface-800 px-6 pt-5 pb-0">
    <div class="mb-4">
      <h1 class="text-lg font-semibold text-white">Project Settings</h1>
      <p class="text-xs text-gray-500">{projectName}</p>
    </div>
    <!-- Tab nav -->
    <nav class="flex gap-0 border-b-0">
      <button type="button" class={tabCls('general')} onclick={() => switchTab('general')}>General</button>
      <button type="button" class={tabCls('environments')} onclick={() => switchTab('environments')}>Environments</button>
      <button type="button" class={tabCls('shared-vars')} onclick={() => switchTab('shared-vars')}>Shared Variables</button>
      <button type="button" class={tabCls('members')} onclick={() => switchTab('members')}>Members</button>
      <button type="button" class={tabCls('tokens')} onclick={() => switchTab('tokens')}>Tokens</button>
      <button type="button" class={tabCls('webhooks')} onclick={() => switchTab('webhooks')}>Webhooks</button>
      <button type="button" class={tabCls('integrations')} onclick={() => switchTab('integrations')}>Integrations</button>
      <button type="button" class={tabCls('danger')} onclick={() => switchTab('danger')}>Danger</button>
    </nav>
  </div>

  <!-- Filter input -->
  <div class="border-b border-surface-600 bg-surface-800 px-6 pb-3">
    <input
      type="text"
      bind:value={filterText}
      placeholder="Filter settings..."
      class="w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-1.5 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
    />
  </div>

  <!-- Tab content -->
  <div class="flex-1 overflow-auto p-6">
    {#if loading}
      <div class="space-y-3">
        {#each Array(3) as _}
          <div class="h-10 animate-pulse rounded bg-surface-700"></div>
        {/each}
      </div>

    {:else if activeTab === 'general'}
      <div class="max-w-lg space-y-5">
        <div>
          <label class={labelCls} for="proj-name">Project name</label>
          <input id="proj-name" type="text" value={projectName} disabled
            class="w-full cursor-not-allowed rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-gray-500" />
          <p class="mt-1 text-xs text-gray-500">Names cannot be changed after creation.</p>
        </div>
        <div>
          <label class={labelCls} for="proj-desc">Description</label>
          <input id="proj-desc" type="text" bind:value={editDesc} placeholder="Optional description" class={inputCls} />
        </div>
        {#if generalError}
          <p class="text-xs text-danger">{generalError}</p>
        {/if}
        <button type="button" onclick={saveGeneral} disabled={savingGeneral} class={btnPrimary}>
          {savingGeneral ? 'Saving…' : 'Save changes'}
        </button>

        <div class="border-t border-surface-600 pt-5">
          <h2 class="mb-3 text-sm font-medium text-white">PR Environments</h2>
          <div class="flex items-start justify-between rounded-md border border-surface-600 p-4">
            <div>
              <p class="text-sm font-medium text-white">Enable PR Environments</p>
              <p class="mt-1 text-xs text-gray-500">Automatically create preview deployments for pull requests.</p>
            </div>
            <button type="button" role="switch" aria-checked={prEnabled}
              onclick={async () => { prEnabled = !prEnabled; await savePR(); }}
              class="relative inline-flex h-5 w-9 items-center rounded-full transition-colors {prEnabled ? 'bg-accent' : 'bg-surface-600'}">
              <span class="inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform {prEnabled ? 'translate-x-4.5' : 'translate-x-0.5'}"></span>
            </button>
          </div>
          <div class="mt-3 space-y-3">
            <div>
              <label class={labelCls} for="pr-domain">Domain template</label>
              <input id="pr-domain" type="text" bind:value={prDomainTemplate}
                placeholder="pr-{'{number}'}.{'{app}'}.example.com" class={inputCls} />
              <p class="mt-1 text-xs text-gray-500">Tokens: {'{number}'}, {'{app}'}</p>
            </div>
            <div>
              <label class={labelCls} for="pr-ttl">TTL after PR close</label>
              <select id="pr-ttl" bind:value={prTtl}
                class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent">
                <option value="1h">1 hour</option>
                <option value="24h">24 hours</option>
                <option value="72h">3 days</option>
                <option value="168h">1 week</option>
              </select>
            </div>
            <button type="button" onclick={savePR} disabled={savingPR} class={btnPrimary}>
              {savingPR ? 'Saving…' : 'Save PR config'}
            </button>
          </div>
        </div>
      </div>

    {:else if activeTab === 'environments'}
      <div class="max-w-lg">
        <div class="mb-4 flex items-center justify-between">
          <div>
            <h2 class="text-sm font-medium text-white">Environments</h2>
            <p class="text-xs text-gray-500">Manage named deployment environments for this project.</p>
          </div>
          <button type="button" onclick={() => showAddEnv = true}
            class="flex items-center gap-1 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover">
            <Plus class="h-3.5 w-3.5" /> New environment
          </button>
        </div>

        <div class="space-y-2">
          {#each ['production', 'staging'] as env}
            <div class="flex items-center justify-between rounded-md border border-surface-600 bg-surface-800 px-4 py-3">
              <div>
                <p class="text-sm font-medium text-white">{env}</p>
                <p class="text-xs text-gray-500">{env === 'production' ? 'Default environment' : 'Staging'}</p>
              </div>
              {#if env !== 'production'}
                <button type="button"
                  class="text-xs text-gray-500 hover:text-danger">Remove</button>
              {:else}
                <span class="text-xs text-gray-500">Default</span>
              {/if}
            </div>
          {/each}
          {#each envList as env}
            <div class="flex items-center justify-between rounded-md border border-surface-600 bg-surface-800 px-4 py-3">
              <p class="text-sm font-medium text-white">{env}</p>
              <button type="button" onclick={() => { envList = envList.filter(e => e !== env); }}
                class="text-xs text-gray-500 hover:text-danger">Remove</button>
            </div>
          {/each}
        </div>

        {#if showAddEnv}
          <div class="mt-3 rounded-md border border-surface-600 bg-surface-800 p-4 space-y-3">
            <div>
              <label class={labelCls} for="new-env">Environment name</label>
              <input id="new-env" type="text" bind:value={newEnvName} placeholder="e.g. staging, preview" class={inputCls} />
            </div>
            {#if envError}<p class="text-xs text-danger">{envError}</p>{/if}
            <div class="flex gap-2">
              <button type="button" onclick={() => { if (newEnvName.trim()) { envList = [...envList, newEnvName.trim()]; newEnvName = ''; showAddEnv = false; } }}
                disabled={!newEnvName.trim() || savingEnv} class={btnPrimary}>
                {savingEnv ? 'Creating…' : 'Create'}
              </button>
              <button type="button" onclick={() => { showAddEnv = false; newEnvName = ''; }} class={btnSecondary}>Cancel</button>
            </div>
          </div>
        {/if}

        <div class="mt-6 rounded-md border border-surface-600 bg-surface-800/50 p-4">
          <p class="text-sm font-medium text-white">PR Environments</p>
          <p class="mt-1 text-xs text-gray-500">Preview environments for pull requests are configured in <button type="button" onclick={() => switchTab('general')} class="text-accent hover:underline">General settings</button>.</p>
          <a href="/projects/{projectName}/previews" class="mt-2 inline-block text-xs text-accent hover:underline">View active PR environments →</a>
        </div>
      </div>

    {:else if activeTab === 'shared-vars'}
      <div class="max-w-lg">
        <div class="mb-4">
          <h2 class="text-sm font-medium text-white">Shared Variables</h2>
          <p class="text-xs text-gray-500">In Mortise, shared variables are configured per-app and can be referenced by multiple environments within that app. Navigate to an app's Variables tab to manage its shared variables.</p>
        </div>

        <div class="rounded-md border border-surface-600 bg-surface-800/50 p-5">
          <p class="text-sm text-gray-400">Shared variables are scoped to individual apps, not the project.</p>
          <p class="mt-2 text-xs text-gray-500">To add shared variables: open an app → Variables tab → "Shared" pseudo-tab → add key/value pairs that apply across all environments of that app.</p>
          <a href="/projects/{projectName}" class="mt-3 inline-block text-xs text-accent hover:underline">Go to project canvas →</a>
        </div>

        <div class="mt-6">
          <h3 class="mb-2 text-xs font-semibold uppercase tracking-wide text-gray-400">Reference syntax</h3>
          <div class="rounded-md bg-surface-900 p-3 font-mono text-xs text-gray-300 space-y-1">
            <div><span class="text-accent">$&#123;shared.KEY&#125;</span> - shared var from this app</div>
            <div><span class="text-accent">$&#123;bindings.APP.KEY&#125;</span> - credential from bound app</div>
            <div><span class="text-accent">$&#123;secrets.SECRET_NAME&#125;</span> - k8s Secret key</div>
          </div>
        </div>
      </div>

    {:else if activeTab === 'members'}
      <div class="max-w-lg">
        <div class="mb-5 rounded-md border border-info/30 bg-info/5 p-3 text-xs text-info">
          All members of the <strong>default workspace</strong> can access this project.
        </div>

        <!-- Invite form -->
        <div class="mb-5 rounded-md border border-surface-600 bg-surface-800 p-4 space-y-3">
          <h2 class="text-sm font-medium text-white">Invite member</h2>
          <div class="flex gap-2">
            <input type="email" bind:value={inviteEmail} placeholder="email@example.com"
              class="flex-1 rounded-md border border-surface-600 bg-surface-900 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
            <select bind:value={inviteRole}
              class="rounded-md border border-surface-600 bg-surface-900 px-3 py-2 text-sm text-white outline-none focus:border-accent">
              <option value="member">Can Edit</option>
              <option value="admin">Admin</option>
            </select>
          </div>
          {#if membersError}<p class="text-xs text-danger">{membersError}</p>{/if}
          <div class="flex gap-2">
            <button type="button" onclick={handleInvite} disabled={inviting || !inviteEmail.trim()} class={btnPrimary}>
              {inviting ? 'Inviting…' : 'Invite'}
            </button>
          </div>
          {#if inviteLink}
            <div class="rounded-md border border-success/30 bg-success/10 p-3">
              <p class="mb-1 text-xs font-medium text-success">Invite link created</p>
              <div class="flex items-center gap-2">
                <code class="flex-1 truncate rounded bg-surface-800 px-2 py-1 font-mono text-xs text-gray-300">{inviteLink}</code>
                <button type="button" onclick={() => copyLink(inviteLink)}
                  class="text-gray-400 hover:text-white" aria-label="Copy invite link">
                  {#if copiedLink}<Check class="h-3.5 w-3.5 text-success" />{:else}<Copy class="h-3.5 w-3.5" />{/if}
                </button>
              </div>
            </div>
          {/if}
        </div>

        <!-- Members list -->
        {#if loadingMembers}
          <div class="h-20 animate-pulse rounded bg-surface-700"></div>
        {:else if members.length === 0}
          <div class="rounded-md border border-dashed border-surface-600 p-8 text-center">
            <p class="text-sm text-gray-500">No project members yet.</p>
          </div>
        {:else}
          <div class="space-y-1.5">
            {#each members as member}
              <div class="flex items-center justify-between rounded-md border border-surface-600 bg-surface-800 px-4 py-3">
                <div>
                  <p class="text-sm text-white">{member.email}</p>
                  <p class="text-xs text-gray-500 capitalize">{member.role}</p>
                </div>
                <button type="button" onclick={() => handleRemoveMember(member.email)}
                  class="flex items-center gap-1 text-xs text-gray-500 hover:text-danger">
                  <Trash2 class="h-3.5 w-3.5" /> Remove
                </button>
              </div>
            {/each}
          </div>
        {/if}
      </div>

    {:else if activeTab === 'tokens'}
      <div class="max-w-lg">
        <div class="mb-4">
          <h2 class="text-sm font-medium text-white">Deploy Tokens</h2>
          <p class="text-xs text-gray-500 mt-1">Project-level deploy tokens provide CI access to all apps in this project. App-specific tokens can be managed per-app in App Settings → Deploy Tokens.</p>
        </div>
        <div class="rounded-md border border-surface-600 bg-surface-800/50 p-5">
          <p class="text-sm text-gray-400">Per-app deploy tokens are managed in each app's Settings tab.</p>
          <a href="/projects/{projectName}" class="mt-2 inline-block text-xs text-accent hover:underline">Go to project canvas →</a>
        </div>
        <div class="mt-4 rounded-md border border-info/20 bg-info/5 p-3 text-xs text-info">
          CI snippet: <code class="font-mono">curl -X POST https://your-domain/api/projects/{projectName}/apps/APP_NAME/deploy -H "Authorization: Bearer TOKEN" -d '&#123;"environment":"production","image":"IMAGE"&#125;'</code>
        </div>
      </div>

    {:else if activeTab === 'webhooks'}
      <div class="max-w-lg">
        <div class="mb-4">
          <h2 class="text-sm font-medium text-white">Webhooks</h2>
          <p class="text-xs text-gray-500 mt-1">Outgoing webhooks for deploy events.</p>
        </div>
        <div class="rounded-md border border-surface-600 bg-surface-800/50 p-5 text-center">
          <p class="text-sm text-gray-400">Outgoing webhooks are coming in v2.</p>
          <p class="text-xs text-gray-500 mt-2">For now, use the Activity rail to monitor deploy events, or integrate with your observability stack via the Extensions page.</p>
          <a href="/extensions" class="mt-3 inline-block text-xs text-accent hover:underline">View Extensions →</a>
        </div>
      </div>

    {:else if activeTab === 'integrations'}
      <div class="max-w-lg">
        <div class="mb-4">
          <h2 class="text-sm font-medium text-white">Integrations</h2>
          <p class="text-xs text-gray-500 mt-1">Connected git providers and platform integrations.</p>
        </div>
        <div class="rounded-md border border-surface-600 bg-surface-800/50 p-5">
          <p class="text-sm text-gray-300 font-medium">Git Providers</p>
          <p class="text-xs text-gray-500 mt-1">Git providers are configured at the platform level and available to all projects.</p>
          <a href="/admin/settings#git-providers" class="mt-2 inline-block text-xs text-accent hover:underline">Manage git providers →</a>
        </div>
        <div class="mt-3 rounded-md border border-surface-600 bg-surface-800/50 p-5">
          <p class="text-sm text-gray-300 font-medium">Extensions</p>
          <p class="text-xs text-gray-500 mt-1">OIDC, external secrets, monitoring, and backup integrations.</p>
          <a href="/extensions" class="mt-2 inline-block text-xs text-accent hover:underline">View Extensions →</a>
        </div>
      </div>

    {:else if activeTab === 'danger'}
      <div class="max-w-lg">
        <!-- Manage Apps -->
        <div class="mb-5 rounded-md border border-surface-600 bg-surface-800/50 p-4">
          <h3 class="mb-3 text-sm font-medium text-white">Manage Apps</h3>
          {#if appDeleteError}
            <div class="mb-2 rounded bg-danger/10 px-3 py-2 text-xs text-danger">{appDeleteError}</div>
          {/if}
          {#if loadingApps}
            <div class="space-y-2">
              {#each Array(3) as _}
                <div class="h-10 animate-pulse rounded bg-surface-700"></div>
              {/each}
            </div>
          {:else if projectApps.length === 0}
            <p class="text-xs text-gray-500">No apps in this project.</p>
          {:else}
            <div class="space-y-1.5">
              {#each projectApps as app}
                <div class="flex items-center justify-between rounded-md border border-surface-600 bg-surface-900 px-3 py-2">
                  <span class="text-sm text-white">{app.name}</span>
                  <button type="button"
                    onclick={() => removeApp(app.name)}
                    disabled={deletingApp === app.name}
                    class="text-xs text-gray-500 hover:text-danger disabled:opacity-50">
                    {deletingApp === app.name ? 'Removing…' : 'Remove'}
                  </button>
                </div>
              {/each}
            </div>
          {/if}
        </div>

        <div class="rounded-md border border-danger/30 bg-danger/5 p-5 space-y-4">
          <h2 class="text-sm font-medium text-danger">Danger Zone</h2>
          <div>
            <p class="text-sm font-medium text-white">Delete Project</p>
            <p class="mt-1 text-xs text-gray-500">
              Permanently deletes all apps, volumes, and secrets in this project. Cannot be undone.
            </p>
          </div>
          <div class="space-y-2">
            <label class={labelCls} for="del-confirm">Type <strong class="text-white">{projectName}</strong> to confirm</label>
            <input id="del-confirm" type="text" bind:value={confirmDeleteText} placeholder={projectName}
              class="w-full rounded-md border border-danger/50 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-600 outline-none focus:border-danger" />
            <button type="button" onclick={deleteProject}
              disabled={confirmDeleteText !== projectName || deleting}
              class="rounded-md bg-danger px-4 py-2 text-sm font-medium text-white hover:bg-danger/80 disabled:cursor-not-allowed disabled:opacity-50">
              {deleting ? 'Deleting…' : 'Delete project'}
            </button>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>
