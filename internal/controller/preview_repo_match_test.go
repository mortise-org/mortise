package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

func TestPreviewTargetsAppRepo(t *testing.T) {
	t.Run("explicit repo matches canonical repo", func(t *testing.T) {
		pe := &mortisev1alpha1.PreviewEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: "preview-pr-6"},
			Spec: mortisev1alpha1.PreviewEnvironmentSpec{
				PullRequest: mortisev1alpha1.PullRequestRef{
					Number: 6,
					Repo:   "git@github.com:example/repo.git",
				},
			},
		}
		app := &mortisev1alpha1.App{
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type: mortisev1alpha1.SourceTypeGit,
					Repo: "https://github.com/example/repo.git",
				},
			},
		}
		if !previewTargetsAppRepo(pe, app) {
			t.Fatal("expected canonical repo forms to match")
		}
	})

	t.Run("explicit repo mismatch is rejected", func(t *testing.T) {
		pe := &mortisev1alpha1.PreviewEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: "preview-pr-6"},
			Spec: mortisev1alpha1.PreviewEnvironmentSpec{
				PullRequest: mortisev1alpha1.PullRequestRef{
					Number: 6,
					Repo:   "https://github.com/example/repo-a.git",
				},
			},
		}
		app := &mortisev1alpha1.App{
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type: mortisev1alpha1.SourceTypeGit,
					Repo: "https://github.com/example/repo-b.git",
				},
			},
		}
		if previewTargetsAppRepo(pe, app) {
			t.Fatal("expected foreign repo preview to not apply")
		}
	})

	t.Run("legacy single-repo PE still applies without repo field", func(t *testing.T) {
		pe := &mortisev1alpha1.PreviewEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: "preview-pr-6"},
			Spec: mortisev1alpha1.PreviewEnvironmentSpec{
				PullRequest: mortisev1alpha1.PullRequestRef{Number: 6},
			},
		}
		app := &mortisev1alpha1.App{
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type: mortisev1alpha1.SourceTypeGit,
					Repo: "https://github.com/example/repo.git",
				},
			},
		}
		if !previewTargetsAppRepo(pe, app) {
			t.Fatal("expected legacy unqualified PE name to apply")
		}
	})

	t.Run("legacy multi-repo PE name only applies to matching repo prefix", func(t *testing.T) {
		repoA := "https://github.com/example/repo-a.git"
		pe := &mortisev1alpha1.PreviewEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name: constants.PreviewEnvironmentName(repoA, 6, true),
			},
			Spec: mortisev1alpha1.PreviewEnvironmentSpec{
				PullRequest: mortisev1alpha1.PullRequestRef{Number: 6},
			},
		}
		matching := &mortisev1alpha1.App{
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type: mortisev1alpha1.SourceTypeGit,
					Repo: repoA,
				},
			},
		}
		foreign := &mortisev1alpha1.App{
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type: mortisev1alpha1.SourceTypeGit,
					Repo: "https://github.com/example/repo-b.git",
				},
			},
		}
		if !previewTargetsAppRepo(pe, matching) {
			t.Fatal("expected legacy repo-qualified PE name to match its repo")
		}
		if previewTargetsAppRepo(pe, foreign) {
			t.Fatal("expected legacy repo-qualified PE name to reject foreign repo")
		}
	})
}
