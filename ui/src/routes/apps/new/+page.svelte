<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';

	let name = $state('');
	let image = $state('');
	let domain = $state('');
	let replicas = $state(1);
	let envVars = $state<{ key: string; value: string }[]>([]);
	let error = $state('');
	let submitting = $state(false);

	function addEnvVar() {
		envVars = [...envVars, { key: '', value: '' }];
	}

	function removeEnvVar(index: number) {
		envVars = envVars.filter((_, i) => i !== index);
	}

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		submitting = true;

		try {
			const env = envVars
				.filter((v) => v.key.trim())
				.map((v) => ({ name: v.key, value: v.value }));

			await api.post('/apps', {
				name,
				spec: {
					source: { type: 'image', image },
					network: { public: true },
					environments: [
						{
							name: 'production',
							replicas,
							env,
							...(domain ? { domain } : {})
						}
					]
				}
			});

			goto('/');
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to create app';
		} finally {
			submitting = false;
		}
	}
</script>

<div class="mx-auto max-w-lg">
	<h1 class="mb-6 text-xl font-semibold text-white">New App</h1>

	<form onsubmit={handleSubmit} class="space-y-5">
		{#if error}
			<div class="rounded-md bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
		{/if}

		<div>
			<label for="name" class="mb-1 block text-sm text-gray-400">App Name</label>
			<input
				id="name"
				type="text"
				bind:value={name}
				required
				pattern="[a-z0-9-]+"
				class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				placeholder="my-app"
			/>
		</div>

		<div>
			<span class="mb-1 block text-sm text-gray-400">Source</span>
			<div class="rounded-md border border-surface-600 bg-surface-700 px-3 py-2 text-sm text-gray-300">
				Container Image
			</div>
		</div>

		<div>
			<label for="image" class="mb-1 block text-sm text-gray-400">Image Reference</label>
			<input
				id="image"
				type="text"
				bind:value={image}
				required
				class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				placeholder="registry.example.com/app:v1.0.0"
			/>
		</div>

		<div>
			<label for="domain" class="mb-1 block text-sm text-gray-400">Domain (optional)</label>
			<input
				id="domain"
				type="text"
				bind:value={domain}
				class="w-full rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
				placeholder="app.example.com"
			/>
		</div>

		<div>
			<label for="replicas" class="mb-1 block text-sm text-gray-400">Replicas</label>
			<input
				id="replicas"
				type="number"
				bind:value={replicas}
				min="1"
				max="20"
				class="w-24 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white outline-none focus:border-accent"
			/>
		</div>

		<div>
			<div class="mb-2 flex items-center justify-between">
				<span class="text-sm text-gray-400">Environment Variables</span>
				<button
					type="button"
					onclick={addEnvVar}
					class="text-xs text-accent hover:text-accent-hover"
				>
					+ Add variable
				</button>
			</div>
			{#each envVars as envVar, i}
				<div class="mb-2 flex gap-2">
					<input
						type="text"
						bind:value={envVar.key}
						placeholder="KEY"
						class="w-1/3 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
					<input
						type="text"
						bind:value={envVar.value}
						placeholder="value"
						class="flex-1 rounded-md border border-surface-600 bg-surface-800 px-3 py-2 text-sm text-white placeholder-gray-500 outline-none focus:border-accent"
					/>
					<button
						type="button"
						onclick={() => removeEnvVar(i)}
						class="px-2 text-gray-500 hover:text-danger"
					>
						&times;
					</button>
				</div>
			{/each}
		</div>

		<div class="flex gap-3 pt-2">
			<button
				type="submit"
				disabled={submitting}
				class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
			>
				{submitting ? 'Creating...' : 'Create App'}
			</button>
			<a
				href="/"
				class="rounded-md border border-surface-600 px-4 py-2 text-sm text-gray-400 transition-colors hover:text-white"
			>
				Cancel
			</a>
		</div>
	</form>
</div>
