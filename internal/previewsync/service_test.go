package previewsync

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/git"
)

type fakeStore struct {
	items   []mortisev1alpha1.PreviewEnvironment
	created []mortisev1alpha1.PreviewEnvironment
	updated []mortisev1alpha1.PreviewEnvironment
	deleted []mortisev1alpha1.PreviewEnvironment
}

func (f *fakeStore) ListPreviewEnvironments(_ context.Context, _ string) ([]mortisev1alpha1.PreviewEnvironment, error) {
	return append([]mortisev1alpha1.PreviewEnvironment(nil), f.items...), nil
}

func (f *fakeStore) CreatePreviewEnvironment(_ context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	f.created = append(f.created, *pe)
	return nil
}

func (f *fakeStore) UpdatePreviewEnvironment(_ context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	f.updated = append(f.updated, *pe)
	return nil
}

func (f *fakeStore) DeletePreviewEnvironment(_ context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	f.deleted = append(f.deleted, *pe)
	return nil
}

func TestReconcileAppPreviews_FullReconcile(t *testing.T) {
	store := &fakeStore{
		items: []mortisev1alpha1.PreviewEnvironment{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "web-preview-pr-1", Namespace: "pj-demo"},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					AppRef: "web",
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: 1,
						Branch: "old",
						SHA:    "oldsha",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "web-preview-pr-2", Namespace: "pj-demo"},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					AppRef: "web",
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: 2,
						Branch: "stale",
						SHA:    "stalesha",
					},
				},
			},
		},
	}
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "pj-demo"},
		Spec: mortisev1alpha1.AppSpec{
			Environments: []mortisev1alpha1.Environment{{Name: "staging"}},
		},
	}
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "production"}, {Name: "staging"}},
			Preview: &mortisev1alpha1.PreviewConfig{
				Enabled: true,
				Domain:  "pr-{number}-{app}.{project}.example.com",
				TTL:     "24h",
			},
		},
	}
	openPRs := []git.PullRequestSnapshot{
		{Number: 1, Branch: "feature-a", SHA: "sha-a"},
		{Number: 3, Branch: "feature-c", SHA: "sha-c"},
	}

	if err := ReconcileAppPreviews(context.Background(), store, app, project, project.Spec.Preview, openPRs); err != nil {
		t.Fatalf("ReconcileAppPreviews: %v", err)
	}

	if len(store.updated) != 1 || store.updated[0].Spec.PullRequest.Number != 1 || store.updated[0].Spec.PullRequest.SHA != "sha-a" {
		t.Fatalf("expected PR #1 update, got %+v", store.updated)
	}
	if len(store.created) != 1 || store.created[0].Spec.PullRequest.Number != 3 {
		t.Fatalf("expected PR #3 create, got %+v", store.created)
	}
	if store.created[0].Spec.Domain != "pr-3-web.demo.example.com" {
		t.Fatalf("unexpected created domain: %q", store.created[0].Spec.Domain)
	}
	if store.created[0].Spec.TTL.Duration != 24*time.Hour {
		t.Fatalf("unexpected created ttl: %s", store.created[0].Spec.TTL.Duration)
	}
	if len(store.deleted) != 1 || store.deleted[0].Spec.PullRequest.Number != 2 {
		t.Fatalf("expected PR #2 delete, got %+v", store.deleted)
	}
}

func TestReconcileAppPreviews_FiltersBotPRsByDefault(t *testing.T) {
	store := &fakeStore{}
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "pj-demo"},
	}
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
			Preview:      &mortisev1alpha1.PreviewConfig{Enabled: true},
		},
	}
	openPRs := []git.PullRequestSnapshot{
		{
			Number: 5,
			Branch: "deps/update",
			SHA:    "botsha",
			Author: git.PullRequestAuthor{Login: "dependabot[bot]", Type: "Bot", IsBot: true},
		},
	}

	if err := ReconcileAppPreviews(context.Background(), store, app, project, project.Spec.Preview, openPRs); err != nil {
		t.Fatalf("ReconcileAppPreviews: %v", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("expected bot PR to be skipped, got creates %+v", store.created)
	}
}
