package bindings

import "testing"

func TestToEnvPrefix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"database", "DATABASE"},
		{"my-database", "MY_DATABASE"},
		{"supabase-postgres", "SUPABASE_POSTGRES"},
		{"cache", "CACHE"},
		{"my.database", "MY_DATABASE"},
		{"3scale", "SCALE"},
		{"___", "BINDING"},
		{"123", "BINDING"},
	}
	for _, tt := range tests {
		if got := toEnvPrefix(tt.in); got != tt.want {
			t.Errorf("toEnvPrefix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestImageBaseName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"postgres:16", "postgres"},
		{"docker.io/library/postgres:16", "postgres"},
		{"ghcr.io/supabase/postgres:15", "postgres"},
		{"supabase/postgres:15.6.1.143", "postgres"},
		{"redis:7", "redis"},
		{"nginx:latest", "nginx"},
		{"my-custom-app:v1", "my-custom-app"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := imageBaseName(tt.in); got != tt.want {
			t.Errorf("imageBaseName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAutoURL(t *testing.T) {
	tests := []struct {
		image, host, port string
		want              string
	}{
		{"postgres:16", "db.ns.svc", "5432", "postgres://db.ns.svc:5432?sslmode=disable"},
		{"docker.io/library/postgres:16", "db.ns.svc", "5432", "postgres://db.ns.svc:5432?sslmode=disable"},
		{"ghcr.io/supabase/postgres:15", "db.ns.svc", "5432", "postgres://db.ns.svc:5432?sslmode=disable"},
		{"redis:7", "cache.ns.svc", "6379", "redis://cache.ns.svc:6379"},
		{"mysql:8", "db.ns.svc", "3306", "mysql://db.ns.svc:3306"},
		{"mariadb:10", "db.ns.svc", "3306", "mysql://db.ns.svc:3306"},
		{"mongo:6", "db.ns.svc", "27017", "mongodb://db.ns.svc:27017"},
		{"nginx:latest", "web.ns.svc", "80", ""},
		{"my-custom-app:v1", "app.ns.svc", "3000", ""},
		{"", "host", "port", ""},
		{"postgres:16", "", "5432", ""},
		{"postgres:16", "host", "", ""},
	}
	for _, tt := range tests {
		got := autoURL(tt.image, tt.host, tt.port)
		if got != tt.want {
			t.Errorf("autoURL(%q, %q, %q) = %q, want %q", tt.image, tt.host, tt.port, got, tt.want)
		}
	}
}
