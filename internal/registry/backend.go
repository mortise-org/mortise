package registry

import "context"

type ImageRef struct {
	Registry string
	Path     string
	Tag      string
	Full     string // registry/path:tag
}

type RegistryBackend interface {
	PushTarget(app, tag string) (ImageRef, error)
	PullTarget(app, tag string) (ImageRef, error)
	PullSecretRef() string
	Tags(ctx context.Context, app string) ([]string, error)
	// ResolveTag reports whether app:tag exists in the registry and, if so,
	// its manifest digest. digest may be empty when the registry does not
	// return a digest header.
	ResolveTag(ctx context.Context, app, tag string) (digest string, found bool, err error)
	DeleteTag(ctx context.Context, app, tag string) error
}
