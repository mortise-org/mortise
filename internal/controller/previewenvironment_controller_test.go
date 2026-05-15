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
	"net/http"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/internal/git"
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

// newTestScheme builds a scheme containing core and Mortise types for fake clients.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	if err := mortisev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add mortise: %v", err)
	}
	return s
}

// TestEnsureProjectEnvSetsPreviewFlag verifies ensureProjectEnv marks the
// created environment as Preview: true.
func TestEnsureProjectEnvSetsPreviewFlag(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "flag-test"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(project).Build()
	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		Clock:  clocktesting.NewFakeClock(time.Now()),
	}

	if err := reconciler.ensureProjectEnv(ctx, "flag-test", "pr-10"); err != nil {
		t.Fatalf("ensureProjectEnv: %v", err)
	}

	var proj mortisev1alpha1.Project
	if err := c.Get(ctx, types.NamespacedName{Name: "flag-test"}, &proj); err != nil {
		t.Fatalf("get project: %v", err)
	}

	var found *mortisev1alpha1.ProjectEnvironment
	for i := range proj.Spec.Environments {
		if proj.Spec.Environments[i].Name == "pr-10" {
			found = &proj.Spec.Environments[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pr-10 env not found on project")
	}
	if !found.Preview {
		t.Errorf("expected Preview=true on pr-10 env")
	}
	// Normal envs should not have Preview set.
	for _, env := range proj.Spec.Environments {
		if env.Name == "staging" && env.Preview {
			t.Errorf("staging env should not have Preview=true")
		}
	}
}

// TestEnsureProjectEnvIdempotent verifies calling ensureProjectEnv twice
// doesn't duplicate the env entry.
func TestEnsureProjectEnvIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "idemp-test"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(project).Build()
	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		Clock:  clocktesting.NewFakeClock(time.Now()),
	}

	// Call twice.
	if err := reconciler.ensureProjectEnv(ctx, "idemp-test", "pr-11"); err != nil {
		t.Fatalf("first ensureProjectEnv: %v", err)
	}
	if err := reconciler.ensureProjectEnv(ctx, "idemp-test", "pr-11"); err != nil {
		t.Fatalf("second ensureProjectEnv: %v", err)
	}

	var proj mortisev1alpha1.Project
	if err := c.Get(ctx, types.NamespacedName{Name: "idemp-test"}, &proj); err != nil {
		t.Fatalf("get project: %v", err)
	}

	count := 0
	for _, env := range proj.Spec.Environments {
		if env.Name == "pr-11" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 pr-11 env, got %d; envs: %+v", count, proj.Spec.Environments)
	}
}

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

// --- Convergence tests (#373) ---

type mockGitAPI struct {
	openPRs       []git.PullRequestSnapshot
	openPRsByRepo map[string][]git.PullRequestSnapshot
	err           error
	errByRepo     map[string]error
}

func (m *mockGitAPI) ListOpenPullRequests(ctx context.Context, repo string) ([]git.PullRequestSnapshot, error) {
	if err, ok := m.errByRepo[repo]; ok {
		return nil, err
	}
	if prs, ok := m.openPRsByRepo[repo]; ok {
		return prs, nil
	}
	return m.openPRs, m.err
}
func (m *mockGitAPI) RegisterWebhook(ctx context.Context, repo string, cfg git.WebhookConfig) error {
	return nil
}
func (m *mockGitAPI) ListWebhooks(ctx context.Context, repo string) ([]git.WebhookInfo, error) {
	return nil, nil
}
func (m *mockGitAPI) DeleteWebhook(ctx context.Context, repo string, hookID int64) error {
	return nil
}
func (m *mockGitAPI) PostCommitStatus(ctx context.Context, repo, sha string, status git.CommitStatus) error {
	return nil
}
func (m *mockGitAPI) VerifyWebhookSignature(body []byte, header http.Header) error { return nil }
func (m *mockGitAPI) ResolveCloneCredentials(ctx context.Context, repo string) (git.GitCredentials, error) {
	return git.GitCredentials{}, nil
}
func (m *mockGitAPI) ListRepos(ctx context.Context) ([]git.Repository, error) { return nil, nil }
func (m *mockGitAPI) ListBranches(ctx context.Context, repo string) ([]git.Branch, error) {
	return nil, nil
}
func (m *mockGitAPI) ResolveBranchHead(ctx context.Context, repo, branch string) (string, error) {
	return "", nil
}
func (m *mockGitAPI) ListTree(ctx context.Context, owner, repo, branch, path string) ([]git.TreeEntry, error) {
	return nil, nil
}

