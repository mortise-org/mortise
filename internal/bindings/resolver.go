package bindings

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
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

// resolvedBinding holds the intermediate state for a single bound app lookup.
type resolvedBinding struct {
	app        *mortisev1alpha1.App
	host       string
	port       string
	prefix     string
	extraCreds []mortisev1alpha1.Credential
	credSecret *corev1.Secret
}

// lookupBinding resolves a single bound app and reads its credentials Secret.
// Returns nil (no error) when the app is missing or disabled — the caller
// decides whether to skip silently (Resolve) or error (ResolveSingle).
func (r *Resolver) lookupBinding(ctx context.Context, project, env, ref string) (*resolvedBinding, error) {
	controlNs := constants.ControlNamespace(project)
	envNs := constants.EnvNamespace(project, env)

	var boundApp mortisev1alpha1.App
	key := client.ObjectKey{Name: ref, Namespace: controlNs}
	if err := r.Client.Get(ctx, key, &boundApp); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve binding %q in project %q: %w", ref, project, err)
	}

	rb := &resolvedBinding{
		app:    &boundApp,
		prefix: toEnvPrefix(ref),
	}

	if boundApp.Spec.Source.Type == mortisev1alpha1.SourceTypeExternal && boundApp.Spec.Source.External != nil {
		rb.host = boundApp.Spec.Source.External.Host
		if boundApp.Spec.Source.External.Port > 0 {
			rb.port = fmt.Sprintf("%d", boundApp.Spec.Source.External.Port)
		}
	} else {
		if !boundAppEnabledIn(&boundApp, env) {
			return nil, nil
		}
		rb.host = fmt.Sprintf("%s.%s.svc.cluster.local", boundApp.Name, envNs)
		port := boundApp.Spec.Network.Port
		if port == 0 {
			port = 8080
		}
		rb.port = fmt.Sprintf("%d", port)
	}

	for _, cred := range boundApp.Spec.Credentials {
		if cred.Name != "host" && cred.Name != "port" {
			rb.extraCreds = append(rb.extraCreds, cred)
		}
	}

	if len(rb.extraCreds) > 0 {
		secretName := fmt.Sprintf("%s-credentials", boundApp.Name)
		var credSecret corev1.Secret
		secretKey := types.NamespacedName{Namespace: envNs, Name: secretName}
		if err := r.Client.Get(ctx, secretKey, &credSecret); err != nil {
			return nil, fmt.Errorf("resolve credentials for binding %q: secret %s/%s: %w",
				ref, envNs, secretName, err)
		}
		rb.credSecret = &credSecret
	}

	return rb, nil
}

// expandBinding returns all auto-generated vars for a resolved binding.
func expandBinding(rb *resolvedBinding) []ResolvedVar {
	var result []ResolvedVar
	result = append(result,
		ResolvedVar{Name: rb.prefix + "_HOST", Value: rb.host},
		ResolvedVar{Name: rb.prefix + "_PORT", Value: rb.port},
	)
	if url := autoURL(rb.app.Spec.Source.Image, rb.host, rb.port); url != "" {
		result = append(result, ResolvedVar{Name: rb.prefix + "_URL", Value: url})
	}
	for _, cred := range rb.extraCreds {
		val := ""
		if rb.credSecret != nil {
			val = string(rb.credSecret.Data[cred.Name])
		}
		result = append(result, ResolvedVar{
			Name:  rb.prefix + "_" + strings.ToUpper(cred.Name),
			Value: val,
		})
	}
	return result
}

// Resolve looks up each bound App and returns fully-resolved env vars for
// all declared credentials plus auto-generated host, port, and URL vars.
//
// All names are prefixed with the bound app name in UPPER_SNAKE_CASE to
// avoid collisions (e.g. binding "database" → DATABASE_HOST, DATABASE_URL).
//
// When a bound app no longer exists (e.g. deleted), the binding is skipped
// and zero vars are produced for it. This lets the caller's ReplaceSource
// call clear stale binding vars from the consumer's env Secret.
// A missing credentials Secret while the App CRD still exists is treated
// as a transient error (the bound app hasn't finished deploying yet) and
// retried rather than silently producing partial vars.
func (r *Resolver) Resolve(
	ctx context.Context,
	project string,
	env string,
	bindings []mortisev1alpha1.Binding,
) ([]ResolvedVar, error) {
	log := logf.FromContext(ctx)
	var result []ResolvedVar

	for _, b := range bindings {
		rb, err := r.lookupBinding(ctx, project, env, b.Ref)
		if err != nil {
			return nil, err
		}
		if rb == nil {
			log.Info("bound app not found or disabled, skipping binding", "binding", b.Ref, "project", project, "env", env)
			continue
		}
		result = append(result, expandBinding(rb)...)
	}

	return result, nil
}

// ResolveSingle resolves a single key from a bound app's credentials.
// Unlike Resolve, this returns an error (not a silent skip) when the
// bound app is missing or disabled — the caller is explicitly requesting
// a specific value.
func (r *Resolver) ResolveSingle(
	ctx context.Context,
	project string,
	env string,
	ref string,
	key string,
) (string, error) {
	rb, err := r.lookupBinding(ctx, project, env, ref)
	if err != nil {
		return "", err
	}
	if rb == nil {
		return "", fmt.Errorf("bound app %q not found or disabled in env %q", ref, env)
	}

	switch key {
	case "host":
		return rb.host, nil
	case "port":
		return rb.port, nil
	case "url":
		url := autoURL(rb.app.Spec.Source.Image, rb.host, rb.port)
		if url == "" {
			return "", fmt.Errorf("binding %q does not expose key %q (auto-URL not available for image %q); available keys: %s",
				ref, key, rb.app.Spec.Source.Image, strings.Join(availableKeys(rb), ", "))
		}
		return url, nil
	default:
		for _, cred := range rb.extraCreds {
			if cred.Name == key {
				if rb.credSecret != nil {
					return string(rb.credSecret.Data[cred.Name]), nil
				}
				return "", nil
			}
		}
		return "", fmt.Errorf("binding %q does not expose key %q; available keys: %s",
			ref, key, strings.Join(availableKeys(rb), ", "))
	}
}

// AvailableKeys returns the list of valid keys for a bound app in an env.
// Returns an error if the bound app is missing or disabled.
func (r *Resolver) AvailableKeys(
	ctx context.Context,
	project string,
	env string,
	ref string,
) ([]string, error) {
	rb, err := r.lookupBinding(ctx, project, env, ref)
	if err != nil {
		return nil, err
	}
	if rb == nil {
		return nil, fmt.Errorf("bound app %q not found or disabled in env %q", ref, env)
	}
	return availableKeys(rb), nil
}

func availableKeys(rb *resolvedBinding) []string {
	keys := []string{"host", "port"}
	if autoURL(rb.app.Spec.Source.Image, rb.host, rb.port) != "" {
		keys = append(keys, "url")
	}
	for _, cred := range rb.extraCreds {
		keys = append(keys, cred.Name)
	}
	return keys
}

var envPrefixSanitizer = regexp.MustCompile(`[^A-Z0-9_]`)

// toEnvPrefix converts an app name to a valid POSIX env var prefix.
// Replaces hyphens, dots, and other non-alphanumeric chars with underscores.
// Strips leading digits so the result is a valid identifier prefix.
func toEnvPrefix(name string) string {
	upper := strings.ToUpper(name)
	sanitized := envPrefixSanitizer.ReplaceAllString(upper, "_")
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
