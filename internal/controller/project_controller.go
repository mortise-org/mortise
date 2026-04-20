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
	// projectFinalizer ensures the controller gets one last reconcile on delete
	// so it can tear down owned namespaces before the CRD disappears.
	projectFinalizer = "mortise.dev/project-finalizer"

	// DefaultProjectEnvironment is seeded into spec.environments when the
	// controller observes an empty list. Matches Railway's default.
	DefaultProjectEnvironment = "production"
)

// Condition types and reasons exposed on Project.status.conditions.
const (
	// ProjectConditionNamespaceReady is True once every project-owned namespace
	// (control + one per env) has been provisioned. False when the controller
	// refused to claim a namespace or when env-namespace length validation
	// failed.
	ProjectConditionNamespaceReady = "NamespaceReady"

	ReasonReconciled             = "Reconciled"
	ReasonNamespaceAlreadyExists = "NamespaceAlreadyExists"
	ReasonNamespaceOwnedByOther  = "NamespaceOwnedByAnotherProject"
	ReasonNamespaceConflict      = "NamespaceConflict"
	ReasonEnvNamespaceTooLong    = "EnvNamespaceTooLong"
)

// ProjectEnvsRevAnnotation is the App annotation the Project controller bumps
// when the project's environment set changes. Purely an informer wake-up
// signal — the App reconciler doesn't read the value.
const ProjectEnvsRevAnnotation = "mortise.dev/project-envs-rev"

// ProjectNamespace returns the control namespace for a Project. Kept for
// callers outside this package that used to rely on `project-{name}`.
func ProjectNamespace(projectName string) string {
	return constants.ControlNamespace(projectName)
}

// ResolveProjectNamespace returns the control namespace for the Project. The
// legacy `NamespaceOverride` knob has been removed in the per-env pivot.
func ResolveProjectNamespace(p *mortisev1alpha1.Project) string {
	return constants.ControlNamespace(p.Name)
}

// namespaceResolveError is a structured reconcile failure carrying both a
// condition reason and the human-readable message to surface on the Project.
type namespaceResolveError struct {
	Reason  string
	Message string
}

func (e *namespaceResolveError) Error() string { return e.Message }

func asNamespaceResolveError(err error) (*namespaceResolveError, bool) {
	var nsErr *namespaceResolveError
	if stderrors.As(err, &nsErr) {
		return nsErr, true
	}
	return nil, false
}