func TestConvergeProjectPreviews_CreatesMissing(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)
	projectName := "converge-create"
	nsName := constants.ControlNamespace(projectName)

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Preview:      &mortisev1alpha1.PreviewConfig{Enabled: true},
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: nsName},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://github.com/org/repo",
				Branch:      "main",
				ProviderRef: "github-converge",
			},
		},
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-converge"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
		},
	}

	pm := &mortisev1alpha1.ProjectMember{
		ObjectMeta: metav1.ObjectMeta{Name: "converge-member", Namespace: nsName},
		Spec: mortisev1alpha1.ProjectMemberSpec{
			Email: "bot@example.com",
			Role:  "owner",
		},
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      git.UserTokenSecretName("github-converge", "bot@example.com"),
			Namespace: git.TokenSecretNamespace,
		},
		Data: map[string][]byte{"token": []byte("fake-token")},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(project, app, gp, pm, tokenSecret).Build()

	mock := &mockGitAPI{
		openPRs: []git.PullRequestSnapshot{
			{Number: 10, Branch: "feat-a", SHA: "aaa"},
			{Number: 20, Branch: "feat-b", SHA: "bbb"},
		},
	}

	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		Clock:  clocktesting.NewFakeClock(time.Now()),
		GitAPIFactory: func(gp *mortisev1alpha1.GitProvider, token, secret string) (git.GitAPI, error) {
			return mock, nil
		},
	}

	if err := reconciler.ConvergeProjectPreviews(ctx, project); err != nil {
		t.Fatalf("converge: %v", err)
	}

	var peList mortisev1alpha1.PreviewEnvironmentList
	if err := c.List(ctx, &peList, client.InNamespace(nsName)); err != nil {
		t.Fatalf("list PEs: %v", err)
	}
	if len(peList.Items) != 2 {
		t.Fatalf("expected 2 PEs, got %d", len(peList.Items))
	}
	prNums := map[int]bool{}
	for _, pe := range peList.Items {
		prNums[pe.Spec.PullRequest.Number] = true
	}
	if !prNums[10] || !prNums[20] {
		t.Errorf("expected PEs for PR #10 and #20, got %v", prNums)
	}
}

func TestConvergeProjectPreviews_DeletesStale(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)
	projectName := "converge-stale"
	nsName := constants.ControlNamespace(projectName)

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Preview:      &mortisev1alpha1.PreviewConfig{Enabled: true},
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: nsName},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://github.com/org/repo",
				Branch:      "main",
				ProviderRef: "github-converge",
			},
		},
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-converge"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
		},
	}

	pm := &mortisev1alpha1.ProjectMember{
		ObjectMeta: metav1.ObjectMeta{Name: "member", Namespace: nsName},
		Spec: mortisev1alpha1.ProjectMemberSpec{
			Email: "dev@example.com",
			Role:  "owner",
		},
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      git.UserTokenSecretName("github-converge", "dev@example.com"),
			Namespace: git.TokenSecretNamespace,
		},
		Data: map[string][]byte{"token": []byte("fake-token")},
	}

	// CreationTimestamp must be older than the convergence grace period so
	// the stale-PE cleanup logic considers it eligible for deletion.
	oldEnough := metav1.NewTime(time.Now().Add(-convergenceGracePeriod - time.Minute))
	stalePE := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "preview-pr-5", Namespace: nsName, CreationTimestamp: oldEnough},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			ProjectRef:  projectName,
			SourceEnv:   "staging",
			PullRequest: mortisev1alpha1.PullRequestRef{Number: 5, Branch: "old", SHA: "old"},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(project, app, gp, pm, tokenSecret, stalePE).Build()

	mock := &mockGitAPI{
		openPRs: []git.PullRequestSnapshot{
			{Number: 7, Branch: "new-feat", SHA: "ccc"},
		},
	}

	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		Clock:  clocktesting.NewFakeClock(time.Now()),
		GitAPIFactory: func(gp *mortisev1alpha1.GitProvider, token, secret string) (git.GitAPI, error) {
			return mock, nil
		},
	}

	if err := reconciler.ConvergeProjectPreviews(ctx, project); err != nil {
		t.Fatalf("converge: %v", err)
	}

	var peList mortisev1alpha1.PreviewEnvironmentList
	if err := c.List(ctx, &peList, client.InNamespace(nsName)); err != nil {
		t.Fatalf("list PEs: %v", err)
	}

	prNums := map[int]bool{}
	for _, pe := range peList.Items {
		if pe.DeletionTimestamp.IsZero() {
			prNums[pe.Spec.PullRequest.Number] = true
		}
	}
	if prNums[5] {
		t.Errorf("stale PE for PR #5 should be deleted")
	}
	if !prNums[7] {
		t.Errorf("PE for PR #7 should be created")
	}
}

