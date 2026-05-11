package api_test

import (
	"net/http"
	"testing"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestUpdateMemberRejectsDemotingLastOwner(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	seedProject(t, k8sClient, "default")
	seedProjectMember(t, k8sClient, "default", "owner@example.com", mortisev1alpha1.ProjectRoleOwner)

	w := doRequest(h, http.MethodPatch, "/api/projects/default/members/owner@example.com", map[string]any{
		"role": "developer",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateMemberAllowsDemotingOwnerWhenAnotherOwnerExists(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	seedProject(t, k8sClient, "default")
	seedProjectMember(t, k8sClient, "default", "owner1@example.com", mortisev1alpha1.ProjectRoleOwner)
	seedProjectMember(t, k8sClient, "default", "owner2@example.com", mortisev1alpha1.ProjectRoleOwner)

	w := doRequest(h, http.MethodPatch, "/api/projects/default/members/owner1@example.com", map[string]any{
		"role": "developer",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
