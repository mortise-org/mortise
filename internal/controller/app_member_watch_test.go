package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

// The CAI-960 regression: git auth resolves tokens through the ProjectMembers
// in the App's control namespace, so an App that reconciled before the first
// member existed latched GitAuthFailed, and creating the member afterwards
// changed nothing -- the App controller had no ProjectMember watch, so the
// valid token sat unread until an unrelated touch or the cache resync.
// Measured live: kubectl-created Project (no created-by annotation, so
// ensureOwnerMember minted nothing), App reconciled first.
func TestAppRequestsForProjectMember(t *testing.T) {
	scheme := gcTestScheme(t)

	gitApp := func(name, project string) *mortisev1alpha1.App {
		return &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: constants.ControlNamespace(project)},
			Spec: mortisev1alpha1.AppSpec{Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeGit, Repo: "https://github.com/org/" + name + ".git", ProviderRef: "github",
			}},
		}
	}
	imageApp := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "pgdb", Namespace: constants.ControlNamespace("demo")},
		Spec: mortisev1alpha1.AppSpec{Source: mortisev1alpha1.AppSource{
			Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16",
		}},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gitApp("web", "demo"), gitApp("api", "demo"), gitApp("site", "other"), imageApp).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	member := func(name, project string) *mortisev1alpha1.ProjectMember {
		return &mortisev1alpha1.ProjectMember{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: constants.ControlNamespace(project)},
			Spec: mortisev1alpha1.ProjectMemberSpec{
				Email: "someone@example.com", Project: project, Role: mortisev1alpha1.ProjectRoleOwner,
			},
		}
	}

	t.Run("a member arriving after the Apps enqueues the project's git-source Apps", func(t *testing.T) {
		reqs := r.appRequestsForProjectMember(context.Background(), member("owner", "demo"))
		got := map[string]bool{}
		for _, req := range reqs {
			got[req.Namespace+"/"+req.Name] = true
		}
		for _, want := range []string{"pj-demo/web", "pj-demo/api"} {
			if !got[want] {
				t.Errorf("git-source App %s was not enqueued; got %v", want, reqs)
			}
		}
		if len(reqs) != 2 {
			t.Errorf("expected exactly the 2 git-source Apps, got %d: %v", len(reqs), reqs)
		}
	})

	t.Run("an image-source App is not enqueued", func(t *testing.T) {
		for _, req := range r.appRequestsForProjectMember(context.Background(), member("owner", "demo")) {
			if req.Name == "pgdb" {
				t.Error("image-source App enqueued; it never resolves git auth")
			}
		}
	})

	t.Run("another project's Apps are not enqueued", func(t *testing.T) {
		for _, req := range r.appRequestsForProjectMember(context.Background(), member("owner", "demo")) {
			if req.Namespace == constants.ControlNamespace("other") {
				t.Errorf("App from another project enqueued: %v", req)
			}
		}
	})

	t.Run("a member in a project with no Apps enqueues nothing", func(t *testing.T) {
		if reqs := r.appRequestsForProjectMember(context.Background(), member("owner", "empty")); len(reqs) != 0 {
			t.Errorf("expected no requests, got %v", reqs)
		}
	})
}
