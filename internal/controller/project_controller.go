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
	stderrors "errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
)

const (
	// projectFinalizer ensures the Project's owned namespace is deleted before
	// the CRD is removed, so cascade GC completes cleanly.
	projectFinalizer = "mortise.dev/project-finalizer"

	// DefaultProjectEnvironment is seeded into spec.environments when the
	// controller observes an empty list. Matches Railway's default.
	DefaultProjectEnvironment = "production"
)

// Condition types and reasons exposed on Project.status.conditions.
const (
	// ProjectConditionNamespaceReady is True once the Project owns its backing
	// namespace and False when the controller refused to claim it for a
	// structural reason (collision, adoption disabled, etc).
	ProjectConditionNamespaceReady = "NamespaceReady"

	// ReasonReconciled is the standard success reason.
	ReasonReconciled = "Reconciled"
	// ReasonAdopted indicates the controller took ownership of a pre-existing
	// namespace because `spec.adoptExistingNamespace: true`.
	ReasonAdopted = "Adopted"
	// ReasonNamespaceAlreadyExists is returned when a namespace with the
	// resolved name already exists (not owned by any Project) and adoption is
	// not enabled.
	ReasonNamespaceAlreadyExists = "NamespaceAlreadyExists"
	// ReasonNamespaceOwnedByAnotherProject is returned when the target
	// namespace has an owner reference to a different Project.
	ReasonNamespaceOwnedByAnotherProject = "NamespaceOwnedByAnotherProject"
	// ReasonNamespaceConflict is returned when another Project has already
	// claimed the resolved namespace name via its own spec.
	ReasonNamespaceConflict = "NamespaceConflict"
)

// ProjectNamespace returns the default backing namespace name for a Project
// name (i.e. `project-{name}`). Callers that need to honor
// `spec.namespaceOverride` should use ResolveProjectNamespace with the live
// Project instead.
func ProjectNamespace(projectName string) string {
	return constants.ProjectNamespacePrefix + projectName
}

// ResolveProjectNamespace returns the backing namespace name the controller
// should use for this Project: the override if set, else `project-{name}`.
func ResolveProjectNamespace(p *mortisev1alpha1.Project) string {
	if p.Spec.NamespaceOverride != "" {
		return p.Spec.NamespaceOverride
	}
	return ProjectNamespace(p.Name)
}

// namespaceResolveError is a structured reconcile failure carrying both a
// condition reason and the human-readable message to surface on the Project.
type namespaceResolveError struct {
	Reason  string
	Message string
}

func (e *namespaceResolveError) Error() string { return e.Message }

// asNamespaceResolveError unwraps err looking for a *namespaceResolveError.
// Uses stderrors.As because the k8s apierrors package is imported as "errors"
// in this file.
func asNamespaceResolveError(err error) (*namespaceResolveError, bool) {
	var nsErr *namespaceResolveError
	if stderrors.As(err, &nsErr) {
		return nsErr, true
	}
	return nil, false
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
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps,verbs=get;list;watch;patch;update
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

	nsName := ResolveProjectNamespace(&project)

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

	// Ensure finalizer and seed the default environment in a single spec
	// update. Batching keeps the reconcile count down (the controller only
	// has one meaningful "wait for spec write" barrier) and avoids the
	// intermediate state where a Project has a finalizer but still no env.
	specChanged := controllerutil.AddFinalizer(&project, projectFinalizer)
	if len(project.Spec.Environments) == 0 {
		project.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{
			{Name: DefaultProjectEnvironment},
		}
		specChanged = true
	}
	if specChanged {
		if err := r.Update(ctx, &project); err != nil {
			return ctrl.Result{}, fmt.Errorf("seed project spec defaults: %w", err)
		}
		// Requeue implicitly via the update event.
		return ctrl.Result{}, nil
	}

	// Cross-Project uniqueness check: another Project may have claimed this
	// namespace name via its own spec. Reject before we touch the namespace.
	if err := r.checkNamespaceUniqueness(ctx, &project, nsName); err != nil {
		if nsErr, ok := asNamespaceResolveError(err); ok {
			return ctrl.Result{}, r.markFailed(ctx, &project, nsName, nsErr.Reason, nsErr.Message)
		}
		return ctrl.Result{}, err
	}

	adopted, err := r.ensureNamespace(ctx, &project, nsName)
	if err != nil {
		if nsErr, ok := asNamespaceResolveError(err); ok {
			return ctrl.Result{}, r.markFailed(ctx, &project, nsName, nsErr.Reason, nsErr.Message)
		}
		log.Error(err, "ensure namespace failed")
		return ctrl.Result{}, err
	}

	appCount, err := r.countApps(ctx, nsName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("count apps: %w", err)
	}

	// Cascade: if the project's env set changed since the last reconcile, bump
	// an annotation on every App in the namespace so the App controller
	// reconciles with the new env list. The bumped revision is just the
	// generation — monotonic and guaranteed to change on any spec edit.
	if envsChanged(project.Spec.Environments, project.Status.Environments) {
		if err := r.cascadeEnvChange(ctx, &project, nsName); err != nil {
			return ctrl.Result{}, fmt.Errorf("cascade env change: %w", err)
		}
	}

	if err := r.markReady(ctx, &project, nsName, appCount, adopted); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{}, nil
}

