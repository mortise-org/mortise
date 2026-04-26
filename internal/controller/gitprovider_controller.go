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
	"os"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// providerClientIDEnvVars maps each GitProvider type to the environment
// variable that can supply a default clientID (matches the mapping in
// internal/api/device_flow.go).
var providerClientIDEnvVars = map[mortisev1alpha1.GitProviderType]string{
	mortisev1alpha1.GitProviderTypeGitHub: "MORTISE_GITHUB_CLIENT_ID",
	mortisev1alpha1.GitProviderTypeGitLab: "MORTISE_GITLAB_CLIENT_ID",
	mortisev1alpha1.GitProviderTypeGitea:  "MORTISE_GITEA_CLIENT_ID",
}

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

	// Backfill clientID from the environment when it is absent from the spec.
	// This handles GitProvider CRs created before clientID was introduced or
	// adopted via manual Helm labeling (issue #139).
	if gp.Spec.ClientID == "" {
		if envVar, ok := providerClientIDEnvVars[gp.Spec.Type]; ok {
			if clientID := os.Getenv(envVar); clientID != "" {
				log.Info("backfilling clientID from env", "envVar", envVar)
				patch := client.MergeFrom(gp.DeepCopy())
				gp.Spec.ClientID = clientID
				if err := r.Patch(ctx, &gp, patch); err != nil {
					return ctrl.Result{}, fmt.Errorf("patch clientID: %w", err)
				}
				return ctrl.Result{Requeue: true}, nil
			}
		}
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
		if err := validateSecretRef(ctx, r.Client, *gp.Spec.ClientSecretRef, "spec.clientSecretRef"); err != nil {
			log.Info("client secret ref invalid", "error", err)
			return ctrl.Result{}, r.markFailed(ctx, &gp, "SecretNotFound", err.Error())
		}
	}
	if gp.Spec.WebhookSecretRef != nil {
		if err := validateSecretRef(ctx, r.Client, *gp.Spec.WebhookSecretRef, "spec.webhookSecretRef"); err != nil {
			log.Info("webhook secret ref invalid", "error", err)
			return ctrl.Result{}, r.markFailed(ctx, &gp, "SecretNotFound", err.Error())
		}
	}

	return ctrl.Result{}, r.markReady(ctx, &gp)
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
