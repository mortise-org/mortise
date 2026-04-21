package bindings

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
)

// Resolver resolves bindings for an App environment into literal env vars.
type Resolver struct {
	Client client.Reader
}

// ResolvedVar is a fully-resolved env var with a plain string value.
// No SecretKeyRef — the resolver reads credential Secrets directly so
// callers don't need cross-namespace Secret access.
type ResolvedVar struct {
	Name  string
	Value string
}

// Resolve looks up each bound App and returns fully-resolved env vars for
// all declared credentials plus auto-generated host, port, and URL vars.
//
// All names are prefixed with the bound app name in UPPER_SNAKE_CASE to
// avoid collisions (e.g. binding "database" → DATABASE_HOST, DATABASE_URL).
//
// Credential values are read from the bound app's {name}-credentials Secret
// in the project's env namespace.
func (r *Resolver) Resolve(
	ctx context.Context,
	project string,
	env string,
	bindings []mortisev1alpha1.Binding,
) ([]ResolvedVar, error) {
	var result []ResolvedVar

	controlNs := constants.ControlNamespace(project)
	envNs := constants.EnvNamespace(project, env)

	for _, b := range bindings {
		var boundApp mortisev1alpha1.App
		key := client.ObjectKey{Name: b.Ref, Namespace: controlNs}
		if err := r.Client.Get(ctx, key, &boundApp); err != nil {
			return nil, fmt.Errorf("resolve binding %q in project %q: %w", b.Ref, project, err)
		}

		var hostValue, portValue string
		if boundApp.Spec.Source.Type == mortisev1alpha1.SourceTypeExternal && boundApp.Spec.Source.External != nil {
			hostValue = boundApp.Spec.Source.External.Host
			if boundApp.Spec.Source.External.Port > 0 {
				portValue = fmt.Sprintf("%d", boundApp.Spec.Source.External.Port)
			}
		} else {
			if !boundAppEnabledIn(&boundApp, env) {
				return nil, fmt.Errorf("binding %q: bound app has no enabled instance in env %q of project %q",
					b.Ref, env, project)
			}
			hostValue = fmt.Sprintf("%s.%s.svc.cluster.local", boundApp.Name, envNs)
			port := boundApp.Spec.Network.Port
			if port == 0 {
				port = 8080
			}
			portValue = fmt.Sprintf("%d", port)
		}

		prefix := toEnvPrefix(b.Ref)

		result = append(result,
			ResolvedVar{Name: prefix + "_HOST", Value: hostValue},
			ResolvedVar{Name: prefix + "_PORT", Value: portValue},
		)

		if url := autoURL(boundApp.Spec.Source.Image, hostValue, portValue); url != "" {
			result = append(result, ResolvedVar{Name: prefix + "_URL", Value: url})
		}

		var extraCreds []mortisev1alpha1.Credential
		for _, cred := range boundApp.Spec.Credentials {
			if cred.Name != "host" && cred.Name != "port" {
				extraCreds = append(extraCreds, cred)
			}
		}
		if len(extraCreds) > 0 {
			secretName := fmt.Sprintf("%s-credentials", boundApp.Name)
			var credSecret corev1.Secret
			secretKey := types.NamespacedName{Namespace: envNs, Name: secretName}
			if err := r.Client.Get(ctx, secretKey, &credSecret); err != nil {
				return nil, fmt.Errorf("resolve credentials for binding %q: secret %s/%s: %w",
					b.Ref, envNs, secretName, err)
			}
			for _, cred := range extraCreds {
				val := string(credSecret.Data[cred.Name])
				result = append(result, ResolvedVar{
					Name:  prefix + "_" + strings.ToUpper(cred.Name),
					Value: val,
				})
			}
		}
	}

	return result, nil
}

var envPrefixSanitizer = regexp.MustCompile(`[^A-Z0-9_]`)

// toEnvPrefix converts an app name to a valid POSIX env var prefix.
// Replaces hyphens, dots, and other non-alphanumeric chars with underscores.
// Strips leading digits so the result is a valid identifier prefix.
func toEnvPrefix(name string) string {
	upper := strings.ToUpper(name)
	sanitized := envPrefixSanitizer.ReplaceAllString(upper, "_")
	// Strip leading digits/underscores to ensure valid identifier.
	sanitized = strings.TrimLeft(sanitized, "0123456789_")
	if sanitized == "" {
		return "BINDING"
	}
	return sanitized
}

// imageBaseName extracts the image name without registry prefix or tag.
// "docker.io/library/postgres:16" → "postgres"
// "postgres:16" → "postgres"
func imageBaseName(image string) string {
	img := strings.ToLower(image)
	if i := strings.LastIndex(img, "/"); i >= 0 {
		img = img[i+1:]
	}
	if i := strings.Index(img, ":"); i >= 0 {
		img = img[:i]
	}
	return img
}

// autoURL generates a connection URL for well-known images.
func autoURL(image, host, port string) string {
	if host == "" || port == "" {
		return ""
	}
	switch imageBaseName(image) {
	case "postgres":
		return fmt.Sprintf("postgres://%s:%s?sslmode=disable", host, port)
	case "redis":
		return fmt.Sprintf("redis://%s:%s", host, port)
	case "mysql", "mariadb":
		return fmt.Sprintf("mysql://%s:%s", host, port)
	case "mongo":
		return fmt.Sprintf("mongodb://%s:%s", host, port)
	}
	return ""
}

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
