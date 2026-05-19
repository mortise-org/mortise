package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestEnsureAppBuildRunCreatesAndClearsRebuildMarkers(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				rebuildRequestedAtAnnotation:        "req-1",
				rebuildNoCacheRequestedAtAnnotation: "req-2",
				"mortise.dev/git-token-owner":       "owner@example.com",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	run, err := r.ensureAppBuildRun(context.Background(), app, "production", "main", "abc1234567890", "registry/push:tag", "registry/pull:tag")
	if err != nil {
		t.Fatalf("ensure buildrun: %v", err)
	}
	if run.Name == "" {
		t.Fatal("expected buildrun name")
	}
	if run.Spec.Environment != "production" {
		t.Fatalf("expected environment production, got %q", run.Spec.Environment)
	}
	if run.Spec.Revision != "abc1234567890" {
		t.Fatalf("expected revision persisted, got %q", run.Spec.Revision)
	}
	if run.Spec.Branch != "main" {
		t.Fatalf("expected branch persisted, got %q", run.Spec.Branch)
	}
	if run.Spec.TokenSecretRef == nil {
		t.Fatal("expected token secret ref")
	}
	if len(run.OwnerReferences) != 1 || run.OwnerReferences[0].Kind != "App" || run.OwnerReferences[0].Name != app.Name {
		t.Fatalf("expected buildrun owner App/%s, got %+v", app.Name, run.OwnerReferences)
	}
	if got := app.Annotations[rebuildRequestedAtAnnotation]; got != "" {
		t.Fatalf("expected rebuild request marker cleared, got %q", got)
	}
	if got := app.Annotations[rebuildNoCacheRequestedAtAnnotation]; got != "" {
		t.Fatalf("expected no-cache rebuild marker cleared, got %q", got)
	}
}

func TestEnsureAppBuildRunDoesNotReuseAnotherEnvsCurrentRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/git-token-owner": "owner@example.com",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "production-run",
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name: "production",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "production-run",
						Phase: mortisev1alpha1.BuildRunPhaseRunning,
					},
				},
			},
		},
	}

	productionRun := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "production-run",
			Namespace: app.Namespace,
		},
		Spec: appBuildRunSpec(app, "production", "main", "abc1234567890", "registry/push:prod", "registry/pull:prod"),
		Status: mortisev1alpha1.BuildRunStatus{
			Phase: mortisev1alpha1.BuildRunPhaseRunning,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app, productionRun).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	run, err := r.ensureAppBuildRun(context.Background(), app, "staging", "main", "abc1234567890", "registry/push:staging", "registry/pull:staging")
	if err != nil {
		t.Fatalf("ensure buildrun: %v", err)
	}
	if run.Name == productionRun.Name {
		t.Fatalf("expected staging buildrun not to reuse production run %q", run.Name)
	}
	if run.Spec.Environment != "staging" {
		t.Fatalf("expected staging buildrun, got %q", run.Spec.Environment)
	}
}

func TestParseImageRefSplitsRegistryPathAndTag(t *testing.T) {
	ref := parseImageRef("registry.example.com/team/app:sha-prod")
	if ref.Registry != "registry.example.com" {
		t.Fatalf("registry = %q", ref.Registry)
	}
	if ref.Path != "team/app" {
		t.Fatalf("path = %q", ref.Path)
	}
	if ref.Tag != "sha-prod" {
		t.Fatalf("tag = %q", ref.Tag)
	}
}

func TestAppBuildRunSpecSeparatesPreviewBranchAndRevision(t *testing.T) {
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
		},
	}

	spec := appBuildRunSpec(app, "pr-12", "feature/preview-slash", "abcdef1234567890", "registry/push:tag", "registry/pull:tag")
	if spec.Branch != "feature/preview-slash" {
		t.Fatalf("branch = %q", spec.Branch)
	}
	if spec.Revision != "abcdef1234567890" {
		t.Fatalf("revision = %q", spec.Revision)
	}
}

func TestEnvImageTagSanitizesInvalidOCICharacters(t *testing.T) {
	if got := envImageTag("fix/stripe-payment-element", "pr-6"); got != "fix-str-pr-6" {
		t.Fatalf("envImageTag = %q", got)
	}
}

