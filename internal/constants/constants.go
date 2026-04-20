package constants

const (
	// ProjectNamespacePrefix is the prefix applied to a Project's default
	// backing Kubernetes namespace. A Project named "infra" is backed by the
	// namespace "project-infra" unless spec.namespaceOverride is set.
	ProjectNamespacePrefix = "project-"

	// DefaultTeamName is the required metadata.name for the singleton Team in v1.
	// Every v1 user is bound to this team.
	DefaultTeamName = "default-team"
)
