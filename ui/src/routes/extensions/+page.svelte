<script lang="ts">
	import { ExternalLink, BookOpen, GitBranch } from 'lucide-svelte';

	interface Extension {
		name: string;
		description: string;
		category: 'Infrastructure' | 'Security' | 'Tenons';
		action: string;
		actionLabel: string;
	}

	const extensions: Extension[] = [
		{
			name: 'cert-manager',
			description: 'Automatic TLS certificates via ACME (Let\'s Encrypt). Bundled with Mortise by default.',
			category: 'Infrastructure',
			action: 'https://cert-manager.io/docs/',
			actionLabel: 'Docs'
		},
		{
			name: 'ExternalDNS',
			description: 'Automatic DNS record management for Cloudflare, AWS Route53, and other providers. Bundled by default.',
			category: 'Infrastructure',
			action: 'https://kubernetes-sigs.github.io/external-dns/',
			actionLabel: 'Docs'
		},
		{
			name: 'Traefik',
			description: 'Ingress controller for routing traffic to your apps. Bundled by default.',
			category: 'Infrastructure',
			action: 'https://doc.traefik.io/traefik/',
			actionLabel: 'Docs'
		},
		{
			name: 'kube-prometheus-stack',
			description: 'Prometheus, Grafana, and Alertmanager for monitoring. Mortise pods emit standard metrics.',
			category: 'Infrastructure',
			action: 'https://github.com/MC-Meesh/mortise/blob/main/docs/recipes/monitoring.md',
			actionLabel: 'Recipe'
		},
		{
			name: 'External Secrets Operator',
			description: 'Sync secrets from Vault, AWS Secrets Manager, or GCP into Kubernetes Secrets that Mortise reads natively.',
			category: 'Security',
			action: 'https://github.com/MC-Meesh/mortise/blob/main/docs/recipes/external-secrets.md',
			actionLabel: 'Recipe'
		},
		{
			name: 'OPA Gatekeeper',
			description: 'Policy enforcement via admission control. Gate deployments with custom rules — Mortise creates standard resources that OPA can evaluate.',
			category: 'Security',
			action: 'https://open-policy-agent.github.io/gatekeeper/',
			actionLabel: 'Docs'
		},
		{
			name: 'OIDC Providers',
			description: 'Authenticate users via Authentik, Keycloak, Okta, or Google. Configure once in PlatformConfig.',
			category: 'Security',
			action: 'https://github.com/MC-Meesh/mortise/blob/main/docs/recipes/oidc.md',
			actionLabel: 'Recipe'
		},
		{
			name: 'cf-for-saas',
			description: 'Customer-managed domains via Cloudflare custom hostnames. Host other people\'s apps on your platform.',
			category: 'Tenons',
			action: 'https://github.com/mortise-tenons/cf-for-saas',
			actionLabel: 'Repo'
		},
		{
			name: 'backup-tenon',
			description: 'Scheduled App backups (PVs + Secrets) to S3 or NFS via Velero. Homelab-friendly.',
			category: 'Tenons',
			action: 'https://github.com/mortise-tenons/backup-tenon',
			actionLabel: 'Repo'
		},
		{
			name: 'Cloudflare Tunnel',
			description: 'Access Mortise from anywhere without a public IP. Deploy cloudflared as an App pointing at Traefik.',
			category: 'Infrastructure',
			action: 'https://github.com/MC-Meesh/mortise/blob/main/docs/recipes/cloudflare-tunnel.md',
			actionLabel: 'Recipe'
		}
	];

	const categories = ['Infrastructure', 'Security', 'Tenons'] as const;

	function forCategory(cat: string) {
		return extensions.filter((e) => e.category === cat);
	}

	const categoryDescriptions: Record<string, string> = {
		Infrastructure: 'Core infrastructure components for networking, TLS, DNS, and monitoring.',
		Security: 'Authentication, secret management, and policy enforcement.',
		Tenons: 'Independent projects that extend Mortise via its REST API.'
	};
</script>

<svelte:head>
	<title>Extensions - Mortise</title>
</svelte:head>

<div class="p-8">
	<div class="mb-8">
		<h1 class="text-xl font-semibold text-white">Extensions</h1>
		<p class="mt-2 text-sm text-gray-400">
			Known integrations and tenons that work with Mortise. These are standard
			Kubernetes tools — Mortise interoperates through native primitives, not a
			plugin API.
		</p>
	</div>

	{#each categories as category}
		<section class="mb-10">
			<h2 class="mb-1 text-sm font-medium text-gray-300">{category}</h2>
			<p class="mb-4 text-xs text-gray-500">{categoryDescriptions[category]}</p>

			<div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
				{#each forCategory(category) as ext}
					<div class="flex flex-col justify-between rounded-lg border border-surface-600 bg-surface-800 p-5 transition-all duration-150 hover:-translate-y-0.5 hover:border-surface-500 hover:shadow-lg hover:shadow-black/20">
						<div>
							<h3 class="text-sm font-medium text-white">{ext.name}</h3>
							<p class="mt-1.5 text-xs leading-relaxed text-gray-400">
								{ext.description}
							</p>
						</div>
						<div class="mt-4">
							<a
								href={ext.action}
								target={ext.action.startsWith('http') ? '_blank' : undefined}
								rel={ext.action.startsWith('http') ? 'noopener noreferrer' : undefined}
								class="inline-flex items-center gap-1.5 rounded-md border border-surface-600 px-3 py-1.5 text-xs font-medium text-accent transition-colors hover:border-accent/50 hover:bg-surface-700"
							>
								{#if ext.action.startsWith('http')}
									<ExternalLink class="h-3 w-3" />
								{:else if ext.actionLabel === 'Recipe'}
									<BookOpen class="h-3 w-3" />
								{:else}
									<GitBranch class="h-3 w-3" />
								{/if}
								{ext.actionLabel}
							</a>
						</div>
					</div>
				{/each}
			</div>
		</section>
	{/each}
</div>
