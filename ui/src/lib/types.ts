export type SourceType = 'git' | 'image' | 'external';

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
	// external source fields
	host?: string;
	port?: number;
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
	storage?: { defaultStorageClass?: string };
	phase?: string;
}

export interface RegistryConfig {
	url: string;
	namespace?: string;
	username?: string;
	password?: string;
	pullSecretRef?: string;
}

export interface BuildConfig {
	address?: string;
	tlsSecretRef?: string;
	platform?: string;
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
	annotations?: Record<string, string>;
	secretMounts?: SecretMount[];
}

export interface Credential {
	name: string;
	value?: string;
	valueFrom?: { secretRef?: string };
}

export interface AppSpec {
	source: AppSource;
	network?: NetworkConfig;
	storage?: VolumeSpec[];
	credentials?: Credential[];
	environments?: Environment[];
	sharedVars?: Array<{ name: string; value: string }>;
	kind?: 'service' | 'cron';
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

export interface Condition {
	type: string;
	status: 'True' | 'False' | 'Unknown';
	reason?: string;
	message?: string;
	lastTransitionTime?: string;
}

export interface AppStatus {
	phase?: AppPhase;
	environments?: EnvironmentStatus[];
	conditions?: Condition[];
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

export interface GitProviderSummary {
	name: string;
	type: GitProviderType;
	host: string;
	phase: GitProviderPhase;
}

export interface GitHubStatusResponse {
	connected: boolean;
}

export interface CreateGitProviderRequest {
	name: string;
	type: GitProviderType;
	host: string;
	clientID?: string;
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

// Canvas position (stored in localStorage for v1)
export interface CanvasPosition {
	x: number;
	y: number;
}

// Shared variables (sharedVars spec §5.8b)
export interface SharedVar {
	key: string;
	value: string;
	environments?: string[]; // which envs this applies to; empty = all
}

// App with extended metadata for canvas
export interface AppMeta {
	uiX?: number;
	uiY?: number;
}

// Preview environment
export type PreviewPhase = 'Pending' | 'Building' | 'Ready' | 'Failed' | 'Expired';

export interface PreviewEnvironment {
	name: string;
	appRef: string;
	pr: { number: number; branch: string; sha: string };
	phase: PreviewPhase;
	url?: string;
	ttl?: string;
	expiresAt?: string;
}

// Activity event (§5.11)
export interface ActivityEvent {
	ts: string;
	actor: string;
	action: string;
	kind: string;
	resource: string;
	project: string;
	msg: string;
}

// Deploy token
export interface DeployToken {
	id: string;
	name: string;
	app: string;
	environment: string;
	createdAt: string;
	lastUsed?: string;
	token?: string; // only on create response
}

// Project environments management
export interface EnvironmentSpec {
	name: string;
	replicas?: number;
	resources?: ResourceRequirements;
	env?: EnvVar[];
	bindings?: Binding[];
	domain?: string;
	customDomains?: string[];
	annotations?: Record<string, string>;
	secretMounts?: SecretMount[];
}

export interface SecretMount {
	name: string;
	secretName: string;
	mountPath: string;
	readOnly?: boolean;
	items?: { key: string; path: string }[];
}

// Shared variables
export interface SharedVarEntry {
	key: string;
	value: string;
}

// Project member
export interface ProjectMember {
	email: string;
	role: 'admin' | 'member';
	createdAt?: string;
}

// Invite response
export interface InviteResponse {
	token: string;
	link: string;
}

// Preview environment list item
export interface PreviewSummary {
	name: string;
	appRef: string;
	pr: { number: number; branch: string; sha: string };
	phase: PreviewPhase;
	url?: string;
	expiresAt?: string;
}

// Notification item
export interface Notification {
	id: string;
	type: 'deploy_success' | 'deploy_failed' | 'build_failed';
	appName: string;
	projectName: string;
	message: string;
	ts: string;
	read: boolean;
}
