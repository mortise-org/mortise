package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/git"
)

const (
	buildRunTargetKindLabel             = "mortise.dev/buildrun-target-kind"
	buildRunTargetNameLabel             = "mortise.dev/buildrun-target-name"
	buildRunEnvironmentLabel            = "mortise.dev/buildrun-environment"
	rebuildRequestedAtAnnotation        = "mortise.dev/rebuild-requested-at"
	rebuildNoCacheRequestedAtAnnotation = "mortise.dev/rebuild-no-cache-requested-at"
	maxK8sNameLength                    = 63
)

func buildRunName(kind, targetName, environment, revision, inputHash, requestID string) string {
	base := fmt.Sprintf("%s-%s-%s-%s", strings.ToLower(kind), targetName, environment, shortTag(revision))
	suffix := shortHash(inputHash)
	if requestID != "" {
		suffix = shortHash(requestID)
	}
	return sanitizeK8sNameWithSuffix(base, suffix, "buildrun")
}

func buildRunLogConfigMapName(runName string) string {
	return sanitizeK8sNameWithSuffix("buildrun-"+runName, shortHash(runName), "buildrun")
}

func shortHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:10]
}

func sanitizeK8sName(v string) string {
	v = sanitizeK8sNamePart(v)
	if len(v) > maxK8sNameLength {
		v = v[:maxK8sNameLength]
	}
	v = strings.Trim(v, "-")
	if v == "" {
		return "buildrun"
	}
	return v
}

func sanitizeK8sNameWithSuffix(prefix, suffix, fallback string) string {
	suffix = sanitizeK8sNamePart(suffix)
	if suffix == "" {
		return sanitizeK8sName(prefix)
	}

	prefix = sanitizeK8sNamePart(prefix)
	maxPrefixLen := maxK8sNameLength - len(suffix) - 1
	if maxPrefixLen <= 0 {
		return suffix[:maxK8sNameLength]
	}

	if len(prefix) > maxPrefixLen {
		prefix = strings.Trim(prefix[:maxPrefixLen], "-")
	}
	if prefix == "" {
		prefix = sanitizeK8sNamePart(fallback)
		if len(prefix) > maxPrefixLen {
			prefix = strings.Trim(prefix[:maxPrefixLen], "-")
		}
	}
	if prefix == "" {
		return suffix
	}
	return prefix + "-" + suffix
}

func sanitizeK8sNamePart(v string) string {
	v = strings.ToLower(v)
	repl := strings.NewReplacer("/", "-", "_", "-", ".", "-", ":", "-", "@", "-")
	v = repl.Replace(v)
	return strings.Trim(v, "-")
}

func buildRunInputHash(spec mortisev1alpha1.BuildRunSpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "target-kind=%s\ntarget-name=%s\ntarget-namespace=%s\nenv=%s\ntrigger=%s\nrepo=%s\nbranch=%s\nrevision=%s\nsource-path=%s\nmode=%s\ndockerfile=%s\ncontext=%s\npush=%s\npull=%s\nnocache=%t\n",
		spec.TargetRef.Kind,
		spec.TargetRef.Name,
		spec.TargetRef.Namespace,
		spec.Environment,
		spec.Trigger,
		spec.Repo,
		spec.Branch,
		spec.Revision,
		spec.SourcePath,
		spec.BuildMode,
		spec.DockerfilePath,
		spec.BuildContext,
		spec.PushTarget,
		spec.PullTarget,
		spec.NoCache,
	)
	keys := make([]string, 0, len(spec.BuildArgs))
	for k := range spec.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "arg:%s=%s\n", k, spec.BuildArgs[k])
	}
	if spec.TokenSecretRef != nil {
		fmt.Fprintf(h, "token-secret=%s/%s:%s\n", spec.TokenSecretRef.Namespace, spec.TokenSecretRef.Name, spec.TokenSecretRef.Key)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildRunSelectionHash(spec mortisev1alpha1.BuildRunSpec) string {
	spec.Trigger = ""
	spec.RequestID = ""
	spec.NoCache = false
	spec.InputHash = ""
	return buildRunInputHash(spec)
}

func buildRunLabels(run *mortisev1alpha1.BuildRun) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "mortise",
		buildRunTargetKindLabel:        strings.ToLower(run.Spec.TargetRef.Kind),
		buildRunTargetNameLabel:        run.Spec.TargetRef.Name,
		buildRunEnvironmentLabel:       run.Spec.Environment,
	}
}

func buildRunReference(run *mortisev1alpha1.BuildRun) *mortisev1alpha1.BuildRunReference {
	if run == nil {
		return nil
	}
	return &mortisev1alpha1.BuildRunReference{
		Name:  run.Name,
		Phase: run.Status.Phase,
	}
}

func ensureEnvStatus(app *mortisev1alpha1.App, envName string) *mortisev1alpha1.EnvironmentStatus {
	if app == nil {
		return nil
	}
	if es := envStatusFor(app, envName); es != nil {
		return es
	}
	app.Status.Environments = append(app.Status.Environments, mortisev1alpha1.EnvironmentStatus{Name: envName})
	return &app.Status.Environments[len(app.Status.Environments)-1]
}