// ProjectReconciler reconciles a Project: provisions the control namespace
// plus one workload namespace per declared environment, tears them down on
// delete, and propagates env-set changes to owned Apps.
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

	controlNs := constants.ControlNamespace(project.Name)

	// Handle deletion: drop every namespace we own (control + env), then the
	// finalizer. Owner refs cascade resource deletion inside each ns.
	if !project.DeletionTimestamp.IsZero() {
		if err := r.markTerminating(ctx, &project, controlNs); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.deleteOwnedNamespaces(ctx, &project); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&project, projectFinalizer) {
			if err := r.Update(ctx, &project); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Seed finalizer + default env in one spec-update pass.
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
		return ctrl.Result{}, nil
	}

	// Validate every env-namespace name fits in 63 chars BEFORE we create
	// anything — catches the overflow case at the project level so status
	// messaging is clear.
	for _, env := range project.Spec.Environments {
		if err := constants.ValidateProjectEnvLengths(project.Name, env.Name); err != nil {
			return ctrl.Result{}, r.markFailed(ctx, &project, controlNs, ReasonEnvNamespaceTooLong, err.Error())
		}
	}

	// Cross-project uniqueness check on the control namespace.
	if err := r.checkControlNamespaceUniqueness(ctx, &project, controlNs); err != nil {
		if nsErr, ok := asNamespaceResolveError(err); ok {
			return ctrl.Result{}, r.markFailed(ctx, &project, controlNs, nsErr.Reason, nsErr.Message)
		}
		return ctrl.Result{}, err
	}

	// Ensure control namespace.
	if err := r.ensureNamespace(ctx, &project, controlNs, namespaceSpec{
		role: constants.NamespaceRoleControl,
	}); err != nil {
		if nsErr, ok := asNamespaceResolveError(err); ok {
			return ctrl.Result{}, r.markFailed(ctx, &project, controlNs, nsErr.Reason, nsErr.Message)
		}
		log.Error(err, "ensure control namespace failed")
		return ctrl.Result{}, err
	}

	// Ensure one namespace per declared env.
	envNsMap := make(map[string]string, len(project.Spec.Environments))
	for _, env := range project.Spec.Environments {
		ns := constants.EnvNamespace(project.Name, env.Name)
		if err := r.ensureNamespace(ctx, &project, ns, namespaceSpec{
			role:    constants.NamespaceRoleEnv,
			envName: env.Name,
		}); err != nil {
			if nsErr, ok := asNamespaceResolveError(err); ok {
				return ctrl.Result{}, r.markFailed(ctx, &project, controlNs, nsErr.Reason, nsErr.Message)
			}
			return ctrl.Result{}, fmt.Errorf("ensure env namespace %q: %w", ns, err)
		}
		envNsMap[env.Name] = ns
	}

	// GC env namespaces that are no longer in spec.
	if err := r.gcStaleEnvNamespaces(ctx, &project, envNsMap); err != nil {
		return ctrl.Result{}, fmt.Errorf("gc stale env namespaces: %w", err)
	}

	appCount, err := r.countApps(ctx, controlNs)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("count apps: %w", err)
	}

	// Cascade env changes to every App in the control namespace so their
	// reconcilers pick up the new env list on the next loop.
	if envsChanged(project.Spec.Environments, project.Status.Environments) {
		if err := r.cascadeEnvChange(ctx, &project, controlNs); err != nil {
			return ctrl.Result{}, fmt.Errorf("cascade env change: %w", err)
		}
	}

	if err := r.markReady(ctx, &project, controlNs, appCount, envNsMap); err != nil {
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

// cascadeEnvChange patches every App in the project's control namespace with a
// revision annotation so the App controller's informer fires.
func (r *ProjectReconciler) cascadeEnvChange(ctx context.Context, project *mortisev1alpha1.Project, controlNs string) error {
	var apps mortisev1alpha1.AppList
	if err := r.List(ctx, &apps, client.InNamespace(controlNs)); err != nil {
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

// namespaceSpec carries the metadata we stamp onto a namespace the Project
// controller owns.
type namespaceSpec struct {
	role    string // constants.NamespaceRole*
	envName string // "" for control, env name otherwise
}

// ensureNamespace creates or updates a namespace owned by the Project. Refuses
// to adopt namespaces already owned by a different Project; refuses to create
// over the top of a user-created namespace (no adoption path in per-env world).
func (r *ProjectReconciler) ensureNamespace(ctx context.Context, project *mortisev1alpha1.Project, nsName string, spec namespaceSpec) error {
	labels := namespaceLabels(project.Name, spec)
	var existing corev1.Namespace
	getErr := r.Get(ctx, types.NamespacedName{Name: nsName}, &existing)
	if errors.IsNotFound(getErr) {
		desired := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nsName,
				Labels: labels,
			},
		}
		if err := controllerutil.SetControllerReference(project, desired, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on namespace: %w", err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create namespace: %w", err)
		}
		return nil
	}
	if getErr != nil {
		return fmt.Errorf("get namespace: %w", getErr)
	}

	// Namespace exists. Only owner: us → sync labels. Anything else → error.
	ownedByUs := false
	ownedByOther := ""
	for _, ref := range existing.OwnerReferences {
		if ref.APIVersion == mortisev1alpha1.GroupVersion.String() && ref.Kind == "Project" {
			if ref.UID == project.UID {
				ownedByUs = true
				break
			}
			ownedByOther = ref.Name
		}
	}
	if ownedByUs {
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		changed := false
		for k, v := range labels {
			if existing.Labels[k] != v {
				existing.Labels[k] = v
				changed = true
			}
		}
		if changed {
			return r.Update(ctx, &existing)
		}
		return nil
	}
	if ownedByOther != "" {
		return &namespaceResolveError{
			Reason:  ReasonNamespaceOwnedByOther,
			Message: fmt.Sprintf("namespace %q is already owned by Project %q", nsName, ownedByOther),
		}
	}
	return &namespaceResolveError{
		Reason:  ReasonNamespaceAlreadyExists,
		Message: fmt.Sprintf("namespace %q already exists and is not managed by mortise; delete it or pick a different project/env name", nsName),
	}
}

// namespaceLabels returns the management labels stamped on a project-owned ns.
func namespaceLabels(projectName string, spec namespaceSpec) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "mortise",
		constants.ProjectLabel:         projectName,
		"mortise.dev/managed-by":       "project",
		constants.NamespaceRoleLabel:   spec.role,
	}
	if spec.envName != "" {
		labels[constants.EnvironmentLabel] = spec.envName
	}
	return labels
}

