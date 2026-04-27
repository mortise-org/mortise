//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

// Dockerfile + app content pushed to the in-cluster Gitea. Kept tiny so the
// build completes in a handful of seconds against the in-cluster BuildKit.
const (
	testDockerfile = `FROM alpine:3.20
RUN echo "mortise integration test" > /hello.txt
EXPOSE 8080
CMD ["sh", "-c", "while true; do echo ok | nc -l -p 8080; done"]
`
	testReadme = "This repository is created by Mortise integration tests.\n"
)

// TestGitSourceAppBuildsAndDeploys exercises the full git-source build path
// end-to-end against the in-cluster Gitea, Zot, and BuildKit installed by
// test/integration/manifests/. On success:
//
//  1. A Project is created (provisioning the pj-* control namespace).
//  2. A repo is provisioned in Gitea with a minimal Dockerfile.
//  3. A GitProvider CRD is created pointing at Gitea, with the admin token
//     pre-populated in gitprovider-token-{name} (bypassing the OAuth flow —
//     integration tests don't own the user's browser).
//  4. An App is created with source.type=git referencing the test repo.
//  5. The App's status.phase progresses through Building → Deploying → Ready.
//  6. Zot's /v2/mortise/{app}/tags/list reports the built tag.
//  7. The Deployment is running with the pushed image.
func TestGitSourceAppBuildsAndDeploys(t *testing.T) {
	t.Parallel()
	projectName := "git-src-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	// --- Port-forward to in-cluster Gitea + registry from the test host.
	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	registryLocalPort := helpers.PortForward(t, "mortise-test-deps", "registry", 5000)

	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	registryLocalURL := fmt.Sprintf("http://127.0.0.1:%d", registryLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"

	// --- Bootstrap a repo in Gitea with a tiny Dockerfile. We derive the
	// repo name from the project name so concurrent tests don't collide.
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

	// --- Provide the webhook secret and per-user token the GitProvider CRD
	//     schema requires. Content is irrelevant — the test talks to Gitea
	//     with the pre-populated admin token, not OAuth-issued tokens.
	providerName := "gitea-int"
	stubSecret(t, "mortise-system", "gitea-webhook-stub", map[string]string{
		"secret": "stub",
	})
	// Pre-populate the per-user token secret the App controller reads via
	// git.ResolveGitToken. Bypasses the OAuth flow for tests.
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "user-"+providerName+"-token-74657374406578616d706c652e636f6d", map[string]string{
		"token": boot.Token,
	})

	// --- Create the GitProvider CRD pointing at in-cluster Gitea.
	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: providerName},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     mortisev1alpha1.GitProviderTypeGitea,
			Host:     giteaInClusterURL,
			ClientID: "test-client-id",
			WebhookSecretRef: &mortisev1alpha1.SecretRef{
				Namespace: "mortise-system", Name: "gitea-webhook-stub", Key: "secret",
			},
		},
	}
	// GitProvider is cluster-scoped; delete on test cleanup so re-runs start clean.
	if err := k8sClient.Create(context.Background(), gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
		})
	})

	// --- Load the fixture and patch the repo URL + trigger annotation.
	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "git-gitea-basic.yaml"))
	app.Namespace = ns
	app.Spec.Source.Repo = boot.CloneURL
	app.Spec.Source.ProviderRef = providerName
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	// "main" is the branch; reconcileGitSource falls back to using it as the
	// pseudo-revision when no SHA has been resolved yet.
	app.Annotations["mortise.dev/revision"] = "main"
	app.Annotations["mortise.dev/created-by"] = testEmail

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	// --- Wait for build → deploy → ready. Generous timeout: first run pulls
	// alpine + go-git clone + BuildKit layer push all on a cold cluster.
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 3*time.Minute)

	// --- Assert the registry has a tag under mortise/<appName>.
	tags := helpers.AssertRegistryHasTags(t, registryLocalURL, "mortise", app.Name, 30*time.Second)
	if len(tags) == 0 {
		t.Fatalf("no tags found in registry for %s", app.Name)
	}
	t.Logf("registry tags for %s: %v", app.Name, tags)

	// --- Assert the Deployment is running the built image (registry host in image ref).
	// The Deployment lives in the env namespace, not the control namespace.
	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)
	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: envNs, Name: app.Name,
	}, &dep); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	if got := dep.Spec.Template.Spec.Containers[0].Image; got == "" {
		t.Fatalf("deployment image is empty")
	} else {
		t.Logf("deployment image: %s", got)
	}
	if dep.Status.ReadyReplicas < 1 {
		t.Fatalf("deployment has %d ready replicas, want >=1", dep.Status.ReadyReplicas)
	}
}

// stubSecret creates (or replaces) an Opaque secret for the test and registers
// cleanup. Cluster-wide, outside the per-test namespace, because the
// GitProvider CRD resolves secrets by namespace/name.
func stubSecret(t *testing.T, ns, name string, data map[string]string) {
	t.Helper()
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		StringData: data,
	}
	ctx := context.Background()
	// Delete-then-create keeps re-runs deterministic; Apps don't own these.
	_ = k8sClient.Delete(ctx, s)
	if err := k8sClient.Create(ctx, s); err != nil {
		t.Fatalf("create secret %s/%s: %v", ns, name, err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		})
	})
}
