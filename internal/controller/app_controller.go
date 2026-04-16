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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/bindings"
)

// maxDeployHistory is the maximum number of deploy records kept per environment.
const maxDeployHistory = 20

// AppReconciler reconciles a App object
type AppReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Clock  clock.Clock
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

func (r *AppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var app mortisev1alpha1.App
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if app.Spec.Source.Type != mortisev1alpha1.SourceTypeImage {
		log.Info("skipping non-image source app", "type", app.Spec.Source.Type)
		return ctrl.Result{}, nil
	}

	if err := r.reconcilePVCs(ctx, &app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile PVCs: %w", err)
	}

	for i := range app.Spec.Environments {
		env := &app.Spec.Environments[i]

		if err := r.reconcileDeployment(ctx, &app, env); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile deployment for env %s: %w", env.Name, err)
		}

		if err := r.reconcileService(ctx, &app, env); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile service for env %s: %w", env.Name, err)
		}

		if app.Spec.Network.Public && env.Domain != "" {
			if err := r.reconcileIngress(ctx, &app, env); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcile ingress for env %s: %w", env.Name, err)
			}
		}
	}

	if err := r.updateStatus(ctx, &app); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *AppReconciler) reconcileDeployment(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := deploymentName(app.Name, env.Name)
	replicas := int32(1)
	if env.Replicas != nil {
		replicas = *env.Replicas
	}

	envVars := toEnvVars(env.Env)

	if len(env.Bindings) > 0 {
		resolver := &bindings.Resolver{Client: r.Client}
		boundVars, err := resolver.Resolve(ctx, app.Namespace, env.Bindings)
		if err != nil {
			return fmt.Errorf("resolve bindings: %w", err)
		}
		envVars = append(boundVars, envVars...)
	}

	containers := []corev1.Container{
		{
			Name:  app.Name,
			Image: app.Spec.Source.Image,
			Env:   envVars,
		},
	}

	if env.Resources.CPU != "" || env.Resources.Memory != "" {
		containers[0].Resources = toResourceRequirements(env.Resources)
	}

	volumes, mounts := toVolumesAndMounts(app)
	if len(mounts) > 0 {
		containers[0].VolumeMounts = mounts
	}

	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels:    appLabels(app.Name, env.Name),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: appLabels(app.Name, env.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: appLabels(app.Name, env.Name),
				},
				Spec: corev1.PodSpec{
					Containers: containers,
					Volumes:    volumes,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return err
	}

	var existing appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Spec = desired.Spec
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcileService(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := serviceName(app.Name, env.Name)

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels:    appLabels(app.Name, env.Name),
		},
		Spec: corev1.ServiceSpec{
			Selector: appLabels(app.Name, env.Name),
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt32(8080),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return err
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcileIngress(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := ingressName(app.Name, env.Name)
	pathType := networkingv1.PathTypePrefix

	desired := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels:    appLabels(app.Name, env.Name),
			Annotations: map[string]string{
				"cert-manager.io/cluster-issuer": "letsencrypt-prod",
			},
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: env.Domain,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: serviceName(app.Name, env.Name),
											Port: networkingv1.ServiceBackendPort{
												Number: 80,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			TLS: []networkingv1.IngressTLS{
				{
					Hosts:      []string{env.Domain},
					SecretName: fmt.Sprintf("%s-tls", name),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return err
	}

	var existing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Spec = desired.Spec
	existing.Annotations = desired.Annotations
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcilePVCs(ctx context.Context, app *mortisev1alpha1.App) error {
	for _, vol := range app.Spec.Storage {
		name := pvcName(app.Name, vol.Name)

		accessMode := corev1.ReadWriteOnce
		if vol.AccessMode != "" {
			accessMode = corev1.PersistentVolumeAccessMode(vol.AccessMode)
		}

		desired := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: app.Namespace,
				Labels:    appLabels(app.Name, ""),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: vol.Size,
					},
				},
			},
		}

		if vol.StorageClass != "" {
			desired.Spec.StorageClassName = &vol.StorageClass
		}

		if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
			return err
		}

		var existing corev1.PersistentVolumeClaim
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
		if errors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}

		// PVC spec is largely immutable; only storage size can be expanded (requires bound claim + expandable SC)
		currentSize := existing.Spec.Resources.Requests[corev1.ResourceStorage]
		if vol.Size.Cmp(currentSize) != 0 {
			existing.Spec.Resources.Requests[corev1.ResourceStorage] = vol.Size
			if err := r.Update(ctx, &existing); err != nil {
				return err
			}
		}
	}
	return nil
}

func pvcName(app, volume string) string {
	return fmt.Sprintf("%s-%s", app, volume)
}

func (r *AppReconciler) clock() clock.Clock {
	if r.Clock != nil {
		return r.Clock
	}
	return clock.RealClock{}
}