// envsChanged reports whether the ordered list of project env names in spec
// differs from what was previously reconciled into status.
func envsChanged(spec []mortisev1alpha1.ProjectEnvironment, status []string) bool {
	if len(spec) != len(status) {
		return true
	}
	for i, env := range spec {
		if env.Name != status[i] {
			return true
		}
	}
	return false
}

// cascadeEnvChange patches every App in the project's namespace with a
// revision annotation so the App controller's informer fires. The annotation
// value is the project's generation — bumped on any spec edit — so repeated
// reconciles of the same generation are no-ops.
func (r *ProjectReconciler) cascadeEnvChange(ctx context.Context, project *mortisev1alpha1.Project, nsName string) error {
	var apps mortisev1alpha1.AppList
	if err := r.List(ctx, &apps, client.InNamespace(nsName)); err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	rev := fmt.Sprintf("%d", project.Generation)
	for i := range apps.Items {
		app := &apps.Items[i]
		patch := client.MergeFrom(app.DeepCopy())
		if app.Annotations == nil {
			app.Annotations = map[string]string{}
		}
		if app.Annotations[ProjectEnvsRevAnnotation] == rev {
			continue
		}
		app.Annotations[ProjectEnvsRevAnnotation] = rev
		if err := r.Patch(ctx, app, patch); err != nil {
			return fmt.Errorf("patch app %q: %w", app.Name, err)
		}
	}
	return nil
}

// ProjectEnvsRevAnnotation is the App annotation the Project controller bumps
// when the project's environment set changes. The App reconciler doesn't
// read it — its only purpose is to force an informer event so the App
// controller re-runs with the new project spec visible.
const ProjectEnvsRevAnnotation = "mortise.dev/project-envs-rev"

// ensureNamespace reconciles the Project's backing namespace. It returns
// adopted=true when it just took ownership of a pre-existing namespace via
// `spec.adoptExistingNamespace`. Structured refusals (collision, adoption
// disabled, foreign owner) come back as *namespaceResolveError so the caller
// can surface them on the Project status.
func (r *ProjectReconciler) ensureNamespace(ctx context.Context, project *mortisev1alpha1.Project, nsName string) (adopted bool, err error) {
	var existing corev1.Namespace
	getErr := r.Get(ctx, types.NamespacedName{Name: nsName}, &existing)
	if errors.IsNotFound(getErr) {
		desired := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nsName,
				Labels: projectNamespaceLabels(project.Name),
			},
		}
		if err := controllerutil.SetControllerReference(project, desired, r.Scheme); err != nil {
			return false, fmt.Errorf("set owner ref on namespace: %w", err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return false, fmt.Errorf("create namespace: %w", err)
		}
		return false, nil
	}
	if getErr != nil {
		return false, fmt.Errorf("get namespace: %w", getErr)
	}

	// Namespace exists. Who owns it?
	ownedByUs := false
	ownedBySomeoneElse := ""
	for _, ref := range existing.OwnerReferences {
		if ref.APIVersion == mortisev1alpha1.GroupVersion.String() && ref.Kind == "Project" {
			if ref.UID == project.UID {
				ownedByUs = true
				break
			}
			ownedBySomeoneElse = ref.Name
		}
	}

	if ownedByUs {
		// Fast path: already ours. Keep labels in sync.
		return false, r.syncLabels(ctx, &existing, project)
	}

	if ownedBySomeoneElse != "" {
		return false, &namespaceResolveError{
			Reason: ReasonNamespaceOwnedByAnotherProject,
			Message: fmt.Sprintf(
				"namespace %q is already owned by Project %q",
				nsName, ownedBySomeoneElse,
			),
		}
	}

	// Namespace exists with no Project owner. If a prior Mortise version
	// created it (labelled but owner-ref stripped), take it back on the fast
	// path — not a user-facing "adoption" of foreign infra. Otherwise
	// adoption requires explicit opt-in.
	if existing.Labels["app.kubernetes.io/managed-by"] == "mortise" {
		if err := controllerutil.SetControllerReference(project, &existing, r.Scheme); err != nil {
			return false, fmt.Errorf("update owner ref: %w", err)
		}
		return false, r.syncLabels(ctx, &existing, project)
	}

	if !project.Spec.AdoptExistingNamespace {
		return false, &namespaceResolveError{
			Reason: ReasonNamespaceAlreadyExists,
			Message: fmt.Sprintf(
				"namespace %q already exists and is not managed by mortise; "+
					"set spec.adoptExistingNamespace: true to take ownership (admin-only)",
				nsName,
			),
		}
	}

	// Adoption path: add owner ref + labels. Do NOT touch any other resource
	// inside the namespace — Mortise only owns what it creates (CLAUDE.md).
	if err := controllerutil.SetControllerReference(project, &existing, r.Scheme); err != nil {
		return false, fmt.Errorf("adopt: set owner ref: %w", err)
	}
	if err := r.syncLabels(ctx, &existing, project); err != nil {
		return false, fmt.Errorf("adopt: sync labels: %w", err)
	}
	return true, nil
}

