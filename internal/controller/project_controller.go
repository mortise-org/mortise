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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

const (
	// ProjectNamespacePrefix is the prefix applied to a Project's backing
	// Kubernetes namespace. A Project named "infra" is backed by the namespace
	// "project-infra".
	ProjectNamespacePrefix = "project-"

	// projectFinalizer ensures the Project's owned namespace is deleted before
	// the CRD is removed, so cascade GC completes cleanly.
	projectFinalizer = "mortise.dev/project-finalizer"
)

// ProjectNamespace returns the backing namespace name for a Project.
func ProjectNamespace(projectName string) string {
	return ProjectNamespacePrefix + projectName
}

// ProjectReconciler reconciles a Project object. On create it provisions a
// backing namespace; on delete it tears the namespace down (k8s GC cascades
// to every App inside).
type ProjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=projects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=projects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=projects/finalizers,verbs=update
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete

func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var project mortisev1alpha1.Project
	if err := r.Get(ctx, req.NamespacedName, &project); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	nsName := ProjectNamespace(project.Name)

	// Handle deletion: drop the namespace, then the finalizer.
	if !project.DeletionTimestamp.IsZero() {
		if err := r.markTerminating(ctx, &project, nsName); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.ensureNamespaceDeleted(ctx, nsName); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&project, projectFinalizer) {
			if err := r.Update(ctx, &project); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer so we can clean up the namespace on delete.
	if controllerutil.AddFinalizer(&project, projectFinalizer) {
		if err := r.Update(ctx, &project); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		// Requeue implicitly via the update event.
		return ctrl.Result{}, nil
	}

	if err := r.ensureNamespace(ctx, &project, nsName); err != nil {
		log.Error(err, "ensure namespace failed")
		return ctrl.Result{}, r.updateStatus(ctx, &project, mortisev1alpha1.ProjectPhaseFailed, nsName, 0)
	}

	appCount, err := r.countApps(ctx, nsName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("count apps: %w", err)
	}

	if err := r.updateStatus(ctx, &project, mortisev1alpha1.ProjectPhaseReady, nsName, appCount); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *ProjectReconciler) ensureNamespace(ctx context.Context, project *mortisev1alpha1.Project, nsName string) error {
	desired := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nsName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/project":          project.Name,
			},
		},
	}
	if err := controllerutil.SetControllerReference(project, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner ref on namespace: %w", err)
	}

	var existing corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: nsName}, &existing)
	if errors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create namespace: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get namespace: %w", err)
	}

	// Ensure labels + owner reference stay in sync on an existing namespace
	// that Mortise manages. Don't touch namespaces we didn't create.
	if existing.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		return fmt.Errorf("namespace %q exists but is not managed by mortise", nsName)
	}
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels["app.kubernetes.io/managed-by"] = "mortise"
	existing.Labels["mortise.dev/project"] = project.Name
	if err := controllerutil.SetControllerReference(project, &existing, r.Scheme); err != nil {
		return fmt.Errorf("update owner ref: %w", err)
	}
	return r.Update(ctx, &existing)
}

func (r *ProjectReconciler) ensureNamespaceDeleted(ctx context.Context, nsName string) error {
	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: nsName}, &ns)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get namespace for delete: %w", err)
	}
	if ns.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		// Refuse to delete namespaces we don't own.
		return nil
	}
	if !ns.DeletionTimestamp.IsZero() {
		return nil
	}
	if err := r.Delete(ctx, &ns); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete namespace: %w", err)
	}
	return nil
}

func (r *ProjectReconciler) countApps(ctx context.Context, nsName string) (int32, error) {
	var list mortisev1alpha1.AppList
	if err := r.List(ctx, &list, client.InNamespace(nsName)); err != nil {
		return 0, err
	}
	return int32(len(list.Items)), nil
}

func (r *ProjectReconciler) markTerminating(ctx context.Context, project *mortisev1alpha1.Project, nsName string) error {
	if project.Status.Phase == mortisev1alpha1.ProjectPhaseTerminating {
		return nil
	}
	project.Status.Phase = mortisev1alpha1.ProjectPhaseTerminating
	project.Status.Namespace = nsName
	return r.Status().Update(ctx, project)
}

func (r *ProjectReconciler) updateStatus(ctx context.Context, project *mortisev1alpha1.Project, phase mortisev1alpha1.ProjectPhase, nsName string, appCount int32) error {
	if project.Status.Phase == phase &&
		project.Status.Namespace == nsName &&
		project.Status.AppCount == appCount {
		return nil
	}
	project.Status.Phase = phase
	project.Status.Namespace = nsName
	project.Status.AppCount = appCount
	return r.Status().Update(ctx, project)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.Project{}).
		Owns(&corev1.Namespace{}).
		Named("project").
		Complete(r)
}
