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
// It redacts rather than deleting the annotation, and the reason is narrower
// than it first appears. An App is a custom resource, so kubectl does NOT use
// strategic merge patch on it: the API server publishes no merge-key metadata
// for CRD lists, and kubectl falls back to an RFC 7386 JSON merge patch, in
// which arrays are atomic. Measured against a real cluster with this CRD:
//
//	warning: OpenAPI V3 path does not support strategic merge patch -
//	  group: mortise.mortise.dev, version v1alpha1, kind App
//	PATCH ...?fieldManager=kubectl-client-side-apply
//	        Content-Type: application/merge-patch+json
//
// So list entries are not deletion-detected by a merge key at all. kubectl
// replaces the whole array from the file, and removing an env entry propagates
// even with the annotation absent.
//
// What the annotation IS load-bearing for is whole-field deletion: with it
// gone, removing an entire field from the file (`sharedVars`, say) does not
// propagate and the old value survives on the live object. That is the
// removal-doesn't-remove bug this must not cause, so the annotation stays and
// only its secrets go.
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

	// redactField blanks one named string field on every element of a list.
	redactField := func(entries any, field string) {
		list, ok := entries.([]any)
		if !ok {
			return
		}
		for _, entry := range list {
			item, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			v, present := item[field]
			if !present {
				continue
			}
			if str, ok := v.(string); ok && (str == "" || str == redactedValue) {
				continue
			}
			item[field] = redactedValue
			changed = true
		}
	}

	// redactMap blanks every value of a string-keyed map field.
	redactMap := func(parent map[string]any, field string) {
		m, ok := parent[field].(map[string]any)
		if !ok {
			return
		}
		for k, v := range m {
			if str, ok := v.(string); ok && (str == "" || str == redactedValue) {
				continue
			}
			m[k] = redactedValue
			changed = true
		}
	}

	// The annotation is a verbatim copy of the WHOLE spec, so every field that
	// can hold a literal has to be covered, not only the two that motivated the
	// original report. sharedVars is the same EnvVar type as
	// environments[].env and its own doc names SENTRY_DSN as an example; build
	// args routinely carry NPM_TOKEN-class values; configFiles hold arbitrary
	// file content.
	redactField(spec["credentials"], "value")
	redactField(spec["sharedVars"], "value")
	redactField(spec["configFiles"], "content")

	if source, ok := spec["source"].(map[string]any); ok {
		if build, ok := source["build"].(map[string]any); ok {
			redactMap(build, "args")
		}
	}

	if envs, ok := spec["environments"].([]any); ok {
		for _, e := range envs {
			env, ok := e.(map[string]any)
			if !ok {
				continue
			}
			redactField(env["env"], "value")
			redactMap(env, "buildArgs")
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
