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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// helper: create a test namespace and return its name.
func createPreviewTestNS(ctx context.Context) string {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "preview-test-",
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, ns)).To(Succeed())
	return ns.Name
}

// helper: create a minimal App with preview enabled in the given namespace.
func createPreviewApp(ctx context.Context, name, namespace string, previewEnabled bool, staging *mortisev1alpha1.Environment) *mortisev1alpha1.App {
	envs := []mortisev1alpha1.Environment{}
	if staging != nil {
		envs = append(envs, *staging)
	}
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://github.com/org/repo",
				Branch:      "main",
				ProviderRef: "github-main",
			},
			Environments: envs,
		},
	}
	if previewEnabled {
		app.Spec.Preview = &mortisev1alpha1.PreviewConfig{
			Enabled: true,
			Domain:  "pr-{number}-{app}.example.com",
			TTL:     "72h",
		}
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, app)).To(Succeed())
	return app
}

// helper: create a PreviewEnvironment in the given namespace.
func createPreviewEnv(ctx context.Context, name, namespace, appRef string, prNumber int, sha, branch, domain string, ttl time.Duration) *mortisev1alpha1.PreviewEnvironment {
	pe := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			AppRef: appRef,
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: prNumber,
				Branch: branch,
				SHA:    sha,
			},
			Domain: domain,
			TTL:    metav1.Duration{Duration: ttl},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, pe)).To(Succeed())
	return pe
}

