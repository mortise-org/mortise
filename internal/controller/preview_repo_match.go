package controller

import (
	"fmt"
	"strings"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

func previewTargetsAppRepo(pe *mortisev1alpha1.PreviewEnvironment, app *mortisev1alpha1.App) bool {
	if pe == nil || app == nil {
		return false
	}
	if app.Spec.Source.Type != mortisev1alpha1.SourceTypeGit || app.Spec.Source.Repo == "" {
		return false
	}

	appRepo := constants.CanonicalRepoKey(app.Spec.Source.Repo)
	prRepo := constants.CanonicalRepoKey(pe.Spec.PullRequest.Repo)
	if prRepo != "" {
		return prRepo == appRepo
	}

	legacySingleRepoName := fmt.Sprintf("preview-pr-%d", pe.Spec.PullRequest.Number)
	if pe.Name == legacySingleRepoName {
		return true
	}

	if strings.HasPrefix(pe.Name, "preview-") && strings.Contains(pe.Name, "-pr-") {
		return strings.HasPrefix(pe.Name, constants.PreviewEnvironmentPrefix(app.Spec.Source.Repo, true))
	}

	return true
}