// projectNamespaceLabels returns the Mortise management labels applied to
// every namespace the Project controller creates or adopts.
func projectNamespaceLabels(projectName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "mortise",
		"mortise.dev/project":          projectName,
		"mortise.dev/managed-by":       "project",
	}
}

// syncLabels writes Mortise's management labels onto the namespace. Always
// issues an Update so that a newly-added owner reference from the caller is
// persisted alongside any label changes.
func (r *ProjectReconciler) syncLabels(ctx context.Context, ns *corev1.Namespace, project *mortisev1alpha1.Project) error {
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	for k, v := range projectNamespaceLabels(project.Name) {
		ns.Labels[k] = v
	}
	return r.Update(ctx, ns)
}

// checkNamespaceUniqueness verifies no other Project already claims the same
// resolved namespace name via its own spec. Prevents two Projects from
// fighting over a single namespace when one uses namespaceOverride.
func (r *ProjectReconciler) checkNamespaceUniqueness(ctx context.Context, project *mortisev1alpha1.Project, nsName string) error {
	var projects mortisev1alpha1.ProjectList
	if err := r.List(ctx, &projects); err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	for i := range projects.Items {
		other := &projects.Items[i]
		if other.UID == project.UID {
			continue
		}
		if !other.DeletionTimestamp.IsZero() {
			continue
		}
		if ResolveProjectNamespace(other) == nsName {
			return &namespaceResolveError{
				Reason: ReasonNamespaceConflict,
				Message: fmt.Sprintf(
					"namespace %q is already claimed by Project %q",
					nsName, other.Name,
				),
			}
		}
	}
	return nil
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

// markReady transitions the Project to Ready and records a NamespaceReady
// condition. When adopted is true, the reason is ReasonAdopted with a message
// identifying the adopted namespace; otherwise ReasonReconciled.
func (r *ProjectReconciler) markReady(ctx context.Context, project *mortisev1alpha1.Project, nsName string, appCount int32, adopted bool) error {
	reason := ReasonReconciled
	msg := fmt.Sprintf("namespace %q ready", nsName)
	if adopted {
		reason = ReasonAdopted
		msg = fmt.Sprintf("adopted pre-existing namespace %q", nsName)
	}
	meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type:               ProjectConditionNamespaceReady,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: project.Generation,
	})
	project.Status.Phase = mortisev1alpha1.ProjectPhaseReady
	project.Status.Namespace = nsName
	project.Status.AppCount = appCount
	project.Status.Environments = specEnvNames(project)
	return r.Status().Update(ctx, project)
}

// specEnvNames projects spec.environments onto just the names, preserving
// spec order. Status consumers use this to know which envs the controller
// has actually observed (post-defaulting).
func specEnvNames(project *mortisev1alpha1.Project) []string {
	names := make([]string, 0, len(project.Spec.Environments))
	for _, env := range project.Spec.Environments {
		names = append(names, env.Name)
	}
	return names
}

// markFailed transitions the Project to Failed with a NamespaceReady=False
// condition carrying the given reason + message.
func (r *ProjectReconciler) markFailed(ctx context.Context, project *mortisev1alpha1.Project, nsName, reason, message string) error {
	meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type:               ProjectConditionNamespaceReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: project.Generation,
	})
	project.Status.Phase = mortisev1alpha1.ProjectPhaseFailed
	project.Status.Namespace = nsName
	return r.Status().Update(ctx, project)
}

// SetupWithManager sets up the controller with the Manager. Watches Apps so
// that `status.appCount` stays current — App changes enqueue the owning
// Project, identified via the namespace's `mortise.dev/project` label.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueueProjectForApp := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		app, ok := obj.(*mortisev1alpha1.App)
		if !ok || app == nil {
			return nil
		}
		// Apps live in namespaces named `project-{name}` (or an override).
		// Look up the namespace's `mortise.dev/project` label to find the
		// owning project — this handles the NamespaceOverride case too.
		var ns corev1.Namespace
		if err := r.Get(ctx, types.NamespacedName{Name: app.Namespace}, &ns); err != nil {
			return nil
		}
		projectName := ns.Labels["mortise.dev/project"]
		if projectName == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: projectName}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.Project{}).
		Owns(&corev1.Namespace{}).
		Watches(&mortisev1alpha1.App{}, enqueueProjectForApp).
		Named("project").
		Complete(r)
}