func projectAppBuildRunStatus(app *mortisev1alpha1.App, envName string, run *mortisev1alpha1.BuildRun) {
	if app == nil || run == nil {
		return
	}

	es := ensureEnvStatus(app, envName)
	ref := buildRunReference(run)
	if es != nil {
		es.CurrentBuildRunRef = ref
	}

	app.Status.CurrentBuildRunName = run.Name
	switch run.Status.Phase {
	case mortisev1alpha1.BuildRunPhaseSucceeded:
		app.Status.CurrentBuildRunName = ""
		app.Status.LastBuildRunName = run.Name
		if es != nil {
			es.LastSuccessfulBuildRunRef = ref
		}
	case mortisev1alpha1.BuildRunPhaseFailed:
		app.Status.CurrentBuildRunName = ""
		app.Status.LastBuildRunName = run.Name
	}
}

func currentBuildRunNameForEnv(app *mortisev1alpha1.App, envName string) string {
	es := envStatusFor(app, envName)
	if es == nil || es.CurrentBuildRunRef == nil {
		return ""
	}
	return es.CurrentBuildRunRef.Name
}

func aggregateAppBuildRunNames(envs []mortisev1alpha1.EnvironmentStatus) (current, last string) {
	for _, es := range envs {
		if es.CurrentBuildRunRef == nil {
			continue
		}
		switch es.CurrentBuildRunRef.Phase {
		case mortisev1alpha1.BuildRunPhasePending, mortisev1alpha1.BuildRunPhaseRunning:
			if current == "" {
				current = es.CurrentBuildRunRef.Name
			}
		case mortisev1alpha1.BuildRunPhaseSucceeded, mortisev1alpha1.BuildRunPhaseFailed:
			if last == "" {
				last = es.CurrentBuildRunRef.Name
			}
		}
	}
	if last == "" {
		for _, es := range envs {
			if es.LastSuccessfulBuildRunRef != nil && es.LastSuccessfulBuildRunRef.Name != "" {
				last = es.LastSuccessfulBuildRunRef.Name
				break
			}
		}
	}
	return current, last
}

func buildRunTokenSecretRef(providerName, email string) *mortisev1alpha1.SecretRef {
	if providerName == "" || email == "" {
		return nil
	}
	return &mortisev1alpha1.SecretRef{
		Namespace: git.TokenSecretNamespace,
		Name:      git.UserTokenSecretName(providerName, email),
		Key:       "token",
	}
}

func appBuildRunSpec(app *mortisev1alpha1.App, envName, branch, revision, pushTarget, pullTarget string) mortisev1alpha1.BuildRunSpec {
	noCache := false
	requestID := app.Annotations[rebuildRequestedAtAnnotation]
	if requestID == "" {
		requestID = app.Annotations[rebuildNoCacheRequestedAtAnnotation]
	}
	if app.Annotations[rebuildNoCacheRequestedAtAnnotation] != "" || app.Annotations["mortise.dev/no-cache-build"] == "true" {
		noCache = true
	}
	trigger := mortisev1alpha1.BuildRunTriggerAuto
	if requestID != "" || app.Annotations["mortise.dev/no-cache-build"] == "true" {
		trigger = mortisev1alpha1.BuildRunTriggerManual
	}
	spec := mortisev1alpha1.BuildRunSpec{
		AppName: app.Name,
		TargetRef: mortisev1alpha1.BuildRunTargetRef{
			Kind:      mortisev1alpha1.BuildRunTargetAppEnvironment,
			Name:      app.Name,
			Namespace: app.Namespace,
		},
		Environment:    envName,
		Trigger:        trigger,
		RequestID:      requestID,
		ProviderRef:    app.Spec.Source.ProviderRef,
		CreatedBy:      app.Annotations["mortise.dev/created-by"],
		TokenOwner:     app.Annotations["mortise.dev/git-token-owner"],
		Repo:           app.Spec.Source.Repo,
		Branch:         firstNonEmpty(branch, "main"),
		Revision:       revision,
		SourcePath:     app.Spec.Source.Path,
		Path:           app.Spec.Source.Path,
		BuildMode:      string(buildModeOf(app)),
		DockerfilePath: dockerfilePath(app),
		Dockerfile:     dockerfilePath(app),
		BuildContext:   buildContextOf(app),
		BuildArgs:      buildArgsForEnv(app, envName),
		PushTarget:     pushTarget,
		PushImage:      pushTarget,
		PullTarget:     pullTarget,
		PullImage:      pullTarget,
		NoCache:        noCache,
		TokenSecretRef: buildRunTokenSecretRef(app.Spec.Source.ProviderRef, app.Annotations["mortise.dev/git-token-owner"]),
	}
	spec.InputHash = buildRunInputHash(spec)
	return spec
}

