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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
)

// forbiddenFastRequeue classifies a Forbidden error inside a young namespace
// as an RBAC-propagation race (the per-namespace RoleBinding created during
// bootstrap has not reached the authorizer yet). It returns a fast errorless
// requeue so the workqueue's exponential rate limiter is not fed — a streak
// of denials during a busy bootstrap would otherwise compound into requeue
// delays of minutes, far beyond the actual propagation window. The second
// return is false when the error is not Forbidden, the namespace cannot be
// read, or the namespace is old enough that the denial is a genuine
// misconfiguration the caller must surface.
func forbiddenFastRequeue(ctx context.Context, c client.Reader, clk clock.Clock, nsName string, err error) (ctrl.Result, bool) {
	if !errors.IsForbidden(err) {
		return ctrl.Result{}, false
	}
	var ns corev1.Namespace
	if getErr := c.Get(ctx, client.ObjectKey{Name: nsName}, &ns); getErr != nil {
		// A Forbidden write into a namespace that doesn't exist yet is the
		// earliest phase of the same bootstrap race: the authorizer denies
		// before existence is checked, so this races namespace creation
		// itself. This arm is deliberately unbounded HERE: the App
		// controller bounds it via the NamespacePending condition in
		// envResourceError, and the remaining callers cannot loop on it —
		// the PE controller checks previewNamespaceNotReadyError before this
		// classifier, and the Project controller passes a namespace it
		// ensured earlier in the same reconcile.
		if errors.IsNotFound(getErr) {
			return ctrl.Result{RequeueAfter: rbacPropagationRequeue}, true
		}
		return ctrl.Result{}, false
	}
	if clk.Since(ns.CreationTimestamp.Time) >= rbacPropagationWindow {
		return ctrl.Result{}, false
	}
	return ctrl.Result{RequeueAfter: rbacPropagationRequeue}, true
}
