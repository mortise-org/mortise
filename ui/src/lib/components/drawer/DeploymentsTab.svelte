<script lang="ts">
	import { api } from '$lib/api';
	import type { App, EnvironmentStatus } from '$lib/types';
	import { Rocket } from 'lucide-svelte';

	let { project, app, onOptimisticPhase }: { project: string; app: App; onOptimisticPhase?: (phase: string) => void } = $props();

	let selectedEnv = $state(app.spec.environments?.[0]?.name ?? 'production');
	let reloading = $state(false);
	let errorMsg = $state('');

	const envStatus = $derived(
		app.status?.environments?.find((e) => e.name === selectedEnv) ?? null
	);

	const phase = $derived(app.status?.phase ?? 'Pending');

	const phaseChip: Record<string, string> = {
		Ready: 'bg-success/10 text-success',
		Building: 'bg-warning/10 text-warning',
		Deploying: 'bg-warning/10 text-warning',
		Failed: 'bg-danger/10 text-danger',
		Pending: 'bg-info/10 text-info'
	};

	function chipClass(p: string): string {
		return phaseChip[p] ?? 'bg-surface-700 text-gray-400';
	}

	function fmtTime(ts: string): string {
		const d = new Date(ts);
		const now = new Date();
		const diff = (now.getTime() - d.getTime()) / 1000;
		if (diff < 60) return 'just now';
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
	}

	function shortDigest(image: string): string {
		// Extract sha256 digest and show first 7 chars (like GitHub commit hashes).
		const match = image.match(/sha256:([a-f0-9]+)/);
		if (match) return match[1].slice(0, 7);
		// Fallback: show tag or last segment.
		const parts = image.split(':');
		return parts[parts.length - 1].slice(0, 12);
	}

	async function doRollback(envName: string, index: number) {
		errorMsg = '';
		reloading = true;
		const prevPhase = app.status?.phase;
		onOptimisticPhase?.('Deploying');
		try {
			await api.rollback(project, app.metadata.name, envName, index);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Rollback failed';
			if (prevPhase) onOptimisticPhase?.(prevPhase);
		} finally {
			reloading = false;
		}
	}

	let showPromoteModal = $state(false);
	let promoteTarget = $state('');

	async function doPromote() {
		if (!promoteTarget) return;
		reloading = true;
		errorMsg = '';
		const prevPhase = app.status?.phase;
		onOptimisticPhase?.('Deploying');
		showPromoteModal = false;
		try {
			await api.promote(project, app.metadata.name, selectedEnv, promoteTarget);
			promoteTarget = '';
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Promote failed';
			showPromoteModal = true;
			if (prevPhase) onOptimisticPhase?.(prevPhase);
		} finally {
			reloading = false;
		}
	}

	async function doRebuild() {
		errorMsg = '';
		reloading = true;
		const prevPhase = app.status?.phase;
		onOptimisticPhase?.('Building');
		try {
			await api.rebuild(project, app.metadata.name);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Rebuild failed';
			if (prevPhase) onOptimisticPhase?.(prevPhase);
		} finally {
			reloading = false;
		}
	}

	async function doRedeploy() {
		if (!envStatus?.currentImage) return;
		errorMsg = '';
		reloading = true;
		const prevPhase = app.status?.phase;
		onOptimisticPhase?.('Deploying');
		try {
			await api.deploy(project, app.metadata.name, selectedEnv, envStatus.currentImage);
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Redeploy failed';
			if (prevPhase) onOptimisticPhase?.(prevPhase);
		} finally {
			reloading = false;
		}
	}
</script>

