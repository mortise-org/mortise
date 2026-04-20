<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { Rocket } from 'lucide-svelte';

	let step = $state(1);
	let error = $state('');
	let saving = $state(false);

	// Step 1: Domain
	let domain = $state('');
	// Step 2: GitHub connection (single step - just authorize)
	let gitStep = $state<'start' | 'polling' | 'done'>('start');
	let userCode = $state('');
	let gitError = $state('');
	let pollTimer: ReturnType<typeof setInterval> | null = null;
	let wizardDeviceCode = $state('');
	let wizardManualChecking = $state(false);

	const steps = ['Domain', 'GitHub', 'Done'];

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

	// Auto-advance to step 3 if GitHub is already connected
	$effect(() => {
		if (step === 2 && gitStep === 'done') {
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
			let polling = false; // guard against overlapping polls

			const doPoll = async () => {
				if (polling) return;
				polling = true;
				try {
					const pd = await api.gitDevicePoll('github', dc);
					if (pd.status === 'complete') {
						if (pollTimer) clearInterval(pollTimer);
						gitStep = 'done';
					} else if (pd.status === 'slow_down') {
						// GitHub says we're too fast — back off per RFC 8628.
						if (pollTimer) clearInterval(pollTimer);
						currentInterval += 5000;
						pollTimer = setInterval(doPoll, currentInterval);
					} else if (pd.status === 'expired' || pd.status === 'denied') {
						if (pollTimer) clearInterval(pollTimer);
						gitError = `Authorization ${pd.status}. Try again.`;
						gitStep = 'start';
					}
				} catch { /* network hiccup, keep polling */ }
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
				<h2 class="mb-4 text-base font-semibold text-white">Connect your GitHub account</h2>

				{#if gitStep === 'done'}
					<div class="space-y-3">
						<div class="flex items-center gap-2">
							<span class="text-success font-medium">GitHub connected</span>
						</div>
						<p class="text-sm text-gray-400">You can now deploy from your GitHub repos.</p>
					</div>

				{:else if gitStep === 'polling'}
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
					<button
						type="button"
						onclick={wizardCheckNow}
						disabled={wizardManualChecking}
						class="mt-2 rounded-md border border-surface-600 px-3 py-1.5 text-xs text-gray-400 hover:bg-surface-600 hover:text-white disabled:opacity-50"
					>
						{wizardManualChecking ? 'Checking...' : 'Check now'}
					</button>
					</div>

				{:else}
					<p class="mb-4 text-sm text-gray-400">Authorize Mortise to access your GitHub repos with a one-time code.</p>
					<button type="button" onclick={startDeviceFlow}
						class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover">
						Connect GitHub
					</button>
					<p class="text-xs text-gray-500 mt-3">For GitLab or Gitea, add a provider in Settings after setup.</p>
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
				{#if step > 1 && step < 3}
					<button type="button" onclick={() => { if (step === 2 && pollTimer) clearInterval(pollTimer); step--; }}
						class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 hover:bg-surface-700 hover:text-white">
						Back
					</button>
				{:else}
					<div></div>
				{/if}

				{#if step < 3}
					{#if step === 2 && gitStep !== 'start'}
						<button type="button" onclick={next}
							class="rounded-md {gitStep === 'done' ? 'bg-accent' : 'border border-surface-600'} px-4 py-2 text-sm font-medium {gitStep === 'done' ? 'text-white hover:bg-accent-hover' : 'text-gray-400 hover:bg-surface-700 hover:text-white'}">
							{gitStep === 'done' ? 'Continue' : 'Skip for now'}
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
