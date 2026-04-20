package templates

import (
	"testing"
)

func TestList(t *testing.T) {
	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("expected at least one template")
	}
	found := false
	for _, n := range names {
		if n == "supabase" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected supabase template, got %v", names)
	}
}

func TestLoadSupabase(t *testing.T) {
	tpl, err := Load("supabase")
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Name != "supabase" {
		t.Errorf("expected name=supabase, got %q", tpl.Name)
	}
	if tpl.Description == "" {
		t.Error("expected non-empty description")
	}
	if tpl.Compose == "" {
		t.Fatal("expected non-empty compose")
	}
	// Should have init.sql bundled.
	if len(tpl.Files) == 0 {
		t.Fatal("expected bundled files")
	}
	if _, ok := tpl.Files["./files/init.sql"]; !ok {
		t.Errorf("expected ./files/init.sql in bundled files, got %v", tpl.Files)
	}
	// Should have postgres as required.
	if len(tpl.Required) == 0 || tpl.Required[0] != "postgres" {
		t.Errorf("expected postgres in required, got %v", tpl.Required)
	}
}

func TestLoadUnknown(t *testing.T) {
	_, err := Load("nonexistent")
	if err == nil {
		t.Error("expected error for unknown template")
	}
}
