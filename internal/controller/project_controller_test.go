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
	"encoding/hex"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/activity"
	"github.com/mortise-org/mortise/internal/constants"
)

var _ = Describe("Project Controller", func() {
	ctx := context.Background()

	Context("when reconciling a new Project", func() {
		const projectName = "reconcile-new"
		nsName := ProjectNamespace(projectName)

		AfterEach(func() {
			proj := &mortisev1alpha1.Project{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, proj); err == nil {
				// Remove finalizer so envtest can cleanly delete (no real GC runs here).
				proj.Finalizers = nil
				_ = k8sClient.Update(ctx, proj)
				_ = k8sClient.Delete(ctx, proj)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})
		})

		It("creates a backing namespace with owner reference and labels", func() {
			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
				Spec:       mortisev1alpha1.ProjectSpec{Description: "x"},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			// First reconcile: adds finalizer.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile: creates namespace + updates status.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			var ns corev1.Namespace
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, &ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "mortise"))
			Expect(ns.Labels).To(HaveKeyWithValue("mortise.dev/project", projectName))
			Expect(ns.OwnerReferences).ToNot(BeEmpty())
			Expect(ns.OwnerReferences[0].Kind).To(Equal("Project"))
			Expect(ns.OwnerReferences[0].Name).To(Equal(projectName))

			var updated mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.ProjectPhaseReady))
			Expect(updated.Status.Namespace).To(Equal(nsName))
			Expect(updated.Finalizers).To(ContainElement(projectFinalizer))
		})
	})

	Context("default environment seeding", func() {
		const projectName = "env-seeding"
		nsName := ProjectNamespace(projectName)

		AfterEach(func() {
			proj := &mortisev1alpha1.Project{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, proj); err == nil {
				proj.Finalizers = nil
				_ = k8sClient.Update(ctx, proj)
				_ = k8sClient.Delete(ctx, proj)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})
		})

		reconcileOnce := func() {
			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())
		}

		envNames := func() []string {
			var proj mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &proj)).To(Succeed())
			names := make([]string, 0, len(proj.Spec.Environments))
			for _, e := range proj.Spec.Environments {
				names = append(names, e.Name)
			}
			return names
		}

		It("seeds production when the environment list is empty", func() {
			project := &mortisev1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: projectName}}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())
			reconcileOnce()
			Expect(envNames()).To(Equal([]string{constants.DefaultProjectEnvironment}))
		})

		It("seeds production when only preview environments exist", func() {
			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
				Spec: mortisev1alpha1.ProjectSpec{
					Environments: []mortisev1alpha1.ProjectEnvironment{
						{Name: "preview-pr-7", Preview: true},
					},
				},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())
			reconcileOnce()
			Expect(envNames()).To(Equal([]string{"preview-pr-7", constants.DefaultProjectEnvironment}))
		})

		It("does not seed production when a non-preview environment exists", func() {
			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
				Spec: mortisev1alpha1.ProjectSpec{
					Environments: []mortisev1alpha1.ProjectEnvironment{
						{Name: "staging"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())
			reconcileOnce()
			Expect(envNames()).To(Equal([]string{"staging"}))
		})
	})

	Context("project create activity backfill", func() {
		const projectName = "activity-backfill"

		AfterEach(func() {
			proj := &mortisev1alpha1.Project{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, proj); err == nil {
				proj.Finalizers = nil
				_ = k8sClient.Update(ctx, proj)
				_ = k8sClient.Delete(ctx, proj)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ProjectNamespace(projectName)}})
		})

		It("records exactly one create event after the control namespace exists", func() {
			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name:        projectName,
					Annotations: map[string]string{"mortise.dev/created-by": "owner@example.com"},
				},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: ProjectNamespace(projectName)},
			})).To(Succeed())

			store := activity.NewConfigMapStore(k8sClient)
			Expect(store.Append(ctx, activity.Event{
				Timestamp:    metav1.Now().Time,
				Actor:        "owner@example.com",
				Action:       "create",
				ResourceKind: "project",
				ResourceName: projectName,
				Project:      projectName,
				Message:      "Created project " + projectName,
			})).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			Expect(r.ensureProjectCreateActivity(ctx, project)).To(Succeed())
			var stale mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &stale)).To(Succeed())
			Expect(r.ensureProjectCreateActivity(ctx, &stale)).To(Succeed())

			events, err := store.List(ctx, projectName, activity.Cap)
			Expect(err).NotTo(HaveOccurred())
			createEvents := 0
			for _, event := range events {
				if event.Action == "create" && event.ResourceKind == "project" && event.ResourceName == projectName {
					createEvents++
				}
			}
			Expect(createEvents).To(Equal(1))

			var updated mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &updated)).To(Succeed())
			Expect(updated.Annotations[projectCreateActivityRecordedAnnotation]).To(Equal("true"))
		})

		It("dedupes concurrent stale backfill attempts", func() {
			const concurrentProjectName = "activity-backfill-concurrent"
			DeferCleanup(func() {
				proj := &mortisev1alpha1.Project{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: concurrentProjectName}, proj); err == nil {
					proj.Finalizers = nil
					_ = k8sClient.Update(ctx, proj)
					_ = k8sClient.Delete(ctx, proj)
				}
				_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ProjectNamespace(concurrentProjectName)}})
			})

			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name:        concurrentProjectName,
					Annotations: map[string]string{"mortise.dev/created-by": "owner@example.com"},
				},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: ProjectNamespace(concurrentProjectName)},
			})).To(Succeed())

			var staleA, staleB mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: concurrentProjectName}, &staleA)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: concurrentProjectName}, &staleB)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			start := make(chan struct{})
			errCh := make(chan error, 2)

			run := func(stale *mortisev1alpha1.Project) {
				defer GinkgoRecover()
				<-start
				errCh <- r.ensureProjectCreateActivity(ctx, stale)
			}

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				run(&staleA)
			}()
			go func() {
				defer wg.Done()
				run(&staleB)
			}()

			close(start)
			wg.Wait()
			close(errCh)
			for err := range errCh {
				Expect(err).NotTo(HaveOccurred())
			}

			store := activity.NewConfigMapStore(k8sClient)
			events, err := store.List(ctx, concurrentProjectName, activity.Cap)
			Expect(err).NotTo(HaveOccurred())

			createEvents := 0
			for _, event := range events {
				if event.Action == "create" && event.ResourceKind == "project" && event.ResourceName == concurrentProjectName {
					createEvents++
				}
			}
			Expect(createEvents).To(Equal(1))

			var updated mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: concurrentProjectName}, &updated)).To(Succeed())
			Expect(updated.Annotations[projectCreateActivityRecordedAnnotation]).To(Equal("true"))
		})
	})

	Context("app counting", func() {
		const projectName = "app-count"
		nsName := ProjectNamespace(projectName)

		AfterEach(func() {
			proj := &mortisev1alpha1.Project{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, proj); err == nil {
				proj.Finalizers = nil
				_ = k8sClient.Update(ctx, proj)
				_ = k8sClient.Delete(ctx, proj)
			}
			// Clean up apps first, then namespace.
			var apps mortisev1alpha1.AppList
			_ = k8sClient.List(ctx, &apps)
			for i := range apps.Items {
				_ = k8sClient.Delete(ctx, &apps.Items[i])
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})
		})

		It("reflects the number of Apps in the project's namespace in status", func() {
			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			// Seed a couple of Apps in the project's namespace.
			for _, name := range []string{"web", "db"} {
				app := &mortisev1alpha1.App{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nsName},
					Spec: mortisev1alpha1.AppSpec{
						Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
					},
				}
				Expect(k8sClient.Create(ctx, app)).To(Succeed())
			}

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			var updated mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &updated)).To(Succeed())
			Expect(updated.Status.AppCount).To(Equal(int32(2)))
		})
	})

	Context("idempotency", func() {
		const projectName = "idempotent"
		nsName := ProjectNamespace(projectName)

		AfterEach(func() {
			proj := &mortisev1alpha1.Project{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, proj); err == nil {
				proj.Finalizers = nil
				_ = k8sClient.Update(ctx, proj)
				_ = k8sClient.Delete(ctx, proj)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})
		})

		It("reconciling twice is a no-op", func() {
			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			// Third reconcile should still succeed without churning status.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			var updated mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.ProjectPhaseReady))
		})
	})

	Context("when the Project is missing", func() {
		It("returns nil without error", func() {
			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "does-not-exist"}})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when a pre-existing namespace isn't managed by mortise", func() {
		const projectName = "collide"
		nsName := ProjectNamespace(projectName)

		AfterEach(func() {
			proj := &mortisev1alpha1.Project{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, proj); err == nil {
				proj.Finalizers = nil
				_ = k8sClient.Update(ctx, proj)
				_ = k8sClient.Delete(ctx, proj)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})
		})

		It("marks the project as Failed and does not mutate the namespace", func() {
			// Create an "external" namespace the operator did not manage.
			orphan := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"owner": "somebody-else"},
				},
			}
			Expect(k8sClient.Create(ctx, orphan)).To(Succeed())

			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			// Add finalizer pass.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())
			// Second pass: tries to ensureNamespace and hits the "not managed" branch.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			var updated mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.ProjectPhaseFailed))

			// Failure is surfaced with the NamespaceAlreadyExists reason so
			// the operator knows adoption is the escape hatch.
			cond := findCondition(updated.Status.Conditions, ProjectConditionNamespaceReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(ReasonNamespaceAlreadyExists))

			// Orphan namespace still belongs to whoever created it.
			var unchanged corev1.Namespace
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, &unchanged)).To(Succeed())
			Expect(unchanged.OwnerReferences).To(BeEmpty())
			Expect(unchanged.Labels["owner"]).To(Equal("somebody-else"))
		})
	})

	Context("on deletion", func() {
		const projectName = "cascade-delete"
		nsName := ProjectNamespace(projectName)

		It("deletes the backing namespace and clears the finalizer", func() {
			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			var ns corev1.Namespace
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, &ns)).To(Succeed())

			// Trigger delete.
			var fresh mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &fresh)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &fresh)).To(Succeed())

			// Reconcile under deletion: should ensure ns delete issued + remove finalizer.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			// Project CRD should be gone now that the finalizer is dropped.
			err = k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, &mortisev1alpha1.Project{})
			Expect(errors.IsNotFound(err)).To(BeTrue(), "expected project to be garbage-collected after finalizer removal, got %v", err)

			// Namespace deletion was issued (envtest may leave it stuck in
			// Terminating without a kube-controller-manager; either gone or
			// DeletionTimestamp set is acceptable).
			err = k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, &ns)
			if err == nil {
				Expect(ns.DeletionTimestamp).ToNot(BeNil())
			} else {
				Expect(errors.IsNotFound(err)).To(BeTrue())
			}
		})
	})

	Context("owner auto-assignment via mortise.dev/created-by", func() {
		const creatorEmail = "creator@example.com"

		cleanupProject := func(projectName, nsName string) {
			var members mortisev1alpha1.ProjectMemberList
			if err := k8sClient.List(ctx, &members, client.InNamespace(nsName)); err == nil {
				for i := range members.Items {
					_ = k8sClient.Delete(ctx, &members.Items[i])
				}
			}
			proj := &mortisev1alpha1.Project{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, proj); err == nil {
				proj.Finalizers = nil
				_ = k8sClient.Update(ctx, proj)
				_ = k8sClient.Delete(ctx, proj)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}})
		}

		It("creates a ProjectMember owner with correct name and label", func() {
			projectName := "oa-creates"
			nsName := ProjectNamespace(projectName)
			expectedMemberName := "member-" + hex.EncodeToString([]byte(creatorEmail))
			DeferCleanup(cleanupProject, projectName, nsName)

			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name:        projectName,
					Annotations: map[string]string{"mortise.dev/created-by": creatorEmail},
				},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			var member mortisev1alpha1.ProjectMember
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      expectedMemberName,
				Namespace: nsName,
			}, &member)).To(Succeed())

			Expect(member.Spec.Email).To(Equal(creatorEmail))
			Expect(member.Spec.Role).To(Equal(mortisev1alpha1.ProjectRoleOwner))
			Expect(member.Spec.Project).To(Equal(projectName))
			Expect(member.Labels).To(HaveKeyWithValue("mortise.dev/member", "true"))
		})

		It("is idempotent — second reconcile does not create a duplicate", func() {
			projectName := "oa-idem"
			nsName := ProjectNamespace(projectName)
			DeferCleanup(cleanupProject, projectName, nsName)

			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name:        projectName,
					Annotations: map[string]string{"mortise.dev/created-by": creatorEmail},
				},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			for i := 0; i < 3; i++ {
				_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
				Expect(err).NotTo(HaveOccurred())
			}

			var members mortisev1alpha1.ProjectMemberList
			Expect(k8sClient.List(ctx, &members, client.InNamespace(nsName))).To(Succeed())
			ownerCount := 0
			for _, m := range members.Items {
				if m.Spec.Email == creatorEmail && m.Spec.Role == mortisev1alpha1.ProjectRoleOwner {
					ownerCount++
				}
			}
			Expect(ownerCount).To(Equal(1))
		})

		It("does not create a ProjectMember when created-by is absent", func() {
			projectName := "oa-no-ann"
			nsName := ProjectNamespace(projectName)
			DeferCleanup(cleanupProject, projectName, nsName)

			project := &mortisev1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), OperatorNamespace: "mortise-system", ServiceAccountName: "mortise-controller"}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: projectName}})
			Expect(err).NotTo(HaveOccurred())

			var members mortisev1alpha1.ProjectMemberList
			Expect(k8sClient.List(ctx, &members, client.InNamespace(nsName))).To(Succeed())
			Expect(members.Items).To(BeEmpty())
		})
	})

})

// findCondition returns a pointer to the first condition with the given type
// or nil. Lets tests assert on condition reason/message without pulling in the
// full k8s meta package locally.
func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}