// checkControlNamespaceUniqueness verifies no other active Project claims the
// same control namespace.
func (r *ProjectReconciler) checkControlNamespaceUniqueness(ctx context.Context, project *mortisev1alpha1.Project, controlNs string) error {
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
		if constants.ControlNamespace(other.Name) == controlNs {
			return &namespaceResolveError{
				Reason:  ReasonNamespaceConflict,
				Message: fmt.Sprintf("namespace %q is already claimed by Project %q", controlNs, other.Name),
			}
		}
	}
	return nil
}

// gcStaleEnvNamespaces deletes any env namespace labelled for this project but
// whose env name is not in the current desired set.
func (r *ProjectReconciler) gcStaleEnvNamespaces(ctx context.Context, project *mortisev1alpha1.Project, desired map[string]string) error {
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabels{
		constants.ProjectLabel:       project.Name,
		constants.NamespaceRoleLabel: constants.NamespaceRoleEnv,
	}); err != nil {
		return fmt.Errorf("list env namespaces: %w", err)
	}
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		envName := ns.Labels[constants.EnvironmentLabel]
		if _, stillWanted := desired[envName]; stillWanted {
			continue
		}
		if !ns.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, ns); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete stale env ns %q: %w", ns.Name, err)
		}
	}
	return nil
}

// deleteOwnedNamespaces requests deletion of every namespace this Project owns
// (control, env, preview). k8s GC cascades to everything inside each ns.
func (r *ProjectReconciler) deleteOwnedNamespaces(ctx context.Context, project *mortisev1alpha1.Project) error {
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabels{
		constants.ProjectLabel: project.Name,
	}); err != nil {
		return fmt.Errorf("list owned namespaces: %w", err)
	}
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		if ns.Labels["app.kubernetes.io/managed-by"] != "mortise" {
			continue
		}
		if !ns.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, ns); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete namespace %q: %w", ns.Name, err)
		}
	}
	return nil
}

func (r *ProjectReconciler) countApps(ctx context.Context, controlNs string) (int32, error) {
	var list mortisev1alpha1.AppList
	if err := r.List(ctx, &list, client.InNamespace(controlNs)); err != nil {
		return 0, err
	}
	return int32(len(list.Items)), nil
}

func (r *ProjectReconciler) markTerminating(ctx context.Context, project *mortisev1alpha1.Project, controlNs string) error {
	if project.Status.Phase == mortisev1alpha1.ProjectPhaseTerminating {
		return nil
	}
	project.Status.Phase = mortisev1alpha1.ProjectPhaseTerminating
	project.Status.Namespace = controlNs
	return r.Status().Update(ctx, project)
}

func (r *ProjectReconciler) markReady(ctx context.Context, project *mortisev1alpha1.Project, controlNs string, appCount int32, envNsMap map[string]string) error {
	meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type:               ProjectConditionNamespaceReady,
		Status:             metav1.ConditionTrue,
		Reason:             ReasonReconciled,
		Message:            fmt.Sprintf("control %q + %d env namespaces ready", controlNs, len(envNsMap)),
		ObservedGeneration: project.Generation,
	})
	project.Status.Phase = mortisev1alpha1.ProjectPhaseReady
	project.Status.Namespace = controlNs
	project.Status.AppCount = appCount
	project.Status.Environments = specEnvNames(project)
	project.Status.EnvNamespaces = envNsMap
	return r.Status().Update(ctx, project)
}

func specEnvNames(project *mortisev1alpha1.Project) []string {
	names := make([]string, 0, len(project.Spec.Environments))
	for _, env := range project.Spec.Environments {
		names = append(names, env.Name)
	}
	return names
}

func (r *ProjectReconciler) markFailed(ctx context.Context, project *mortisev1alpha1.Project, controlNs, reason, message string) error {
	meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type:               ProjectConditionNamespaceReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: project.Generation,
	})
	project.Status.Phase = mortisev1alpha1.ProjectPhaseFailed
	project.Status.Namespace = controlNs
	return r.Status().Update(ctx, project)
}

// SetupWithManager wires the controller. Watches Apps (to keep appCount fresh)
// and owned Namespaces (to trigger cleanup after cascade deletion).
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueueProjectForApp := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		app, ok := obj.(*mortisev1alpha1.App)
		if !ok || app == nil {
			return nil
		}
		// App CRDs live in control namespaces (`pj-{name}`). Look up the
		// namespace's project label so we find the owning project in both
		// the common case and when cached label→ns mapping is fresher.
		var ns corev1.Namespace
		if err := r.Get(ctx, types.NamespacedName{Name: app.Namespace}, &ns); err != nil {
			return nil
		}
		projectName := ns.Labels[constants.ProjectLabel]
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
