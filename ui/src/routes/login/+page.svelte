<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { store } from '$lib/store.svelte';

	let email = $state('');
	let password = $state('');
	let loading = $state(false);
	let error = $state('');
	let flash = $state('');

	onMount(() => {
		const msg = sessionStorage.getItem('loginFlash');
		if (msg) {
			flash = msg;
			sessionStorage.removeItem('loginFlash');
		}
	});

	async function handleLogin() {
		if (!email || !password) return;
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/auth/login', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ email, password })
			});
			if (!res.ok) {
				const data = await res.json().catch(() => ({}));
				throw new Error(data.error || 'Invalid credentials');
			}
			const data = await res.json() as { token: string; user: { Email: string; Role: string } };
			store.login(data.token, { email: data.user.Email, role: (data.user.Role as 'admin' | 'member') ?? 'member' });
			await goto('/');
		} catch (e) {
			error = e instanceof Error ? e.message : 'Login failed';
		} finally {
			loading = false;
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-surface-900">
	<div class="w-full max-w-sm">
		<div class="mb-8 text-center">
			<h1 class="text-2xl font-bold text-white">Mortise</h1>
			<p class="mt-2 text-sm text-gray-500">Sign in to your platform</p>
		</div>

		<div class="rounded-lg border border-surface-600 bg-surface-800 p-6">
			{#if flash}
				<div class="mb-4 rounded-md border border-accent/30 bg-accent/10 px-3 py-2 text-sm text-accent">{flash}</div>
			{/if}
			<form onsubmit={(e) => { e.preventDefault(); handleLogin(); }} class="space-y-4">
				<div>
					<label for="email" class="block text-sm text-gray-400">Username</label>
					<input
						id="email"
						type="text"
						bind:value={email}
						placeholder="admin"
						autocomplete="username"
						required
						class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
				</div>

				<div>
					<label for="password" class="block text-sm text-gray-400">Password</label>
					<input
						id="password"
						type="password"
						bind:value={password}
						placeholder="••••••••"
						autocomplete="current-password"
						required
						class="mt-1 w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
				</div>

				{#if error}
					<p class="text-sm text-danger">{error}</p>
				{/if}

				<button
					type="submit"
					disabled={loading || !email || !password}
					class="w-full rounded-md bg-accent py-2.5 text-sm font-medium text-white hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50 transition-colors"
				>
					{loading ? 'Signing in...' : 'Sign in'}
				</button>
			</form>
		</div>
	</div>
</div>
