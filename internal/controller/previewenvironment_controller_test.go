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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
)

// helper: create a test Project and its control namespace (`pj-{name}`). The
// PreviewEnvironment CRD lives in the control namespace; preview workloads
// reconcile into `pj-{name}-pr-{num}` which the controller creates on demand.
// Returns the Project and the control-namespace name.
func createPreviewTestProject(ctx context.Context, previewEnabled bool) (*mortisev1alpha1.Project, string) {
	projectName := fmt.Sprintf("prevtest-%d", time.Now().UnixNano())
	nsName := constants.ControlNamespace(projectName)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, ns)).To(Succeed())

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec:       mortisev1alpha1.ProjectSpec{},
	}
	if previewEnabled {
		project.Spec.Preview = &mortisev1alpha1.PreviewConfig{
			Enabled: true,
		}
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, project)).To(Succeed())
	return project, nsName
}

// helper: create a minimal App in the given namespace. Project-level preview
// is controlled via createPreviewTestProject; Apps no longer carry preview
// config (SPEC §5.8).
func createPreviewApp(ctx context.Context, name, namespace string, staging *mortisev1alpha1.Environment) *mortisev1alpha1.App {
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
	ExpectWithOffset(1, k8sClient.Create(ctx, app)).To(Succeed())
	return app
}

// helper: create a PreviewEnvironment in the given namespace.
func createPreviewEnv(ctx context.Context, name, namespace, projectRef string, prNumber int, sha, branch string) *mortisev1alpha1.PreviewEnvironment {
	pe := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			ProjectRef: projectRef,
			SourceEnv:  "staging",
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: prNumber,
				Branch: branch,
				SHA:    sha,
			},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, pe)).To(Succeed())
	return pe
}

// newPEReconciler returns a PreviewEnvironmentReconciler using k8sClient.
func newPEReconciler() *PreviewEnvironmentReconciler {
	return &PreviewEnvironmentReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		Clock:  clocktesting.NewFakeClock(time.Now()),
	}
}