func buildModeOf(app *mortisev1alpha1.App) mortisev1alpha1.BuildMode {
	if app.Spec.Source.Build != nil && app.Spec.Source.Build.Mode != "" {
		return app.Spec.Source.Build.Mode
	}
	return mortisev1alpha1.BuildModeAuto
}

func buildRunMatchesAppSpec(run *mortisev1alpha1.BuildRun, app *mortisev1alpha1.App, envName string, spec mortisev1alpha1.BuildRunSpec) bool {
	if run == nil || app == nil {
		return false
	}
	return run.Spec.TargetRef.Kind == mortisev1alpha1.BuildRunTargetAppEnvironment &&
		run.Spec.TargetRef.Name == app.Name &&
		run.Spec.Environment == envName &&
		run.Spec.Revision == spec.Revision &&
		buildRunSelectionHash(run.Spec) == buildRunSelectionHash(spec)
}

func (r *AppReconciler) ensureAppBuildRun(ctx context.Context, app *mortisev1alpha1.App, envName, branch, revision, pushTarget, pullTarget string) (*mortisev1alpha1.BuildRun, error) {
	spec := appBuildRunSpec(app, envName, branch, revision, pushTarget, pullTarget)
	if !hasPendingRebuildRequest(app) {
		currentRunName := currentBuildRunNameForEnv(app, envName)
		if currentRunName != "" {
			var current mortisev1alpha1.BuildRun
			if err := r.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: currentRunName}, &current); err == nil {
				if buildRunMatchesAppSpec(&current, app, envName, spec) {
					return &current, nil
				}
			} else if !errors.IsNotFound(err) {
				return nil, err
			}
		}
	}
	name := buildRunName("app", app.Name, envName, revision, spec.InputHash, spec.RequestID)
	var run mortisev1alpha1.BuildRun
	err := r.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: name}, &run)
	if err == nil {
		return &run, nil
	}
	if !errors.IsNotFound(err) {
		return nil, err
	}
	run = mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
		},
		Spec: spec,
	}
	if err := controllerutil.SetControllerReference(app, &run, r.Scheme); err != nil {
		return nil, err
	}
	for k, v := range appLabels(app, envName) {
		if run.Labels == nil {
			run.Labels = map[string]string{}
		}
		run.Labels[k] = v
	}
	for k, v := range buildRunLabels(&run) {
		if run.Labels == nil {
			run.Labels = map[string]string{}
		}
		run.Labels[k] = v
	}
	if err := r.Create(ctx, &run); err != nil {
		if errors.IsAlreadyExists(err) {
			if err := r.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: name}, &run); err != nil {
				return nil, err
			}
			return &run, nil
		}
		return nil, err
	}
	// Rebuild request markers are deliberately NOT cleared here: the env loop
	// reuses one *App across environments, so clearing after the first env's
	// Create would make envs 2..N short-circuit and skip the rebuild. The
	// caller clears the markers once after every env has consumed them
	// (reconcileEnvBuild's shouldClearNoCache return).
	return &run, nil
}

func clearRebuildRequestMarkers(app *mortisev1alpha1.App) {
	if app.Annotations == nil {
		return
	}
	delete(app.Annotations, rebuildRequestedAtAnnotation)
	delete(app.Annotations, rebuildNoCacheRequestedAtAnnotation)
	delete(app.Annotations, "mortise.dev/no-cache-build")
}

func hasPendingRebuildRequest(app *mortisev1alpha1.App) bool {
	if app == nil || app.Annotations == nil {
		return false
	}
	return app.Annotations[rebuildRequestedAtAnnotation] != "" ||
		app.Annotations[rebuildNoCacheRequestedAtAnnotation] != "" ||
		app.Annotations["mortise.dev/no-cache-build"] == "true"
}

func clearNoCacheBuildAnnotation(ctx context.Context, c client.Client, app *mortisev1alpha1.App) error {
	patch := client.MergeFrom(app.DeepCopy())
	clearRebuildRequestMarkers(app)
	return c.Patch(ctx, app, patch)
}

func isBuildRunTerminal(br *mortisev1alpha1.BuildRun) bool {
	return br.Status.Phase == mortisev1alpha1.BuildRunPhaseSucceeded || br.Status.Phase == mortisev1alpha1.BuildRunPhaseFailed
}

func buildRunTrackerKey(br *mortisev1alpha1.BuildRun) types.NamespacedName {
	return types.NamespacedName{Namespace: br.Namespace, Name: br.Name}
}

func setBuildRunCondition(conditions *[]metav1.Condition, phase mortisev1alpha1.BuildRunPhase, generation int64, message string, now metav1.Time) {
	status := metav1.ConditionFalse
	reason := string(phase)
	if phase == mortisev1alpha1.BuildRunPhaseSucceeded {
		status = metav1.ConditionTrue
		reason = "BuildSucceeded"
	} else if phase == mortisev1alpha1.BuildRunPhaseRunning {
		reason = "BuildRunning"
	}
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               "Completed",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
		LastTransitionTime: now,
	})
}
