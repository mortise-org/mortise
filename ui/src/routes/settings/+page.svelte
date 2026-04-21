<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { store } from '$lib/store.svelte';
	import type { GitProviderSummary, PlatformResponse } from '$lib/types';
	import { GitBranch, Plus, Trash2, Check, Loader2, Lock } from 'lucide-svelte';

	let platform = $state<PlatformResponse | null>(null);
	let providers = $state<GitProviderSummary[]>([]);
	let loading = $state(true);
	let filterText = $state('');
	let saving = $state(false);
	let error = $state('');

	// Platform form (admin only)
	let domain = $state('');
	let defaultStorageClass = $state('');
	let tlsClusterIssuer = $state('');
	let savingStorage = $state(false);
	let savingTls = $state(false);

	// Registry (admin only)
	let registryUrl = $state('');
	let registryNamespace = $state('');
	let registryUsername = $state('');
	let savingRegistry = $state(false);

	// Build (admin only)
	let buildkitAddress = $state('');
	let buildkitPlatform = $state('linux/amd64');
	let savingBuild = $state(false);

	// --- Add Provider Modal ---
	type ProviderType = 'github' | 'gitlab' | 'gitea';
	type AuthMethod = 'device_flow' | 'pat' | 'oauth_app';
	interface AuthOption {
		method: AuthMethod;
		label: string;
		description: string;
		available: boolean;
	}

	const authOptions: Record<ProviderType, AuthOption[]> = {
		github: [
			{ method: 'device_flow', label: 'Device Flow', description: 'Authorize via one-time code on github.com', available: true },
			{ method: 'pat', label: 'Personal Access Token', description: 'Paste a token with repo, admin:repo_hook, read:org scopes', available: true },
			{ method: 'oauth_app', label: 'OAuth App', description: 'Redirect-based OAuth with callback URL', available: false },
		],
		gitlab: [
			{ method: 'pat', label: 'Personal Access Token', description: 'Paste a token with api scope', available: true },
			{ method: 'device_flow', label: 'Device Flow', description: 'OAuth device authorization (GitLab 16+)', available: false },
			{ method: 'oauth_app', label: 'OAuth Application', description: 'Redirect-based OAuth with callback URL', available: false },
		],
		gitea: [
			{ method: 'pat', label: 'Personal Access Token', description: 'Paste a token with repo scope', available: true },
			{ method: 'device_flow', label: 'Device Flow', description: 'OAuth device authorization', available: false },
			{ method: 'oauth_app', label: 'OAuth Application', description: 'Redirect-based OAuth with callback URL', available: false },
		],
	};

	const defaultHosts: Record<ProviderType, string> = {
		github: 'https://github.com',
		gitlab: 'https://gitlab.com',
		gitea: '',
	};

	let showAddModal = $state(false);
	let addStep = $state<'type' | 'method' | 'execute'>('type');
	let selectedProviderType = $state<ProviderType>('github');
	let selectedAuthMethod = $state<AuthMethod | null>(null);
	let newProviderHost = $state('');
	let newProviderClientID = $state('');
	let patToken = $state('');
	let savingPat = $state(false);

	// Device flow state
	let connectingProvider = $state<string | null>(null);
	let deviceCode = $state('');
	let deviceUserCode = $state('');
	let deviceVerificationURI = $state('');
	let devicePollTimer: ReturnType<typeof setInterval> | null = null;

	// Manual check during device flow
	let manualChecking = $state(false);

	async function manualCheckDeviceFlow() {
		if (!connectingProvider || !deviceCode) return;
		if (devicePollTimer) {
			clearInterval(devicePollTimer);
			devicePollTimer = null;
		}
		manualChecking = true;
		try {
			const pd = await api.gitDevicePoll(connectingProvider, deviceCode);
			if (pd.status === 'complete') {
				connectionStatus[connectingProvider] = true;
				store.githubConnected = true;
				closeAddModal();
				providers = await api.listGitProviders().catch(() => providers);
				return;
			} else if (pd.status === 'expired' || pd.status === 'denied') {
				error = `Authorization ${pd.status}. Try again.`;
				connectingProvider = null;
				deviceUserCode = '';
				addStep = 'method';
				return;
			}
		} catch {
			// network error
		} finally {
			manualChecking = false;
		}
		if (connectingProvider && deviceCode) {
			devicePollTimer = setInterval(autoDevicePoll, 8000);
		}
	}

	// Connection status per provider
	let connectionStatus = $state<Record<string, boolean>>({});

	function openAddModal() {
		showAddModal = true;
		addStep = 'type';
		selectedProviderType = 'github';
		selectedAuthMethod = null;
		newProviderHost = '';
		newProviderClientID = '';
		patToken = '';
		error = '';
	}

	function closeAddModal() {
		showAddModal = false;
		if (devicePollTimer) clearInterval(devicePollTimer);
		connectingProvider = null;
		deviceUserCode = '';
		deviceCode = '';
	}

	function selectProviderType(type: ProviderType) {
		selectedProviderType = type;
		newProviderHost = defaultHosts[type];
		addStep = 'method';
	}

	function selectAuthMethod(method: AuthMethod) {
		selectedAuthMethod = method;
		addStep = 'execute';
		if (method === 'device_flow') {
			startDeviceFlow();
		}
	}

	let autoPollInterval = 8000;
	let autoPollBusy = false;

	async function autoDevicePoll() {
		if (autoPollBusy || !connectingProvider || !deviceCode) return;
		autoPollBusy = true;
		try {
			const pd = await api.gitDevicePoll(connectingProvider, deviceCode);
			if (pd.status === 'complete') {
				if (devicePollTimer) clearInterval(devicePollTimer);
				connectionStatus[connectingProvider] = true;
				store.githubConnected = true;
				closeAddModal();
				providers = await api.listGitProviders().catch(() => providers);
			} else if (pd.status === 'slow_down') {
				if (devicePollTimer) clearInterval(devicePollTimer);
				autoPollInterval += 5000;
				devicePollTimer = setInterval(autoDevicePoll, autoPollInterval);
			} else if (pd.status === 'expired' || pd.status === 'denied') {
				if (devicePollTimer) clearInterval(devicePollTimer);
				error = `Authorization ${pd.status}. Try again.`;
				connectingProvider = null;
				deviceUserCode = '';
				addStep = 'method';
			}
		} catch { /* keep polling */ }
		finally { autoPollBusy = false; }
	}

	async function startDeviceFlow() {
		error = '';
		connectingProvider = selectedProviderType;
		try {
			const data = await api.gitDeviceCode(selectedProviderType);
			deviceUserCode = data.user_code;
			deviceCode = data.device_code;
			deviceVerificationURI = data.verification_uri;
			try { await navigator.clipboard.writeText(deviceUserCode); } catch {}
			window.open(data.verification_uri, '_blank');

			autoPollInterval = Math.max((data.interval || 5) * 1000, 8000);
			devicePollTimer = setInterval(autoDevicePoll, autoPollInterval);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Connection failed';
			connectingProvider = null;
			addStep = 'method';
		}
	}

	async function savePAT() {
		if (!patToken.trim()) return;
		savingPat = true;
		error = '';
		try {
			const providerName = selectedProviderType;
			// Ensure provider CRD exists
			const existingProvider = providers.find(p => p.type === selectedProviderType);
			if (!existingProvider) {
				await api.createGitProvider({
					name: providerName,
					type: selectedProviderType,
					host: newProviderHost || defaultHosts[selectedProviderType],
					clientID: newProviderClientID
				});
			}
			await api.storeGitToken(providerName, patToken.trim(), newProviderHost || undefined);
			connectionStatus[providerName] = true;
			providers = await api.listGitProviders().catch(() => []);
			closeAddModal();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to save token';
		} finally {
			savingPat = false;
		}
	}

	async function deleteProvider(name: string) {
		if (!confirm(`Delete git provider "${name}"?`)) return;
		try {
			await api.deleteGitProvider(name);
			providers = providers.filter((p) => p.name !== name);
			delete connectionStatus[name];
		} catch {
			// ignore
		}
	}

	onMount(async () => {
		try {
			const [plat, provs] = await Promise.all([
				store.isAdmin ? api.getPlatform().catch(() => null) : Promise.resolve(null),
				api.listGitProviders().catch(() => [])
			]);
			platform = plat;
			providers = provs;
			if (platform) {
				domain = platform.domain ?? '';
				tlsClusterIssuer = platform.tls?.certManagerClusterIssuer ?? '';
				defaultStorageClass = platform.storage?.defaultStorageClass ?? '';
			}
			// Check connection status for all providers
			for (const p of providers) {
				try {
					const status = await api.gitTokenStatus(p.name);
					connectionStatus[p.name] = status.connected;
					if (p.type === 'github') store.githubConnected = status.connected;
				} catch {
					connectionStatus[p.name] = false;
				}
			}
		} finally {
			loading = false;
		}
	});

	async function savePlatform() {
		saving = true;
		error = '';
		try {
			await api.patchPlatform({ domain });
		} catch (e) {
			error = e instanceof Error ? e.message : 'Save failed';
		} finally {
			saving = false;
		}
	}

	async function saveRegistry() {
		savingRegistry = true;
		try { await api.patchPlatform({ registry: { url: registryUrl, namespace: registryNamespace } }); }
		catch (e) { error = e instanceof Error ? e.message : 'Failed to save registry config'; }
		finally { savingRegistry = false; }
	}

	async function saveBuildConfig() {
		savingBuild = true;
		try { await api.patchPlatform({ build: { buildkitAddr: buildkitAddress, defaultPlatform: buildkitPlatform } }); }
		catch (e) { error = e instanceof Error ? e.message : 'Failed to save build config'; }
		finally { savingBuild = false; }
	}

	async function saveStorage() {
		savingStorage = true;
		try { await api.patchPlatform({ storage: { defaultStorageClass } }); }
		catch (e) { error = e instanceof Error ? e.message : 'Failed to save storage config'; }
		finally { savingStorage = false; }
	}

	async function saveTls() {
		savingTls = true;
		try { await api.patchPlatform({ tls: { certManagerClusterIssuer: tlsClusterIssuer } }); }
		catch (e) { error = e instanceof Error ? e.message : 'Failed to save TLS config'; }
		finally { savingTls = false; }
	}

	const sectionKeywords: Record<string, string[]> = {
		'git-providers': ['git', 'provider', 'github', 'gitlab', 'gitea', 'oauth', 'connect'],
		general: ['general', 'domain', 'platform'],
		registry: ['registry', 'oci', 'zot', 'image'],
		build: ['build', 'buildkit', 'container'],
		storage: ['storage', 'storageclass', 'pvc', 'volume'],
		tls: ['tls', 'cert', 'issuer', 'ssl'],
		users: ['users', 'invite', 'admin']
	};
	function sectionVisible(id: string): boolean {
		if (!filterText) return true;
		const q = filterText.toLowerCase();
		return (sectionKeywords[id] ?? [id]).some((k) => k.includes(q));
	}
