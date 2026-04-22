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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// singletonName is the required metadata.name for the singleton PlatformConfig.
const singletonName = "platform"

// PlatformConfigReconciler reconciles a PlatformConfig object
type PlatformConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=platformconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=platformconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=platformconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *PlatformConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pc mortisev1alpha1.PlatformConfig
	if err := r.Get(ctx, req.NamespacedName, &pc); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Enforce singleton: only the instance named "platform" is valid.
	if pc.Name != singletonName {
		log.Info("rejecting non-singleton PlatformConfig", "name", pc.Name)
		return ctrl.Result{}, r.markFailed(ctx, &pc, "InvalidName",
			fmt.Sprintf("PlatformConfig must be named %q; got %q", singletonName, pc.Name))
	}

	// Validate optional registry credentials secret.
	if pc.Spec.Registry.CredentialsSecretRef != nil {
		if err := validateSecretRef(ctx, r.Client, *pc.Spec.Registry.CredentialsSecretRef, "spec.registry.credentialsSecretRef"); err != nil {
			log.Info("registry credentials secret ref invalid", "error", err)
			return ctrl.Result{}, r.markFailed(ctx, &pc, "SecretNotFound", err.Error())
		}
	}

	// Validate optional BuildKit TLS secret.
	if pc.Spec.Build.TLSSecretRef != nil {
		if err := validateSecretRef(ctx, r.Client, *pc.Spec.Build.TLSSecretRef, "spec.build.tlsSecretRef"); err != nil {
			log.Info("buildkit TLS secret ref invalid", "error", err)
			return ctrl.Result{}, r.markFailed(ctx, &pc, "SecretNotFound", err.Error())
		}
	}

	// Validate optional observability adapter token secrets.
	if pc.Spec.Observability.LogsAdapterTokenSecretRef != nil {
		if err := validateSecretRef(ctx, r.Client, *pc.Spec.Observability.LogsAdapterTokenSecretRef, "spec.observability.logsAdapterTokenSecretRef"); err != nil {
			log.Info("logs adapter token secret ref invalid", "error", err)
			return ctrl.Result{}, r.markFailed(ctx, &pc, "SecretNotFound", err.Error())
		}
	}
	if pc.Spec.Observability.MetricsAdapterTokenSecretRef != nil {
		if err := validateSecretRef(ctx, r.Client, *pc.Spec.Observability.MetricsAdapterTokenSecretRef, "spec.observability.metricsAdapterTokenSecretRef"); err != nil {
			log.Info("metrics adapter token secret ref invalid", "error", err)
			return ctrl.Result{}, r.markFailed(ctx, &pc, "SecretNotFound", err.Error())
		}
	}

	return ctrl.Result{}, r.markReady(ctx, &pc)
}

func (r *PlatformConfigReconciler) markReady(ctx context.Context, pc *mortisev1alpha1.PlatformConfig) error {
	pc.Status.Phase = mortisev1alpha1.PlatformConfigPhaseReady
	meta.SetStatusCondition(&pc.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "PlatformConfig is ready",
		ObservedGeneration: pc.Generation,
	})
	if err := r.Status().Update(ctx, pc); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

func (r *PlatformConfigReconciler) markFailed(ctx context.Context, pc *mortisev1alpha1.PlatformConfig, reason, msg string) error {
	pc.Status.Phase = mortisev1alpha1.PlatformConfigPhaseFailed
	meta.SetStatusCondition(&pc.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: pc.Generation,
	})
	if err := r.Status().Update(ctx, pc); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PlatformConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.PlatformConfig{}).
		Named("platformconfig").
		Complete(r)
}
