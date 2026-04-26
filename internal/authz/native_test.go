package authz

import (
	"context"
	"encoding/hex"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/constants"
)

func authzScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

func memberForProject(email, project string, role mortisev1alpha1.ProjectRole) *mortisev1alpha1.ProjectMember {
	name := "member-" + hex.EncodeToString([]byte(email))
	return &mortisev1alpha1.ProjectMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: constants.ControlNamespace(project),
			Labels:    map[string]string{"mortise.dev/member": "true"},
		},
		Spec: mortisev1alpha1.ProjectMemberSpec{
			Email:   email,
			Project: project,
			Role:    role,
		},
	}
}

func TestAdminCanDoEverything(t *testing.T) {
	engine := NewNativePolicyEngine(nil)
	ctx := context.Background()
	admin := auth.Principal{ID: "admin@example.com", Email: "admin@example.com", Role: auth.RoleAdmin}

	resources := []Resource{
		{Kind: "app", Namespace: "default", Name: "myapp", Project: "myproject"},
		{Kind: "secret", Namespace: "default", Name: "myapp", Project: "myproject"},
		{Kind: "user", Name: "someone"},
		{Kind: "platform", Name: "platform"},
		{Kind: "project", Name: "myproject"},
		{Kind: "gitprovider", Name: "github"},
		{Kind: "member", Name: "someone", Project: "myproject"},
		{Kind: "token", Name: "tok", Project: "myproject"},
	}

	for _, r := range resources {
		for _, a := range []Action{ActionCreate, ActionRead, ActionUpdate, ActionDelete} {
			ok, err := engine.Authorize(ctx, admin, r, a)
			if err != nil {
				t.Fatalf("Authorize(%s, %s): %v", r.Kind, a, err)
			}
			if !ok {
				t.Errorf("admin should be allowed %s on %s", a, r.Kind)
			}
		}
	}
}

func TestViewerReadOnly(t *testing.T) {
	engine := NewNativePolicyEngine(nil)
	ctx := context.Background()
	viewer := auth.Principal{ID: "viewer@example.com", Email: "viewer@example.com", Role: auth.RoleViewer}

	resources := []Resource{
		{Kind: "platform", Name: "platform"},
		{Kind: "project", Name: "myproject"},
		{Kind: "gitprovider", Name: "github"},
	}

	for _, r := range resources {
		ok, err := engine.Authorize(ctx, viewer, r, ActionRead)
		if err != nil {
			t.Fatalf("Authorize(%s, read): %v", r.Kind, err)
		}
		if !ok {
			t.Errorf("viewer should be allowed to read %s", r.Kind)
		}
		for _, a := range []Action{ActionCreate, ActionUpdate, ActionDelete} {
			ok, err := engine.Authorize(ctx, viewer, r, a)
			if err != nil {
				t.Fatalf("Authorize(%s, %s): %v", r.Kind, a, err)
			}
			if ok {
				t.Errorf("viewer should not be allowed %s on %s", a, r.Kind)
			}
		}
	}
}

func TestMemberPlatformScoped(t *testing.T) {
	engine := NewNativePolicyEngine(nil)
	ctx := context.Background()
	member := auth.Principal{ID: "member@example.com", Email: "member@example.com", Role: auth.RoleMember}

	// Member can read platform config
	ok, err := engine.Authorize(ctx, member, Resource{Kind: "platform", Name: "platform"}, ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("member should be allowed to read platform")
	}

	// Member cannot write platform config
	ok, err = engine.Authorize(ctx, member, Resource{Kind: "platform", Name: "platform"}, ActionUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("member should not be allowed to update platform")
	}

	// Member can create projects
	ok, err = engine.Authorize(ctx, member, Resource{Kind: "project", Name: "myproject"}, ActionCreate)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("member should be allowed to create project")
	}

	// Member can read projects
	ok, err = engine.Authorize(ctx, member, Resource{Kind: "project", Name: "myproject"}, ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("member should be allowed to read project")
	}

	// Member cannot update/delete projects (platform-scoped, no Project field)
	for _, a := range []Action{ActionUpdate, ActionDelete} {
		ok, err := engine.Authorize(ctx, member, Resource{Kind: "project", Name: "myproject"}, a)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("member should not be allowed %s on project (platform-scoped)", a)
		}
	}

	// Member can read gitproviders but not write
	ok, err = engine.Authorize(ctx, member, Resource{Kind: "gitprovider", Name: "github"}, ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("member should be allowed to read gitprovider")
	}
	for _, a := range []Action{ActionCreate, ActionUpdate, ActionDelete} {
		ok, err := engine.Authorize(ctx, member, Resource{Kind: "gitprovider", Name: "github"}, a)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("member should not be allowed %s on gitprovider", a)
		}
	}

	// Member cannot manage users
	for _, a := range []Action{ActionCreate, ActionRead, ActionUpdate, ActionDelete} {
		ok, err := engine.Authorize(ctx, member, Resource{Kind: "user", Name: "someone"}, a)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("member should not be allowed %s on user", a)
		}
	}
}

func TestProjectOwnerCanDoAll(t *testing.T) {
	const email = "owner@example.com"
	const project = "my-project"

	c := fake.NewClientBuilder().
		WithScheme(authzScheme(t)).
		WithObjects(memberForProject(email, project, mortisev1alpha1.ProjectRoleOwner)).
		Build()
	engine := NewNativePolicyEngine(c)
	ctx := context.Background()
	principal := auth.Principal{ID: email, Email: email, Role: auth.RoleMember}
	res := Resource{Kind: "app", Project: project}

	for _, a := range []Action{ActionCreate, ActionRead, ActionUpdate, ActionDelete} {
		ok, err := engine.Authorize(ctx, principal, res, a)
		if err != nil {
			t.Fatalf("Authorize(app, %s): %v", a, err)
		}
		if !ok {
			t.Errorf("project owner should be allowed %s on app", a)
		}
	}
}

func TestNonMemberDeniedProjectAccess(t *testing.T) {
	const project = "my-project"

	// No ProjectMember records in the cluster.
	c := fake.NewClientBuilder().
		WithScheme(authzScheme(t)).
		Build()
	engine := NewNativePolicyEngine(c)
	ctx := context.Background()
	outsider := auth.Principal{ID: "outsider@example.com", Email: "outsider@example.com", Role: auth.RoleMember}

	for _, a := range []Action{ActionCreate, ActionRead, ActionUpdate, ActionDelete} {
		ok, err := engine.Authorize(ctx, outsider, Resource{Kind: "app", Project: project}, a)
		if err != nil {
			t.Fatalf("Authorize(app, %s): %v", a, err)
		}
		if ok {
			t.Errorf("non-member should not be allowed %s on project app", a)
		}
	}
}

func TestProjectMemberNameConvention(t *testing.T) {
	// Verify the name used by ensureOwnerMember matches what authorizeProject looks up.
	// If these diverge the owner can never access their own project.
	const email = "chase@mortise.dev"
	expected := "member-" + hex.EncodeToString([]byte(email))

	c := fake.NewClientBuilder().
		WithScheme(authzScheme(t)).
		WithObjects(memberForProject(email, "proj", mortisev1alpha1.ProjectRoleOwner)).
		Build()
	engine := NewNativePolicyEngine(c)
	ctx := context.Background()
	principal := auth.Principal{ID: email, Email: email, Role: auth.RoleMember}

	ok, err := engine.Authorize(ctx, principal, Resource{Kind: "project", Project: "proj"}, ActionRead)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !ok {
		t.Errorf("owner lookup failed: ProjectMember name %q not found by authorizeProject", expected)
	}
}
