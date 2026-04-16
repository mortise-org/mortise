package bindings

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// projectNamespacePrefix is the label prefix applied to a Project's backing
// namespace. Centralised here so the resolver and the project controller
// agree on the naming convention.
const projectNamespacePrefix = "project-"

// Resolver resolves bindings for an App environment into env vars
// that can be injected into a Deployment's container spec.
type Resolver struct {
	Client client.Reader
}

// Resolve looks up each bound App and returns env vars for all declared credentials.
// Service DNS facts (host, port) are literal values; all other credential keys
// are secretKeyRef projections to the bound App's credentials secret.
//
// If Binding.Project is set, the ref is resolved inside the namespace
// `project-{project}`. Otherwise the ref is resolved in the binder's own
// namespace (same-project binding — the common case).
func (r *Resolver) Resolve(ctx context.Context, namespace string, bindings []mortisev1alpha1.Binding) ([]corev1.EnvVar, error) {
	var result []corev1.EnvVar

	for _, b := range bindings {
		ns := namespace
		if b.Project != "" {
			ns = projectNamespacePrefix + b.Project
		}

		var boundApp mortisev1alpha1.App
		key := client.ObjectKey{Name: b.Ref, Namespace: ns}
		if err := r.Client.Get(ctx, key, &boundApp); err != nil {
			return nil, fmt.Errorf("resolve binding %q: %w", b.Ref, err)
		}

		if len(boundApp.Spec.Credentials) == 0 {
			continue
		}

		if len(boundApp.Spec.Environments) == 0 {
			return nil, fmt.Errorf("bound app %q has no environments", b.Ref)
		}

		svcName := fmt.Sprintf("%s-%s", boundApp.Name, boundApp.Spec.Environments[0].Name)
		svcHost := fmt.Sprintf("%s.%s.svc.cluster.local", svcName, ns)
		secretName := fmt.Sprintf("%s-credentials", boundApp.Name)

		// NOTE: Issue #2 (cross-project bindings) remains open. When b.Project is
		// set, we still emit a plain SecretKeyRef pointing at a Secret in the
		// bound app's project namespace — the kubelet resolves secretKeyRef in
		// the Pod's own namespace, so the Pod will fail to start with
		// CreateContainerConfigError. Do not "fix" that here by renaming the
		// Secret or dropping the ref; the resolution is a Secret-replication
		// or projected-volume design, tracked separately.
		for _, cred := range boundApp.Spec.Credentials {
			switch cred.Name {
			case "host":
				result = append(result, corev1.EnvVar{
					Name:  "host",
					Value: svcHost,
				})
			case "port":
				result = append(result, corev1.EnvVar{
					Name:  "port",
					Value: "80",
				})
			default:
				result = append(result, corev1.EnvVar{
					Name: cred.Name,
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
							Key:                  cred.Name,
						},
					},
				})
			}
		}
	}

	return result, nil
}
