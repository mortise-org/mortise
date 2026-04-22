package constants

import "fmt"

const (
	// ControlNamespacePrefix prefixes the Project control namespace: `pj-{name}`.
	// The control namespace holds App CRDs, GitProvider webhook secrets scoped to
	// the project, and other project-owned objects that aren't env-scoped.
	ControlNamespacePrefix = "pj-"

	// ProjectNamespacePrefix is retained for backwards-compat at a few
	// integration-test touch points; new code should use ControlNamespacePrefix.
	//
	// Deprecated: use ControlNamespacePrefix.
	ProjectNamespacePrefix = ControlNamespacePrefix

	// MaxNamespaceLen is the Kubernetes DNS-label cap for namespace names.
	MaxNamespaceLen = 63
)

// ControlNamespace returns the control-namespace name for a Project
// (e.g. `pj-my-saas`). App CRDs live here; per-env workload resources live in
// per-env namespaces returned by EnvNamespace.
func ControlNamespace(projectName string) string {
	return ControlNamespacePrefix + projectName
}

// EnvNamespace returns the workload namespace for a given Project × environment
// (e.g. `pj-my-saas-production`). Every env declared on Project.spec.environments
// gets its own namespace; per-env resources (Deployment, Service, Ingress, PVC,
// ConfigMap, credentials Secret) live here.
func EnvNamespace(projectName, envName string) string {
	return ControlNamespacePrefix + projectName + "-" + envName
}

// PreviewNamespace returns the per-PR preview namespace for a Project
// (e.g. `pj-my-saas-pr-42`). Created on PR open, deleted on PR close or TTL.
func PreviewNamespace(projectName string, prNumber int) string {
	return fmt.Sprintf("%s%s-pr-%d", ControlNamespacePrefix, projectName, prNumber)
}

// ValidateProjectEnvLengths returns an error when the combined project+env name
// would overflow the 63-char namespace cap (`pj-{project}-{env}`). Callers
// should run this at Project create and env-add time so users fail early.
func ValidateProjectEnvLengths(projectName, envName string) error {
	if got := len(EnvNamespace(projectName, envName)); got > MaxNamespaceLen {
		return fmt.Errorf("namespace %q exceeds %d-char limit by %d chars; shorten project or env name",
			EnvNamespace(projectName, envName), MaxNamespaceLen, got-MaxNamespaceLen)
	}
	return nil
}

// ProjectFromControlNs trims the `pj-` prefix. Callers MUST already know the
// input is a control namespace — this helper can't disambiguate control vs env
// (`pj-foo` vs `pj-foo-production` both start with `pj-`). Since App CRDs only
// live in control namespaces, `app.Namespace` passed to this helper is safe.
//
// Returns (name, true) when the prefix matched and the remainder is non-empty.
func ProjectFromControlNs(ns string) (string, bool) {
	if len(ns) <= len(ControlNamespacePrefix) {
		return "", false
	}
	if ns[:len(ControlNamespacePrefix)] != ControlNamespacePrefix {
		return "", false
	}
	return ns[len(ControlNamespacePrefix):], true
}

// Namespace role label values — stamped on every namespace the Project
// controller owns so callers can distinguish control / env / preview.
const (
	NamespaceRoleLabel   = "mortise.dev/namespace-role"
	NamespaceRoleControl = "control"
	NamespaceRoleEnv     = "env"
	NamespaceRolePreview = "preview"

	// AppNameLabel identifies the app by name on managed resources.
	AppNameLabel = "app.kubernetes.io/name"

	// ProjectLabel is the name of the owning Project; stamped on every
	// namespace and on every resource Mortise creates.
	ProjectLabel = "mortise.dev/project"

	// EnvironmentLabel is the name of the owning environment; stamped on env
	// namespaces, preview namespaces, and every per-env resource.
	EnvironmentLabel = "mortise.dev/environment"
)