func TestBuildRunNamePreservesUniquenessSuffixForLongNames(t *testing.T) {
	targetName := strings.Repeat("demo-app-", 8)
	first := buildRunName("app", targetName, "production", "abcdef1234567890", "input-one", "request-one")
	second := buildRunName("app", targetName, "production", "abcdef1234567890", "input-two", "request-two")

	if len(first) > maxK8sNameLength {
		t.Fatalf("first buildrun name too long: %d", len(first))
	}
	if len(second) > maxK8sNameLength {
		t.Fatalf("second buildrun name too long: %d", len(second))
	}
	if first == second {
		t.Fatalf("expected distinct buildrun names, got %q", first)
	}
	if !strings.HasSuffix(first, "-"+shortHash("request-one")) {
		t.Fatalf("expected first buildrun name to preserve request suffix, got %q", first)
	}
	if !strings.HasSuffix(second, "-"+shortHash("request-two")) {
		t.Fatalf("expected second buildrun name to preserve request suffix, got %q", second)
	}
}

func TestBuildRunLogConfigMapNamePreservesUniquenessForLongRunNames(t *testing.T) {
	runOne := buildRunName("app", strings.Repeat("service-", 8), "production", "abcdef1234567890", "input-one", "request-one")
	runTwo := buildRunName("app", strings.Repeat("service-", 8), "production", "abcdef1234567890", "input-two", "request-two")

	logOne := buildRunLogConfigMapName(runOne)
	logTwo := buildRunLogConfigMapName(runTwo)

	if len(logOne) > maxK8sNameLength {
		t.Fatalf("first buildrun log configmap name too long: %d", len(logOne))
	}
	if len(logTwo) > maxK8sNameLength {
		t.Fatalf("second buildrun log configmap name too long: %d", len(logTwo))
	}
	if logOne == logTwo {
		t.Fatalf("expected distinct log configmap names, got %q", logOne)
	}
	if !strings.HasSuffix(logOne, "-"+shortHash(runOne)) {
		t.Fatalf("expected first configmap name to preserve run hash suffix, got %q", logOne)
	}
	if !strings.HasSuffix(logTwo, "-"+shortHash(runTwo)) {
		t.Fatalf("expected second configmap name to preserve run hash suffix, got %q", logTwo)
	}
}

func TestEnsureAppBuildRunCreatesDistinctRunsForManualRebuildRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				rebuildNoCacheRequestedAtAnnotation: "req-1",
				"mortise.dev/git-token-owner":       "owner@example.com",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	first, err := r.ensureAppBuildRun(context.Background(), app, "production", "main", "same-sha", "registry/push:tag", "registry/pull:tag")
	if err != nil {
		t.Fatalf("first ensure buildrun: %v", err)
	}
	if first.Spec.Trigger != mortisev1alpha1.BuildRunTriggerManual {
		t.Fatalf("expected first run trigger manual, got %q", first.Spec.Trigger)
	}

	app.Annotations[rebuildNoCacheRequestedAtAnnotation] = "req-2"
	second, err := r.ensureAppBuildRun(context.Background(), app, "production", "main", "same-sha", "registry/push:tag", "registry/pull:tag")
	if err != nil {
		t.Fatalf("second ensure buildrun: %v", err)
	}
	if second.Name == first.Name {
		t.Fatalf("expected unique buildrun name for second manual rebuild, got %q", second.Name)
	}
	if second.Spec.RequestID != "req-2" {
		t.Fatalf("expected second request id req-2, got %q", second.Spec.RequestID)
	}
	if !second.Spec.NoCache {
		t.Fatal("expected second manual rebuild to force no-cache")
	}
}

func TestEnsureAppBuildRunReusesCurrentManualRunAfterMarkersClear(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/git-token-owner": "owner@example.com",
			},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "manual-run",
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name: "production",
				CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  "manual-run",
					Phase: mortisev1alpha1.BuildRunPhaseRunning,
				},
			}},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
		},
	}
	manualApp := app.DeepCopy()
	manualApp.Annotations[rebuildRequestedAtAnnotation] = "req-1"
	manualApp.Annotations[rebuildNoCacheRequestedAtAnnotation] = "req-1"
	manualSpec := appBuildRunSpec(manualApp, "production", "main", "same-sha", "registry/push:tag", "registry/pull:tag")
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "manual-run",
			Namespace: app.Namespace,
		},
		Spec: manualSpec,
		Status: mortisev1alpha1.BuildRunStatus{
			Phase: mortisev1alpha1.BuildRunPhaseRunning,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app, run).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	got, err := r.ensureAppBuildRun(context.Background(), app, "production", "main", "same-sha", "registry/push:tag", "registry/pull:tag")
	if err != nil {
		t.Fatalf("ensure buildrun: %v", err)
	}
	if got.Name != run.Name {
		t.Fatalf("expected current manual buildrun %q, got %q", run.Name, got.Name)
	}

	var runs mortisev1alpha1.BuildRunList
	if err := c.List(context.Background(), &runs, client.InNamespace(app.Namespace)); err != nil {
		t.Fatalf("list buildruns: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected one buildrun, got %d", len(runs.Items))
	}
}

