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
	context?: 'root' | 'subdir';
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
	tls: { certManagerClusterIssuer?: string };
	storage?: { defaultStorageClass?: string };
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
	enabled?: boolean;
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

export type AppPhase = 'Pending' | 'Building' | 'Deploying' | 'Ready' | 'CrashLooping' | 'Failed';

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

export type EnvHealth = 'healthy' | 'warning' | 'danger' | 'unknown';

// Mirrors internal/api.projectEnvResponse. `health` is server-aggregated
// across every participating App for rendering the navbar status dot.
export interface ProjectEnvironment {
	name: string;
	displayOrder: number;
	health?: EnvHealth;
}

// Canvas edge for GET /api/projects/{p}/bindings?environment=X.
export interface BindingEdge {
	from: string;
	to: string;
	toProject?: string;
	environment: string;
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

// Pod descriptor returned from GET /projects/{p}/apps/{a}/pods
export interface Pod {
	name: string;
	phase: string;
	restartCount: number;
	ready: boolean;
	startedAt?: string; // RFC3339
	createdAt: string;  // RFC3339
}

// Build logs response for the Build sub-tab in the Logs drawer.
export interface BuildLogsResponse {
	lines: string[];
	building: boolean;
	timestamp?: string;  // RFC3339
	commitSHA?: string;
	status?: 'Running' | 'Succeeded' | 'Failed';
	error?: string;
}

// A single event emitted by the logs SSE stream.
export interface LogLineEvent {
	pod: string;
	ts: string;       // RFC3339; may be empty for synthetic (e.g. build) events
	line: string;
	stream?: string;  // "stdout" | "stderr"
}
