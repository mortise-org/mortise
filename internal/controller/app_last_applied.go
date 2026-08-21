package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// lastAppliedAnnotation is written by client-side `kubectl apply`. It holds a
// verbatim JSON snapshot of the applied spec — including every plaintext env
// and credential literal — and is readable by anyone who can `kubectl get app`,
// a wider audience than those who can read Secrets (CAI-151).
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// redactedValue replaces a plaintext literal in the annotation. It is a fixed
// string so re-reconciling an already-redacted App is a no-op.
const redactedValue = "[redacted by mortise]"

// redactLastApplied strips plaintext values out of a last-applied-configuration
// snapshot, leaving its structure intact. Returns the rewritten JSON and
// whether anything changed.
//
// It redacts values rather than deleting the annotation outright, and the
// distinction is load-bearing. `kubectl apply` uses this annotation as the only
// record of what the user previously declared, and computes deletions from it
// via each list's merge key — `name`, for both env and credentials. Deleting
// the annotation would drop kubectl to a two-way merge, at which point fields
// removed from the user's YAML stop being removed from the live object: a
// removal-doesn't-remove bug, which is the defect family this annotation's leak
// already sits in. Redaction preserves every merge key, so deletion detection
// keeps working, while the secrets stop being there.
//
// This bounds the exposure window rather than closing it: the next client-side
// apply rewrites the annotation from the user's file, and the operator redacts
// it again on the following reconcile. Server-side apply is the actual fix,
// because it never writes this annotation at all.
func redactLastApplied(raw string) (string, bool, error) {
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return "", false, fmt.Errorf("parse last-applied-configuration: %w", err)
	}

	spec, ok := snapshot["spec"].(map[string]any)
	if !ok {
		return "", false, nil
	}

	changed := false
	redactList := func(entries any) {
		list, ok := entries.([]any)
		if !ok {
			return
		}
		for _, entry := range list {
			item, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			v, present := item["value"]
			if !present {
				continue
			}
			if s, ok := v.(string); ok && (s == "" || s == redactedValue) {
				continue
			}
			item["value"] = redactedValue
			changed = true
		}
	}

	redactList(spec["credentials"])
	if envs, ok := spec["environments"].([]any); ok {
		for _, e := range envs {
			env, ok := e.(map[string]any)
			if !ok {
				continue
			}
			redactList(env["env"])
		}
	}

	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(snapshot)
	if err != nil {
		return "", false, fmt.Errorf("re-marshal last-applied-configuration: %w", err)
	}
	return string(out), true, nil
}

// redactAppLastApplied rewrites an App's last-applied-configuration annotation
// in place if it carries plaintext literals. A no-op when the annotation is
// absent or already redacted — this runs on every reconcile, so it must not
// write unless something actually changed, or the App watch would spin.
func (r *AppReconciler) redactAppLastApplied(ctx context.Context, key types.NamespacedName) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh mortisev1alpha1.App
		if err := r.Get(ctx, key, &fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		raw, ok := fresh.Annotations[lastAppliedAnnotation]
		if !ok || raw == "" {
			return nil
		}
		redacted, changed, err := redactLastApplied(raw)
		if err != nil {
			// A snapshot we can't parse is not ours to rewrite; leave it and
			// say so rather than guessing at its shape.
			logf.FromContext(ctx).Error(err, "cannot redact last-applied-configuration",
				"app", key.String())
			return nil
		}
		if !changed {
			return nil
		}
		fresh.Annotations[lastAppliedAnnotation] = redacted
		logf.FromContext(ctx).Info(
			"redacted plaintext literals from last-applied-configuration; "+
				"use `kubectl apply --server-side` to stop this annotation being written (CAI-151)",
			"app", key.String())
		return r.Update(ctx, &fresh)
	})
}
