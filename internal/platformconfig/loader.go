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

// Package platformconfig provides a loader that fetches the singleton
// PlatformConfig, resolves all referenced Secrets, and returns a plain Go
// Config struct for use by other packages.
package platformconfig

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// ErrNotFound is returned when no PlatformConfig named "platform" exists in
// the cluster. Callers can use errors.Is to distinguish "not configured yet"
// from real errors.
var ErrNotFound = errors.New("PlatformConfig \"platform\" not found")

// Config is a resolved, plain-Go representation of the singleton PlatformConfig.
// All SecretRefs have been dereferenced; values are ready for direct use.
type Config struct {
	// Domain is the base domain for the platform.
	Domain string

	// DNS holds resolved DNS provider configuration.
	DNS DNSConfig

	// Storage holds platform storage defaults.
	Storage StorageConfig

	// Registry holds the resolved OCI registry configuration.
	Registry RegistryConfig

	// Build holds the resolved BuildKit configuration.
	Build BuildConfig

	// TLS holds TLS/cert-manager configuration.
	TLS TLSConfig
}

// DNSConfig is the resolved DNS provider configuration.
type DNSConfig struct {
	// Provider is the DNS backend (cloudflare, route53, externaldns-noop).
	Provider mortisev1alpha1.DNSProviderType
	// APIToken is the resolved API token value.
	APIToken string
}

// StorageConfig is the resolved storage configuration.
type StorageConfig struct {
	// DefaultStorageClass is the cluster storage class for App volumes.
	DefaultStorageClass string
}

// RegistryConfig is the resolved OCI registry configuration.
type RegistryConfig struct {
	// URL is the registry endpoint.
	URL string
	// Namespace is the registry namespace for images (default: "mortise").
	Namespace string
	// Username is the resolved registry username (empty if not configured).
	Username string
	// Password is the resolved registry password (empty if not configured).
	Password string
	// PullSecretName is the k8s image-pull Secret name.
	PullSecretName string
	// InsecureSkipTLSVerify disables TLS verification for the registry.
	InsecureSkipTLSVerify bool
}

// BuildConfig is the resolved BuildKit configuration.
type BuildConfig struct {
	// BuildkitAddr is the BuildKit daemon address.
	BuildkitAddr string
	// TLSCA is the resolved CA certificate PEM (empty if not configured).
	TLSCA string
	// TLSCert is the resolved client certificate PEM (empty if not configured).
	TLSCert string
	// TLSKey is the resolved client key PEM (empty if not configured).
	TLSKey string
	// DefaultPlatform is the target OCI platform string.
	DefaultPlatform string
}

// TLSConfig is the TLS configuration.
type TLSConfig struct {
	// CertManagerClusterIssuer is the cert-manager ClusterIssuer name.
	CertManagerClusterIssuer string
}

// Load fetches the singleton PlatformConfig (name "platform"), resolves all
// referenced Secrets, and returns a fully populated Config.
//
// Returns ErrNotFound (use errors.Is) if no PlatformConfig named "platform"
// exists. Returns other errors for unexpected API failures.
func Load(ctx context.Context, c client.Reader) (*Config, error) {
	var pc mortisev1alpha1.PlatformConfig
	if err := c.Get(ctx, types.NamespacedName{Name: "platform"}, &pc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get PlatformConfig: %w", err)
	}

	cfg := &Config{
		Domain: pc.Spec.Domain,
		Storage: StorageConfig{
			DefaultStorageClass: pc.Spec.Storage.DefaultStorageClass,
		},
		Registry: RegistryConfig{
			URL:                   pc.Spec.Registry.URL,
			Namespace:             pc.Spec.Registry.Namespace,
			PullSecretName:        pc.Spec.Registry.PullSecretName,
			InsecureSkipTLSVerify: pc.Spec.Registry.InsecureSkipTLSVerify,
		},
		Build: BuildConfig{
			BuildkitAddr:    pc.Spec.Build.BuildkitAddr,
			DefaultPlatform: pc.Spec.Build.DefaultPlatform,
		},
		TLS: TLSConfig{
			CertManagerClusterIssuer: pc.Spec.TLS.CertManagerClusterIssuer,
		},
	}

	// Resolve DNS API token.
	dnsToken, err := resolveSecretKey(ctx, c, pc.Spec.DNS.APITokenSecretRef)
	if err != nil {
		return nil, fmt.Errorf("spec.dns.apiTokenSecretRef: %w", err)
	}
	cfg.DNS = DNSConfig{
		Provider: pc.Spec.DNS.Provider,
		APIToken: dnsToken,
	}

	// Resolve optional registry credentials.
	if ref := pc.Spec.Registry.CredentialsSecretRef; ref != nil {
		secret, err := resolveSecret(ctx, c, *ref)
		if err != nil {
			return nil, fmt.Errorf("spec.registry.credentialsSecretRef: %w", err)
		}
		cfg.Registry.Username = string(secret.Data["username"])
		cfg.Registry.Password = string(secret.Data["password"])
	}

	// Resolve optional BuildKit TLS secret.
	if ref := pc.Spec.Build.TLSSecretRef; ref != nil {
		secret, err := resolveSecret(ctx, c, *ref)
		if err != nil {
			return nil, fmt.Errorf("spec.build.tlsSecretRef: %w", err)
		}
		cfg.Build.TLSCA = string(secret.Data["ca.crt"])
		cfg.Build.TLSCert = string(secret.Data["tls.crt"])
		cfg.Build.TLSKey = string(secret.Data["tls.key"])
	}

	return cfg, nil
}

// resolveSecretKey fetches a single key from a Secret.
func resolveSecretKey(ctx context.Context, c client.Reader, ref mortisev1alpha1.SecretRef) (string, error) {
	secret, err := resolveSecret(ctx, c, ref)
	if err != nil {
		return "", err
	}
	val, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("key %q not present in secret %s/%s", ref.Key, ref.Namespace, ref.Name)
	}
	return string(val), nil
}

// resolveSecret fetches a Secret by namespace/name.
func resolveSecret(ctx context.Context, c client.Reader, ref mortisev1alpha1.SecretRef) (*corev1.Secret, error) {
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	if err := c.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("secret %s/%s not found", ref.Namespace, ref.Name)
		}
		return nil, fmt.Errorf("get secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	return &secret, nil
}
