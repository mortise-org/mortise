//go:build integration

package integration

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
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

// observerLocalPort is the local port for the shared observer port-forward.
// observerAvailable is false when the observer is not reachable.
var (
	observerLocalPort int
	observerAvailable bool
	observerCancel    context.CancelFunc
)

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

	// Soft check: try to port-forward to the observer and verify /healthz.
	observerLocalPort, observerAvailable, observerCancel = probeObserver()

	code := m.Run()
	if observerCancel != nil {
		observerCancel()
	}
	os.Exit(code)
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
	deadline := time.Now().Add(90 * time.Second)
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

func probeObserver() (int, bool, context.CancelFunc) {
	// Check that the observer Deployment exists and is available.
	deadline := time.Now().Add(60 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		var dep appsv1.Deployment
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      "mortise-observer",
			Namespace: "mortise-deps",
		}, &dep)
		if err == nil && dep.Status.AvailableReplicas > 0 {
			found = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !found {
		log.Println("observer: Deployment not available — observer tests will be skipped")
		return 0, false, nil
	}

	// Pick a free port and start a port-forward.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("observer: failed to pick free port: %v — observer tests will be skipped", err)
		return 0, false, nil
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "kubectl",
		"-n", "mortise-deps",
		"port-forward",
		"svc/mortise-observer",
		fmt.Sprintf("%d:9091", port),
	)
	if err := cmd.Start(); err != nil {
		cancel()
		log.Printf("observer: port-forward start failed: %v — observer tests will be skipped", err)
		return 0, false, nil
	}

	// Wait for port to accept connections.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	connDeadline := time.Now().Add(30 * time.Second)
	connected := false
	for time.Now().Before(connDeadline) {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			c.Close()
			connected = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !connected {
		cancel()
		_ = cmd.Wait()
		log.Println("observer: port-forward not reachable — observer tests will be skipped")
		return 0, false, nil
	}

	// Hit /healthz.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil || resp.StatusCode != 200 {
		cancel()
		_ = cmd.Wait()
		log.Printf("observer: /healthz check failed — observer tests will be skipped")
		return 0, false, nil
	}
	resp.Body.Close()

	log.Printf("observer: healthy on localhost:%d", port)
	return port, true, cancel
}

func skipIfObserverUnavailable(t *testing.T) {
	t.Helper()
	if !observerAvailable {
		t.Skip("observer not available")
	}
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
