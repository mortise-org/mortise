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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
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

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
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

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
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

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
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
			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
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

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
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

			r := &ProjectReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
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