func TestEnsureAppBuildRunReusesCurrentTerminalRunBeforeStatusProjection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/git-token-owner": "owner@example.com",
			},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "manual-run",
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name: "production",
				CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  "manual-run",
					Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
				},
			}},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
		},
	}
	manualApp := app.DeepCopy()
	manualApp.Annotations[rebuildRequestedAtAnnotation] = "req-1"
	manualApp.Annotations[rebuildNoCacheRequestedAtAnnotation] = "req-1"
	manualSpec := appBuildRunSpec(manualApp, "production", "main", "same-sha", "registry/push:tag", "registry/pull:tag")
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "manual-run",
			Namespace: app.Namespace,
		},
		Spec: manualSpec,
		Status: mortisev1alpha1.BuildRunStatus{
			Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
			Image: "registry.example.com/demo:same-sha",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app, run).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	got, err := r.ensureAppBuildRun(context.Background(), app, "production", "main", "same-sha", "registry/push:tag", "registry/pull:tag")
	if err != nil {
		t.Fatalf("ensure buildrun: %v", err)
	}
	if got.Name != run.Name {
		t.Fatalf("expected current terminal buildrun %q, got %q", run.Name, got.Name)
	}

	var runs mortisev1alpha1.BuildRunList
	if err := c.List(context.Background(), &runs, client.InNamespace(app.Namespace)); err != nil {
		t.Fatalf("list buildruns: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected one buildrun, got %d", len(runs.Items))
	}
}

func TestEnsureAppBuildRunDoesNotReuseCurrentRunWhenInputHashChanges(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/git-token-owner": "owner@example.com",
			},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "current-run",
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name: "production",
				CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  "current-run",
					Phase: mortisev1alpha1.BuildRunPhaseRunning,
				},
			}},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", BuildArgs: map[string]string{"FOO": "old"}},
			},
		},
	}
	currentApp := app.DeepCopy()
	currentSpec := appBuildRunSpec(currentApp, "production", "main", "same-sha", "registry/push:tag", "registry/pull:tag")
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "current-run",
			Namespace: app.Namespace,
		},
		Spec: currentSpec,
		Status: mortisev1alpha1.BuildRunStatus{
			Phase: mortisev1alpha1.BuildRunPhaseRunning,
		},
	}

	app.Spec.Environments[0].BuildArgs["FOO"] = "new"

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app, run).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	got, err := r.ensureAppBuildRun(context.Background(), app, "production", "main", "same-sha", "registry/push:tag", "registry/pull:tag")
	if err != nil {
		t.Fatalf("ensure buildrun: %v", err)
	}
	if got.Name == run.Name {
		t.Fatalf("expected a fresh buildrun when input hash changes, reused %q", got.Name)
	}

	var runs mortisev1alpha1.BuildRunList
	if err := c.List(context.Background(), &runs, client.InNamespace(app.Namespace)); err != nil {
		t.Fatalf("list buildruns: %v", err)
	}
	if len(runs.Items) != 2 {
		t.Fatalf("expected two buildruns after input-hash change, got %d", len(runs.Items))
	}
}