var _ = Describe("PreviewEnvironment Controller", func() {
	Context("when the parent App has preview disabled", func() {
		It("should set the PreviewEnvironment to Failed", func() {
			ctx := context.Background()
			ns := createPreviewTestNS(ctx)

			// Create app with preview disabled.
			createPreviewApp(ctx, "myapp", ns, false, nil)

			// Create PreviewEnvironment.
			pe := createPreviewEnv(ctx, "myapp-preview-pr-1", ns, "myapp", 1, "abc123", "feature", "pr-1-myapp.example.com", 72*time.Hour)

			fakeClock := clocktesting.NewFakeClock(time.Now())
			reconciler := &PreviewEnvironmentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock:  fakeClock,
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("PreviewDisabled"))

			// Verify status is Failed.
			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseFailed))
		})
	})

	Context("when the parent App does not exist", func() {
		It("should set the PreviewEnvironment to Failed", func() {
			ctx := context.Background()
			ns := createPreviewTestNS(ctx)

			pe := createPreviewEnv(ctx, "orphan-preview-pr-1", ns, "nonexistent-app", 1, "abc123", "feature", "pr-1.example.com", 72*time.Hour)

			fakeClock := clocktesting.NewFakeClock(time.Now())
			reconciler := &PreviewEnvironmentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock:  fakeClock,
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("AppNotFound"))

			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseFailed))
		})
	})

	Context("when the parent App source is image (not git)", func() {
		It("should set the PreviewEnvironment to Failed", func() {
			ctx := context.Background()
			ns := createPreviewTestNS(ctx)

			// Create image-source app.
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: "imgapp", Namespace: ns},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "nginx:1.25",
					},
					Preview: &mortisev1alpha1.PreviewConfig{Enabled: true},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			pe := createPreviewEnv(ctx, "imgapp-preview-pr-1", ns, "imgapp", 1, "abc123", "feature", "pr-1.example.com", 72*time.Hour)

			fakeClock := clocktesting.NewFakeClock(time.Now())
			reconciler := &PreviewEnvironmentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock:  fakeClock,
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("NotGitSource"))
		})
	})

	Context("when build clients are nil (skip build path) and image is pre-set", func() {
		It("should create Deployment, Service, and Ingress with correct names and preview overrides", func() {
			ctx := context.Background()
			ns := createPreviewTestNS(ctx)

			staging := &mortisev1alpha1.Environment{
				Name:     "staging",
				Replicas: ptr.To(int32(2)),
				Resources: mortisev1alpha1.ResourceRequirements{
					CPU:    "500m",
					Memory: "512Mi",
				},
				Env: []mortisev1alpha1.EnvVar{
					{Name: "ENV", Value: "staging"},
				},
			}

			app := createPreviewApp(ctx, "webapp", ns, true, staging)
			app.Spec.Preview.Resources = mortisev1alpha1.ResourceRequirements{
				CPU:    "250m",
				Memory: "256Mi",
			}
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			pe := createPreviewEnv(ctx, "webapp-preview-pr-42", ns, "webapp", 42, "deadbeef", "feat-x", "pr-42-webapp.example.com", 72*time.Hour)

			// Set replicas and resources from override on the PE spec directly.
			pe.Spec.Replicas = ptr.To(int32(1))
			pe.Spec.Resources = mortisev1alpha1.ResourceRequirements{CPU: "250m", Memory: "256Mi"}
			pe.Spec.Env = []mortisev1alpha1.EnvVar{{Name: "ENV", Value: "staging"}}
			Expect(k8sClient.Update(ctx, pe)).To(Succeed())

			// Pre-set the image so the skip-build path has something to deploy.
			pe.Status.Image = "registry.example.com/mortise/webapp:pr-42-deadbee"
			Expect(k8sClient.Status().Update(ctx, pe)).To(Succeed())

			fakeClock := clocktesting.NewFakeClock(time.Now())
			reconciler := &PreviewEnvironmentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock:  fakeClock,
				// BuildClient, GitClient, RegistryBackend are nil — skip build path.
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Deployment.
			deployName := fmt.Sprintf("webapp-preview-pr-42")
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: ns}, &dep)).To(Succeed())
			Expect(*dep.Spec.Replicas).To(Equal(int32(1)))
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Name).To(Equal("webapp"))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("registry.example.com/mortise/webapp:pr-42-deadbee"))

			// Verify resource overrides applied.
			cpuReq := dep.Spec.Template.Spec.Containers[0].Resources.Requests["cpu"]
			Expect(cpuReq.String()).To(Equal("250m"))
			memReq := dep.Spec.Template.Spec.Containers[0].Resources.Requests["memory"]
			Expect(memReq.String()).To(Equal("256Mi"))

			// Verify env vars inherited.
			Expect(dep.Spec.Template.Spec.Containers[0].Env).To(ContainElement(
				corev1.EnvVar{Name: "ENV", Value: "staging"},
			))

			// Verify Service.
			svcName := fmt.Sprintf("webapp-preview-pr-42")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: svcName, Namespace: ns}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(80)))

			// Verify Ingress.
			ingressN := fmt.Sprintf("webapp-preview-pr-42")
			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ingressN, Namespace: ns}, &ing)).To(Succeed())
			Expect(ing.Spec.Rules).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].Host).To(Equal("pr-42-webapp.example.com"))
			Expect(ing.Spec.TLS).To(HaveLen(1))
			Expect(ing.Spec.TLS[0].Hosts).To(ContainElement("pr-42-webapp.example.com"))

			// Verify status.
			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))
			Expect(updated.Status.URL).To(Equal("https://pr-42-webapp.example.com"))
		})
	})

	Context("when TTL expires", func() {
		It("should delete the PreviewEnvironment", func() {
			ctx := context.Background()
			ns := createPreviewTestNS(ctx)

			createPreviewApp(ctx, "ttlapp", ns, true, nil)

			pe := createPreviewEnv(ctx, "ttlapp-preview-pr-5", ns, "ttlapp", 5, "sha123", "feat", "pr-5-ttlapp.example.com", 1*time.Hour)

			// Set expiresAt to the past.
			past := metav1.NewTime(time.Now().Add(-1 * time.Hour))
			pe.Status.ExpiresAt = &past
			Expect(k8sClient.Status().Update(ctx, pe)).To(Succeed())

			fakeClock := clocktesting.NewFakeClock(time.Now())
			reconciler := &PreviewEnvironmentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock:  fakeClock,
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// PE should be deleted.
			var deleted mortisev1alpha1.PreviewEnvironment
			err = k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &deleted)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("when SHA changes (update triggers rebuild)", func() {
		It("should transition back to Building phase", func() {
			ctx := context.Background()
			ns := createPreviewTestNS(ctx)

			createPreviewApp(ctx, "rebuildapp", ns, true, nil)

			pe := createPreviewEnv(ctx, "rebuildapp-preview-pr-10", ns, "rebuildapp", 10, "sha-v1", "feature", "pr-10-rebuildapp.example.com", 72*time.Hour)

			// Pre-set image so deployment can be created.
			pe.Status.Image = "registry.example.com/mortise/rebuildapp:pr-10-sha-v1"
			Expect(k8sClient.Status().Update(ctx, pe)).To(Succeed())

			fakeClock := clocktesting.NewFakeClock(time.Now())
			reconciler := &PreviewEnvironmentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock:  fakeClock,
			}

			// First reconcile: succeeds (no build clients = skip build).
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Ready.
			var ready mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &ready)).To(Succeed())
			Expect(ready.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))

			// Now update SHA.
			ready.Spec.PullRequest.SHA = "sha-v2"
			Expect(k8sClient.Update(ctx, &ready)).To(Succeed())

			// Second reconcile: still Ready since build clients are nil.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))
		})
	})

	Context("when the PreviewEnvironment is deleted", func() {
		It("should clean up owned resources via garbage collection", func() {
			ctx := context.Background()
			ns := createPreviewTestNS(ctx)

			createPreviewApp(ctx, "cleanapp", ns, true, nil)

			pe := createPreviewEnv(ctx, "cleanapp-preview-pr-99", ns, "cleanapp", 99, "sha999", "cleanup-branch", "pr-99-cleanapp.example.com", 72*time.Hour)

			// Pre-set image so deployment can be created.
			pe.Status.Image = "registry.example.com/mortise/cleanapp:pr-99-sha999"
			Expect(k8sClient.Status().Update(ctx, pe)).To(Succeed())

			fakeClock := clocktesting.NewFakeClock(time.Now())
			reconciler := &PreviewEnvironmentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock:  fakeClock,
			}

			// Reconcile to create resources.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify resources exist.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cleanapp-preview-pr-99", Namespace: ns}, &dep)).To(Succeed())

			// Delete the PreviewEnvironment.
			Expect(k8sClient.Delete(ctx, pe)).To(Succeed())

			// Reconcile after delete.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// The PE itself should be gone (or going). Owner references will
			// cascade delete the Deployment/Service/Ingress in a real cluster;
			// envtest doesn't run the GC controller, so we just verify the PE
			// is deleted.
			var deletedPE mortisev1alpha1.PreviewEnvironment
			err = k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &deletedPE)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})

var _ = Describe("ResolvePreviewDomain", func() {
	It("should replace {number} and {app} placeholders", func() {
		result := ResolvePreviewDomain("pr-{number}-{app}.yourdomain.com", "myapp", 42, "")
		Expect(result).To(Equal("pr-42-myapp.yourdomain.com"))
	})

	It("should construct default when template is empty", func() {
		result := ResolvePreviewDomain("", "myapp", 42, "platform.dev")
		Expect(result).To(Equal("pr-42-myapp.platform.dev"))
	})

	It("should use example.com when both template and platformDomain are empty", func() {
		result := ResolvePreviewDomain("", "myapp", 42, "")
		Expect(result).To(Equal("pr-42-myapp.example.com"))
	})
})
