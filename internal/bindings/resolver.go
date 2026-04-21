package bindings

import (
	"context"
	"fmt"
	"strings"

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
// credentials plus auto-generated host, port, and URL vars.
//
// All injected env var names are prefixed with the bound app name in
// UPPER_SNAKE_CASE to avoid collisions when binding to multiple apps.
// For example, binding to "database" injects DATABASE_HOST, DATABASE_PORT,
// DATABASE_URL (if image is recognized), plus any declared credentials
// like DATABASE_PASSWORD.
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

		// Compute host and port for the bound service.
		var hostValue, portValue string
		if boundApp.Spec.Source.Type == mortisev1alpha1.SourceTypeExternal && boundApp.Spec.Source.External != nil {
			hostValue = boundApp.Spec.Source.External.Host
			if boundApp.Spec.Source.External.Port > 0 {
				portValue = fmt.Sprintf("%d", boundApp.Spec.Source.External.Port)
			}
		} else {
			if !boundAppEnabledIn(&boundApp, binderEnv) {
				return nil, fmt.Errorf("binding %q: bound app has no enabled instance in env %q of project %q",
					b.Ref, binderEnv, targetProject)
			}
			hostValue = fmt.Sprintf("%s.%s.svc.cluster.local", boundApp.Name, envNs)
			port := boundApp.Spec.Network.Port
			if port == 0 {
				port = 8080
			}
			portValue = fmt.Sprintf("%d", port)
		}

		prefix := toEnvPrefix(b.Ref)

		// Always inject HOST and PORT.
		result = append(result,
			corev1.EnvVar{Name: prefix + "_HOST", Value: hostValue},
			corev1.EnvVar{Name: prefix + "_PORT", Value: portValue},
		)

		// Auto-generate a connection URL for recognized images.
		if url := autoURL(boundApp.Spec.Source.Image, hostValue, portValue); url != "" {
			result = append(result, corev1.EnvVar{Name: prefix + "_URL", Value: url})
		}

		// Inject declared credentials with prefixed names.
		if len(boundApp.Spec.Credentials) > 0 {
			secretName := fmt.Sprintf("%s-credentials", boundApp.Name)
			for _, cred := range boundApp.Spec.Credentials {
				if cred.Name == "host" || cred.Name == "port" {
					continue // already injected above
				}
				result = append(result, corev1.EnvVar{
					Name: prefix + "_" + strings.ToUpper(cred.Name),
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

// toEnvPrefix converts an app name like "my-database" to "MY_DATABASE".
func toEnvPrefix(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// autoURL generates a connection URL for well-known images.
// Returns "" if the image isn't recognized.
func autoURL(image, host, port string) string {
	img := strings.ToLower(image)
	switch {
	case strings.HasPrefix(img, "postgres:") || strings.HasPrefix(img, "supabase/postgres:"):
		return fmt.Sprintf("postgres://postgres@%s:%s/postgres?sslmode=disable", host, port)
	case strings.HasPrefix(img, "redis:"):
		return fmt.Sprintf("redis://%s:%s", host, port)
	case strings.HasPrefix(img, "mysql:") || strings.HasPrefix(img, "mariadb:"):
		return fmt.Sprintf("mysql://root@%s:%s/mysql", host, port)
	case strings.HasPrefix(img, "mongo:"):
		return fmt.Sprintf("mongodb://%s:%s", host, port)
	}
	return ""
}

// boundAppEnabledIn reports whether the bound App has an override for env
// that explicitly disables it.
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
