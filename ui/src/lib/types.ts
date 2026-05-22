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
	auto?: string;
}

export interface PlatformResponse {
	domain: string;
	externalDomain?: string;
	domainTemplate?: string;
	defaults?: { cpu?: string; memory?: string };
	tls: { certManagerClusterIssuer?: string };
	storage?: { defaultStorageClass?: string };
	registry?: { url?: string; namespace?: string };
	build?: { buildkitAddr?: string; defaultPlatform?: string };
	phase?: string;
	observability?: {
		logsAdapterEndpoint?: string;
		hasLogsToken?: boolean;
		metricsAdapterEndpoint?: string;
		hasMetricsToken?: boolean;
		trafficAdapterEndpoint?: string;
		hasTrafficToken?: boolean;
	};
	github?: { clientID?: string };
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
	envHash?: string;
	timestamp: string;
}

export type BuildRunPhase = 'Pending' | 'Running' | 'Succeeded' | 'Failed';

export interface BuildRunReference {
	name: string;
	phase?: BuildRunPhase;
}

export interface EnvironmentStatus {
	name: string;
	phase?: AppPhase;
	message?: string;
	readyReplicas?: number;
	currentImage?: string;
	currentDigest?: string;
	domain?: string;
	autoDomain?: string;
	deployHistory?: DeployRecord[];
	currentBuildRunRef?: BuildRunReference;
	lastSuccessfulBuildRunRef?: BuildRunReference;
	pendingEnvHash?: string;
	deployedEnvHash?: string;
	certificateStatus?: string;
	certificateMessage?: string;
}

export type AppPhase = 'Pending' | 'Building' | 'Deploying' | 'Ready' | 'Degraded' | 'CrashLooping' | 'Failed';

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

export function appNeedsRedeploy(app: App): boolean {
	return app.status?.environments?.some(env =>
		!!env.pendingEnvHash && !!env.deployedEnvHash &&
		env.pendingEnvHash !== env.deployedEnvHash
	) ?? false;
}

export function staleEnvironments(app: App): string[] {
	return (app.status?.environments ?? [])
		.filter(env => !!env.pendingEnvHash && !!env.deployedEnvHash && env.pendingEnvHash !== env.deployedEnvHash)
		.map(env => env.name);
}

export function resolveAppEnvironment(app: App, requestedEnv: string | null | undefined): string {
	const knownEnvs = [
		...(app.spec.environments ?? []).map((env) => env.name),
		...(app.status?.environments ?? []).map((env) => env.name)
	];
	const uniqueEnvs = [...new Set(knownEnvs.filter(Boolean))];
	if (requestedEnv && uniqueEnvs.includes(requestedEnv)) return requestedEnv;
	return uniqueEnvs[0] ?? requestedEnv ?? 'production';
}

function isBuildingRunPhase(phase: BuildRunPhase | undefined): boolean {
	return phase === 'Pending' || phase === 'Running';
}

export function appPhaseForEnvironment(app: App, envName: string | null | undefined): AppPhase | null {
	if (!envName) return app.status?.phase ?? null;

	const envStatus = app.status?.environments?.find((env) => env.name === envName) ?? null;
	if (!envStatus) {
		// Only fall back to top-level phase when no per-environment status exists at all.
		return (app.status?.environments?.length ?? 0) === 0 ? (app.status?.phase ?? null) : null;
	}

	if (isBuildingRunPhase(envStatus.currentBuildRunRef?.phase)) {
		return 'Building';
	}
	if (envStatus.phase) {
		return envStatus.phase;
	}

	return (app.status?.environments?.length ?? 0) === 0 ? (app.status?.phase ?? null) : null;
}

export interface SecretResponse {
	name: string;
	keys: string[];
}

export type ProjectPhase = 'Pending' | 'Ready' | 'Terminating' | 'Failed';

export interface PreviewConfig {
	enabled: boolean;
	sourceEnvironment?: string;
	botPR?: boolean;
}

export interface Project {
	name: string;
	description?: string;
	namespace: string;
	phase?: ProjectPhase;
	appCount: number;
	autoRedeploy: boolean;
	createdAt?: string;
	preview?: PreviewConfig;
	health?: EnvHealth;
}

export type EnvHealth = 'healthy' | 'warning' | 'danger' | 'unknown';

// Mirrors internal/api.projectEnvResponse. `health` is server-aggregated
// across every participating App for rendering the navbar status dot.
export interface ProjectEnvironment {
	name: string;
	displayOrder: number;
	health?: EnvHealth;
	restricted?: boolean;
	preview?: boolean;
}

// Canvas edge for GET /api/projects/{p}/bindings?environment=X.
export interface BindingEdge {
	from: string;
	to: string;
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
export type PreviewPhase = 'Pending' | 'Ready' | 'Failed';

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
	secret: string;
	path: string;
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
	role: 'owner' | 'developer' | 'viewer';
	addedAt?: string;
	addedBy?: string;
}

// Platform user (admin management)
export interface PlatformUser {
	id: string;
	email: string;
	role: 'admin' | 'member' | 'viewer';
}

// Preview environment list item
export interface PreviewSummary {
	name: string;
	environmentName: string;
	pr: { number: number; branch: string; sha: string };
	phase: PreviewPhase;
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
	offset: number;
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
	kind?: string;
	code?: string;
	fatal?: boolean;
}

export interface MetricsCurrentResponse {
	available: boolean;
	pods?: PodMetricsCurrent[];
}

export interface PodMetricsCurrent {
	name: string;
	cpu: number;
	memory: number;
}

export interface MetricsHistoryResponse {
	available: boolean;
	pods?: PodMetricsSeries[];
	error?: string;
	detail?: string;
}

export interface PodMetricsSeries {
	name: string;
	cpu: [number, number][];
	memory: [number, number][];
}

export interface TrafficSeries {
	requests: [number, number][];
	status2xx: [number, number][];
	status3xx: [number, number][];
	status4xx: [number, number][];
	status5xx: [number, number][];
	latencyP50: [number, number][];
	latencyP95: [number, number][];
	latencyP99: [number, number][];
	bytesIn: [number, number][];
	bytesOut: [number, number][];
}

export interface TrafficHistoryResponse {
	available: boolean;
	series?: TrafficSeries;
	error?: string;
	detail?: string;
}

export interface LogHistoryResponse {
	available: boolean;
	lines?: LogHistoryLine[];
	hasMore?: boolean;
	error?: string;
	detail?: string;
}

export interface LogHistoryLine {
	ts: string;
	pod: string;
	text: string;
	stream?: string;
}
