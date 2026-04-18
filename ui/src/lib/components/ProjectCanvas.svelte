<script lang="ts">
	import { browser } from '$app/environment';
	import { SvelteFlow, Background, Controls, MiniMap, BackgroundVariant } from '@xyflow/svelte';
	import type { Node, Edge } from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import { Plus } from 'lucide-svelte';
	import type { App } from '$lib/types';
	import AppNode from './AppNode.svelte';

	interface AppNodeData {
		app: App;
		projectName: string;
		onOpen: (appName: string) => void;
	}

	interface Props {
		projectName: string;
		apps: App[];
		selectedApp?: string | null;
		onAppOpen: (appName: string) => void;
		onAddApp?: () => void;
		onDeleteApp?: (appName: string) => void;
	}

	let { projectName, apps, selectedApp = null, onAppOpen, onAddApp, onDeleteApp }: Props = $props();

	// Register custom node type
	const nodeTypes = { app: AppNode };

	// Context menu state
	let ctxMenu = $state<{ x: number; y: number; appName: string } | null>(null);

	function appsToNodes(appsArr: App[]): Node[] {
		return appsArr.map((app, i) => {
			const key = `mortise_pos_${projectName}_${app.metadata.name}`;
			const saved = browser ? localStorage.getItem(key) : null;
			const pos = saved ? JSON.parse(saved) : { x: (i % 4) * 280 + 40, y: Math.floor(i / 4) * 200 + 40 };
			return {
				id: app.metadata.name,
				type: 'app',
				position: pos,
				selected: app.metadata.name === selectedApp,
				data: {
					app,
					projectName,
					onOpen: onAppOpen
				} satisfies AppNodeData
			};
		});
	}

	function appsToEdges(appsArr: App[]): Edge[] {
		const edges: Edge[] = [];
		for (const app of appsArr) {
			const envs = app.spec.environments ?? [];
			const seen = new Set<string>();
			for (const env of envs) {
				for (const binding of (env.bindings ?? [])) {
					// Only draw edges for same-project bindings (no project field or matches current)
					if (binding.project && binding.project !== projectName) continue;
					const edgeId = `${app.metadata.name}->${binding.ref}`;
					if (!seen.has(edgeId)) {
						seen.add(edgeId);
						edges.push({
							id: edgeId,
							source: app.metadata.name,
							target: binding.ref,
							type: 'smoothstep',
							animated: true,
							style: 'stroke: var(--color-surface-500); stroke-width: 1.5;'
						});
					}
				}
			}
		}
		return edges;
	}

	let nodes = $derived(appsToNodes(apps));
	let edges = $derived(appsToEdges(apps));

	function nodeColor(node: Node): string {
		const phase = (node.data as unknown as AppNodeData).app.status?.phase;
		if (phase === 'Ready') return 'var(--color-success)';
		if (phase === 'Failed') return 'var(--color-danger)';
		if (phase === 'Building' || phase === 'Deploying') return 'var(--color-warning)';
		return '#3a3a48';
	}

	function onNodeDragStop({ nodes: draggedNodes }: { nodes: Node[] }) {
		for (const node of draggedNodes) {
			const key = `mortise_pos_${projectName}_${node.id}`;
			localStorage.setItem(key, JSON.stringify(node.position));
		}
	}

	function onNodeContextMenu(event: { event: MouseEvent; node: Node }) {
		const { event: e, node } = event;
		e.preventDefault();
		ctxMenu = { x: e.clientX, y: e.clientY, appName: node.id };
	}

	function closeCtxMenu() {
		ctxMenu = null;
	}
</script>

<svelte:window onclick={closeCtxMenu} />

<div class="h-full w-full">
	{#if apps.length === 0}
		<div class="flex h-full flex-col items-center justify-center gap-4 text-center">
			<div class="flex h-14 w-14 items-center justify-center rounded-full bg-surface-700 text-accent">
				<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="h-7 w-7" aria-hidden="true">
					<path stroke-linecap="round" stroke-linejoin="round" d="M21 7.5l-2.25-1.313M21 7.5v2.25m0-2.25l-2.25 1.313M3 7.5l2.25-1.313M3 7.5l2.25 1.313M3 7.5v2.25m9 3l2.25-1.313M12 12.75l-2.25-1.313M12 12.75V15m0 6.75l2.25-1.313M12 21.75V19.5m0 2.25l-2.25-1.313m0-16.875L12 2.25l2.25 1.313M21 14.25v2.25l-9 5.25-9-5.25v-2.25" />
				</svg>
			</div>
			<div>
				<h2 class="text-base font-medium text-white">No apps yet</h2>
				<p class="mt-1 text-sm text-gray-500">Deploy your first app to see it here.</p>
			</div>
			{#if onAddApp}
				<button
					type="button"
					onclick={onAddApp}
					class="flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-accent-hover"
				>
					<Plus class="h-4 w-4" /> Add app
				</button>
			{/if}
		</div>
	{:else}
		<SvelteFlow
			{nodes}
			{edges}
			{nodeTypes}
			fitView
			snapGrid={[20, 20]}
			onnodedragstop={({ nodes: n }) => onNodeDragStop({ nodes: n })}
			onnodeclick={({ node }) => onAppOpen(node.id)}
			onnodecontextmenu={onNodeContextMenu}
			colorMode="dark"
		>
			<Background variant={BackgroundVariant.Dots} gap={24} size={1.5} patternColor="#252530" />
			<Controls />
			<MiniMap nodeColor={nodeColor} class="bg-surface-800" />
		</SvelteFlow>
	{/if}

	<!-- Context menu -->
	{#if ctxMenu}
		<div
			class="fixed z-50 min-w-[160px] rounded-md border border-surface-600 bg-surface-800 py-1 shadow-xl"
			style="left: {ctxMenu.x}px; top: {ctxMenu.y}px"
			role="menu"
		>
			<button
				type="button"
				role="menuitem"
				onclick={() => { onAppOpen(ctxMenu!.appName); closeCtxMenu(); }}
				class="flex w-full items-center px-3 py-2 text-sm text-gray-300 hover:bg-surface-700 hover:text-white"
			>
				Open drawer
			</button>
			{#if onDeleteApp}
				<div class="my-1 border-t border-surface-600"></div>
				<button
					type="button"
					role="menuitem"
					onclick={() => { onDeleteApp!(ctxMenu!.appName); closeCtxMenu(); }}
					class="flex w-full items-center px-3 py-2 text-sm text-danger hover:bg-surface-700"
				>
					Delete app
				</button>
			{/if}
		</div>
	{/if}
</div>
