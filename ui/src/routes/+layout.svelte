<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';

	let { children } = $props();

	const isLogin = $derived(page.url.pathname === '/login');
	const isSetup = $derived(page.url.pathname === '/setup');
	const bareLayout = $derived(isLogin || isSetup);

	let checking = $state(true);

	async function checkSetupStatus() {
		try {
			const res = await fetch('/api/auth/status');
			if (!res.ok) {
				return;
			}
			const data = (await res.json()) as { setupRequired: boolean };
			const path = page.url.pathname;
			if (data.setupRequired && path !== '/setup') {
				await goto('/setup', { replaceState: true });
			} else if (!data.setupRequired && path === '/setup') {
				await goto('/login', { replaceState: true });
			}
		} catch {
			// Status endpoint unreachable — fall through and let the page render.
		}
	}

	onMount(async () => {
		await checkSetupStatus();
		checking = false;
	});

	function logout() {
		localStorage.removeItem('token');
		goto('/login');
	}

	const navItems = [
		{ href: '/', label: 'Apps' },
		{ href: '/apps/new', label: 'Deploy' }
	];
</script>

{#if checking}
	<div class="flex min-h-screen items-center justify-center bg-surface-900"></div>
{:else if bareLayout}
	{@render children()}
{:else}
	<div class="flex h-screen bg-surface-900 text-gray-100">
		<aside class="flex w-56 flex-col border-r border-surface-600 bg-surface-800">
			<div class="flex h-14 items-center px-5">
				<span class="text-lg font-semibold tracking-tight text-white">Mortise</span>
			</div>
			<nav class="mt-2 flex flex-1 flex-col gap-1 px-3">
				{#each navItems as item}
					<a
						href={item.href}
						class="rounded-md px-3 py-2 text-sm transition-colors {page.url.pathname === item.href
							? 'bg-surface-600 text-white'
							: 'text-gray-400 hover:bg-surface-700 hover:text-white'}"
					>
						{item.label}
					</a>
				{/each}
			</nav>
			<div class="border-t border-surface-600 p-3">
				<button
					onclick={logout}
					class="w-full rounded-md px-3 py-2 text-left text-sm text-gray-400 transition-colors hover:bg-surface-700 hover:text-white"
				>
					Sign out
				</button>
			</div>
		</aside>
		<main class="flex-1 overflow-y-auto p-8">
			{@render children()}
		</main>
	</div>
{/if}
