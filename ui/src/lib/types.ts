export type SourceType = 'git' | 'image';

export interface AppSource {
	type: SourceType;
	repo?: string;
	branch?: string;
	path?: string;
	watchPaths?: string[];
	build?: Build;
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
