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

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
)

// TeamReconciler reconciles a Team object.
//
// v1 scope: enforce the `default-team` singleton. Any other instance is marked
// Failed with reason InvalidName. When v2 splits the implicit team into
// multiple teams, this controller gains real work.
type TeamReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=teams,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=teams/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=teams/finalizers,verbs=update

func (r *TeamReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var team mortisev1alpha1.Team
	if err := r.Get(ctx, req.NamespacedName, &team); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if team.Name != constants.DefaultTeamName {
		log.Info("rejecting non-singleton Team", "name", team.Name)
		return ctrl.Result{}, r.markFailed(ctx, &team, "InvalidName",
			fmt.Sprintf("Team must be named %q in v1; got %q", constants.DefaultTeamName, team.Name))
	}

	return ctrl.Result{}, r.markReady(ctx, &team)
}

func (r *TeamReconciler) markReady(ctx context.Context, team *mortisev1alpha1.Team) error {
	team.Status.Phase = mortisev1alpha1.TeamPhaseReady
	meta.SetStatusCondition(&team.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "Team is ready",
		ObservedGeneration: team.Generation,
	})
	if err := r.Status().Update(ctx, team); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

func (r *TeamReconciler) markFailed(ctx context.Context, team *mortisev1alpha1.Team, reason, msg string) error {
	team.Status.Phase = mortisev1alpha1.TeamPhaseFailed
	meta.SetStatusCondition(&team.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: team.Generation,
	})
	if err := r.Status().Update(ctx, team); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TeamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.Team{}).
		Named("team").
		Complete(r)
}
