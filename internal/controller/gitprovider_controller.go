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
	"net/url"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// GitProviderReconciler reconciles a GitProvider object
type GitProviderReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=gitproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=gitproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=gitproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *GitProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var gp mortisev1alpha1.GitProvider
	if err := r.Get(ctx, req.NamespacedName, &gp); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Validate the type field.
	switch gp.Spec.Type {
	case mortisev1alpha1.GitProviderTypeGitHub,
		mortisev1alpha1.GitProviderTypeGitLab,
		mortisev1alpha1.GitProviderTypeGitea:
		// valid
	default:
		log.Info("unknown provider type", "type", gp.Spec.Type)
		return ctrl.Result{}, r.markFailed(ctx, &gp, "UnknownType",
			fmt.Sprintf("spec.type %q is not one of: github, gitlab, gitea", gp.Spec.Type))
	}

	// Validate host is a parseable URL.
	if _, err := url.ParseRequestURI(gp.Spec.Host); err != nil {
		return ctrl.Result{}, r.markFailed(ctx, &gp, "InvalidHost",
			fmt.Sprintf("spec.host %q is not a valid URL: %v", gp.Spec.Host, err))
	}

	// Validate optional secret refs.
	if gp.Spec.ClientSecretRef != nil {
		if err := r.validateSecretRef(ctx, *gp.Spec.ClientSecretRef, "spec.clientSecretRef"); err != nil {
			log.Info("client secret ref invalid", "error", err)
			return ctrl.Result{}, r.markFailed(ctx, &gp, "SecretNotFound", err.Error())
		}
	}
	if gp.Spec.WebhookSecretRef != nil {
		if err := r.validateSecretRef(ctx, *gp.Spec.WebhookSecretRef, "spec.webhookSecretRef"); err != nil {
			log.Info("webhook secret ref invalid", "error", err)
			return ctrl.Result{}, r.markFailed(ctx, &gp, "SecretNotFound", err.Error())
		}
	}

	return ctrl.Result{}, r.markReady(ctx, &gp)
}

// validateSecretRef returns an error if the secret does not exist or the key is absent.
func (r *GitProviderReconciler) validateSecretRef(ctx context.Context, ref mortisev1alpha1.SecretRef, desc string) error {
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("%s: secret %s/%s not found", desc, ref.Namespace, ref.Name)
		}
		return fmt.Errorf("%s: get secret %s/%s: %w", desc, ref.Namespace, ref.Name, err)
	}
	if _, ok := secret.Data[ref.Key]; !ok {
		return fmt.Errorf("%s: key %q not present in secret %s/%s", desc, ref.Key, ref.Namespace, ref.Name)
	}
	return nil
}

func (r *GitProviderReconciler) markReady(ctx context.Context, gp *mortisev1alpha1.GitProvider) error {
	gp.Status.Phase = mortisev1alpha1.GitProviderPhaseReady
	meta.SetStatusCondition(&gp.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "GitProvider is ready",
		ObservedGeneration: gp.Generation,
	})
	if err := r.Status().Update(ctx, gp); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

func (r *GitProviderReconciler) markFailed(ctx context.Context, gp *mortisev1alpha1.GitProvider, reason, msg string) error {
	gp.Status.Phase = mortisev1alpha1.GitProviderPhaseFailed
	meta.SetStatusCondition(&gp.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: gp.Generation,
	})
	if err := r.Status().Update(ctx, gp); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GitProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.GitProvider{}).
		Named("gitprovider").
		Complete(r)
}