func TestConvergeProjectPreviews_PreviewsDisabled(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "disabled-project"},
		Spec: mortisev1alpha1.ProjectSpec{
			Preview: &mortisev1alpha1.PreviewConfig{Enabled: false},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(project).Build()

	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		GitAPIFactory: func(gp *mortisev1alpha1.GitProvider, token, secret string) (git.GitAPI, error) {
			t.Fatal("GitAPIFactory should not be called when previews are disabled")
			return nil, nil
		},
	}

	if err := reconciler.ConvergeProjectPreviews(ctx, project); err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

func TestConvergeProjectPreviews_NoGitSourceApps(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)
	projectName := "converge-noapp"
	nsName := constants.ControlNamespace(projectName)

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Preview:      &mortisev1alpha1.PreviewConfig{Enabled: true},
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "imgapp", Namespace: nsName},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(project, app).Build()

	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		GitAPIFactory: func(gp *mortisev1alpha1.GitProvider, token, secret string) (git.GitAPI, error) {
			t.Fatal("GitAPIFactory should not be called with only image-source apps")
			return nil, nil
		},
	}

	if err := reconciler.ConvergeProjectPreviews(ctx, project); err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

func TestConvergeProjectPreviews_BotPRFiltering(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)
	projectName := "converge-bot"
	nsName := constants.ControlNamespace(projectName)

	botPRFalse := false
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Preview:      &mortisev1alpha1.PreviewConfig{Enabled: true, BotPR: &botPRFalse},
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: nsName},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://github.com/org/repo",
				Branch:      "main",
				ProviderRef: "github-converge",
			},
		},
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-converge"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
		},
	}

	pm := &mortisev1alpha1.ProjectMember{
		ObjectMeta: metav1.ObjectMeta{Name: "member", Namespace: nsName},
		Spec: mortisev1alpha1.ProjectMemberSpec{
			Email: "dev@example.com",
			Role:  "owner",
		},
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      git.UserTokenSecretName("github-converge", "dev@example.com"),
			Namespace: git.TokenSecretNamespace,
		},
		Data: map[string][]byte{"token": []byte("fake-token")},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(project, app, gp, pm, tokenSecret).Build()

	mock := &mockGitAPI{
		openPRs: []git.PullRequestSnapshot{
			{Number: 30, Branch: "bot-update", SHA: "ddd", Author: git.PullRequestAuthor{Login: "dependabot", IsBot: true}},
			{Number: 31, Branch: "human-feat", SHA: "eee", Author: git.PullRequestAuthor{Login: "developer"}},
		},
	}

	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		Clock:  clocktesting.NewFakeClock(time.Now()),
		GitAPIFactory: func(gp *mortisev1alpha1.GitProvider, token, secret string) (git.GitAPI, error) {
			return mock, nil
		},
	}

	if err := reconciler.ConvergeProjectPreviews(ctx, project); err != nil {
		t.Fatalf("converge: %v", err)
	}

	var peList mortisev1alpha1.PreviewEnvironmentList
	if err := c.List(ctx, &peList, client.InNamespace(nsName)); err != nil {
		t.Fatalf("list PEs: %v", err)
	}

	// Only PR #31 (human) should get a PE, not PR #30 (bot).
	if len(peList.Items) != 1 {
		t.Fatalf("expected 1 PE, got %d", len(peList.Items))
	}
	if peList.Items[0].Spec.PullRequest.Number != 31 {
		t.Errorf("expected PE for PR #31, got #%d", peList.Items[0].Spec.PullRequest.Number)
	}
}

func TestConvergeProjectPreviews_GitAPIErrorSkipsRepo(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)
	projectName := "converge-err"
	nsName := constants.ControlNamespace(projectName)

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Preview:      &mortisev1alpha1.PreviewConfig{Enabled: true},
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: nsName},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://github.com/org/repo",
				Branch:      "main",
				ProviderRef: "github-converge",
			},
		},
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-converge"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
		},
	}

	pm := &mortisev1alpha1.ProjectMember{
		ObjectMeta: metav1.ObjectMeta{Name: "member", Namespace: nsName},
		Spec: mortisev1alpha1.ProjectMemberSpec{
			Email: "dev@example.com",
			Role:  "owner",
		},
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      git.UserTokenSecretName("github-converge", "dev@example.com"),
			Namespace: git.TokenSecretNamespace,
		},
		Data: map[string][]byte{"token": []byte("fake-token")},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(project, app, gp, pm, tokenSecret).Build()

	mock := &mockGitAPI{err: fmt.Errorf("forge API unavailable")}

	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		Clock:  clocktesting.NewFakeClock(time.Now()),
		GitAPIFactory: func(gp *mortisev1alpha1.GitProvider, token, secret string) (git.GitAPI, error) {
			return mock, nil
		},
	}

	err := reconciler.ConvergeProjectPreviews(ctx, project)
	if err != nil {
		t.Fatalf("expected nil when PR listing fails for a repo, got %v", err)
	}

	var peList mortisev1alpha1.PreviewEnvironmentList
	if err := c.List(ctx, &peList, client.InNamespace(nsName)); err != nil {
		t.Fatalf("list PEs: %v", err)
	}
	if len(peList.Items) != 0 {
		t.Errorf("expected no PEs on error, got %d", len(peList.Items))
	}
}

