package controller

import (
	"testing"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestWebhookScheme(t *testing.T) {
	cases := []struct {
		name   string
		spec   mortisev1alpha1.PlatformConfigSpec
		expect string
	}{
		{"unset without cert-manager infers http", mortisev1alpha1.PlatformConfigSpec{}, "http"},
		{"unset with cert-manager infers https", mortisev1alpha1.PlatformConfigSpec{TLS: mortisev1alpha1.TLSConfig{CertManagerClusterIssuer: "letsencrypt"}}, "https"},
		{"explicit https wins without cert-manager (edge TLS)", mortisev1alpha1.PlatformConfigSpec{ExternalScheme: "https"}, "https"},
		{"explicit http wins with cert-manager", mortisev1alpha1.PlatformConfigSpec{ExternalScheme: "http", TLS: mortisev1alpha1.TLSConfig{CertManagerClusterIssuer: "x"}}, "http"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := webhookScheme(&mortisev1alpha1.PlatformConfig{Spec: c.spec}); got != c.expect {
				t.Fatalf("got %q, want %q", got, c.expect)
			}
		})
	}
}
