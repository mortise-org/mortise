package authz

import (
	"context"

	"github.com/mortise-org/mortise/internal/auth"
)

type Resource struct {
	Kind        string // "app", "secret", "platform", "user", "project", "gitprovider", "member", "token"
	Namespace   string
	Name        string
	Project     string // project name for project-scoped checks
	Environment string // environment name for restricted-env checks
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
