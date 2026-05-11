//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestGitSourceManualRebuildCreatesFreshBuildRunForSameSHA(t *testing.T) {
	t.Parallel()
	projectName := "git-rebuild-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"

	repoName := "repo-" + projectName
	boot := (&helpers.GiteaBootstrap{
		BaseURL:  giteaLocalURL,
		Username: "mortise-test",
		Password: "mortise-test-pw",
	}).Ensure(t, giteaInClusterURL, "mortise-test", repoName,
		map[string]string{
			"Dockerfile": testDockerfile,
			"README.md":  testReadme,
		},
	)

	providerName := "gitea-rebuild"
	providerName = providerName + "-" + randSuffix()
	webhookSecretName := "gitea-rebuild-webhook-" + randSuffix()
	stubSecret(t, "mortise-system", webhookSecretName, map[string]string{"secret": "stub"})
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "user-"+providerName+"-token-74657374406578616d706c652e636f6d", map[string]string{
		"token": boot.Token,
	})

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: providerName},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     mortisev1alpha1.GitProviderTypeGitea,
			Host:     giteaInClusterURL,
			ClientID: "test-client-id",
			WebhookSecretRef: &mortisev1alpha1.SecretRef{
				Namespace: "mortise-system", Name: webhookSecretName, Key: "secret",
			},
		},
	}
	if err := k8sClient.Create(context.Background(), gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
		})
	})

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")
	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "git-gitea-basic.yaml"))
	app.Name = "rebuild-app-" + randSuffix()
	app.Namespace = ns
	app.Spec.Source.Repo = boot.CloneURL
	app.Spec.Source.ProviderRef = providerName
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	app.Annotations["mortise.dev/revision"] = "main"
	app.Annotations["mortise.dev/created-by"] = testEmail

	ctx := context.Background()
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	firstRun, err := latestAppBuildRun(ctx, ns, app.Name)
	if err != nil {
		t.Fatalf("get first buildrun: %v", err)
	}
	if firstRun == nil {
		t.Fatal("expected first buildrun")
	}

	var currentApp mortisev1alpha1.App
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: app.Name}, &currentApp); err != nil {
		t.Fatalf("get App after first build: %v", err)
	}
	requestID := time.Now().UTC().Format(time.RFC3339Nano)
	base := currentApp.DeepCopy()
	if currentApp.Annotations == nil {
		currentApp.Annotations = map[string]string{}
	}
	currentApp.Annotations["mortise.dev/rebuild-requested-at"] = requestID
	currentApp.Annotations["mortise.dev/rebuild-no-cache-requested-at"] = requestID
	if err := k8sClient.Patch(ctx, &currentApp, client.MergeFrom(base)); err != nil {
		t.Fatalf("patch rebuild annotations: %v", err)
	}

	secondRun, err := waitForNewAppBuildRun(ctx, ns, app.Name, firstRun.Name, 5*time.Minute)
	if err != nil {
		t.Fatalf("wait for second buildrun: %v", err)
	}
	if secondRun.Spec.RequestID != requestID {
		t.Fatalf("expected second buildrun requestID %q, got %q", requestID, secondRun.Spec.RequestID)
	}
	if !secondRun.Spec.NoCache {
		t.Fatal("expected manual rebuild to set noCache")
	}
	if secondRun.Spec.Revision != firstRun.Spec.Revision {
		t.Fatalf("expected same revision rebuild, got first=%q second=%q", firstRun.Spec.Revision, secondRun.Spec.Revision)
	}

	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: app.Name}, &currentApp); err != nil {
			t.Fatalf("get App after rebuild: %v", err)
		}
		if currentApp.Status.LastBuildRunName == secondRun.Name && currentApp.Status.CurrentBuildRunName == "" {
			break
		}
		if time.Now().After(deadline) {
			var runs mortisev1alpha1.BuildRunList
			if err := k8sClient.List(ctx, &runs,
				client.InNamespace(ns),
				client.MatchingLabels{
					"mortise.dev/buildrun-target-kind": "appenvironment",
					"mortise.dev/buildrun-target-name": app.Name,
				},
			); err == nil {
				for i := range runs.Items {
					t.Logf("buildrun[%d]: name=%s phase=%s requestID=%q noCache=%t revision=%s current=%t last=%t",
						i,
						runs.Items[i].Name,
						runs.Items[i].Status.Phase,
						runs.Items[i].Spec.RequestID,
						runs.Items[i].Spec.NoCache,
						runs.Items[i].Spec.Revision,
						currentApp.Status.CurrentBuildRunName == runs.Items[i].Name,
						currentApp.Status.LastBuildRunName == runs.Items[i].Name,
					)
				}
			}
			t.Fatalf("expected app last buildrun %q, got current=%q last=%q", secondRun.Name, currentApp.Status.CurrentBuildRunName, currentApp.Status.LastBuildRunName)
		}
		time.Sleep(2 * time.Second)
	}

	time.Sleep(5 * time.Second)
	latestRun, err := latestAppBuildRun(ctx, ns, app.Name)
	if err != nil {
		t.Fatalf("get latest buildrun after status convergence: %v", err)
	}
	if latestRun == nil || latestRun.Name != secondRun.Name {
		t.Fatalf("expected no extra buildrun after marker clear, latest=%v second=%s", latestRun, secondRun.Name)
	}
}

func latestAppBuildRun(ctx context.Context, namespace, appName string) (*mortisev1alpha1.BuildRun, error) {
	var runs mortisev1alpha1.BuildRunList
	if err := k8sClient.List(ctx, &runs,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"mortise.dev/buildrun-target-kind": "appenvironment",
			"mortise.dev/buildrun-target-name": appName,
		},
	); err != nil {
		return nil, err
	}
	if len(runs.Items) == 0 {
		return nil, nil
	}
	var latest *mortisev1alpha1.BuildRun
	for i := range runs.Items {
		run := &runs.Items[i]
		if latest == nil || run.CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = run
		}
	}
	return latest, nil
}

func waitForNewAppBuildRun(ctx context.Context, namespace, appName, previousRunName string, timeout time.Duration) (*mortisev1alpha1.BuildRun, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, err := latestAppBuildRun(ctx, namespace, appName)
		if err != nil {
			return nil, err
		}
		if run != nil && run.Name != previousRunName && run.Status.Phase == mortisev1alpha1.BuildRunPhaseSucceeded {
			return run, nil
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("timed out waiting for new buildrun for %s", appName)
}
