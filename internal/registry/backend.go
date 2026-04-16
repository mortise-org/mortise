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
	PullSecretRef() string
	Tags(ctx context.Context, app string) ([]string, error)
	DeleteTag(ctx context.Context, app, tag string) error
}