var _ = Describe("PreviewEnvironment Controller", func() {
	Context("when the parent Project has project-level preview disabled", func() {
		It("should set the PreviewEnvironment to Failed", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, false)

			pe := createPreviewEnv(ctx, "disabled-preview-pr-1", ns, project.Name, 1, "abc123", "feature")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("PreviewDisabled"))

			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseFailed))
		})
	})

	Context("when the parent Project does not exist", func() {
		It("should set the PreviewEnvironment to Failed", func() {
			ctx := context.Background()
			// Create a control namespace but no Project object.
			nsName := fmt.Sprintf("pj-noproject-%d", time.Now().UnixNano())
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			pe := &mortisev1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "orphan-preview-pr-1",
					Namespace: nsName,
				},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					ProjectRef: "nonexistent-project",
					SourceEnv:  "staging",
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: 1,
						Branch: "feature",
						SHA:    "abc123",
					},
				},
			}
			Expect(k8sClient.Create(ctx, pe)).To(Succeed())

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: nsName},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ProjectNotFound"))

			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: nsName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseFailed))
		})
	})

	Context("when the PreviewEnvironment omits sourceEnv", func() {
		It("should resolve the default source environment and reach Ready", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			staging := &mortisev1alpha1.Environment{Name: "staging"}
			createPreviewApp(ctx, "sourceenv-app", ns, staging)

			pe := &mortisev1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sourceenv-app-preview-pr-7",
					Namespace: ns,
				},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					ProjectRef: project.Name,
					// SourceEnv omitted — controller should resolve to "staging".
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: 7,
						Branch: "main",
						SHA:    "deadbeef",
					},
				},
			}
			Expect(k8sClient.Create(ctx, pe)).To(Succeed())

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// PE should be Ready.
			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))
			Expect(updated.Status.EnvironmentName).To(Equal("pr-7"))

			// pr-7 env entry should exist on the Project.
			var proj mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: project.Name}, &proj)).To(Succeed())
			envNames := make([]string, len(proj.Spec.Environments))
			for i, e := range proj.Spec.Environments {
				envNames[i] = e.Name
			}
			Expect(envNames).To(ContainElement("pr-7"))
		})
	})

	Context("when the source environment is restricted", func() {
		It("should set the PreviewEnvironment to Failed", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{
				{Name: "production", Restricted: true},
			}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			pe := &mortisev1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restricted-preview-pr-1",
					Namespace: ns,
				},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					ProjectRef: project.Name,
					SourceEnv:  "production",
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: 1,
						Branch: "feature",
						SHA:    "abc123",
					},
				},
			}
			Expect(k8sClient.Create(ctx, pe)).To(Succeed())

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("RestrictedSourceEnv"))

			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseFailed))
		})
	})

	Context("when Project.Preview.SourceEnvironment is configured", func() {
		It("should use the configured source environment", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Preview.SourceEnvironment = "dev"
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{
				{Name: "dev"},
				{Name: "staging"},
			}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			devEnv := &mortisev1alpha1.Environment{
				Name: "dev",
				Env:  []mortisev1alpha1.EnvVar{{Name: "LEVEL", Value: "dev"}},
			}
			createPreviewApp(ctx, "source-config-app", ns, devEnv)

			pe := &mortisev1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "source-config-preview-pr-5",
					Namespace: ns,
				},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					ProjectRef: project.Name,
					SourceEnv:  "dev",
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: 5,
						Branch: "feature/dev",
						SHA:    "abc123",
					},
				},
			}
			Expect(k8sClient.Create(ctx, pe)).To(Succeed())

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify the app got a pr-5 override cloned from "dev".
			var app mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "source-config-app", Namespace: ns}, &app)).To(Succeed())
			var prEnv *mortisev1alpha1.Environment
			for i := range app.Spec.Environments {
				if app.Spec.Environments[i].Name == "pr-5" {
					prEnv = &app.Spec.Environments[i]
					break
				}
			}
			Expect(prEnv).NotTo(BeNil())
			// Cloned from dev: should have the env var from dev and branch set.
			Expect(prEnv.Env).To(ContainElement(mortisev1alpha1.EnvVar{Name: "LEVEL", Value: "dev"}))
			Expect(prEnv.Branch).To(Equal("feature/dev"))
		})
	})

	Context("happy path: project with staging env and git-source app", func() {
		It("should create env entry on Project, clone app override, set branch, and reach Ready", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

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
			createPreviewApp(ctx, "webapp", ns, staging)

			pe := createPreviewEnv(ctx, "webapp-preview-pr-42", ns, project.Name, 42, "deadbeef", "feat-x")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify PE status.
			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))
			Expect(updated.Status.EnvironmentName).To(Equal("pr-42"))

			// Verify pr-42 env entry on Project.
			var proj mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: project.Name}, &proj)).To(Succeed())
			envNames := make([]string, len(proj.Spec.Environments))
			for i, e := range proj.Spec.Environments {
				envNames[i] = e.Name
			}
			Expect(envNames).To(ContainElement("pr-42"))

			// Verify app got pr-42 override cloned from staging.
			var app mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "webapp", Namespace: ns}, &app)).To(Succeed())
			var prEnv *mortisev1alpha1.Environment
			for i := range app.Spec.Environments {
				if app.Spec.Environments[i].Name == "pr-42" {
					prEnv = &app.Spec.Environments[i]
					break
				}
			}
			Expect(prEnv).NotTo(BeNil())
			Expect(prEnv.Replicas).To(Equal(ptr.To(int32(2))))
			Expect(prEnv.Resources.CPU).To(Equal("500m"))
			Expect(prEnv.Env).To(ContainElement(mortisev1alpha1.EnvVar{Name: "ENV", Value: "staging"}))
			Expect(prEnv.Branch).To(Equal("feat-x"))
		})

		It("should copy shared-env Secret from source to preview namespace", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			createPreviewApp(ctx, "shared-app", ns, &mortisev1alpha1.Environment{Name: "staging"})

			// Create source env namespace and seed shared-env Secret.
			sourceEnvNs := constants.EnvNamespace(project.Name, "staging")
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: sourceEnvNs},
			})).To(Succeed())

			sharedSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      envstore.SharedEnvName,
					Namespace: sourceEnvNs,
				},
				Data: map[string][]byte{
					"SENTRY_DSN":   []byte("https://sentry.io/123"),
					"FEATURE_FLAG": []byte("true"),
				},
			}
			Expect(k8sClient.Create(ctx, sharedSecret)).To(Succeed())

			// Create the preview env namespace (normally the project controller
			// creates it when the env entry is added, but in envtest we do it manually).
			previewNs := constants.EnvNamespace(project.Name, "pr-20")
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: previewNs},
			})).To(Succeed())

			pe := createPreviewEnv(ctx, "shared-app-preview-pr-20", ns, project.Name, 20, "def456", "feat")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify shared-env Secret was copied.
			var copied corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      envstore.SharedEnvName,
				Namespace: previewNs,
			}, &copied)).To(Succeed())
			Expect(copied.Data).To(HaveKeyWithValue("SENTRY_DSN", []byte("https://sentry.io/123")))
			Expect(copied.Data).To(HaveKeyWithValue("FEATURE_FLAG", []byte("true")))
		})

		It("should skip shared-env copy when source env has no shared-env Secret", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			createPreviewApp(ctx, "empty-shared-app", ns, &mortisev1alpha1.Environment{Name: "staging"})

			pe := createPreviewEnv(ctx, "empty-shared-preview-pr-40", ns, project.Name, 40, "jkl012", "feat")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))
		})

		It("should succeed even when no apps exist in the project", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			// No apps created — PE should still add env entry and reach Ready.
			pe := createPreviewEnv(ctx, "noapp-preview-pr-3", ns, project.Name, 3, "abc123", "feature")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))

			var proj mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: project.Name}, &proj)).To(Succeed())
			envNames := make([]string, len(proj.Spec.Environments))
			for i, e := range proj.Spec.Environments {
				envNames[i] = e.Name
			}
			Expect(envNames).To(ContainElement("pr-3"))
		})

		It("should not set branch on image-source apps", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			// Create an image-source app.
			imgApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "img-app",
					Namespace: ns,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "nginx:1.25",
					},
					Environments: []mortisev1alpha1.Environment{{Name: "staging"}},
				},
			}
			Expect(k8sClient.Create(ctx, imgApp)).To(Succeed())

			pe := createPreviewEnv(ctx, "img-preview-pr-9", ns, project.Name, 9, "sha123", "feat")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify image app got override but Branch is empty.
			var app mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "img-app", Namespace: ns}, &app)).To(Succeed())
			var prEnv *mortisev1alpha1.Environment
			for i := range app.Spec.Environments {
				if app.Spec.Environments[i].Name == "pr-9" {
					prEnv = &app.Spec.Environments[i]
					break
				}
			}
			Expect(prEnv).NotTo(BeNil())
			Expect(prEnv.Branch).To(BeEmpty())
		})
	})

	Context("when the project has multiple apps", func() {
		It("should clone env overrides and set branch for all apps", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			staging := &mortisev1alpha1.Environment{
				Name:     "staging",
				Replicas: ptr.To(int32(2)),
				Env:      []mortisev1alpha1.EnvVar{{Name: "ENV", Value: "staging"}},
			}
			createPreviewApp(ctx, "frontend", ns, staging)
			createPreviewApp(ctx, "backend", ns, &mortisev1alpha1.Environment{
				Name:     "staging",
				Replicas: ptr.To(int32(3)),
				Env:      []mortisev1alpha1.EnvVar{{Name: "SVC", Value: "backend"}},
			})

			pe := createPreviewEnv(ctx, "multi-preview-pr-55", ns, project.Name, 55, "sha55", "feat-multi")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify PE is Ready.
			var updated mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))

			// Verify pr-55 env entry on Project.
			var proj mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: project.Name}, &proj)).To(Succeed())
			envNames := make([]string, len(proj.Spec.Environments))
			for i, e := range proj.Spec.Environments {
				envNames[i] = e.Name
			}
			Expect(envNames).To(ContainElement("pr-55"))

			// Verify frontend app got pr-55 override.
			var frontend mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "frontend", Namespace: ns}, &frontend)).To(Succeed())
			var feEnv *mortisev1alpha1.Environment
			for i := range frontend.Spec.Environments {
				if frontend.Spec.Environments[i].Name == "pr-55" {
					feEnv = &frontend.Spec.Environments[i]
					break
				}
			}
			Expect(feEnv).NotTo(BeNil())
			Expect(feEnv.Replicas).To(Equal(ptr.To(int32(2))))
			Expect(feEnv.Env).To(ContainElement(mortisev1alpha1.EnvVar{Name: "ENV", Value: "staging"}))
			Expect(feEnv.Branch).To(Equal("feat-multi"))

			// Verify backend app got pr-55 override.
			var backend mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "backend", Namespace: ns}, &backend)).To(Succeed())
			var beEnv *mortisev1alpha1.Environment
			for i := range backend.Spec.Environments {
				if backend.Spec.Environments[i].Name == "pr-55" {
					beEnv = &backend.Spec.Environments[i]
					break
				}
			}
			Expect(beEnv).NotTo(BeNil())
			Expect(beEnv.Replicas).To(Equal(ptr.To(int32(3))))
			Expect(beEnv.Env).To(ContainElement(mortisev1alpha1.EnvVar{Name: "SVC", Value: "backend"}))
			Expect(beEnv.Branch).To(Equal("feat-multi"))
		})
	})

	Context("when SHA changes (re-reconcile)", func() {
		It("should remain Ready (idempotent)", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			createPreviewApp(ctx, "rebuildapp", ns, &mortisev1alpha1.Environment{Name: "staging"})

			pe := createPreviewEnv(ctx, "rebuildapp-preview-pr-10", ns, project.Name, 10, "sha-v1", "feature")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			var ready mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &ready)).To(Succeed())
			Expect(ready.Status.Phase).To(Equal(mortisev1alpha1.PreviewPhaseReady))

			// Update SHA and re-reconcile.
			ready.Spec.PullRequest.SHA = "sha-v2"
			Expect(k8sClient.Update(ctx, &ready)).To(Succeed())

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
		It("should remove env entry from Project and app env overrides", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			staging := &mortisev1alpha1.Environment{
				Name: "staging",
				Env:  []mortisev1alpha1.EnvVar{{Name: "ENV", Value: "staging"}},
			}
			createPreviewApp(ctx, "cleanapp", ns, staging)

			pe := createPreviewEnv(ctx, "cleanapp-preview-pr-99", ns, project.Name, 99, "sha999", "cleanup-branch")

			reconciler := newPEReconciler()

			// First reconcile: creates env entry and app override.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify env entry and app override exist.
			var proj mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: project.Name}, &proj)).To(Succeed())
			envNames := make([]string, len(proj.Spec.Environments))
			for i, e := range proj.Spec.Environments {
				envNames[i] = e.Name
			}
			Expect(envNames).To(ContainElement("pr-99"))

			var app mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cleanapp", Namespace: ns}, &app)).To(Succeed())
			appEnvNames := make([]string, len(app.Spec.Environments))
			for i, e := range app.Spec.Environments {
				appEnvNames[i] = e.Name
			}
			Expect(appEnvNames).To(ContainElement("pr-99"))

			// Delete the PE.
			Expect(k8sClient.Delete(ctx, pe)).To(Succeed())

			// Reconcile after delete: finalizer runs cleanup.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// PE should be gone.
			var deletedPE mortisev1alpha1.PreviewEnvironment
			err = k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: ns}, &deletedPE)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			// pr-99 env entry should be removed from Project.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: project.Name}, &proj)).To(Succeed())
			envNames = make([]string, len(proj.Spec.Environments))
			for i, e := range proj.Spec.Environments {
				envNames[i] = e.Name
			}
			Expect(envNames).NotTo(ContainElement("pr-99"))

			// pr-99 app override should be removed.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cleanapp", Namespace: ns}, &app)).To(Succeed())
			appEnvNames = make([]string, len(app.Spec.Environments))
			for i, e := range app.Spec.Environments {
				appEnvNames[i] = e.Name
			}
			Expect(appEnvNames).NotTo(ContainElement("pr-99"))
		})

		It("should handle deletion gracefully when the Project is already gone", func() {
			ctx := context.Background()
			project, ns := createPreviewTestProject(ctx, true)
			project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}}
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			createPreviewApp(ctx, "gone-app", ns, &mortisev1alpha1.Environment{Name: "staging"})

			pe := createPreviewEnv(ctx, "gone-preview-pr-50", ns, project.Name, 50, "sha50", "feat")

			reconciler := newPEReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Delete the Project first.
			Expect(k8sClient.Delete(ctx, project)).To(Succeed())

			// Delete the PE.
			Expect(k8sClient.Delete(ctx, pe)).To(Succeed())

			// Cleanup should not error when Project is gone.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

