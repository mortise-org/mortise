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
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/platformconfig"
)

// singletonName is the required metadata.name for the singleton PlatformConfig.
const singletonName = "platform"

// Condition types surfacing configuration problems on the CR itself instead
// of only in operator logs (#449). No hot reload in v1 — these conditions
// are the visibility half; the operator still consumes registry/build config
// once at boot.
const (
	// pcRegistryPullConfigCondition is False when the registry URL is
	// cluster-internal and no kubelet-facing pullURL is set: fresh deploys
	// will ImagePullBackOff.
	pcRegistryPullConfigCondition = "RegistryPullConfig"
	// pcConfigAppliedCondition is False when the running operator booted
	// from a different config than the current spec (or from the env
	// fallback before this CR existed): a restart is required to apply.
	pcConfigAppliedCondition = "ConfigApplied"
)

// PlatformConfigReconciler reconciles a PlatformConfig object
type PlatformConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// BootConfig is the resolved config the operator actually consumed at
	// startup; nil means the operator booted on the env fallback (no
	// PlatformConfig existed yet).
	BootConfig *platformconfig.Config
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

	r.setRegistryPullCondition(&pc)
	r.setConfigAppliedCondition(ctx, &pc)

	return ctrl.Result{}, r.markReady(ctx, &pc)
}

// setRegistryPullCondition mirrors (and sharpens) the boot-time warning: a
// cluster-internal registry URL with no pullURL means kubelet-facing image
// refs fall back to the push URL, which nodes cannot resolve.
func (r *PlatformConfigReconciler) setRegistryPullCondition(pc *mortisev1alpha1.PlatformConfig) {
	url := pc.Spec.Registry.URL
	if url != "" && pc.Spec.Registry.PullURL == "" && platformconfig.LooksClusterInternal(url) {
		meta.SetStatusCondition(&pc.Status.Conditions, metav1.Condition{
			Type:               pcRegistryPullConfigCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "PullURLMissing",
			Message:            "spec.registry.url is cluster-internal and spec.registry.pullURL is empty; kubelet-facing image refs will use the push URL, which nodes cannot resolve — set spec.registry.pullURL, then restart the operator",
			ObservedGeneration: pc.Generation,
		})
		return
	}
	meta.RemoveStatusCondition(&pc.Status.Conditions, pcRegistryPullConfigCondition)
}

// setConfigAppliedCondition compares the config the operator booted with
// (snapshot handed over by main) against the current spec. Drift — or an
// env-fallback boot from before this CR existed — surfaces as
// ConfigApplied=False/RestartRequired. There is deliberately no hot reload.
func (r *PlatformConfigReconciler) setConfigAppliedCondition(ctx context.Context, pc *mortisev1alpha1.PlatformConfig) {
	if r.BootConfig == nil {
		meta.SetStatusCondition(&pc.Status.Conditions, metav1.Condition{
			Type:               pcConfigAppliedCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "RestartRequired",
			Message:            "the operator started before this PlatformConfig existed and is running on environment-variable fallback config; restart the operator deployment to apply this configuration",
			ObservedGeneration: pc.Generation,
		})
		return
	}
	current, err := platformconfig.Load(ctx, r.Client)
	if err != nil {
		// Can't compare — leave whatever state exists rather than flapping.
		return
	}
	if changed := platformconfig.DiffSections(r.BootConfig, current); len(changed) > 0 {
		meta.SetStatusCondition(&pc.Status.Conditions, metav1.Condition{
			Type:               pcConfigAppliedCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "RestartRequired",
			Message:            fmt.Sprintf("configuration changed since the operator started (changed: %s); the running operator still uses the old settings — restart the operator deployment to apply (kubectl -n mortise-system rollout restart deployment/mortise)", strings.Join(changed, ", ")),
			ObservedGeneration: pc.Generation,
		})
		return
	}
	meta.SetStatusCondition(&pc.Status.Conditions, metav1.Condition{
		Type:               pcConfigAppliedCondition,
		Status:             metav1.ConditionTrue,
		Reason:             "Applied",
		Message:            "the running operator was started with this configuration",
		ObservedGeneration: pc.Generation,
	})
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
