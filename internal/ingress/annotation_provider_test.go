package ingress

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestAnnotationProvider_ClassName(t *testing.T) {
	t.Run("returns configured value", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{ClassName: "traefik"})
		if got := p.ClassName(); got != "traefik" {
			t.Fatalf("ClassName() = %q, want %q", got, "traefik")
		}
	})

	t.Run("returns empty when unconfigured", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{})
		if got := p.ClassName(); got != "" {
			t.Fatalf("ClassName() = %q, want empty", got)
		}
	})
}

func TestAnnotationProvider_Annotations(t *testing.T) {
	ref := AppRef{Name: "myapp", Namespace: "default"}

	t.Run("includes ExternalDNS hostname annotation", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{})
		ann := p.Annotations(context.Background(), ref, []string{"app.example.com"}, nil)
		got, ok := ann[ExternalDNSHostnameAnnotation]
		if !ok {
			t.Fatal("missing external-dns annotation")
		}
		if got != "app.example.com" {
			t.Fatalf("external-dns annotation = %q, want %q", got, "app.example.com")
		}
	})

	t.Run("comma-joins multiple hostnames", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{})
		ann := p.Annotations(context.Background(), ref, []string{"a.example.com", "b.example.com", "c.example.com"}, nil)
		got := ann[ExternalDNSHostnameAnnotation]
		want := "a.example.com,b.example.com,c.example.com"
		if got != want {
			t.Fatalf("external-dns annotation = %q, want %q", got, want)
		}
	})

	t.Run("with issuer includes cert-manager annotation", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{DefaultClusterIssuer: "letsencrypt-prod"})
		ann := p.Annotations(context.Background(), ref, []string{"app.example.com"}, nil)
		got, ok := ann[CertManagerClusterIssuerAnnotation]
		if !ok {
			t.Fatal("missing cert-manager annotation")
		}
		if got != "letsencrypt-prod" {
			t.Fatalf("cert-manager annotation = %q, want %q", got, "letsencrypt-prod")
		}
	})

	t.Run("without issuer omits cert-manager annotation", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{})
		ann := p.Annotations(context.Background(), ref, []string{"app.example.com"}, nil)
		if _, ok := ann[CertManagerClusterIssuerAnnotation]; ok {
			t.Fatal("cert-manager annotation should not be present without an issuer")
		}
	})

	t.Run("no hostnames returns nil", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{})
		ann := p.Annotations(context.Background(), ref, nil, nil)
		if ann != nil {
			t.Fatalf("expected nil annotations for empty hostnames, got %v", ann)
		}
	})

	t.Run("live PlatformConfig read overrides static default", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = mortisev1alpha1.AddToScheme(scheme)
		pc := &mortisev1alpha1.PlatformConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "platform"},
			Spec: mortisev1alpha1.PlatformConfigSpec{
				TLS: mortisev1alpha1.TLSConfig{CertManagerClusterIssuer: "live-issuer"},
			},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pc).Build()
		p := NewAnnotationProvider(AnnotationProviderConfig{
			DefaultClusterIssuer: "stale-issuer",
			Reader:               c,
		})
		ann := p.Annotations(context.Background(), ref, []string{"app.example.com"}, nil)
		got := ann[CertManagerClusterIssuerAnnotation]
		if got != "live-issuer" {
			t.Fatalf("expected live issuer, got %q", got)
		}
	})

	t.Run("falls back to static default when PlatformConfig missing", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = mortisev1alpha1.AddToScheme(scheme)
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		p := NewAnnotationProvider(AnnotationProviderConfig{
			DefaultClusterIssuer: "fallback-issuer",
			Reader:               c,
		})
		ann := p.Annotations(context.Background(), ref, []string{"app.example.com"}, nil)
		got := ann[CertManagerClusterIssuerAnnotation]
		if got != "fallback-issuer" {
			t.Fatalf("expected fallback issuer, got %q", got)
		}
	})
}
