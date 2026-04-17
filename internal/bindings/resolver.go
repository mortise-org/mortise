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
			// Extract the binder's project name from its namespace.
			binderProject := ""
			if len(namespace) > len(projectNamespacePrefix) {
				binderProject = namespace[len(projectNamespacePrefix):]
			}
			if b.Project != binderProject {
				return nil, fmt.Errorf("cross-project binding to %q in project %q is not supported in v1; "+
					"bindings can only reference Apps in the same project (see github.com/MC-Meesh/mortise/issues/2)",
					b.Ref, b.Project)
			}
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

		// Resolve host and port differently for external vs managed apps.
		var hostValue, portValue string
		if boundApp.Spec.Source.Type == mortisev1alpha1.SourceTypeExternal && boundApp.Spec.Source.External != nil {
			hostValue = boundApp.Spec.Source.External.Host
			if boundApp.Spec.Source.External.Port > 0 {
				portValue = fmt.Sprintf("%d", boundApp.Spec.Source.External.Port)
			}
		} else {
			if len(boundApp.Spec.Environments) == 0 {
				return nil, fmt.Errorf("bound app %q has no environments", b.Ref)
			}
			svcName := fmt.Sprintf("%s-%s", boundApp.Name, boundApp.Spec.Environments[0].Name)
			hostValue = fmt.Sprintf("%s.%s.svc.cluster.local", svcName, ns)
			portValue = "80"
		}

		secretName := fmt.Sprintf("%s-credentials", boundApp.Name)

		for _, cred := range boundApp.Spec.Credentials {
			switch cred.Name {
			case "host":
				result = append(result, corev1.EnvVar{
					Name:  "host",
					Value: hostValue,
				})
			case "port":
				result = append(result, corev1.EnvVar{
					Name:  "port",
					Value: portValue,
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
