package authz

import (
	"context"
	"testing"

	"github.com/MC-Meesh/mortise/internal/auth"
)

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
