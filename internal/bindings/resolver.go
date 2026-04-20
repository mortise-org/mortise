package bindings

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
)

// Resolver resolves bindings for an App environment into env vars that
// can be injected into a Deployment's container spec.
type Resolver struct {
	Client client.Reader
}

// Resolve looks up each bound App and returns env vars for all declared
// credentials. `host` / `port` credentials synthesize literal env vars
// pointing at the bound App's in-cluster Service; every other credential
// becomes a SecretKeyRef projection to the bound App's credentials secret.
//
// `binderProject` is the project the binder App belongs to; `binderEnv` is
// the environment the binder is reconciling for. A same-project binding
// (no `Binding.Project`) resolves inside that project's env namespace
// (`pj-{binderProject}-{binderEnv}`). A cross-project binding (`project: Y`)
// resolves inside the target project's matching env namespace
// (`pj-Y-{binderEnv}`) — if Y doesn't declare `binderEnv`, callers should
// either use an External app or split the binding into a shared instance.
//
// The bound App CRD itself always lives in the target project's control
// namespace (`pj-{project}`); we Get it there to read credentials/source,
// then render Service DNS at the env namespace.
func (r *Resolver) Resolve(
	ctx context.Context,
	binderProject string,
	binderEnv string,
	bindings []mortisev1alpha1.Binding,
) ([]corev1.EnvVar, error) {
	var result []corev1.EnvVar

	for _, b := range bindings {
		targetProject := binderProject
		if b.Project != "" {
			targetProject = b.Project
		}

		controlNs := constants.ControlNamespace(targetProject)
		envNs := constants.EnvNamespace(targetProject, binderEnv)

		var boundApp mortisev1alpha1.App
		key := client.ObjectKey{Name: b.Ref, Namespace: controlNs}
		if err := r.Client.Get(ctx, key, &boundApp); err != nil {
			return nil, fmt.Errorf("resolve binding %q in project %q: %w", b.Ref, targetProject, err)
		}

		if len(boundApp.Spec.Credentials) == 0 {
			continue
		}

		var hostValue, portValue string
		if boundApp.Spec.Source.Type == mortisev1alpha1.SourceTypeExternal && boundApp.Spec.Source.External != nil {
			hostValue = boundApp.Spec.Source.External.Host
			if boundApp.Spec.Source.External.Port > 0 {
				portValue = fmt.Sprintf("%d", boundApp.Spec.Source.External.Port)
			}
		} else {
			if !boundAppEnabledIn(&boundApp, binderEnv) {
				return nil, fmt.Errorf("binding %q: bound app has no enabled instance in env %q of project %q "+
					"(use an External app for cross-env shared instances)",
					b.Ref, binderEnv, targetProject)
			}
			hostValue = fmt.Sprintf("%s.%s.svc.cluster.local", boundApp.Name, envNs)
			port := boundApp.Spec.Network.Port
			if port == 0 {
				port = 8080
			}
			portValue = fmt.Sprintf("%d", port)
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

// boundAppEnabledIn reports whether the bound App has an override for env
// that explicitly disables it. A missing override is treated as enabled —
// apps auto-participate in every project env unless opted out.
func boundAppEnabledIn(app *mortisev1alpha1.App, env string) bool {
	for i := range app.Spec.Environments {
		e := &app.Spec.Environments[i]
		if e.Name != env {
			continue
		}
		if e.Enabled != nil && !*e.Enabled {
			return false
		}
		return true
	}
	return true
}
