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

package platformconfig_test

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/platformconfig"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1.AddToScheme: %v", err)
	}
	return s
}

func minimalPC() *mortisev1alpha1.PlatformConfig {
	return &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Domain: "example.com",
		},
	}
}

func secret(ns, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Data:       data,
	}
}

func TestLoad_FoundAndResolved(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)

	pc := minimalPC()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pc).Build()

	cfg, err := platformconfig.Load(ctx, c)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Domain != "example.com" {
		t.Errorf("Domain = %q, want %q", cfg.Domain, "example.com")
	}
}

func TestLoad_NotFound(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)

	c := fake.NewClientBuilder().WithScheme(s).Build()

	_, err := platformconfig.Load(ctx, c)
	if !errors.Is(err, platformconfig.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestLoad_RegistryCredentials(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)

	pc := minimalPC()
	ref := mortisev1alpha1.SecretRef{Namespace: "ns", Name: "reg-creds", Key: "username"}
	pc.Spec.Registry = mortisev1alpha1.RegistryConfig{
		URL:                  "registry.example.com",
		Namespace:            "myns",
		CredentialsSecretRef: &ref,
	}

	reg := secret("ns", "reg-creds", map[string][]byte{
		"username": []byte("admin"),
		"password": []byte("s3cr3t"),
	})

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pc, reg).Build()

	cfg, err := platformconfig.Load(ctx, c)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Registry.URL != "registry.example.com" {
		t.Errorf("Registry.URL = %q, want registry.example.com", cfg.Registry.URL)
	}
	if cfg.Registry.Username != "admin" {
		t.Errorf("Registry.Username = %q, want admin", cfg.Registry.Username)
	}
	if cfg.Registry.Password != "s3cr3t" {
		t.Errorf("Registry.Password = %q, want s3cr3t", cfg.Registry.Password)
	}
}

func TestLoad_BadRegistrySecretRef(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)

	pc := minimalPC()
	pc.Spec.Registry = mortisev1alpha1.RegistryConfig{
		URL: "registry.example.com",
		CredentialsSecretRef: &mortisev1alpha1.SecretRef{
			Namespace: "ns", Name: "missing", Key: "username",
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pc).Build()

	_, err := platformconfig.Load(ctx, c)
	if err == nil {
		t.Fatal("expected error for missing registry credentials secret, got nil")
	}
}

func TestLoad_ObservabilityEndpointsOnly(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)

	pc := minimalPC()
	pc.Spec.Observability = mortisev1alpha1.ObservabilitySpec{
		LogsAdapterEndpoint:    "http://observer:9091",
		MetricsAdapterEndpoint: "http://observer:9091",
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pc).Build()

	cfg, err := platformconfig.Load(ctx, c)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Observability.LogsAdapterEndpoint != "http://observer:9091" {
		t.Errorf("LogsAdapterEndpoint = %q", cfg.Observability.LogsAdapterEndpoint)
	}
	if cfg.Observability.MetricsAdapterEndpoint != "http://observer:9091" {
		t.Errorf("MetricsAdapterEndpoint = %q", cfg.Observability.MetricsAdapterEndpoint)
	}
	if cfg.Observability.LogsAdapterToken != "" {
		t.Errorf("LogsAdapterToken = %q, want empty", cfg.Observability.LogsAdapterToken)
	}
	if cfg.Observability.MetricsAdapterToken != "" {
		t.Errorf("MetricsAdapterToken = %q, want empty", cfg.Observability.MetricsAdapterToken)
	}
}

func TestLoad_ObservabilityWithTokens(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)

	pc := minimalPC()
	pc.Spec.Observability = mortisev1alpha1.ObservabilitySpec{
		LogsAdapterEndpoint: "http://loki-adapter:8080",
		LogsAdapterTokenSecretRef: &mortisev1alpha1.SecretRef{
			Namespace: "ns", Name: "logs-token", Key: "token",
		},
		MetricsAdapterEndpoint: "http://prom-adapter:8080",
		MetricsAdapterTokenSecretRef: &mortisev1alpha1.SecretRef{
			Namespace: "ns", Name: "metrics-token", Key: "token",
		},
	}

	logsSecret := secret("ns", "logs-token", map[string][]byte{"token": []byte("logs-bearer")})
	metricsSecret := secret("ns", "metrics-token", map[string][]byte{"token": []byte("metrics-bearer")})

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pc, logsSecret, metricsSecret).Build()

	cfg, err := platformconfig.Load(ctx, c)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Observability.LogsAdapterToken != "logs-bearer" {
		t.Errorf("LogsAdapterToken = %q, want logs-bearer", cfg.Observability.LogsAdapterToken)
	}
	if cfg.Observability.MetricsAdapterToken != "metrics-bearer" {
		t.Errorf("MetricsAdapterToken = %q, want metrics-bearer", cfg.Observability.MetricsAdapterToken)
	}
}

func TestLoad_ObservabilityBadTokenSecret(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)

	pc := minimalPC()
	pc.Spec.Observability = mortisev1alpha1.ObservabilitySpec{
		LogsAdapterEndpoint: "http://adapter:8080",
		LogsAdapterTokenSecretRef: &mortisev1alpha1.SecretRef{
			Namespace: "ns", Name: "missing", Key: "token",
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pc).Build()

	_, err := platformconfig.Load(ctx, c)
	if err == nil {
		t.Fatal("expected error for missing logs adapter token secret, got nil")
	}
}

func TestLoad_BuildTLS(t *testing.T) {
	ctx := context.Background()
	s := scheme(t)

	pc := minimalPC()
	pc.Spec.Build = mortisev1alpha1.BuildConfig{
		BuildkitAddr: "tcp://buildkitd:1234",
		TLSSecretRef: &mortisev1alpha1.SecretRef{Namespace: "ns", Name: "bk-tls", Key: "ca.crt"},
	}

	bkTLS := secret("ns", "bk-tls", map[string][]byte{
		"ca.crt":  []byte("CA"),
		"tls.crt": []byte("CERT"),
		"tls.key": []byte("KEY"),
	})

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pc, bkTLS).Build()

	cfg, err := platformconfig.Load(ctx, c)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Build.BuildkitAddr != "tcp://buildkitd:1234" {
		t.Errorf("Build.BuildkitAddr = %q", cfg.Build.BuildkitAddr)
	}
	if cfg.Build.TLSCA != "CA" {
		t.Errorf("Build.TLSCA = %q, want CA", cfg.Build.TLSCA)
	}
	if cfg.Build.TLSCert != "CERT" {
		t.Errorf("Build.TLSCert = %q, want CERT", cfg.Build.TLSCert)
	}
	if cfg.Build.TLSKey != "KEY" {
		t.Errorf("Build.TLSKey = %q, want KEY", cfg.Build.TLSKey)
	}
}