<div class="space-y-4">
	<!-- Private service badge -->
	{#if app.spec.network?.public === false}
		<span class="inline-flex items-center gap-1 rounded bg-surface-700 px-2 py-0.5 text-xs text-gray-400">
			Private service
		</span>
	{/if}

	{#if errorMsg}
		<div class="rounded-md bg-danger/10 px-3 py-2 text-xs text-danger">{errorMsg}</div>
	{/if}

	<!-- Environment tabs -->
	{#if app.spec.environments && app.spec.environments.length > 1}
		<div class="flex gap-1 border-b border-surface-600 pb-0">
			{#each app.spec.environments as env}
				<button
					type="button"
					onclick={() => (selectedEnv = env.name)}
					class="px-3 py-1.5 text-xs transition-colors {selectedEnv === env.name
						? 'border-b-2 border-accent text-white'
						: 'text-gray-400 hover:text-white'}"
				>
					{env.name}
				</button>
			{/each}
		</div>
	{/if}

	<!-- Current deploy -->
	<div class="rounded-lg border border-surface-600 bg-surface-900 p-3">
		<div class="flex items-center justify-between">
			<div class="flex items-center gap-2">
				<span class="rounded-full px-2 py-0.5 text-xs font-medium {chipClass(phase)}">
					{phase}
				</span>
				{#if envStatus?.currentImage}
					<span class="font-mono text-xs text-gray-400">{shortDigest(envStatus.currentImage)}</span>
				{/if}
			</div>
			<div class="flex items-center gap-2">
				{#if app.spec.source.type === 'git'}
					<button
						type="button"
						onclick={doRebuild}
						disabled={reloading}
						class="flex items-center gap-1 rounded-md bg-accent px-2 py-1 text-xs text-white transition-colors hover:bg-accent-hover disabled:opacity-40"
					>
						<Rocket class="h-3 w-3" /> Rebuild
					</button>
				{/if}
				<button
					type="button"
					onclick={doRedeploy}
					disabled={reloading || !envStatus?.currentImage}
					class="flex items-center gap-1 rounded-md bg-surface-700 px-2 py-1 text-xs text-gray-300 transition-colors hover:bg-surface-600 hover:text-white disabled:opacity-40"
				>
					Redeploy
				</button>
				{#if (envStatus?.deployHistory?.length ?? 0) > 1}
					<button
						type="button"
						onclick={() => doRollback(selectedEnv, 1)}
						disabled={reloading}
						class="rounded-md bg-surface-700 px-2 py-1 text-xs text-gray-300 transition-colors hover:bg-surface-600 hover:text-white disabled:opacity-40"
					>
						Rollback
					</button>
				{/if}
				{#if app.spec.environments && app.spec.environments.length > 1}
					<button
						type="button"
						onclick={() => showPromoteModal = true}
						disabled={reloading || !envStatus?.currentImage}
						class="flex items-center gap-1 rounded-md bg-surface-700 px-2 py-1 text-xs text-gray-300 transition-colors hover:bg-surface-600 hover:text-white disabled:opacity-40"
					>
						Promote →
					</button>
				{/if}
			</div>
		</div>
		{#if envStatus?.currentImage}
			<p class="mt-1.5 text-xs text-gray-500">
				{#if envStatus.deployHistory?.length}
					{@const latest = envStatus.deployHistory[envStatus.deployHistory.length - 1]}
					Deployed {fmtTime(latest.timestamp)}
					{#if latest.gitSHA} · git {latest.gitSHA.slice(0, 7)}{/if}
				{/if}
			</p>
		{:else}
			<p class="mt-1.5 text-xs text-gray-500">No deploy yet</p>
		{/if}
	</div>

	<!-- Deploy history -->
	{#if envStatus?.deployHistory && envStatus.deployHistory.length > 1}
		<div>
			<h3 class="mb-2 text-xs font-medium uppercase tracking-wide text-gray-500">History</h3>
			<div class="space-y-1.5">
				{#each envStatus.deployHistory.toReversed().slice(1) as record, i}
					<div class="flex items-center justify-between rounded-md bg-surface-900 px-3 py-2">
						<div class="min-w-0 flex-1">
							<p class="truncate font-mono text-xs text-gray-300">{shortDigest(record.image)}</p>
							{#if record.gitSHA}
								<p class="text-xs text-gray-500">{record.gitSHA.slice(0, 7)}</p>
							{/if}
						</div>
						<div class="ml-3 flex shrink-0 items-center gap-3">
							<span class="text-xs text-gray-500">{fmtTime(record.timestamp)}</span>
							<button
								type="button"
								onclick={() => doRollback(selectedEnv, envStatus!.deployHistory!.length - 1 - i - 1)}
								disabled={reloading}
								class="text-xs text-accent hover:text-accent-hover disabled:opacity-40"
							>
								Rollback
							</button>
						</div>
					</div>
				{/each}
			</div>
		</div>
	{:else if !envStatus?.currentImage}
		<div class="rounded-lg border border-dashed border-surface-600 p-8 text-center">
			<p class="text-sm text-gray-500">No deployments yet</p>
		</div>
	{/if}

	{#if showPromoteModal}
		<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
			<div class="w-80 rounded-lg border border-surface-600 bg-surface-800 p-5">
				<h3 class="mb-3 text-sm font-semibold text-white">Promote to environment</h3>
				<select bind:value={promoteTarget}
					class="w-full rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-white outline-none focus:border-accent">
					{#each (app.spec.environments ?? []).filter(e => e.name !== selectedEnv) as env}
						<option value={env.name}>{env.name}</option>
					{/each}
				</select>
				<div class="mt-3 flex justify-end gap-2">
					<button type="button" onclick={() => showPromoteModal = false}
						class="rounded-md border border-surface-600 px-3 py-1.5 text-xs text-gray-400 hover:text-white">
						Cancel
					</button>
					<button type="button" onclick={doPromote} disabled={reloading || !promoteTarget}
						class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover disabled:opacity-50">
						{reloading ? 'Promoting...' : 'Promote'}
					</button>
				</div>
			</div>
		</div>
	{/if}
</div>