</script>

<div class="mx-auto max-w-3xl p-8">
	<div class="mb-6">
		<h1 class="text-xl font-semibold text-white">Settings</h1>
		<p class="mt-1 text-sm text-gray-500">{store.isAdmin ? 'Platform and personal settings' : 'Personal settings'}</p>
	</div>

	<input
		type="text"
		bind:value={filterText}
		placeholder="Filter settings..."
		class="mb-6 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
	/>

	{#if error && !showAddModal}
		<div class="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
	{/if}

	<!-- Git Providers (all users) -->
	<section class="mb-8 space-y-4" id="git-providers" style:display={sectionVisible('git-providers') ? '' : 'none'}>
		<div class="flex items-center justify-between border-b border-surface-600 pb-2">
			<h2 class="text-sm font-medium text-gray-300">Git Providers</h2>
			<button
				type="button"
				onclick={openAddModal}
				class="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
			>
				<Plus class="h-3.5 w-3.5" /> Add Connection
			</button>
		</div>

		<p class="text-xs text-gray-500">Connect your git accounts to deploy from repositories. Each user manages their own connections.</p>

		<!-- Provider list -->
		{#if providers.length > 0}
			<div class="space-y-2">
				{#each providers as provider}
					<div class="flex items-center justify-between rounded-md border border-surface-600 bg-surface-700 px-4 py-3">
						<div class="flex items-center gap-3">
							<GitBranch class="h-4 w-4 text-gray-400" />
							<div>
								<p class="text-sm font-medium text-white">{provider.name}</p>
								<p class="text-xs text-gray-500">{provider.type} · {provider.host}</p>
							</div>
							{#if connectionStatus[provider.name]}
								<span class="inline-flex items-center gap-1 rounded-full bg-success/10 px-2 py-0.5 text-xs font-medium text-success">
									<Check class="h-3 w-3" /> Connected
								</span>
							{:else if connectionStatus[provider.name] === false}
								<span class="inline-flex items-center rounded-full bg-warning/10 px-2 py-0.5 text-xs font-medium text-warning">
									Not connected
								</span>
							{/if}
						</div>
						<div class="flex items-center gap-2">
							{#if !connectionStatus[provider.name]}
								<button
									onclick={openAddModal}
									class="rounded-md border border-surface-600 px-2.5 py-1 text-xs text-gray-400 hover:bg-surface-600 hover:text-white"
								>
									Connect
								</button>
							{/if}
							<button
								type="button"
								onclick={() => deleteProvider(provider.name)}
								class="rounded-md p-1.5 text-gray-500 hover:bg-surface-600 hover:text-danger"
							>
								<Trash2 class="h-4 w-4" />
							</button>
						</div>
					</div>
				{/each}
			</div>
		{:else}
			<div class="rounded-md border border-surface-600 p-4 text-center">
				<p class="text-sm text-gray-500">No git providers connected. Click "Add Connection" to get started.</p>
			</div>
		{/if}
	</section>

	<!-- Admin-only sections below -->
	{#if store.isAdmin}
		<!-- General -->
		<section class="mb-8 space-y-4" id="general" style:display={sectionVisible('general') ? '' : 'none'}>
			<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Platform Domain</h2>
			<div class="space-y-2 text-xs text-gray-500">
				<p>The base domain for automatic app URLs. When set, every public app gets a URL like <span class="font-mono text-gray-300">myapp.{domain || 'yourdomain.com'}</span> for production, or <span class="font-mono text-gray-300">myapp-staging.{domain || 'yourdomain.com'}</span> for other environments. Without this, apps still run normally — they just won't get automatic public URLs. You can always set a custom domain on any individual app regardless of this setting.</p>
				<p>This domain also serves as the callback address for git push webhooks. Your git provider sends push notifications here to trigger automatic builds. For <span class="font-mono text-gray-300">github.com</span> or <span class="font-mono text-gray-300">gitlab.com</span>, that means the domain must be reachable from the public internet. For self-hosted git (Gitea, GitLab), it just needs to be reachable from wherever your git server runs — a local network address works fine. If webhooks aren't an option, you can always trigger deploys via the API or CLI.</p>
				<p>DNS setup: point a wildcard record (<span class="font-mono text-gray-300">*.{domain || 'yourdomain.com'}</span>) at your server's IP address. If you're using a Cloudflare Tunnel, the tunnel config handles routing and no wildcard DNS record is needed.</p>
			</div>
			<div>
				<label class="text-sm text-gray-400" for="platform-domain">Domain</label>
				<input id="platform-domain" type="text" bind:value={domain} placeholder="apps.example.com"
					class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
			</div>
			<button type="button" onclick={savePlatform} disabled={saving}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
				{saving ? 'Saving...' : 'Save'}
			</button>
		</section>

		<!-- Registry -->
		<section id="registry" class="mb-8 space-y-4" style:display={sectionVisible('registry') ? '' : 'none'}>
			<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Registry</h2>
			<p class="text-xs text-gray-500">OCI registry for storing built images. Defaults to the bundled Zot registry.</p>
			<div class="space-y-3 rounded-md border border-surface-600 bg-surface-800/50 p-4">
				<div>
					<label class="block text-xs text-gray-500 mb-1" for="reg-url">Registry URL</label>
					<input id="reg-url" type="text" bind:value={registryUrl} placeholder="registry.example.com (leave empty for built-in Zot)"
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
				</div>
				<button type="button" onclick={saveRegistry} disabled={savingRegistry}
					class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
					{savingRegistry ? 'Saving...' : 'Save registry config'}
				</button>
			</div>
		</section>

		<!-- Build -->
		<section id="build" class="mb-8 space-y-4" style:display={sectionVisible('build') ? '' : 'none'}>
			<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Build</h2>
			<p class="text-xs text-gray-500">BuildKit configuration for building container images from source.</p>
			<div class="space-y-3 rounded-md border border-surface-600 bg-surface-800/50 p-4">
				<div>
					<label class="block text-xs text-gray-500 mb-1" for="bk-addr">BuildKit address</label>
					<input id="bk-addr" type="text" bind:value={buildkitAddress} placeholder="tcp://buildkitd.mortise-system:1234 (leave empty for built-in)"
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none font-mono focus:border-accent" />
				</div>
				<button type="button" onclick={saveBuildConfig} disabled={savingBuild}
					class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
					{savingBuild ? 'Saving...' : 'Save build config'}
				</button>
			</div>
		</section>

		<!-- Storage -->
		<section id="storage" class="mb-8 space-y-4" style:display={sectionVisible('storage') ? '' : 'none'}>
			<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Storage</h2>
			<div class="space-y-3 rounded-md border border-surface-600 bg-surface-800/50 p-4">
				<div>
					<label class="block text-xs text-gray-500 mb-1" for="storage-class">Default storage class</label>
					<input id="storage-class" type="text" bind:value={defaultStorageClass} placeholder="local-path"
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none font-mono focus:border-accent" />
				</div>
				<button type="button" onclick={saveStorage} disabled={savingStorage}
					class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
					{savingStorage ? 'Saving...' : 'Save storage config'}
				</button>
			</div>
		</section>

		<!-- TLS -->
		<section id="tls" class="mb-8 space-y-4" style:display={sectionVisible('tls') ? '' : 'none'}>
			<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">TLS</h2>
			<div class="space-y-3 rounded-md border border-surface-600 bg-surface-800/50 p-4">
				<div>
					<label class="block text-xs text-gray-500 mb-1" for="tls-issuer">Default cluster issuer</label>
					<input id="tls-issuer" type="text" bind:value={tlsClusterIssuer} placeholder="letsencrypt-prod"
						class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none font-mono focus:border-accent" />
				</div>
				<button type="button" onclick={saveTls} disabled={savingTls}
					class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
					{savingTls ? 'Saving...' : 'Save TLS config'}
				</button>
			</div>
		</section>

		<!-- Users -->
		<section id="users" class="mb-8 space-y-4" style:display={sectionVisible('users') ? '' : 'none'}>
			<h2 class="border-b border-surface-600 pb-2 text-sm font-medium text-gray-300">Users &amp; Invites</h2>
			<div class="rounded-md border border-surface-600 bg-surface-800/50 p-4 text-sm text-gray-400">
				<p>User management is handled per-project. To invite users to a project, go to <strong class="text-white">Project Settings &rarr; Members</strong>.</p>
			</div>
		</section>
	{/if}
</div>

<!-- Add Git Connection Modal -->
{#if showAddModal}
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div class="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 backdrop-blur-sm" tabindex="-1" onkeydown={(e) => { if (e.key === 'Escape') closeAddModal(); }}>
		<div class="w-full max-w-md rounded-lg border border-surface-600 bg-surface-800 shadow-2xl">
			<!-- Header -->
			<div class="flex items-center justify-between border-b border-surface-600 px-6 py-4">
				<h3 class="text-lg font-semibold text-white">
					{#if addStep === 'type'}Add Git Connection
					{:else if addStep === 'method'}Connect {selectedProviderType.charAt(0).toUpperCase() + selectedProviderType.slice(1)}
					{:else}Authorize
					{/if}
				</h3>
				<button onclick={closeAddModal} class="text-gray-500 hover:text-gray-300 text-lg">&times;</button>
			</div>

			<div class="p-6">
				{#if error}
					<div class="mb-4 rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
				{/if}

				<!-- Step 1: Pick provider type -->
				{#if addStep === 'type'}
					<p class="text-sm text-gray-400 mb-4">Select a git provider to connect:</p>
					<div class="space-y-2">
						{#each (['github', 'gitlab', 'gitea'] as ProviderType[]) as type}
							<button
								onclick={() => selectProviderType(type)}
								class="flex w-full items-center gap-3 rounded-md border border-surface-600 bg-surface-700 px-4 py-3 text-left hover:border-accent/50 hover:bg-surface-600 transition-colors"
							>
								<GitBranch class="h-5 w-5 text-gray-400" />
								<div>
									<p class="text-sm font-medium text-white">{type.charAt(0).toUpperCase() + type.slice(1)}</p>
									<p class="text-xs text-gray-500">
										{type === 'github' ? 'github.com or GitHub Enterprise' : type === 'gitlab' ? 'gitlab.com or self-hosted GitLab' : 'Self-hosted Gitea instance'}
									</p>
								</div>
							</button>
						{/each}
					</div>

				<!-- Step 2: Pick auth method -->
				{:else if addStep === 'method'}
					<p class="text-sm text-gray-400 mb-4">Choose how to authenticate:</p>
					<div class="space-y-2">
						{#each authOptions[selectedProviderType] as opt}
							<button
								onclick={() => opt.available && selectAuthMethod(opt.method)}
								disabled={!opt.available}
								class="flex w-full items-center gap-3 rounded-md border px-4 py-3 text-left transition-colors
									{opt.available
										? 'border-surface-600 bg-surface-700 hover:border-accent/50 hover:bg-surface-600 cursor-pointer'
										: 'border-surface-700 bg-surface-800/50 opacity-50 cursor-not-allowed'}"
							>
								{#if opt.available}
									<GitBranch class="h-5 w-5 text-gray-400" />
								{:else}
									<Lock class="h-5 w-5 text-gray-600" />
								{/if}
								<div class="flex-1">
									<div class="flex items-center gap-2">
										<p class="text-sm font-medium {opt.available ? 'text-white' : 'text-gray-500'}">{opt.label}</p>
										{#if !opt.available}
											<span class="text-[10px] px-1.5 py-0.5 rounded bg-surface-600 text-gray-500">coming soon</span>
										{/if}
									</div>
									<p class="text-xs text-gray-500">{opt.description}</p>
								</div>
							</button>
						{/each}
					</div>
					<button onclick={() => { addStep = 'type'; error = ''; }} class="mt-4 text-xs text-gray-500 hover:text-gray-300">Back</button>

				<!-- Step 3: Execute auth method -->
				{:else if addStep === 'execute'}
					{#if selectedAuthMethod === 'device_flow'}
						<!-- Device flow -->
						{#if deviceUserCode}
							<p class="text-sm text-gray-400 mb-4">Enter this code to authorize Mortise:</p>
							<code class="block rounded-lg bg-surface-900 border border-surface-600 px-4 py-3 text-center text-2xl font-mono font-bold text-white tracking-[0.3em] select-all mb-2">{deviceUserCode}</code>
							<p class="text-xs text-success text-center mb-4">Copied to clipboard</p>
							<a
								href={deviceVerificationURI}
								target="_blank"
								rel="noopener noreferrer"
								class="block w-full rounded-md bg-accent px-4 py-2 text-center text-sm font-medium text-white hover:bg-accent-hover mb-3"
							>
								Open {deviceVerificationURI}
							</a>
							<div class="flex items-center justify-center gap-2 text-xs text-gray-500">
								<div class="h-3 w-3 animate-spin rounded-full border-2 border-gray-600 border-t-accent"></div>
								Waiting for authorization...
							</div>
							<button
								type="button"
								onclick={manualCheckDeviceFlow}
								disabled={manualChecking}
								class="mt-3 w-full rounded-md border border-surface-600 px-3 py-1.5 text-xs text-gray-400 hover:bg-surface-600 hover:text-white disabled:opacity-50"
							>
								{manualChecking ? 'Checking...' : 'Check now'}
							</button>
						{:else}
							<div class="flex items-center justify-center gap-2 text-sm text-gray-400 py-8">
								<Loader2 class="h-4 w-4 animate-spin" /> Requesting device code...
							</div>
						{/if}
						<button onclick={() => { addStep = 'method'; error = ''; if (devicePollTimer) clearInterval(devicePollTimer); connectingProvider = null; deviceUserCode = ''; }}
							class="mt-4 text-xs text-gray-500 hover:text-gray-300">Back</button>

					{:else if selectedAuthMethod === 'pat'}
						<!-- PAT entry -->
						<p class="text-sm text-gray-400 mb-4">Paste your personal access token:</p>
						<div class="space-y-3">
							{#if selectedProviderType !== 'github'}
								<div>
									<label class="text-xs text-gray-400" for="pat-host">Host URL</label>
									<input id="pat-host" type="text" bind:value={newProviderHost}
										placeholder={defaultHosts[selectedProviderType] || 'https://gitea.example.com'}
										class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
								</div>
							{/if}
							<div>
								<label class="text-xs text-gray-400" for="pat-token">Access Token</label>
								<input id="pat-token" type="password" bind:value={patToken}
									placeholder="ghp_... / glpat-... / paste token here"
									class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white font-mono placeholder-gray-500 outline-none focus:border-accent" />
								<p class="mt-1 text-xs text-gray-500">
									{#if selectedProviderType === 'github'}Required scopes: <code class="bg-surface-700 px-1 rounded">repo</code>, <code class="bg-surface-700 px-1 rounded">admin:repo_hook</code>, <code class="bg-surface-700 px-1 rounded">read:org</code>
									{:else if selectedProviderType === 'gitlab'}Required scope: <code class="bg-surface-700 px-1 rounded">api</code>
									{:else}Required scope: <code class="bg-surface-700 px-1 rounded">repo</code>
									{/if}
								</p>
							</div>
							<button onclick={savePAT} disabled={savingPat || !patToken.trim()}
								class="w-full rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
								{savingPat ? 'Saving...' : 'Save Token'}
							</button>
						</div>
						<button onclick={() => { addStep = 'method'; error = ''; }}
							class="mt-4 text-xs text-gray-500 hover:text-gray-300">Back</button>
					{/if}
				{/if}
			</div>
		</div>
	</div>
{/if}
