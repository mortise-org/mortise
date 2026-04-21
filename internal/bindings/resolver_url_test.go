package bindings

import "testing"

func TestToEnvPrefix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"database", "DATABASE"},
		{"my-database", "MY_DATABASE"},
		{"supabase-postgres", "SUPABASE_POSTGRES"},
		{"cache", "CACHE"},
	}
	for _, tt := range tests {
		if got := toEnvPrefix(tt.in); got != tt.want {
			t.Errorf("toEnvPrefix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAutoURL(t *testing.T) {
	tests := []struct {
		image, host, port string
		want              string
	}{
		{"postgres:16", "db.ns.svc", "5432", "postgres://postgres@db.ns.svc:5432/postgres?sslmode=disable"},
		{"supabase/postgres:15.6.1.143", "db.ns.svc", "5432", "postgres://postgres@db.ns.svc:5432/postgres?sslmode=disable"},
		{"redis:7", "cache.ns.svc", "6379", "redis://cache.ns.svc:6379"},
		{"mysql:8", "db.ns.svc", "3306", "mysql://root@db.ns.svc:3306/mysql"},
		{"mariadb:10", "db.ns.svc", "3306", "mysql://root@db.ns.svc:3306/mysql"},
		{"mongo:6", "db.ns.svc", "27017", "mongodb://db.ns.svc:27017"},
		{"nginx:latest", "web.ns.svc", "80", ""},
		{"my-custom-app:v1", "app.ns.svc", "3000", ""},
	}
	for _, tt := range tests {
		got := autoURL(tt.image, tt.host, tt.port)
		if got != tt.want {
			t.Errorf("autoURL(%q, %q, %q) = %q, want %q", tt.image, tt.host, tt.port, got, tt.want)
		}
	}
}
