<script lang="ts">
	import { Globe, GitBranch, Settings, Lock, HardDrive, Container } from 'lucide-svelte';
</script>

<div class="mx-auto max-w-2xl p-8">
	<div class="mb-8">
		<h1 class="text-xl font-semibold text-white">Getting Started</h1>
		<p class="mt-2 text-sm text-gray-400">Mortise is ready to deploy apps. Everything below is optional — configure what you need as you need it.</p>
	</div>

	<div class="space-y-4">
		<a href="/settings#general" class="block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-colors hover:border-surface-500">
			<div class="flex items-start gap-4">
				<div class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-surface-700 text-gray-400">
					<Globe class="h-5 w-5" />
				</div>
				<div>
					<h3 class="text-sm font-medium text-white">Platform domain</h3>
					<p class="mt-1 text-xs text-gray-400">Set a base domain (e.g. <span class="font-mono text-gray-300">apps.example.com</span>) and your apps automatically get URLs like <span class="font-mono text-gray-300">myapp.apps.example.com</span>. This is also how your git provider delivers push notifications — it needs to be reachable from wherever your git host runs. Without it, apps still run but won't get automatic URLs or push-to-deploy.</p>
					<p class="mt-2 text-xs text-accent">Settings &rarr; Platform Domain</p>
				</div>
			</div>
		</a>

		<a href="/settings#git-providers" class="block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-colors hover:border-surface-500">
			<div class="flex items-start gap-4">
				<div class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-surface-700 text-gray-400">
					<GitBranch class="h-5 w-5" />
				</div>
				<div>
					<h3 class="text-sm font-medium text-white">Git provider</h3>
					<p class="mt-1 text-xs text-gray-400">Connect GitHub, GitLab, or Gitea to deploy from git repos with automatic push-to-deploy. You can use a personal access token or the one-click device flow (GitHub). Not needed if you're deploying pre-built images.</p>
					<p class="mt-2 text-xs text-accent">Settings &rarr; Git Providers &rarr; Add Connection</p>
				</div>
			</div>
		</a>

		<a href="/settings#tls" class="block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-colors hover:border-surface-500">
			<div class="flex items-start gap-4">
				<div class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-surface-700 text-gray-400">
					<Lock class="h-5 w-5" />
				</div>
				<div>
					<h3 class="text-sm font-medium text-white">HTTPS</h3>
					<p class="mt-1 text-xs text-gray-400">If you have cert-manager installed, set the cluster issuer name (e.g. <span class="font-mono text-gray-300">letsencrypt-prod</span>) and your apps automatically get TLS certificates. Works with Let's Encrypt, ZeroSSL, or any ACME provider.</p>
					<p class="mt-2 text-xs text-accent">Settings &rarr; TLS</p>
				</div>
			</div>
		</a>

		<a href="/settings#storage" class="block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-colors hover:border-surface-500">
			<div class="flex items-start gap-4">
				<div class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-surface-700 text-gray-400">
					<HardDrive class="h-5 w-5" />
				</div>
				<div>
					<h3 class="text-sm font-medium text-white">Storage</h3>
					<p class="mt-1 text-xs text-gray-400">Set a default storage class for persistent volumes (databases, file uploads). Your cluster likely already has one — Mortise uses whatever is set as the cluster default if you don't override it here.</p>
					<p class="mt-2 text-xs text-accent">Settings &rarr; Storage</p>
				</div>
			</div>
		</a>

		<a href="/settings#registry" class="block rounded-lg border border-surface-600 bg-surface-800 p-5 transition-colors hover:border-surface-500">
			<div class="flex items-start gap-4">
				<div class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-surface-700 text-gray-400">
					<Container class="h-5 w-5" />
				</div>
				<div>
					<h3 class="text-sm font-medium text-white">Image registry</h3>
					<p class="mt-1 text-xs text-gray-400">Mortise includes a built-in image registry for storing builds. If you prefer to use an external registry (Docker Hub, GitHub Container Registry, Harbor, ECR), configure it here.</p>
					<p class="mt-2 text-xs text-accent">Settings &rarr; Registry</p>
				</div>
			</div>
		</a>
	</div>

	<div class="mt-8 rounded-lg border border-surface-600 bg-surface-800/50 p-5">
		<h3 class="text-sm font-medium text-white">What you can do right now</h3>
		<ul class="mt-2 space-y-1 text-xs text-gray-400">
			<li>Deploy any container image (e.g. <span class="font-mono text-gray-300">nginx:1.27</span>, <span class="font-mono text-gray-300">postgres:16</span>)</li>
			<li>One-click databases: Postgres, Redis, MySQL, MinIO</li>
			<li>Deploy a full Supabase stack</li>
			<li>Import a docker-compose.yml</li>
			<li>Bind services together (auto-injected <span class="font-mono text-gray-300">DATABASE_URL</span>, etc.)</li>
			<li>Manage environment variables per app and per environment</li>
		</ul>
	</div>
</div>