func TestReconcileEnvBuildProjectsCurrentTerminalRunBeforeRevisionShortCircuit(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/revision": "same-sha",
			},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "manual-run",
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltSHA:   "same-sha",
					LastBuiltImage: "registry.example.com/demo:old",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "manual-run",
						Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
					},
				},
			},
		},
	}
	manualApp := app.DeepCopy()
	manualApp.Annotations[rebuildRequestedAtAnnotation] = "req-1"
	manualApp.Annotations[rebuildNoCacheRequestedAtAnnotation] = "req-1"
	manualSpec := appBuildRunSpec(manualApp, "production", "main", "same-sha", "registry.example.com/mortise/demo:same-sh-production", "registry.example.com/mortise/demo:same-sh-production")
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "manual-run",
			Namespace: app.Namespace,
		},
		Spec: manualSpec,
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:        mortisev1alpha1.BuildRunPhaseSucceeded,
			Image:        "registry.example.com/demo:new",
			Digest:       "sha256:new",
			DetectedPort: 8080,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	r := &AppReconciler{
		Client:          c,
		Scheme:          scheme,
		RegistryBackend: &fakeRegistryBackend{},
	}

	image, requeue, statusDirty, _, err := r.reconcileEnvBuild(context.Background(), app, []string{"production"}, "production", "main", "same-sha")
	if err != nil {
		t.Fatalf("reconcileEnvBuild: %v", err)
	}
	if requeue {
		t.Fatal("expected terminal current run to avoid requeue")
	}
	if !statusDirty {
		t.Fatal("expected status to be marked dirty")
	}
	if image != "registry.example.com/demo:new" {
		t.Fatalf("expected image from current terminal run, got %q", image)
	}
	if app.Status.LastBuildRunName != "manual-run" {
		t.Fatalf("expected last buildrun updated to manual run, got %q", app.Status.LastBuildRunName)
	}
	if app.Status.CurrentBuildRunName != "" {
		t.Fatalf("expected current buildrun cleared after success, got %q", app.Status.CurrentBuildRunName)
	}
	if es := envStatusFor(app, "production"); es == nil || es.LastBuiltImage != "registry.example.com/demo:new" {
		t.Fatalf("expected env status updated from manual run, got %+v", es)
	}
}

func TestReconcileEnvBuildProjectsFailedCurrentRunIntoStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/revision": "same-sha",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:   mortisev1alpha1.SourceTypeGit,
				Branch: "main",
			},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "manual-run",
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name: "pr-6",
				CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  "manual-run",
					Phase: mortisev1alpha1.BuildRunPhaseFailed,
				},
			}},
		},
	}
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "manual-run",
			Namespace: app.Namespace,
		},
		Spec: appBuildRunSpec(app, "pr-6", "feature/preview-fail", "same-sha", "registry.example.com/mortise/demo:same-sh-pr-6", "registry.example.com/mortise/demo:same-sh-pr-6"),
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:          mortisev1alpha1.BuildRunPhaseFailed,
			FailureReason:  "BuildFailed",
			FailureMessage: "invalid reference format",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app, run).WithStatusSubresource(app).Build()
	r := &AppReconciler{
		Client:          c,
		Scheme:          scheme,
		RegistryBackend: &fakeRegistryBackend{},
	}

	image, requeue, statusDirty, _, err := r.reconcileEnvBuild(context.Background(), app, []string{"pr-6"}, "pr-6", "feature/preview-fail", "same-sha")
	if err != nil {
		t.Fatalf("reconcileEnvBuild: %v", err)
	}
	if image != "" {
		t.Fatalf("expected no image on failed build, got %q", image)
	}
	if requeue {
		t.Fatal("expected failed current run to avoid requeue")
	}
	if !statusDirty {
		t.Fatal("expected failed buildrun projection to dirty status")
	}
	es := envStatusFor(app, "pr-6")
	if es == nil || es.CurrentBuildRunRef == nil || es.CurrentBuildRunRef.Phase != mortisev1alpha1.BuildRunPhaseFailed {
		t.Fatalf("expected failed buildrun ref to be projected into env status, got %+v", es)
	}
	cond := meta.FindStatusCondition(app.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected app build failure condition to be set, got %+v", cond)
	}
}

func TestReconcileEnvBuildDoesNotReuseAnotherEnvsCurrentRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/revision":        "same-sha",
				"mortise.dev/git-token-owner": "owner@example.com",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "production-run",
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name: "production",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "production-run",
						Phase: mortisev1alpha1.BuildRunPhaseRunning,
					},
				},
			},
		},
	}
	productionRun := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "production-run",
			Namespace: app.Namespace,
		},
		Spec: appBuildRunSpec(app, "production", "main", "same-sha", "registry.example.com/mortise/demo:same-sh-production", "registry.example.com/mortise/demo:same-sh-production"),
		Status: mortisev1alpha1.BuildRunStatus{
			Phase: mortisev1alpha1.BuildRunPhaseRunning,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app, productionRun).Build()
	r := &AppReconciler{
		Client:          c,
		Scheme:          scheme,
		RegistryBackend: &fakeRegistryBackend{},
	}

	image, requeue, statusDirty, _, err := r.reconcileEnvBuild(context.Background(), app, []string{"production", "staging"}, "staging", "main", "same-sha")
	if err != nil {
		t.Fatalf("reconcileEnvBuild: %v", err)
	}
	if image != "" {
		t.Fatalf("expected staging build to stay in flight, got %q", image)
	}
	if !requeue {
		t.Fatal("expected staging buildrun creation to requeue")
	}
	if !statusDirty {
		t.Fatal("expected status dirty after staging buildrun creation")
	}
	if app.Status.CurrentBuildRunName == "production-run" {
		t.Fatal("expected staging buildrun to replace app-level current name during projection")
	}
	stagingStatus := envStatusFor(app, "staging")
	if stagingStatus == nil || stagingStatus.CurrentBuildRunRef == nil || stagingStatus.CurrentBuildRunRef.Name == "production-run" {
		t.Fatalf("expected staging env to get its own buildrun ref, got %+v", stagingStatus)
	}
}