func (r *AppReconciler) updateStatus(ctx context.Context, app *mortisev1alpha1.App) error {
	// Index existing environment statuses by name for deploy history carryover.
	existingByName := make(map[string]mortisev1alpha1.EnvironmentStatus, len(app.Status.Environments))
	for _, es := range app.Status.Environments {
		existingByName[es.Name] = es
	}

	var envStatuses []mortisev1alpha1.EnvironmentStatus

	for _, env := range app.Spec.Environments {
		name := deploymentName(app.Name, env.Name)
		var dep appsv1.Deployment
		es := mortisev1alpha1.EnvironmentStatus{
			Name:         env.Name,
			CurrentImage: app.Spec.Source.Image,
		}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &dep); err == nil {
			es.ReadyReplicas = dep.Status.ReadyReplicas
		}

		// Carry forward deploy history and append if image changed.
		if prev, ok := existingByName[env.Name]; ok {
			es.DeployHistory = prev.DeployHistory
		}
		if needsDeployRecord(es.CurrentImage, es.DeployHistory) {
			record := mortisev1alpha1.DeployRecord{
				Image:     es.CurrentImage,
				Timestamp: metav1.NewTime(r.clock().Now()),
			}
			es.DeployHistory = append([]mortisev1alpha1.DeployRecord{record}, es.DeployHistory...)
			if len(es.DeployHistory) > maxDeployHistory {
				es.DeployHistory = es.DeployHistory[:maxDeployHistory]
			}
		}

		envStatuses = append(envStatuses, es)
	}

	phase := mortisev1alpha1.AppPhaseDeploying
	allReady := true
	for _, es := range envStatuses {
		expectedReplicas := int32(1)
		for _, env := range app.Spec.Environments {
			if env.Name == es.Name && env.Replicas != nil {
				expectedReplicas = *env.Replicas
			}
		}
		if es.ReadyReplicas < expectedReplicas {
			allReady = false
			break
		}
	}
	if allReady && len(envStatuses) > 0 {
		phase = mortisev1alpha1.AppPhaseReady
	}

	app.Status.Phase = phase
	app.Status.Environments = envStatuses
	return r.Status().Update(ctx, app)
}

// needsDeployRecord returns true if a new deploy record should be created —
// either the history is empty or the current image differs from the most recent entry.
func needsDeployRecord(currentImage string, history []mortisev1alpha1.DeployRecord) bool {
	return len(history) == 0 || history[0].Image != currentImage
}

// RollbackDeployment patches the Deployment for the given App + environment back
// to the image at the specified deploy history index.
func (r *AppReconciler) RollbackDeployment(ctx context.Context, app *mortisev1alpha1.App, envName string, historyIndex int) error {
	var envStatus *mortisev1alpha1.EnvironmentStatus
	for i := range app.Status.Environments {
		if app.Status.Environments[i].Name == envName {
			envStatus = &app.Status.Environments[i]
			break
		}
	}
	if envStatus == nil {
		return fmt.Errorf("environment %q not found in app status", envName)
	}
	if historyIndex < 0 || historyIndex >= len(envStatus.DeployHistory) {
		return fmt.Errorf("deploy history index %d out of range (len=%d)", historyIndex, len(envStatus.DeployHistory))
	}

	target := envStatus.DeployHistory[historyIndex]
	rollbackImage := target.Image
	if target.Digest != "" {
		// Use digest for deterministic rollback when available.
		rollbackImage = target.Digest
	}

	name := deploymentName(app.Name, envName)
	var dep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &dep); err != nil {
		return fmt.Errorf("get deployment %s: %w", name, err)
	}

	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("deployment %s has no containers", name)
	}

	dep.Spec.Template.Spec.Containers[0].Image = rollbackImage
	return r.Update(ctx, &dep)
}

func deploymentName(app, env string) string {
	return fmt.Sprintf("%s-%s", app, env)
}

func serviceName(app, env string) string {
	return fmt.Sprintf("%s-%s", app, env)
}

func ingressName(app, env string) string {
	return fmt.Sprintf("%s-%s", app, env)
}

func appLabels(app, env string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       app,
		"app.kubernetes.io/managed-by": "mortise",
		"mortise.dev/environment":      env,
	}
}

func toEnvVars(envs []mortisev1alpha1.EnvVar) []corev1.EnvVar {
	var result []corev1.EnvVar
	for _, e := range envs {
		ev := corev1.EnvVar{Name: e.Name, Value: e.Value}
		if e.ValueFrom != nil && e.ValueFrom.SecretRef != "" {
			ev.ValueFrom = &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.ValueFrom.SecretRef},
					Key:                  e.Name,
				},
			}
			ev.Value = ""
		}
		result = append(result, ev)
	}
	return result
}

func toResourceRequirements(r mortisev1alpha1.ResourceRequirements) corev1.ResourceRequirements {
	req := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}
	if r.CPU != "" {
		q := resource.MustParse(r.CPU)
		req.Requests[corev1.ResourceCPU] = q
		req.Limits[corev1.ResourceCPU] = q
	}
	if r.Memory != "" {
		q := resource.MustParse(r.Memory)
		req.Requests[corev1.ResourceMemory] = q
		req.Limits[corev1.ResourceMemory] = q
	}
	return req
}

func toVolumesAndMounts(app *mortisev1alpha1.App) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	for _, v := range app.Spec.Storage {
		volumes = append(volumes, corev1.Volume{
			Name: v.Name,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("%s-%s", app.Name, v.Name),
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      v.Name,
			MountPath: v.MountPath,
		})
	}

	return volumes, mounts
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.App{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&networkingv1.Ingress{}).
		Named("app").
		Complete(r)
}

// ensure ptr is available
var _ = ptr.To[int32]
