<script lang="ts">
	import { goto } from '$app/navigation';

	let email = $state('');
	let password = $state('');
	let confirmPassword = $state('');
	let error = $state('');
	let loading = $state(false);

	const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

	async function handleSetup(e: SubmitEvent) {
		e.preventDefault();
		error = '';

		if (!emailPattern.test(email)) {
			error = 'Enter a valid email address';
			return;
		}
		if (password.length < 8) {
			error = 'Password must be at least 8 characters';
			return;
		}
		if (password !== confirmPassword) {
			error = 'Passwords do not match';
			return;
		}

		loading = true;
		try {
			const res = await fetch('/api/auth/setup', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ email, password })
			});

			if (res.status === 409) {
				sessionStorage.setItem('loginFlash', 'Setup already complete. Please sign in.');
				goto('/login');
				return;
			}

			if (!res.ok) {
				const body = await res.json().catch(() => ({ error: 'Setup failed' }));
				throw new Error(body.error || 'Setup failed');
			}

			const data = await res.json();
			localStorage.setItem('token', data.token);
			goto('/setup/wizard');
		} catch (err) {
			error = err instanceof Error ? err.message : 'Setup failed';
		} finally {
			loading = false;
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-surface-900">
	<div class="w-full max-w-sm">
		<div class="mb-8 text-center">
			<h1 class="text-2xl font-semibold text-white">Welcome to Mortise</h1>
			<p class="mt-1 text-sm text-gray-500">Create your first admin account</p>
		</div>

		<form onsubmit={handleSetup} class="space-y-4">
			{#if error}
				<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
			{/if}

			<div>
				<label for="email" class="mb-1 block text-sm text-gray-400">Email</label>
				<input
					id="email"
					type="email"
					bind:value={email}
					required
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					placeholder="you@example.com"
				/>
			</div>

			<div>
				<label for="password" class="mb-1 block text-sm text-gray-400">Password</label>
				<input
					id="password"
					type="password"
					bind:value={password}
					required
					minlength="8"
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					placeholder="At least 8 characters"
				/>
			</div>

			<div>
				<label for="confirm-password" class="mb-1 block text-sm text-gray-400">
					Confirm password
				</label>
				<input
					id="confirm-password"
					type="password"
					bind:value={confirmPassword}
					required
					minlength="8"
					class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				/>
			</div>

			<button
				type="submit"
				disabled={loading}
				class="w-full rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
			>
				{loading ? 'Creating account...' : 'Create admin account'}
			</button>
		</form>

		<p class="mt-6 text-center text-xs text-gray-500">
			<a
				href="https://github.com/MC-Meesh/mortise#readme"
				target="_blank"
				rel="noopener noreferrer"
				class="text-gray-400 hover:text-accent"
			>
				Check out the docs
			</a>
		</p>
	</div>
</div>
