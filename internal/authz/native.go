package authz

import (
	"context"

	"github.com/MC-Meesh/mortise/internal/auth"
)

type NativePolicyEngine struct{}

func NewNativePolicyEngine() *NativePolicyEngine {
	return &NativePolicyEngine{}
}

func (e *NativePolicyEngine) Authorize(_ context.Context, p auth.Principal, resource Resource, action Action) (bool, error) {
	if p.Role == auth.RoleAdmin {
		return true, nil
	}

	if p.Role == auth.RoleMember {
		switch resource.Kind {
		case "user":
			return false, nil
		case "platform", "project", "gitprovider":
			return action == ActionRead, nil
		case "app", "secret":
			return true, nil
		}
	}

	return false, nil
}
