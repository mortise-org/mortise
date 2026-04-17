export type SourceType = 'git' | 'image';

export interface AppSource {
	type: SourceType;
	repo?: string;
	branch?: string;
	path?: string;
	watchPaths?: string[];
	build?: Build;
	providerRef?: string;
	image?: string;
	pullSecretRef?: string;
}

export interface Build {
	mode?: 'auto' | 'dockerfile' | 'railpack';
	dockerfilePath?: string;
	cache?: boolean;
	args?: Record<string, string>;
}

export interface NetworkConfig {
	public?: boolean;
	port?: number;
}

export interface DomainsResponse {
	primary: string;
	custom: string[];
}

export interface PlatformResponse {
	domain: string;
	dns: { provider: string; apiTokenSecretRef?: { namespace?: string; name?: string; key?: string } };
	tls: { certManagerClusterIssuer?: string };
	phase?: string;
}

export interface VolumeSpec {
	name: string;
	mountPath: string;
	size?: string;
	storageClass?: string;
	accessMode?: string;
}

export interface EnvVar {
	name: string;
	value?: string;
	valueFrom?: { secretRef?: string };
}

export interface Binding {
	ref: string;
	project?: string;
}

export interface ResourceRequirements {
	cpu?: string;
	memory?: string;
}

export interface Environment {
	name: string;
	replicas?: number;
	resources?: ResourceRequirements;
	env?: EnvVar[];
	bindings?: Binding[];
	domain?: string;
	customDomains?: string[];
}

export interface AppSpec {
	source: AppSource;
	network?: NetworkConfig;
	storage?: VolumeSpec[];
	credentials?: string[];
	environments?: Environment[];
}

export interface DeployRecord {
	image: string;
	digest?: string;
	gitSHA?: string;
	timestamp: string;
}

export interface EnvironmentStatus {
	name: string;
	readyReplicas?: number;
	currentImage?: string;
	currentDigest?: string;
	deployHistory?: DeployRecord[];
}

export type AppPhase = 'Pending' | 'Building' | 'Deploying' | 'Ready' | 'Failed';

export interface AppStatus {
	phase?: AppPhase;
	environments?: EnvironmentStatus[];
}

export interface App {
	metadata: {
		name: string;
		namespace?: string;
		creationTimestamp?: string;
	};
	spec: AppSpec;
	status?: AppStatus;
}

export interface SecretResponse {
	name: string;
	keys: string[];
}

export type ProjectPhase = 'Pending' | 'Ready' | 'Terminating' | 'Failed';

export interface Project {
	name: string;
	description?: string;
	namespace: string;
	phase?: ProjectPhase;
	appCount: number;
	createdAt?: string;
}

export type GitProviderType = 'github' | 'gitlab' | 'gitea';
export type GitProviderPhase = 'Pending' | 'Ready' | 'Failed';

export type GitProviderMode = 'oauth' | 'github-app';

export interface GitProviderSummary {
	name: string;
	type: GitProviderType;
	host: string;
	mode: GitProviderMode;
	phase: GitProviderPhase;
	hasToken: boolean;
	githubAppSlug?: string;
	githubAppInstallationID?: number;
}

export interface GitHubAppManifestResponse {
	redirectUrl: string;
	manifest: Record<string, unknown>;
	state: string;
}

export interface CreateGitProviderRequest {
	name: string;
	type: GitProviderType;
	host: string;
	oauth: { clientID: string; clientSecret: string };
	webhookSecret: string;
}

export interface Repository {
	fullName: string;
	name: string;
	description: string;
	defaultBranch: string;
	cloneURL: string;
	updatedAt: string;
	language: string;
	private: boolean;
}

export interface Branch {
	name: string;
	default: boolean;
}

export interface DeviceCodeResponse {
	device_code: string;
	user_code: string;
	verification_uri: string;
	expires_in: number;
	interval: number;
}

export interface DevicePollResponse {
	status: 'pending' | 'slow_down' | 'complete' | 'expired' | 'denied' | 'error';
	access_token?: string;
}