func TestReconcileEnvBuildDoesNotShortCircuitLastBuiltSHAWhenInputHashChanges(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/revision":        "same-sha",
				"mortise.dev/git-token-owner": "owner@example.com",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://example.com/repo.git",
				Branch:      "main",
				ProviderRef: "github",
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", BuildArgs: map[string]string{"FOO": "new"}},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltSHA:   "same-sha",
					LastBuiltImage: "registry.example.com/demo:old",
					LastSuccessfulBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name: "last-run",
					},
				},
			},
		},
	}
	lastApp := app.DeepCopy()
	lastApp.Spec.Environments[0].BuildArgs["FOO"] = "old"
	lastRun := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "last-run",
			Namespace: app.Namespace,
		},
		Spec: appBuildRunSpec(lastApp, "production", "main", "same-sha", "registry.example.com/mortise/demo:same-sh-production", "registry.example.com/mortise/demo:same-sh-production"),
		Status: mortisev1alpha1.BuildRunStatus{
			Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
			Image: "registry.example.com/demo:old",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(lastRun).Build()
	r := &AppReconciler{
		Client:          c,
		Scheme:          scheme,
		RegistryBackend: &fakeRegistryBackend{},
	}

	image, requeue, statusDirty, _, err := r.reconcileEnvBuild(context.Background(), app, []string{"production"}, "production", "main", "same-sha")
	if err != nil {
		t.Fatalf("reconcileEnvBuild: %v", err)
	}
	if image != "" {
		t.Fatalf("expected rebuild instead of reusing stale image, got %q", image)
	}
	if !requeue {
		t.Fatal("expected new buildrun to requeue")
	}
	if !statusDirty {
		t.Fatal("expected build start to dirty status")
	}
	if app.Status.CurrentBuildRunName == "" {
		t.Fatal("expected a fresh current buildrun name after input-hash change")
	}
	if app.Status.CurrentBuildRunName == "last-run" {
		t.Fatalf("expected a fresh buildrun, still pointing at %q", app.Status.CurrentBuildRunName)
	}
}

func TestProjectAppBuildRunStatusProjectsRefs(t *testing.T) {
	app := &mortisev1alpha1.App{}
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-1"},
		Status:     mortisev1alpha1.BuildRunStatus{Phase: mortisev1alpha1.BuildRunPhaseRunning},
	}

	projectAppBuildRunStatus(app, "production", run)

	es := envStatusFor(app, "production")
	if es == nil {
		t.Fatal("expected environment status to be created")
	}
	if app.Status.CurrentBuildRunName != "buildrun-1" {
		t.Fatalf("current buildrun name = %q", app.Status.CurrentBuildRunName)
	}
	if es.CurrentBuildRunRef == nil || es.CurrentBuildRunRef.Name != "buildrun-1" || es.CurrentBuildRunRef.Phase != mortisev1alpha1.BuildRunPhaseRunning {
		t.Fatalf("unexpected current buildrun ref: %+v", es.CurrentBuildRunRef)
	}
	if es.LastSuccessfulBuildRunRef != nil {
		t.Fatalf("expected no last successful ref while running, got %+v", es.LastSuccessfulBuildRunRef)
	}

	run.Status.Phase = mortisev1alpha1.BuildRunPhaseSucceeded
	projectAppBuildRunStatus(app, "production", run)

	if app.Status.CurrentBuildRunName != "" {
		t.Fatalf("expected current buildrun name cleared after success, got %q", app.Status.CurrentBuildRunName)
	}
	if app.Status.LastBuildRunName != "buildrun-1" {
		t.Fatalf("last buildrun name = %q", app.Status.LastBuildRunName)
	}
	if es.CurrentBuildRunRef == nil || es.CurrentBuildRunRef.Phase != mortisev1alpha1.BuildRunPhaseSucceeded {
		t.Fatalf("expected current buildrun ref to track terminal phase, got %+v", es.CurrentBuildRunRef)
	}
	if es.LastSuccessfulBuildRunRef == nil || es.LastSuccessfulBuildRunRef.Name != "buildrun-1" {
		t.Fatalf("expected last successful buildrun ref, got %+v", es.LastSuccessfulBuildRunRef)
	}
}

