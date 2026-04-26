package api

import (
	"context"
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
