package authz

import (
	"context"
	"testing"

	"github.com/MC-Meesh/mortise/internal/auth"
)

func TestAdminCanDoEverything(t *testing.T) {
	engine := NewNativePolicyEngine()
	ctx := context.Background()
	admin := auth.Principal{ID: "admin@example.com", Email: "admin@example.com", Role: auth.RoleAdmin}

	resources := []Resource{
		{Kind: "app", Namespace: "default", Name: "myapp"},
		{Kind: "user", Name: "someone"},
		{Kind: "platform", Name: "platform"},
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

func TestMemberRestrictions(t *testing.T) {
	engine := NewNativePolicyEngine()
	ctx := context.Background()
	member := auth.Principal{ID: "member@example.com", Email: "member@example.com", Role: auth.RoleMember}

	// Member can CRUD apps
	for _, a := range []Action{ActionCreate, ActionRead, ActionUpdate, ActionDelete} {
		ok, err := engine.Authorize(ctx, member, Resource{Kind: "app", Namespace: "default", Name: "myapp"}, a)
		if err != nil {
			t.Fatalf("Authorize(app, %s): %v", a, err)
		}
		if !ok {
			t.Errorf("member should be allowed %s on app", a)
		}
	}

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