func TestConvergencePEName_MultiRepoSanitizesSlug(t *testing.T) {
	tests := []struct {
		name      string
		repo      string
		number    int
		multiRepo bool
		want      string
	}{
		{
			name:      "single repo keeps classic name",
			repo:      "https://github.com/org/repo_name.git",
			number:    42,
			multiRepo: false,
			want:      "preview-pr-42",
		},
		{
			name:      "underscores and git suffix are normalized",
			repo:      "https://github.com/org/repo_name.git",
			number:    42,
			multiRepo: true,
			want:      "preview-repo-name-pr-42",
		},
		{
			name:      "ssh style repo URL is normalized",
			repo:      "git@github.com:org/another.repo_name/",
			number:    7,
			multiRepo: true,
			want:      "preview-another-repo-name-pr-7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convergencePEName(tt.repo, tt.number, tt.multiRepo)
			if got != tt.want {
				t.Fatalf("convergencePEName(%q, %d, %t) = %q, want %q", tt.repo, tt.number, tt.multiRepo, got, tt.want)
			}
		})
	}

	longName := convergencePEName("https://github.com/org/THIS_IS_A_VERY_LONG_REPOSITORY_NAME_WITH_EXTRA_PARTS_AND_SUFFIX.git", 123456, true)
	if len(longName) > 63 {
		t.Fatalf("expected PE name to fit DNS label limit, got %q (%d chars)", longName, len(longName))
	}
	if strings.ContainsAny(longName, "_.") {
		t.Fatalf("expected sanitized PE name without invalid DNS label chars, got %q", longName)
	}
}

func TestConvergeProjectPreviews_MultiRepoSanitizesNames(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)
	projectName := "converge-multirepo"
	nsName := constants.ControlNamespace(projectName)

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Preview:      &mortisev1alpha1.PreviewConfig{Enabled: true},
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	apps := []client.Object{
		&mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "web-a", Namespace: nsName},
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type:        mortisev1alpha1.SourceTypeGit,
					Repo:        "https://github.com/org/repo_name.git",
					Branch:      "main",
					ProviderRef: "github-converge",
				},
			},
		},
		&mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "web-b", Namespace: nsName},
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type:        mortisev1alpha1.SourceTypeGit,
					Repo:        "git@github.com:org/another.repo_name/",
					Branch:      "main",
					ProviderRef: "github-converge",
				},
			},
		},
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-converge"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
		},
	}

	pm := &mortisev1alpha1.ProjectMember{
		ObjectMeta: metav1.ObjectMeta{Name: "member", Namespace: nsName},
		Spec: mortisev1alpha1.ProjectMemberSpec{
			Email: "dev@example.com",
			Role:  "owner",
		},
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      git.UserTokenSecretName("github-converge", "dev@example.com"),
			Namespace: git.TokenSecretNamespace,
		},
		Data: map[string][]byte{"token": []byte("fake-token")},
	}

	objects := append([]client.Object{project, gp, pm, tokenSecret}, apps...)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()

	mock := &mockGitAPI{
		openPRsByRepo: map[string][]git.PullRequestSnapshot{
			"https://github.com/org/repo_name.git":  {{Number: 10, Branch: "feat-a", SHA: "aaa"}},
			"git@github.com:org/another.repo_name/": {{Number: 20, Branch: "feat-b", SHA: "bbb"}},
		},
	}

	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		Clock:  clocktesting.NewFakeClock(time.Now()),
		GitAPIFactory: func(gp *mortisev1alpha1.GitProvider, token, secret string) (git.GitAPI, error) {
			return mock, nil
		},
	}

	if err := reconciler.ConvergeProjectPreviews(ctx, project); err != nil {
		t.Fatalf("converge: %v", err)
	}

	var peList mortisev1alpha1.PreviewEnvironmentList
	if err := c.List(ctx, &peList, client.InNamespace(nsName)); err != nil {
		t.Fatalf("list PEs: %v", err)
	}
	if len(peList.Items) != 2 {
		t.Fatalf("expected 2 PEs, got %d", len(peList.Items))
	}

	gotNames := map[string]bool{}
	for _, pe := range peList.Items {
		gotNames[pe.Name] = true
		if len(pe.Name) > 63 {
			t.Fatalf("PE name %q exceeds 63 chars", pe.Name)
		}
	}

	if !gotNames["preview-repo-name-pr-10"] {
		t.Fatalf("expected sanitized PE name for repo_name.git, got %v", gotNames)
	}
	if !gotNames["preview-another-repo-name-pr-20"] {
		t.Fatalf("expected sanitized PE name for another.repo_name, got %v", gotNames)
	}
}

