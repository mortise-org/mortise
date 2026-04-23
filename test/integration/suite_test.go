//go:build integration

package integration

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/test/helpers"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// k8sClient is the package-level client shared by all integration tests.
var k8sClient client.Client

func TestMain(m *testing.M) {
	cfg := loadKubeconfig()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		log.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		log.Fatalf("failed to add appsv1 to scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		log.Fatalf("failed to add batchv1 to scheme: %v", err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		log.Fatalf("failed to add networkingv1 to scheme: %v", err)
	}
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		log.Fatalf("failed to add mortise scheme: %v", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("failed to create k8s client: %v", err)
	}
	k8sClient = c

	// Assert cluster is reachable by listing nodes.
	var nodes corev1.NodeList
	if err := k8sClient.List(context.Background(), &nodes); err != nil {
		log.Fatalf("cluster not reachable (list nodes failed: %v). "+
			"Run `make dev-up` or `make test-integration` first.", err)
	}
	if len(nodes.Items) == 0 {
		log.Fatal("cluster has no nodes. Run `make dev-up` or `make test-integration` first.")
	}

	// Assert the Mortise manager Deployment is available.
	assertMortiseReady()

	os.Exit(m.Run())
}

func loadKubeconfig() *rest.Config {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("cannot determine home directory: %v", err)
		}
		kubeconfig = home + "/.kube/config"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("failed to load kubeconfig from %s: %v", kubeconfig, err)
	}
	return cfg
}

func assertMortiseReady() {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var dep appsv1.Deployment
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      "mortise",
			Namespace: "mortise-system",
		}, &dep)
		if err == nil && dep.Status.AvailableReplicas > 0 {
			return
		}
		time.Sleep(2 * time.Second)
	}
	log.Fatal("mortise Deployment in mortise-system is not available after 60s. " +
		"Run `make dev-up` or `make test-integration` to install the chart first.")
}

func createProjectForTest(t *testing.T, name string) string {
	t.Helper()
	return helpers.CreateTestProject(t, k8sClient, name)
}

func createTestNamespace(t *testing.T) string {
	t.Helper()
	return helpers.CreateTestNamespace(t, k8sClient)
}

func randSuffix() string {
	return helpers.RandSuffix()
}

func fixturesDir() string {
	return helpers.FixturesDir()
}
