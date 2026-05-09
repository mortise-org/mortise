package api

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestNormalizeRepoURL(t *testing.T) {
	if err := mortisev1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}

	gitea := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "gitea-main"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitea,
			Host: "https://gitea.internal/",
		},
	}

	withProvider := &Server{client: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(gitea).Build()}
	noProvider := &Server{client: fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()}

	cases := []struct {
		name        string
		s           *Server
		providerRef string
		repo        string
		want        string
	}{
		{
			name: "full https URL unchanged",
			s:    noProvider, repo: "https://github.com/owner/repo.git",
			want: "https://github.com/owner/repo.git",
		},
		{
			name: "full http URL unchanged",
			s:    noProvider, repo: "http://gitea.local/owner/repo",
			want: "http://gitea.local/owner/repo",
		},
		{
			name: "short form defaults to github",
			s:    noProvider, repo: "owner/repo",
			want: "https://github.com/owner/repo.git",
		},
		{
			name: "short form already has .git",
			s:    noProvider, repo: "owner/repo.git",
			want: "https://github.com/owner/repo.git",
		},
		{
			name: "short form with named providerRef uses provider host",
			s:    withProvider, providerRef: "gitea-main", repo: "owner/repo",
			want: "https://gitea.internal/owner/repo.git",
		},
		{
			name: "short form with no providerRef uses first provider in cluster",
			s:    withProvider, repo: "owner/repo",
			want: "https://gitea.internal/owner/repo.git",
		},
		{
			name: "provider host trailing slash is stripped",
			s:    withProvider, providerRef: "gitea-main", repo: "owner/repo.git",
			want: "https://gitea.internal/owner/repo.git",
		},
		{
			name: "missing named provider falls back to github",
			s:    noProvider, providerRef: "does-not-exist", repo: "owner/repo",
			want: "https://github.com/owner/repo.git",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.s.normalizeRepoURL(context.Background(), "pj-test", tc.providerRef, tc.repo)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCreateAppRequestUnmarshalJSON(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantName   string
		wantType   mortisev1alpha1.SourceType
		wantImage  string
		wantRepo   string
		wantEnvLen int
	}{
		{
			name:      "wrapped format with image source",
			input:     `{"name":"my-app","spec":{"source":{"type":"image","image":"nginx:1.27"},"environments":[{"name":"production"}]}}`,
			wantName:  "my-app",
			wantType:  mortisev1alpha1.SourceTypeImage,
			wantImage: "nginx:1.27",
		},
		{
			name:     "wrapped format with git source",
			input:    `{"name":"my-app","spec":{"source":{"type":"git","repo":"https://github.com/org/repo.git"},"environments":[{"name":"production"}]}}`,
			wantName: "my-app",
			wantType: mortisev1alpha1.SourceTypeGit,
			wantRepo: "https://github.com/org/repo.git",
		},
		{
			name:      "flat format with image source",
			input:     `{"name":"my-app","source":{"type":"image","image":"nginx:1.27"},"environments":[{"name":"production"}]}`,
			wantName:  "my-app",
			wantType:  mortisev1alpha1.SourceTypeImage,
			wantImage: "nginx:1.27",
		},
		{
			name:     "flat format with git source",
			input:    `{"name":"my-app","source":{"type":"git","repo":"https://github.com/org/repo.git"},"environments":[{"name":"staging"}]}`,
			wantName: "my-app",
			wantType: mortisev1alpha1.SourceTypeGit,
			wantRepo: "https://github.com/org/repo.git",
		},
		{
			name:       "flat format preserves environments",
			input:      `{"name":"my-app","source":{"type":"image","image":"nginx:1.27"},"environments":[{"name":"production"},{"name":"staging"}]}`,
			wantName:   "my-app",
			wantType:   mortisev1alpha1.SourceTypeImage,
			wantImage:  "nginx:1.27",
			wantEnvLen: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req createAppRequest
			if err := json.Unmarshal([]byte(tc.input), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if req.Name != tc.wantName {
				t.Errorf("name: got %q, want %q", req.Name, tc.wantName)
			}
			if req.Spec.Source.Type != tc.wantType {
				t.Errorf("source.type: got %q, want %q", req.Spec.Source.Type, tc.wantType)
			}
			if tc.wantImage != "" && req.Spec.Source.Image != tc.wantImage {
				t.Errorf("source.image: got %q, want %q", req.Spec.Source.Image, tc.wantImage)
			}
			if tc.wantRepo != "" && req.Spec.Source.Repo != tc.wantRepo {
				t.Errorf("source.repo: got %q, want %q", req.Spec.Source.Repo, tc.wantRepo)
			}
			if tc.wantEnvLen > 0 && len(req.Spec.Environments) != tc.wantEnvLen {
				t.Errorf("environments length: got %d, want %d", len(req.Spec.Environments), tc.wantEnvLen)
			}
		})
	}
}

func TestCreateAppRequestUnmarshalJSON_InvalidJSON(t *testing.T) {
	var req createAppRequest
	if err := json.Unmarshal([]byte(`{not json}`), &req); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestCreateAppRequestUnmarshalJSON_NoSource(t *testing.T) {
	var req createAppRequest
	if err := json.Unmarshal([]byte(`{"name":"x"}`), &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Name != "x" {
		t.Errorf("name: got %q, want %q", req.Name, "x")
	}
	if req.Spec.Source.Type != "" {
		t.Errorf("source.type should be empty, got %q", req.Spec.Source.Type)
	}
}

func TestCreateAppRequestUnmarshalJSON_WrappedTakesPrecedence(t *testing.T) {
	// When both spec.source and top-level source are present, wrapped wins.
	input := `{"name":"x","spec":{"source":{"type":"image","image":"nginx:1.27"}},"source":{"type":"git","repo":"https://github.com/o/r.git"}}`
	var req createAppRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Spec.Source.Type != mortisev1alpha1.SourceTypeImage {
		t.Errorf("wrapped format should take precedence, got source.type %q", req.Spec.Source.Type)
	}
	if req.Spec.Source.Image != "nginx:1.27" {
		t.Errorf("source.image: got %q, want %q", req.Spec.Source.Image, "nginx:1.27")
	}
}
