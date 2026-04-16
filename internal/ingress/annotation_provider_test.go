package ingress

import (
	"testing"
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
		ann := p.Annotations(ref, []string{"app.example.com"}, nil)
		got, ok := ann["external-dns.alpha.kubernetes.io/hostname"]
		if !ok {
			t.Fatal("missing external-dns annotation")
		}
		if got != "app.example.com" {
			t.Fatalf("external-dns annotation = %q, want %q", got, "app.example.com")
		}
	})

	t.Run("comma-joins multiple hostnames", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{})
		ann := p.Annotations(ref, []string{"a.example.com", "b.example.com", "c.example.com"}, nil)
		got := ann["external-dns.alpha.kubernetes.io/hostname"]
		want := "a.example.com,b.example.com,c.example.com"
		if got != want {
			t.Fatalf("external-dns annotation = %q, want %q", got, want)
		}
	})

	t.Run("with issuer includes cert-manager annotation", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{DefaultClusterIssuer: "letsencrypt-prod"})
		ann := p.Annotations(ref, []string{"app.example.com"}, nil)
		got, ok := ann["cert-manager.io/cluster-issuer"]
		if !ok {
			t.Fatal("missing cert-manager annotation")
		}
		if got != "letsencrypt-prod" {
			t.Fatalf("cert-manager annotation = %q, want %q", got, "letsencrypt-prod")
		}
	})

	t.Run("without issuer omits cert-manager annotation", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{})
		ann := p.Annotations(ref, []string{"app.example.com"}, nil)
		if _, ok := ann["cert-manager.io/cluster-issuer"]; ok {
			t.Fatal("cert-manager annotation should not be present without an issuer")
		}
	})

	t.Run("no hostnames returns nil", func(t *testing.T) {
		p := NewAnnotationProvider(AnnotationProviderConfig{})
		ann := p.Annotations(ref, nil, nil)
		if ann != nil {
			t.Fatalf("expected nil annotations for empty hostnames, got %v", ann)
		}
	})
}
