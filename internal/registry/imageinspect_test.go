package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// fakeOCI serves a minimal OCI distribution read path: an index (optional),
// a platform manifest, and a config blob, with an optional bearer token dance.
func fakeOCI(t *testing.T, exposed []string, withIndex, withToken bool) *httptest.Server {
	t.Helper()
	cfgBlob, _ := json.Marshal(map[string]any{
		"config": map[string]any{"ExposedPorts": func() map[string]any {
			m := map[string]any{}
			for _, p := range exposed {
				m[p] = map[string]any{}
			}
			return m
		}()},
	})
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"config":        map[string]any{"digest": "sha256:cfg", "mediaType": "application/vnd.oci.image.config.v1+json"},
	})
	index, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests": []map[string]any{
			{"digest": "sha256:other", "platform": map[string]any{"os": "linux", "architecture": "other-arch"}},
			{"digest": "sha256:plat", "platform": map[string]any{"os": "linux", "architecture": runtime.GOARCH}},
		},
	})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if withToken && r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok123"})
			return
		}
		if withToken && r.Header.Get("Authorization") != "Bearer tok123" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="reg"`, srv.URL+"/token"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/") && strings.Contains(r.URL.Path, "/manifests/latest"):
			if withIndex {
				w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
				_, _ = w.Write(index)
			} else {
				_, _ = w.Write(manifest)
			}
		case strings.Contains(r.URL.Path, "/manifests/sha256:plat"):
			_, _ = w.Write(manifest)
		case strings.Contains(r.URL.Path, "/blobs/sha256:cfg"):
			_, _ = w.Write(cfgBlob)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func inspectRef(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://") + "/some/app:latest"
}

func TestDetectExposedPortLowestTCP(t *testing.T) {
	srv := fakeOCI(t, []string{"8443/tcp", "80/tcp", "53/udp"}, false, false)
	defer srv.Close()
	got, err := (&HTTPImageInspector{}).DetectExposedPort(context.Background(), inspectRef(srv), nil)
	if err != nil || got != 80 {
		t.Fatalf("got %d, %v; want 80, nil", got, err)
	}
}

func TestDetectExposedPortViaIndexAndToken(t *testing.T) {
	srv := fakeOCI(t, []string{"9898/tcp"}, true, true)
	defer srv.Close()
	got, err := (&HTTPImageInspector{}).DetectExposedPort(context.Background(), inspectRef(srv), nil)
	if err != nil || got != 9898 {
		t.Fatalf("got %d, %v; want 9898, nil", got, err)
	}
}

func TestDetectExposedPortNoneExposed(t *testing.T) {
	srv := fakeOCI(t, nil, false, false)
	defer srv.Close()
	got, err := (&HTTPImageInspector{}).DetectExposedPort(context.Background(), inspectRef(srv), nil)
	if err != nil || got != 0 {
		t.Fatalf("got %d, %v; want 0, nil", got, err)
	}
}

func TestParseImageRef(t *testing.T) {
	cases := []struct{ in, base, repo, ref string }{
		{"nginx:1.27", "https://registry-1.docker.io", "library/nginx", "1.27"},
		{"nginx", "https://registry-1.docker.io", "library/nginx", "latest"},
		{"ghcr.io/org/app:v1", "https://ghcr.io", "org/app", "v1"},
		{"localhost:30500/mortise/web@sha256:abc", "http://localhost:30500", "mortise/web", "sha256:abc"},
		{"docker.io/grafana/grafana:10", "https://registry-1.docker.io", "grafana/grafana", "10"},
	}
	for _, c := range cases {
		base, repo, ref := parseImageRef(c.in)
		if base != c.base || repo != c.repo || ref != c.ref {
			t.Errorf("parseImageRef(%q) = %q %q %q; want %q %q %q", c.in, base, repo, ref, c.base, c.repo, c.ref)
		}
	}
}
