package registry

import "context"

type ImageRef struct {
	Registry string
	Path     string
	Tag      string
	Full     string // registry/path:tag
}

// The repository path is project-scoped ({namespace}/{project}/{app}) because
// app names are unique only within a project; without the project two projects'
// same-named apps would share one repository and could adopt each other's
// images (CAI-454).
type RegistryBackend interface {
	PushTarget(project, app, tag string) (ImageRef, error)
	PullTarget(project, app, tag string) (ImageRef, error)
	PullSecretRef() string
	Tags(ctx context.Context, project, app string) ([]string, error)
	// ResolveTag reports whether project/app:tag exists in the registry and, if
	// so, its manifest digest. digest may be empty when the registry does not
	// return a digest header.
	ResolveTag(ctx context.Context, project, app, tag string) (digest string, found bool, err error)
	DeleteTag(ctx context.Context, project, app, tag string) error
}
