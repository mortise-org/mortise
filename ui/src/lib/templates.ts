import type { AppSpec } from './types';
import { Database, FileText, Lock, UtensilsCrossed, GitBranch, Sparkles } from 'lucide-svelte';
import type { ComponentType } from 'svelte';

/** Map template icon names to Lucide components for rendering. */
export const templateIcons: Record<string, ComponentType> = {
	Database,
	FileText,
	Lock,
	UtensilsCrossed,
	GitBranch,
	Sparkles
};

/** Generate a cryptographically random alphanumeric string. */
function generatePassword(length = 24): string {
	const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
	const values = new Uint8Array(length);
	crypto.getRandomValues(values);
	return Array.from(values, (v) => chars[v % chars.length]).join('');
}

export type TemplateCategory = 'database' | 'app' | 'blank';

export interface TemplateField {
	key: 'name' | 'domain';
	label: string;
	placeholder?: string;
	required?: boolean;
}

export interface Template {
	id: string;
	name: string;
	description: string;
	icon: string;
	category: TemplateCategory;
	submitLabel: string;
	fields: TemplateField[];
	defaults: {
		name: string;
		spec: AppSpec;
	};
}

export const templates: Template[] = [
	{
		id: 'postgres-16',
		name: 'Postgres 16',
		description: 'Managed PostgreSQL 16 database with persistent storage and automatic credential wiring.',
		icon: 'Database',
		category: 'database',
		submitLabel: 'Deploy Postgres',
		fields: [
			{ key: 'name', label: 'App Name', placeholder: 'postgres', required: true }
		],
		defaults: {
			name: 'postgres',
			spec: {
				source: { type: 'image', image: 'postgres:16' },
				network: { public: false },
				storage: [
					{ name: 'pgdata', mountPath: '/var/lib/postgresql/data', size: '10Gi' }
				],
				credentials: [
					{ name: 'DATABASE_URL' },
					{ name: 'host' },
					{ name: 'port' },
					{ name: 'user' },
					{ name: 'password' }
				],
				environments: [
					{
						name: 'production',
						replicas: 1,
						env: [
							{ name: 'POSTGRES_USER', value: 'postgres' },
							{ name: 'POSTGRES_PASSWORD', value: generatePassword() }
						]
					}
				]
			}
		}
	},
	{
		id: 'redis-7',
		name: 'Redis 7',
		description: 'In-memory Redis 7 cache with persistent AOF storage on a private network.',
		icon: 'Database', // Redis
		category: 'database',
		submitLabel: 'Deploy Redis',
		fields: [
			{ key: 'name', label: 'App Name', placeholder: 'redis', required: true }
		],
		defaults: {
			name: 'redis',
			spec: {
				source: { type: 'image', image: 'redis:7-alpine' },
				network: { public: false },
				storage: [
					{ name: 'redis-data', mountPath: '/data', size: '1Gi' }
				],
				credentials: [
					{ name: 'REDIS_URL' },
					{ name: 'host' },
					{ name: 'port' }
				],
				environments: [
					{
						name: 'production',
						replicas: 1
					}
				]
			}
		}
	},
	{
		id: 'paperless-ngx',
		name: 'Paperless-ngx',
		description: 'Document management system that turns physical docs into a searchable archive.',
		icon: 'FileText',
		category: 'app',
		submitLabel: 'Deploy Paperless-ngx',
		fields: [
			{ key: 'name', label: 'App Name', placeholder: 'paperless', required: true },
			{ key: 'domain', label: 'Domain', placeholder: 'paperless.example.com' }
		],
		defaults: {
			name: 'paperless',
			spec: {
				source: { type: 'image', image: 'ghcr.io/paperless-ngx/paperless-ngx:latest' },
				network: { public: true },
				storage: [
					{ name: 'data', mountPath: '/usr/src/paperless/data', size: '5Gi' },
					{ name: 'media', mountPath: '/usr/src/paperless/media', size: '20Gi' }
				],
				environments: [
					{
						name: 'production',
						replicas: 1,
						env: [
							{ name: 'PAPERLESS_URL', value: '' },
							{ name: 'PAPERLESS_SECRET_KEY', value: generatePassword(48) },
							{ name: 'PAPERLESS_TIME_ZONE', value: 'UTC' },
							{ name: 'PAPERLESS_OCR_LANGUAGE', value: 'eng' }
						]
					}
				]
			}
		}
	},
	{
		id: 'vaultwarden',
		name: 'Vaultwarden',
		description: 'Self-hosted, lightweight Bitwarden-compatible password manager server.',
		icon: 'Lock',
		category: 'app',
		submitLabel: 'Deploy Vaultwarden',
		fields: [
			{ key: 'name', label: 'App Name', placeholder: 'vaultwarden', required: true },
			{ key: 'domain', label: 'Domain', placeholder: 'vault.example.com' }
		],
		defaults: {
			name: 'vaultwarden',
			spec: {
				source: { type: 'image', image: 'vaultwarden/server:latest' },
				network: { public: true },
				storage: [
					{ name: 'data', mountPath: '/data', size: '2Gi' }
				],
				environments: [
					{
						name: 'production',
						replicas: 1,
						env: [
							{ name: 'DOMAIN', value: '' },
							{ name: 'SIGNUPS_ALLOWED', value: 'false' },
							{ name: 'ADMIN_TOKEN', value: generatePassword(48) }
						]
					}
				]
			}
		}
	},
	{
		id: 'mealie',
		name: 'Mealie',
		description: 'Self-hosted recipe manager and meal planner with a clean modern UI.',
		icon: 'UtensilsCrossed',
		category: 'app',
		submitLabel: 'Deploy Mealie',
		fields: [
			{ key: 'name', label: 'App Name', placeholder: 'mealie', required: true },
			{ key: 'domain', label: 'Domain', placeholder: 'mealie.example.com' }
		],
		defaults: {
			name: 'mealie',
			spec: {
				source: { type: 'image', image: 'ghcr.io/mealie-recipes/mealie:latest' },
				network: { public: true },
				storage: [
					{ name: 'data', mountPath: '/app/data', size: '2Gi' }
				],
				environments: [
					{
						name: 'production',
						replicas: 1,
						env: [
							{ name: 'BASE_URL', value: '' },
							{ name: 'ALLOW_SIGNUP', value: 'false' },
							{ name: 'TZ', value: 'UTC' },
							{ name: 'MAX_WORKERS', value: '1' },
							{ name: 'WEB_CONCURRENCY', value: '1' }
						]
					}
				]
			}
		}
	},
	{
		id: 'git-dockerfile',
		name: 'Deploy from Git',
		description: 'Connect a Git repository and build with Dockerfile or auto-detect.',
		icon: 'GitBranch',
		category: 'app',
		submitLabel: 'Create App',
		fields: [
			{ key: 'name', label: 'App Name', placeholder: 'my-app', required: true },
			{ key: 'domain', label: 'Domain', placeholder: 'app.example.com' }
		],
		defaults: {
			name: '',
			spec: {
				source: { type: 'git', repo: '', branch: 'main', build: { mode: 'auto' } },
				network: { public: true },
				environments: [
					{
						name: 'production',
						replicas: 1,
						env: []
					}
				]
			}
		}
	},
	{
		id: 'blank',
		name: 'Blank',
		description: 'Start from scratch with a custom container image and your own configuration.',
		icon: 'Sparkles',
		category: 'blank',
		submitLabel: 'Create App',
		fields: [
			{ key: 'name', label: 'App Name', placeholder: 'my-app', required: true },
			{ key: 'domain', label: 'Domain', placeholder: 'app.example.com' }
		],
		defaults: {
			name: '',
			spec: {
				source: { type: 'image', image: '' },
				network: { public: true },
				environments: [
					{
						name: 'production',
						replicas: 1,
						env: []
					}
				]
			}
		}
	}
];

export function getTemplate(id: string): Template | undefined {
	return templates.find((t) => t.id === id);
}