func TestApplyEnvBuildSuccessDoesNotOverwriteProjectedMetadataWithLaterEnv(t *testing.T) {
	app := &mortisev1alpha1.App{
		Status: mortisev1alpha1.AppStatus{
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltSHA:   "prod-sha",
					LastBuiltImage: "registry.example.com/demo:prod",
				},
				{
					Name: "preview",
				},
			},
			LastBuiltSHA:   "prod-sha",
			LastBuiltImage: "registry.example.com/demo:prod",
			DetectedPort:   8081,
		},
	}

	r := &AppReconciler{}
	r.applyEnvBuildSuccess(context.Background(), app, []string{"production", "preview"}, "preview", "preview-sha", "registry.example.com/demo:preview", "sha256:preview", 3000)

	if app.Status.LastBuiltSHA != "prod-sha" {
		t.Fatalf("expected projected SHA to stay on production, got %q", app.Status.LastBuiltSHA)
	}
	if app.Status.LastBuiltImage != "registry.example.com/demo:prod" {
		t.Fatalf("expected projected image to stay on production, got %q", app.Status.LastBuiltImage)
	}
	if app.Status.DetectedPort != 8081 {
		t.Fatalf("expected projected port to stay on production, got %d", app.Status.DetectedPort)
	}
	previewStatus := envStatusFor(app, "preview")
	if previewStatus == nil || previewStatus.LastBuiltImage != "registry.example.com/demo:preview" {
		t.Fatalf("expected preview env build recorded, got %+v", previewStatus)
	}
}

func TestPersistBuildRunLogKeepsLegacyAppConfigMap(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			UID:       types.UID("app-uid"),
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	fakeClock := clocktesting.NewFakeClock(time.Date(2026, 5, 10, 18, 45, 0, 123456789, time.UTC))
	r := &BuildRunReconciler{Client: c, Scheme: scheme, Clock: fakeClock}

	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-1",
			Namespace: app.Namespace,
			UID:       types.UID("run-uid"),
		},
		Spec: mortisev1alpha1.BuildRunSpec{
			AppName: "demo",
			TargetRef: mortisev1alpha1.BuildRunTargetRef{
				Kind: mortisev1alpha1.BuildRunTargetAppEnvironment,
				Name: "demo",
			},
			Revision: "abcdef1234",
		},
	}
	tracker := &buildTracker{
		phase: buildPhaseSucceeded,
		logs:  []string{"step 1", "step 2"},
	}

	if _, err := r.persistBuildRunLog(context.Background(), run, tracker); err != nil {
		t.Fatalf("persist buildrun log: %v", err)
	}

	var durable corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Name: buildRunLogConfigMapName(run.Name), Namespace: app.Namespace}, &durable); err != nil {
		t.Fatalf("get durable configmap: %v", err)
	}
	if durable.OwnerReferences[0].Kind != "BuildRun" {
		t.Fatalf("expected durable log owner BuildRun, got %+v", durable.OwnerReferences)
	}
	if got := durable.Annotations[buildLogAnnotationTimestamp]; got != fakeClock.Now().UTC().Format(time.RFC3339Nano) {
		t.Fatalf("expected durable log timestamp %q, got %q", fakeClock.Now().UTC().Format(time.RFC3339Nano), got)
	}

	var legacy corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Name: buildLogsConfigMapName(app.Name), Namespace: app.Namespace}, &legacy); err != nil {
		t.Fatalf("get legacy configmap: %v", err)
	}
	if legacy.OwnerReferences[0].Kind != "App" || legacy.OwnerReferences[0].Name != app.Name {
		t.Fatalf("expected legacy log owner App/%s, got %+v", app.Name, legacy.OwnerReferences)
	}
}
