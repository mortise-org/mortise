<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { Rocket } from 'lucide-svelte';
	import type { GitProviderType } from '$lib/types';

	let step = $state(1);
	let error = $state('');
	let saving = $state(false);

	// Step 1: Domain
	let domain = $state('');

	// Step 2: Git provider
	let gitProvider = $state<GitProviderType | null>(null);
	let gitConnected = $state(false);

	// Device flow (GitHub)
	let gitStep = $state<'start' | 'polling' | 'done'>('start');
	let userCode = $state('');
	let gitError = $state('');
	let pollTimer: ReturnType<typeof setInterval> | null = null;
	let wizardDeviceCode = $state('');
	let wizardManualChecking = $state(false);

	// PAT (GitLab / Gitea)
	let patToken = $state('');
	let patHost = $state('');
	let savingPat = $state(false);

	const steps = ['Domain', 'Git', 'Done'];

	const defaultHosts: Record<string, string> = {
		gitlab: 'https://gitlab.com',
		gitea: 'https://gitea.example.com',
	};

	async function next() {
		error = '';
		if (step === 1) {
			if (!domain) { error = 'Domain is required'; return; }
			saving = true;
			try {
				await api.patchPlatform({ domain });
				step = 2;
			} catch(e) {
				error = e instanceof Error ? e.message : 'Failed to save';
			} finally { saving = false; }
		} else if (step === 2) {
			step = 3;
		}
	}

	$effect(() => {
		if (step === 2 && gitStep === 'done') {
			gitConnected = true;
			step = 3;
		}
	});

	async function startDeviceFlow() {
		gitError = '';
		gitStep = 'polling';
		try {
			const data = await api.gitDeviceCode('github');
			userCode = data.user_code;
			const dc = data.device_code;
			wizardDeviceCode = dc;
			try { await navigator.clipboard.writeText(userCode); } catch {}

			let currentInterval = (data.interval || 5) * 1000;
			let polling = false;

			const doPoll = async () => {
				if (polling) return;
				polling = true;
				try {
					const pd = await api.gitDevicePoll('github', dc);
					if (pd.status === 'complete') {
						if (pollTimer) clearInterval(pollTimer);
						gitStep = 'done';
					} else if (pd.status === 'slow_down') {
						if (pollTimer) clearInterval(pollTimer);
						currentInterval += 5000;
						pollTimer = setInterval(doPoll, currentInterval);
					} else if (pd.status === 'expired' || pd.status === 'denied') {
						if (pollTimer) clearInterval(pollTimer);
						gitError = `Authorization ${pd.status}. Try again.`;
						gitStep = 'start';
					}
				} catch { /* network hiccup */ }
				finally { polling = false; }
			};

			pollTimer = setInterval(doPoll, currentInterval);
		} catch (e) {
			gitError = e instanceof Error ? e.message : 'Connection failed';
			gitStep = 'start';
		}
	}

	async function wizardCheckNow() {
		if (!wizardDeviceCode) return;
		wizardManualChecking = true;
		try {
			const pd = await api.gitDevicePoll('github', wizardDeviceCode);
			if (pd.status === 'complete') {
				if (pollTimer) clearInterval(pollTimer);
				gitStep = 'done';
			} else if (pd.status === 'expired' || pd.status === 'denied') {
				if (pollTimer) clearInterval(pollTimer);
				gitError = `Authorization ${pd.status}. Try again.`;
				gitStep = 'start';
			}
		} catch { /* ignore */ }
		finally { wizardManualChecking = false; }
	}

	async function savePAT() {
		if (!patToken.trim()) return;
		savingPat = true;
		gitError = '';
		try {
			const host = patHost.trim() || defaultHosts[gitProvider!] || undefined;
			await api.storeGitToken(gitProvider!, patToken.trim(), host);
			gitConnected = true;
			step = 3;
		} catch(e) {
			gitError = e instanceof Error ? e.message : 'Failed to save token';
		} finally {
			savingPat = false;
		}
	}

	function resetGitStep() {
		if (pollTimer) clearInterval(pollTimer);
		gitProvider = null;
		gitStep = 'start';
		gitError = '';
		patToken = '';
		patHost = '';
	}

	async function finish() {
		await goto('/');
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-surface-900">
	<div class="w-full max-w-md">
		<div class="mb-8 text-center">
			<h1 class="text-2xl font-bold text-white">Platform Setup</h1>
			<p class="mt-2 text-sm text-gray-500">Configure your Mortise installation</p>
		</div>

		<!-- Step indicators -->
		<div class="mb-6 flex items-center justify-center gap-2">
			{#each steps as s, i}
				<div class="flex items-center gap-2">
					<div class="flex h-6 w-6 items-center justify-center rounded-full text-xs font-medium {i + 1 < step ? 'bg-success text-white' : i + 1 === step ? 'bg-accent text-white' : 'bg-surface-700 text-gray-400'}">
						{i + 1 < step ? '✓' : i + 1}
					</div>
					<span class="text-xs {i + 1 === step ? 'text-white' : 'text-gray-500'}">{s}</span>
					{#if i < steps.length - 1}
						<div class="h-px w-6 bg-surface-600"></div>
					{/if}
				</div>
			{/each}
		</div>

		<div class="rounded-lg border border-surface-600 bg-surface-800 p-6">
			{#if step === 1}
				<h2 class="mb-4 text-base font-semibold text-white">Platform Domain</h2>
				<p class="mb-4 text-sm text-gray-400">The base domain for all apps deployed to this platform (e.g. <span class="font-mono text-gray-300">apps.example.com</span>).</p>
				<input type="text" bind:value={domain} placeholder="apps.example.com"
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />

			{:else if step === 2}
				{#if gitProvider === null}
					<h2 class="mb-4 text-base font-semibold text-white">Connect a Git Provider</h2>
					<p class="mb-4 text-sm text-gray-400">Choose a provider to connect your repositories:</p>
					<div class="space-y-2">
						{#each (['github', 'gitlab', 'gitea'] as GitProviderType[]) as type}
							<button
								type="button"
								onclick={() => { gitProvider = type; if (type === 'github') startDeviceFlow(); }}
								class="flex w-full items-center gap-3 rounded-md border border-surface-600 bg-surface-700 px-4 py-3 text-left hover:border-accent/50 hover:bg-surface-700 transition-colors"
							>
								<div>
									<p class="text-sm font-medium text-white">{type.charAt(0).toUpperCase() + type.slice(1)}</p>
									<p class="text-xs text-gray-500">
										{type === 'github' ? 'github.com or GitHub Enterprise' : type === 'gitlab' ? 'gitlab.com or self-hosted GitLab' : 'Self-hosted Gitea instance'}
									</p>
								</div>
							</button>
						{/each}
					</div>

				{:else if gitProvider === 'github'}
					<h2 class="mb-4 text-base font-semibold text-white">Connect GitHub</h2>
					{#if gitStep === 'polling'}
						<div class="space-y-3">
							<p class="text-sm text-gray-400">Enter this code on GitHub to authorize Mortise:</p>
							<div class="flex items-center gap-3">
								<code class="rounded bg-surface-900 px-4 py-2 text-2xl font-mono font-bold text-white tracking-widest">{userCode}</code>
								<span class="text-xs text-gray-500">Copied to clipboard</span>
							</div>
							<a href="https://github.com/login/device" target="_blank" rel="noopener noreferrer"
								class="inline-flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover">
								Open github.com/login/device
							</a>
							<p class="text-xs text-gray-500 mt-1">Paste the code and authorize. This page updates automatically.</p>
							<button type="button" onclick={wizardCheckNow} disabled={wizardManualChecking}
								class="mt-2 rounded-md border border-surface-600 px-3 py-1.5 text-xs text-gray-400 hover:bg-surface-700 hover:text-white disabled:opacity-50">
								{wizardManualChecking ? 'Checking...' : 'Check now'}
							</button>
						</div>
					{:else}
						<p class="mb-4 text-sm text-gray-400">Authorize Mortise to access your GitHub repos with a one-time code.</p>
						<button type="button" onclick={startDeviceFlow}
							class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover">
							Connect GitHub
						</button>
					{/if}

				{:else}
					<!-- GitLab / Gitea PAT form -->
					<h2 class="mb-4 text-base font-semibold text-white">Connect {gitProvider.charAt(0).toUpperCase() + gitProvider.slice(1)}</h2>
					<div class="space-y-3">
						<div>
							<label class="text-xs text-gray-400" for="pat-host">Host URL</label>
							<input id="pat-host" type="text" bind:value={patHost}
								placeholder={defaultHosts[gitProvider]}
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent" />
						</div>
						<div>
							<label class="text-xs text-gray-400" for="pat-token">Personal Access Token</label>
							<input id="pat-token" type="password" bind:value={patToken}
								placeholder={gitProvider === 'gitlab' ? 'glpat-...' : 'paste token here'}
								class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white font-mono placeholder-gray-500 outline-none focus:border-accent" />
							<p class="mt-1 text-xs text-gray-500">
								{#if gitProvider === 'gitlab'}Required scope: <code class="bg-surface-700 px-1 rounded">api</code>
								{:else}Required scope: <code class="bg-surface-700 px-1 rounded">repo</code>
								{/if}
							</p>
						</div>
						<button onclick={savePAT} disabled={savingPat || !patToken.trim()}
							class="w-full rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
							{savingPat ? 'Saving...' : 'Save Token'}
						</button>
					</div>
				{/if}

				{#if gitError}
					<p class="mt-3 text-xs text-danger">{gitError}</p>
				{/if}

			{:else if step === 3}
				<div class="py-6 text-center">
					<div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-success/10 text-success"><Rocket class="h-7 w-7" /></div>
					<h2 class="mb-2 text-lg font-semibold text-white">All set! Go ship something awesome.</h2>
					<p class="text-sm text-gray-400">Your platform is ready. Create a project and deploy your first app.</p>
				</div>
			{/if}

			{#if error}
				<p class="mt-3 text-sm text-danger">{error}</p>
			{/if}

			<div class="mt-6 flex justify-between">
				{#if step === 2 && gitProvider !== null}
					<button type="button" onclick={resetGitStep}
						class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
						Back
					</button>
				{:else if step > 1 && step < 3}
					<button type="button" onclick={() => { step--; }}
						class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
						Back
					</button>
				{:else}
					<div></div>
				{/if}

				{#if step < 3}
					{#if step === 2 && gitProvider !== null && gitProvider !== 'github'}
						<!-- PAT form has its own save button; no footer action needed -->
						<div></div>
					{:else if step === 2 && gitProvider === 'github' && gitStep === 'done'}
						<button type="button" onclick={next}
							class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover">
							Continue
						</button>
					{:else}
						<button type="button" onclick={next} disabled={saving}
							class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover disabled:opacity-50">
							{saving ? 'Saving...' : step === 2 ? 'Skip for now' : 'Continue'}
						</button>
					{/if}
				{:else}
					<button type="button" onclick={finish}
						class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover">
						Go to Dashboard
					</button>
				{/if}
			</div>
		</div>
	</div>
</div>