func TestConvergeProjectPreviews_ListOpenPullRequestsContinuesPerRepo(t *testing.T) {
	ctx := context.Background()
	s := newTestScheme(t)
	projectName := "converge-continue"
	nsName := constants.ControlNamespace(projectName)
	oldEnough := metav1.NewTime(time.Now().Add(-convergenceGracePeriod - time.Minute))

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: projectName},
		Spec: mortisev1alpha1.ProjectSpec{
			Preview:      &mortisev1alpha1.PreviewConfig{Enabled: true},
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "staging"}},
		},
	}

	apps := []client.Object{
		&mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "web-a", Namespace: nsName},
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type:        mortisev1alpha1.SourceTypeGit,
					Repo:        "https://github.com/org/fails",
					Branch:      "main",
					ProviderRef: "github-converge",
				},
			},
		},
		&mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "web-b", Namespace: nsName},
			Spec: mortisev1alpha1.AppSpec{
				Source: mortisev1alpha1.AppSource{
					Type:        mortisev1alpha1.SourceTypeGit,
					Repo:        "https://github.com/org/works",
					Branch:      "main",
					ProviderRef: "github-converge",
				},
			},
		},
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-converge"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
		},
	}

	pm := &mortisev1alpha1.ProjectMember{
		ObjectMeta: metav1.ObjectMeta{Name: "member", Namespace: nsName},
		Spec: mortisev1alpha1.ProjectMemberSpec{
			Email: "dev@example.com",
			Role:  "owner",
		},
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      git.UserTokenSecretName("github-converge", "dev@example.com"),
			Namespace: git.TokenSecretNamespace,
		},
		Data: map[string][]byte{"token": []byte("fake-token")},
	}

	existingFailedRepoPE := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "preview-fails-pr-30",
			Namespace:         nsName,
			CreationTimestamp: oldEnough,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			ProjectRef:  projectName,
			SourceEnv:   "staging",
			PullRequest: mortisev1alpha1.PullRequestRef{Number: 30, Branch: "old", SHA: "old"},
		},
	}

	objects := append([]client.Object{project, gp, pm, tokenSecret, existingFailedRepoPE}, apps...)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()

	mock := &mockGitAPI{
		openPRsByRepo: map[string][]git.PullRequestSnapshot{
			"https://github.com/org/works": {{Number: 31, Branch: "human-feat", SHA: "eee"}},
		},
		errByRepo: map[string]error{
			"https://github.com/org/fails": fmt.Errorf("forge API unavailable"),
		},
	}

	reconciler := &PreviewEnvironmentReconciler{
		Client: c,
		Scheme: s,
		Clock:  clocktesting.NewFakeClock(time.Now()),
		GitAPIFactory: func(gp *mortisev1alpha1.GitProvider, token, secret string) (git.GitAPI, error) {
			return mock, nil
		},
	}

	if err := reconciler.ConvergeProjectPreviews(ctx, project); err != nil {
		t.Fatalf("expected convergence to continue after per-repo PR list failure, got %v", err)
	}

	var peList mortisev1alpha1.PreviewEnvironmentList
	if err := c.List(ctx, &peList, client.InNamespace(nsName)); err != nil {
		t.Fatalf("list PEs: %v", err)
	}
	if len(peList.Items) != 2 {
		t.Fatalf("expected preserved failed-repo PE plus 1 PE from the healthy repo, got %d", len(peList.Items))
	}

	gotNames := map[string]bool{}
	for _, pe := range peList.Items {
		if pe.DeletionTimestamp.IsZero() {
			gotNames[pe.Name] = true
		}
	}
	if !gotNames["preview-fails-pr-30"] {
		t.Fatalf("expected failed repo PE to be preserved, got %v", gotNames)
	}
	if !gotNames["preview-works-pr-31"] {
		t.Fatalf("expected PE from healthy repo, got %v", gotNames)
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
