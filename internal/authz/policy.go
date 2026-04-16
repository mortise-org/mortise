package authz

import (
	"context"

	"github.com/MC-Meesh/mortise/internal/auth"
)

type Resource struct {
	Kind      string // "app", "secret", "platform", "user"
	Namespace string
	Name      string
}

type Action string

const (
	ActionCreate Action = "create"
	ActionRead   Action = "read"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
)

type PolicyEngine interface {
	Authorize(ctx context.Context, p auth.Principal, resource Resource, action Action) (bool, error)
}
