<script lang="ts">
	import { goto } from '$app/navigation';
	import { Globe, GitBranch, Rocket, ArrowRight, Settings } from 'lucide-svelte';

	async function goToDashboard() {
		await goto('/');
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-surface-900">
	<div class="w-full max-w-lg">
		<div class="mb-8 text-center">
			<div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-accent/10 text-accent"><Rocket class="h-7 w-7" /></div>
			<h1 class="text-2xl font-bold text-white">You're in</h1>
			<p class="mt-2 text-sm text-gray-400">Your platform is ready. You can deploy image-based apps right now. To unlock more, configure these in <a href="/settings" class="text-accent hover:underline">Settings</a>:</p>
		</div>

		<div class="space-y-3">
			<div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
				<div class="flex items-start gap-3">
					<div class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-surface-700 text-gray-400">
						<Globe class="h-4 w-4" />
					</div>
					<div>
						<h3 class="text-sm font-medium text-white">Platform domain</h3>
						<p class="mt-1 text-xs text-gray-400">Set a base domain (e.g. <span class="font-mono text-gray-300">apps.example.com</span>) and your apps automatically get URLs like <span class="font-mono text-gray-300">myapp.apps.example.com</span>. This is also how your git provider delivers push notifications — it needs to be reachable from wherever your git host runs (the public internet for github.com, or just your local network for self-hosted Gitea/GitLab). Without it, apps still run but won't get automatic URLs or push-to-deploy.</p>
					</div>
				</div>
			</div>

			<div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
				<div class="flex items-start gap-3">
					<div class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-surface-700 text-gray-400">
						<GitBranch class="h-4 w-4" />
					</div>
					<div>
						<h3 class="text-sm font-medium text-white">Git provider</h3>
						<p class="mt-1 text-xs text-gray-400">Connect GitHub, GitLab, or Gitea to deploy from git repos with automatic push-to-deploy. Not needed if you're deploying pre-built images.</p>
					</div>
				</div>
			</div>

			<div class="rounded-lg border border-surface-600 bg-surface-800 p-4">
				<div class="flex items-start gap-3">
					<div class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-surface-700 text-gray-400">
						<Settings class="h-4 w-4" />
					</div>
					<div>
						<h3 class="text-sm font-medium text-white">HTTPS, storage, registry</h3>
						<p class="mt-1 text-xs text-gray-400">Enable automatic HTTPS for your app URLs, configure persistent storage for databases and stateful apps, or connect an external image registry. All optional — sensible defaults are already in place.</p>
					</div>
				</div>
			</div>
		</div>

		<div class="mt-8 text-center">
			<button type="button" onclick={goToDashboard}
				class="inline-flex items-center gap-2 rounded-md bg-accent px-6 py-2.5 text-sm font-medium text-white hover:bg-accent-hover">
				Go to Dashboard <ArrowRight class="h-4 w-4" />
			</button>
		</div>
	</div>
</div>
