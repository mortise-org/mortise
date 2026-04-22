/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = mortisev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// App controller tests create Apps in the `default` namespace for
	// convenience. Now that Apps must belong to a parent Project, seed a
	// synthetic Project (with the standard production + staging envs) and
	// wire it to the default namespace via the `mortise.dev/project` label.
	seedDefaultProject(ctx)
})

// withStagingEnv adds `staging` to the default-project's environments. Tests
// that need multi-env reconcile call this first and pair it with
// withoutStagingEnv in a deferred cleanup so they don't leak state.
func withStagingEnv(ctx context.Context) {
	var proj mortisev1alpha1.Project
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "default-project"}, &proj)).To(Succeed())
	for _, e := range proj.Spec.Environments {
		if e.Name == "staging" {
			ensureNamespace(ctx, "pj-default-project-staging")
			return
		}
	}
	proj.Spec.Environments = append(proj.Spec.Environments,
		mortisev1alpha1.ProjectEnvironment{Name: "staging", DisplayOrder: 1})
	Expect(k8sClient.Update(ctx, &proj)).To(Succeed())
	ensureNamespace(ctx, "pj-default-project-staging")
}

func withoutStagingEnv(ctx context.Context) {
	var proj mortisev1alpha1.Project
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "default-project"}, &proj)).To(Succeed())
	kept := proj.Spec.Environments[:0]
	for _, e := range proj.Spec.Environments {
		if e.Name != "staging" {
			kept = append(kept, e)
		}
	}
	proj.Spec.Environments = kept
	Expect(k8sClient.Update(ctx, &proj)).To(Succeed())

	var ns corev1.Namespace
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "pj-default-project-staging"}, &ns); err == nil {
		_ = k8sClient.Delete(ctx, &ns)
	}
}

// seedDefaultProject creates the Project record that parents Apps living in
// the `default` namespace, and labels that namespace so `fetchParentProject`
// resolves via the override path.
func seedDefaultProject(ctx context.Context) {
	proj := &mortisev1alpha1.Project{}
	proj.Name = "default-project"
	proj.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{
		{Name: "production"},
	}
	Expect(k8sClient.Create(ctx, proj)).To(Succeed())

	ensureNamespace(ctx, "pj-default-project")
	ensureNamespace(ctx, "pj-default-project-production")

	var controlNs corev1.Namespace
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "pj-default-project"}, &controlNs)).To(Succeed())
	if controlNs.Labels == nil {
		controlNs.Labels = map[string]string{}
	}
	controlNs.Labels["mortise.dev/project"] = proj.Name
	Expect(k8sClient.Update(ctx, &controlNs)).To(Succeed())

	var ns corev1.Namespace
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "default"}, &ns)).To(Succeed())
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	ns.Labels["mortise.dev/project"] = proj.Name
	Expect(k8sClient.Update(ctx, &ns)).To(Succeed())
}

func ensureNamespace(ctx context.Context, name string) {
	var ns corev1.Namespace
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, &ns)
	if err == nil {
		return
	}
	ns = corev1.Namespace{}
	ns.Name = name
	Expect(k8sClient.Create(ctx, &ns)).To(Succeed())
}

// purgeAllAppsIn clears every App in the given namespace, removing finalizers
// if necessary. Called from AfterEach so tests don't need to individually
// reconcile-until-gone; the App finalizer otherwise leaves objects pending and
// blocks the next test's Create(appName).
func purgeAllAppsIn(ctx context.Context, namespace string) {
	var list mortisev1alpha1.AppList
	if err := k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return
	}
	for i := range list.Items {
		app := &list.Items[i]
		if app.DeletionTimestamp.IsZero() {
			_ = k8sClient.Delete(ctx, app)
		}
		var fresh mortisev1alpha1.App
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(app), &fresh); err != nil {
			continue
		}
		if len(fresh.Finalizers) > 0 {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, &fresh)
		}
	}
	Eventually(func() bool {
		var cur mortisev1alpha1.AppList
		if err := k8sClient.List(ctx, &cur, client.InNamespace(namespace)); err != nil {
			return false
		}
		return len(cur.Items) == 0
	}, time.Second*5, time.Millisecond*50).Should(BeTrue())
}

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Eventually(func() error {
		return testEnv.Stop()
	}, time.Minute, time.Second).Should(Succeed())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