func TestCloneEnvironment_DeepCopiesAllFields(t *testing.T) {
	app := &mortisev1alpha1.App{
		Spec: mortisev1alpha1.AppSpec{
			Environments: []mortisev1alpha1.Environment{
				{
					Name:     "staging",
					Replicas: ptr.To(int32(3)),
					Resources: mortisev1alpha1.ResourceRequirements{
						CPU:    "500m",
						Memory: "512Mi",
					},
					Env: []mortisev1alpha1.EnvVar{
						{Name: "APP_ENV", Value: "staging"},
						{Name: "DEBUG", Value: "false"},
					},
					Bindings: []mortisev1alpha1.Binding{
						{Ref: "my-db"},
						{Ref: "cache"},
					},
					Annotations: map[string]string{
						"team": "platform",
					},
					BuildArgs: map[string]string{
						"GO_VERSION": "1.22",
					},
					LivenessProbe: &mortisev1alpha1.ProbeConfig{
						Path:                "/healthz",
						Port:                8080,
						InitialDelaySeconds: 10,
						PeriodSeconds:       30,
						TimeoutSeconds:      5,
					},
					ReadinessProbe: &mortisev1alpha1.ProbeConfig{
						Path: "/ready",
						Port: 8080,
					},
					StartupProbe: &mortisev1alpha1.ProbeConfig{
						Path:                "/startup",
						Port:                8080,
						InitialDelaySeconds: 5,
					},
					Schedule:          "*/5 * * * *",
					ConcurrencyPolicy: mortisev1alpha1.ConcurrencyPolicy("Forbid"),
				},
			},
		},
	}

	cloned := cloneEnvironment("staging", "pr-1", app)

	// Verify name is set to target.
	if cloned.Name != "pr-1" {
		t.Fatalf("expected name pr-1, got %q", cloned.Name)
	}

	// Verify scalar fields.
	if cloned.Replicas == nil || *cloned.Replicas != 3 {
		t.Fatalf("expected replicas=3, got %v", cloned.Replicas)
	}
	if cloned.Resources.CPU != "500m" || cloned.Resources.Memory != "512Mi" {
		t.Fatalf("resources mismatch: %+v", cloned.Resources)
	}
	if cloned.Schedule != "*/5 * * * *" {
		t.Fatalf("expected schedule '*/5 * * * *', got %q", cloned.Schedule)
	}
	if cloned.ConcurrencyPolicy != mortisev1alpha1.ConcurrencyPolicy("Forbid") {
		t.Fatalf("expected concurrencyPolicy Forbid, got %q", cloned.ConcurrencyPolicy)
	}

	// Verify probes.
	if cloned.LivenessProbe == nil || cloned.LivenessProbe.Path != "/healthz" || cloned.LivenessProbe.Port != 8080 {
		t.Fatalf("liveness probe mismatch: %+v", cloned.LivenessProbe)
	}
	if cloned.ReadinessProbe == nil || cloned.ReadinessProbe.Path != "/ready" {
		t.Fatalf("readiness probe mismatch: %+v", cloned.ReadinessProbe)
	}
	if cloned.StartupProbe == nil || cloned.StartupProbe.Path != "/startup" {
		t.Fatalf("startup probe mismatch: %+v", cloned.StartupProbe)
	}

	// Verify slices.
	if len(cloned.Env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(cloned.Env))
	}
	if len(cloned.Bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(cloned.Bindings))
	}

	// Verify maps.
	if cloned.Annotations["team"] != "platform" {
		t.Fatalf("annotations mismatch: %v", cloned.Annotations)
	}
	if cloned.BuildArgs["GO_VERSION"] != "1.22" {
		t.Fatalf("buildArgs mismatch: %v", cloned.BuildArgs)
	}

	// Verify independence of deep-copied fields (slices and maps).
	// Note: pointer fields (Replicas, probes) are shallow-copied by cloneEnvironment.
	source := &app.Spec.Environments[0]
	source.Env[0].Value = "mutated"
	source.Bindings[0].Ref = "mutated-db"
	source.Annotations["team"] = "mutated"
	source.BuildArgs["GO_VERSION"] = "9.99"

	for _, ev := range cloned.Env {
		if ev.Name == "APP_ENV" && ev.Value != "staging" {
			t.Errorf("clone env affected by source mutation: %s", ev.Value)
		}
	}
	if cloned.Bindings[0].Ref != "my-db" {
		t.Errorf("clone bindings affected by source mutation: %s", cloned.Bindings[0].Ref)
	}
	if cloned.Annotations["team"] != "platform" {
		t.Errorf("clone annotations affected by source mutation: %s", cloned.Annotations["team"])
	}
	if cloned.BuildArgs["GO_VERSION"] != "1.22" {
		t.Errorf("clone buildArgs affected by source mutation: %s", cloned.BuildArgs["GO_VERSION"])
	}
}

func TestCloneEnvironment_NoSourceEnv_ReturnsBare(t *testing.T) {
	app := &mortisev1alpha1.App{
		Spec: mortisev1alpha1.AppSpec{
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}

	cloned := cloneEnvironment("staging", "pr-1", app)
	if cloned.Name != "pr-1" {
		t.Fatalf("expected name pr-1, got %q", cloned.Name)
	}
	if cloned.Replicas != nil {
		t.Errorf("expected nil replicas for bare clone, got %v", cloned.Replicas)
	}
	if len(cloned.Env) != 0 {
		t.Errorf("expected no env vars for bare clone, got %d", len(cloned.Env))
	}
}
