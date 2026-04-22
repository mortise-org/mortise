package authz

import (
	"context"
	"encoding/hex"
	"fmt"

	v1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/constants"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type NativePolicyEngine struct {
	client client.Client
}

func NewNativePolicyEngine(c client.Client) *NativePolicyEngine {
	return &NativePolicyEngine{client: c}
}

func (e *NativePolicyEngine) Authorize(ctx context.Context, p auth.Principal, resource Resource, action Action) (bool, error) {
	if p.Role == auth.RoleAdmin {
		return true, nil
	}

	if p.Role == auth.RoleViewer {
		return action == ActionRead, nil
	}

	// Platform-scoped resources (no project context)
	if resource.Project == "" {
		return e.authorizePlatform(resource, action)
	}

	return e.authorizeProject(ctx, p, resource, action)
}

func (e *NativePolicyEngine) authorizePlatform(resource Resource, action Action) (bool, error) {
	switch resource.Kind {
	case "user":
		return false, nil
	case "project":
		// Members can create projects
		if action == ActionCreate {
			return true, nil
		}
		return action == ActionRead, nil
	default:
		return action == ActionRead, nil
	}
}

func (e *NativePolicyEngine) authorizeProject(ctx context.Context, p auth.Principal, resource Resource, action Action) (bool, error) {
	memberName := fmt.Sprintf("member-%s", hex.EncodeToString([]byte(p.Email)))
	ns := constants.ControlNamespace(resource.Project)

	var member v1alpha1.ProjectMember
	err := e.client.Get(ctx, types.NamespacedName{Name: memberName, Namespace: ns}, &member)
	if errors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	switch member.Spec.Role {
	case v1alpha1.ProjectRoleOwner:
		return true, nil

	case v1alpha1.ProjectRoleViewer:
		return action == ActionRead, nil

	case v1alpha1.ProjectRoleDeveloper:
		return e.authorizeDeveloper(ctx, resource, action)
	}

	return false, nil
}

func (e *NativePolicyEngine) authorizeDeveloper(ctx context.Context, resource Resource, action Action) (bool, error) {
	switch resource.Kind {
	case "member", "token":
		return false, nil
	case "project":
		if action == ActionDelete {
			return false, nil
		}
		return true, nil
	case "app":
		if action == ActionDelete {
			return false, nil
		}
	}

	// Restricted environment check
	if resource.Environment != "" {
		restricted, err := e.isRestrictedEnv(ctx, resource.Project, resource.Environment)
		if err != nil {
			return false, err
		}
		if restricted {
			return action == ActionRead, nil
		}
	}

	return true, nil
}

func (e *NativePolicyEngine) isRestrictedEnv(ctx context.Context, projectName, envName string) (bool, error) {
	var project v1alpha1.Project
	if err := e.client.Get(ctx, types.NamespacedName{Name: projectName}, &project); err != nil {
		return false, err
	}

	for _, env := range project.Spec.Environments {
		if env.Name == envName {
			return env.Restricted, nil
		}
	}

	return false, nil
}
