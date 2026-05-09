package previewsync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/git"
)

const defaultPreviewTTL = 72 * time.Hour

// Store is the k8s CRUD surface needed to reconcile PreviewEnvironment CRs.
type Store interface {
	ListPreviewEnvironments(ctx context.Context, namespace string) ([]mortisev1alpha1.PreviewEnvironment, error)
	CreatePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
	UpdatePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
	DeletePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
}

// ReconcileAppPreviews converges one App's PreviewEnvironment CRs onto the
// provided open PR snapshot list using full reconcile semantics.
func ReconcileAppPreviews(
	ctx context.Context,
	store Store,
	app *mortisev1alpha1.App,
	project *mortisev1alpha1.Project,
	preview *mortisev1alpha1.PreviewConfig,
	openPRs []git.PullRequestSnapshot,
) error {
	if app == nil || project == nil || preview == nil {
		return nil
	}

	sourceEnv := ResolveSourceEnv(project)
	if sourceEnv == "" {
		return nil
	}

	existing, err := store.ListPreviewEnvironments(ctx, app.Namespace)
	if err != nil {
		return err
	}

	desired := make(map[int]mortisev1alpha1.PreviewEnvironment, len(openPRs))
	for _, pr := range openPRs {
		if pr.Number == 0 {
			continue
		}
		if !preview.BotPR && pr.Author.IsBot {
			continue
		}
		desired[pr.Number] = desiredPreviewEnvironment(app, project, preview, sourceEnv, pr)
	}

	existingByPR := make(map[int]*mortisev1alpha1.PreviewEnvironment)
	for i := range existing {
		pe := &existing[i]
		if pe.Spec.AppRef != app.Name {
			continue
		}
		existingByPR[pe.Spec.PullRequest.Number] = pe
	}

	for number, pe := range desired {
		current := existingByPR[number]
		if current == nil {
			copy := pe
			if err := store.CreatePreviewEnvironment(ctx, &copy); err != nil {
				return err
			}
			continue
		}
		if equality.Semantic.DeepEqual(current.Spec, pe.Spec) {
			continue
		}
		updated := current.DeepCopy()
		updated.Spec = pe.Spec
		if err := store.UpdatePreviewEnvironment(ctx, updated); err != nil {
			return err
		}
	}

	for number, pe := range existingByPR {
		if _, ok := desired[number]; ok {
			continue
		}
		if err := store.DeletePreviewEnvironment(ctx, pe); err != nil {
			return err
		}
	}

	return nil
}

func desiredPreviewEnvironment(
	app *mortisev1alpha1.App,
	project *mortisev1alpha1.Project,
	preview *mortisev1alpha1.PreviewConfig,
	sourceEnv string,
	pr git.PullRequestSnapshot,
) mortisev1alpha1.PreviewEnvironment {
	sourceEnvOverride := FindAppEnv(app, sourceEnv)
	ttl := resolveTTL(preview)
	domain := ResolveDomainTemplate(preview.Domain, app.Name, project.Name, pr.Number)

	pe := mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PreviewEnvName(app.Name, pr.Number),
			Namespace: app.Namespace,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			AppRef:    app.Name,
			SourceEnv: sourceEnv,
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: pr.Number,
				Branch: pr.Branch,
				SHA:    pr.SHA,
			},
			Domain: domain,
			TTL:    metav1.Duration{Duration: ttl},
		},
	}

	if sourceEnvOverride != nil {
		pe.Spec.Replicas = sourceEnvOverride.Replicas
		pe.Spec.Resources = sourceEnvOverride.Resources
		pe.Spec.Env = sourceEnvOverride.Env
		pe.Spec.Bindings = sourceEnvOverride.Bindings
	}

	if preview.Resources.CPU != "" || preview.Resources.Memory != "" {
		pe.Spec.Resources = preview.Resources
	}

	return pe
}

func resolveTTL(preview *mortisev1alpha1.PreviewConfig) time.Duration {
	if preview == nil || preview.TTL == "" {
		return defaultPreviewTTL
	}
	ttl, err := time.ParseDuration(preview.TTL)
	if err != nil {
		return defaultPreviewTTL
	}
	return ttl
}

func PreviewEnvName(appName string, prNumber int) string {
	return fmt.Sprintf("%s-preview-pr-%d", appName, prNumber)
}

func ResolveDomainTemplate(template, appName, projectName string, prNumber int) string {
	if template == "" {
		return ""
	}
	result := strings.ReplaceAll(template, "{number}", fmt.Sprintf("%d", prNumber))
	result = strings.ReplaceAll(result, "{app}", appName)
	result = strings.ReplaceAll(result, "{project}", projectName)
	return result
}

func FindAppEnv(app *mortisev1alpha1.App, envName string) *mortisev1alpha1.Environment {
	for i := range app.Spec.Environments {
		if app.Spec.Environments[i].Name == envName {
			return &app.Spec.Environments[i]
		}
	}
	return nil
}

func ResolveSourceEnv(project *mortisev1alpha1.Project) string {
	if project == nil {
		return ""
	}
	var firstNonProd string
	for _, env := range project.Spec.Environments {
		if env.Name == "staging" {
			return "staging"
		}
		if env.Name != "production" && firstNonProd == "" {
			firstNonProd = env.Name
		}
	}
	return firstNonProd
}
