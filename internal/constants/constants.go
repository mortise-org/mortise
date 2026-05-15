package constants

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

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

	// AppFinalizer gates App deletion until cross-namespace cleanup completes.
	AppFinalizer = "mortise.dev/app-finalizer"

	maxPreviewPRNumberDigits = 19
	previewRepoHashLen       = 8
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

// PreviewEnvironmentName returns the PreviewEnvironment object name for a PR.
// Single-repo projects keep the legacy preview-pr-{n} format; multi-repo
// projects include a repo-qualified RFC1123-safe prefix.
func PreviewEnvironmentName(repo string, prNumber int, multiRepo bool) string {
	if !multiRepo {
		return fmt.Sprintf("preview-pr-%d", prNumber)
	}
	return fmt.Sprintf("%s%d", PreviewEnvironmentPrefix(repo, true), prNumber)
}

// PreviewEnvironmentPrefix returns the prefix used for multi-repo
// PreviewEnvironment names, excluding the PR number.
func PreviewEnvironmentPrefix(repo string, multiRepo bool) string {
	if !multiRepo {
		return "preview-pr-"
	}
	canonicalRepo := CanonicalRepoKey(repo)
	slug := previewRepoSlug(canonicalRepo)
	hash := previewRepoHash(canonicalRepo)
	maxSlugLen := 63 - len("preview-") - len("-") - previewRepoHashLen - len("-pr-") - maxPreviewPRNumberDigits
	if maxSlugLen < 1 {
		maxSlugLen = 1
	}
	if len(slug) > maxSlugLen {
		slug = strings.Trim(slug[:maxSlugLen], "-")
		if slug == "" {
			slug = "repo"
		}
	}
	return fmt.Sprintf("preview-%s-%s-pr-", slug, hash)
}

// CanonicalRepoKey returns a stable lowercased repository key suitable for
// matching and deterministic naming across owner/repo, URL, and SSH forms.
func CanonicalRepoKey(repo string) string {
	repo = strings.TrimSpace(strings.TrimSuffix(repo, ".git"))
	if repo == "" {
		return ""
	}

	if strings.Contains(repo, "://") {
		if u, err := url.Parse(repo); err == nil {
			repo = strings.TrimPrefix(u.Path, "/")
		}
	}
	if idx := strings.Index(repo, "@"); idx >= 0 && strings.Contains(repo[idx:], ":") {
		repo = repo[strings.LastIndex(repo, ":")+1:]
	}
	repo = strings.TrimSuffix(repo, "/")
	parts := strings.Split(repo, "/")
	if len(parts) >= 2 {
		return strings.ToLower(parts[len(parts)-2] + "/" + parts[len(parts)-1])
	}
	return strings.ToLower(repo)
}

func previewRepoSlug(repo string) string {
	slug := strings.TrimSuffix(strings.TrimSpace(repo), "/")
	if idx := strings.LastIndex(slug, "/"); idx >= 0 {
		slug = slug[idx+1:]
	}
	if idx := strings.LastIndex(slug, ":"); idx >= 0 {
		slug = slug[idx+1:]
	}
	slug = strings.TrimSuffix(strings.ToLower(slug), ".git")

	var b strings.Builder
	b.Grow(len(slug))
	lastDash := false
	for _, r := range slug {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	slug = strings.Trim(b.String(), "-")
	if slug == "" {
		return "repo"
	}
	return slug
}

func previewRepoHash(repo string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(repo)))
	return hex.EncodeToString(sum[:])[:previewRepoHashLen]
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

// MemberCRDName returns the deterministic ProjectMember CRD name for an email.
// Uses hex encoding for short emails (backward-compatible) and falls back to
// SHA-256 for long emails that would exceed the 253-char k8s name limit.
func MemberCRDName(email string) string {
	encoded := hex.EncodeToString([]byte(email))
	if len("member-")+len(encoded) <= 253 {
		return "member-" + encoded
	}
	hash := sha256.Sum256([]byte(email))
	return "member-" + hex.EncodeToString(hash[:])
}

// DeploymentName returns the Deployment name for an App in any env namespace.
func DeploymentName(appName string) string { return appName }

// CronJobName returns the CronJob name for an App in any env namespace.
func CronJobName(appName string) string { return appName }

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
