/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"
	"testing"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestValidateCredential(t *testing.T) {
	tests := []struct {
		name    string
		cred    mortisev1alpha1.Credential
		wantErr string // "" means no error expected
	}{
		{
			name: "inline value only — valid",
			cred: mortisev1alpha1.Credential{Name: "DATABASE_URL", Value: "postgres://localhost/db"},
		},
		{
			name: "valueFrom secretRef only — valid",
			cred: mortisev1alpha1.Credential{
				Name: "password",
				ValueFrom: &mortisev1alpha1.CredentialSource{
					SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "my-secret", Key: "pw"},
				},
			},
		},
		{
			name: "neither value nor valueFrom — valid (well-known key)",
			cred: mortisev1alpha1.Credential{Name: "host"},
		},
		{
			name: "both value and valueFrom — rejected",
			cred: mortisev1alpha1.Credential{
				Name:  "conflict",
				Value: "inline",
				ValueFrom: &mortisev1alpha1.CredentialSource{
					SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "s", Key: "k"},
				},
			},
			wantErr: "mutually exclusive",
		},
		{
			name: "valueFrom with empty name — rejected",
			cred: mortisev1alpha1.Credential{
				Name: "bad-ref",
				ValueFrom: &mortisev1alpha1.CredentialSource{
					SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "", Key: "k"},
				},
			},
			wantErr: "requires name and key",
		},
		{
			name: "valueFrom with empty key — rejected",
			cred: mortisev1alpha1.Credential{
				Name: "bad-key",
				ValueFrom: &mortisev1alpha1.CredentialSource{
					SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "s", Key: ""},
				},
			},
			wantErr: "requires name and key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCredential(&tc.cred)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestHashCredentialData(t *testing.T) {
	t.Run("empty data returns empty string", func(t *testing.T) {
		if got := hashCredentialData(nil); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
		if got := hashCredentialData(map[string][]byte{}); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})

	t.Run("stable across calls (sorted keys)", func(t *testing.T) {
		data := map[string][]byte{
			"z_key": []byte("z_val"),
			"a_key": []byte("a_val"),
			"m_key": []byte("m_val"),
		}
		h1 := hashCredentialData(data)
		h2 := hashCredentialData(data)
		if h1 != h2 {
			t.Fatalf("hash not stable: %q != %q", h1, h2)
		}
		if len(h1) != 64 {
			t.Fatalf("expected sha256 hex (64 chars), got %d chars", len(h1))
		}
	})

	t.Run("different data produces different hash", func(t *testing.T) {
		h1 := hashCredentialData(map[string][]byte{"k": []byte("v1")})
		h2 := hashCredentialData(map[string][]byte{"k": []byte("v2")})
		if h1 == h2 {
			t.Fatal("different values should produce different hashes")
		}
	})
}

func TestMergeAnnotations(t *testing.T) {
	t.Run("both nil returns nil", func(t *testing.T) {
		if got := mergeAnnotations(nil, nil); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("user wins on conflict", func(t *testing.T) {
		owned := map[string]string{"cert-manager.io/cluster-issuer": "default"}
		user := map[string]string{"cert-manager.io/cluster-issuer": "override", "extra": "yes"}
		got := mergeAnnotations(owned, user)
		if got["cert-manager.io/cluster-issuer"] != "override" {
			t.Fatalf("expected user value 'override', got %q", got["cert-manager.io/cluster-issuer"])
		}
		if got["extra"] != "yes" {
			t.Fatal("expected user key 'extra'")
		}
	})

	t.Run("owned only", func(t *testing.T) {
		got := mergeAnnotations(map[string]string{"a": "1"}, nil)
		if got["a"] != "1" || len(got) != 1 {
			t.Fatalf("unexpected: %v", got)
		}
	})
}

func TestResolveProjectNamespace(t *testing.T) {
	t.Run("derives control namespace pj-{name}", func(t *testing.T) {
		p := &mortisev1alpha1.Project{}
		p.Name = "web"
		if got := ResolveProjectNamespace(p); got != "pj-web" {
			t.Fatalf("expected pj-web, got %q", got)
		}
	})
}

func TestAnnotationsEqual(t *testing.T) {
	t.Run("nil equals empty", func(t *testing.T) {
		if !annotationsEqual(nil, map[string]string{}) {
			t.Fatal("nil and empty should be equal")
		}
	})

	t.Run("same keys same values", func(t *testing.T) {
		a := map[string]string{"a": "1", "b": "2"}
		b := map[string]string{"b": "2", "a": "1"}
		if !annotationsEqual(a, b) {
			t.Fatal("should be equal")
		}
	})

	t.Run("different values", func(t *testing.T) {
		if annotationsEqual(map[string]string{"a": "1"}, map[string]string{"a": "2"}) {
			t.Fatal("should not be equal")
		}
	})
}

func TestBuildProbe_SetsKubernetesDefaults(t *testing.T) {
	probe := buildProbe(nil, 8080)

	if probe.FailureThreshold != 3 {
		t.Fatalf("FailureThreshold = %d, want 3 (k8s default)", probe.FailureThreshold)
	}
	if probe.SuccessThreshold != 1 {
		t.Fatalf("SuccessThreshold = %d, want 1 (k8s default)", probe.SuccessThreshold)
	}
}

func TestBuildProbe_CustomConfig(t *testing.T) {
	pc := &mortisev1alpha1.ProbeConfig{
		Path:                "/healthz",
		Port:                9090,
		InitialDelaySeconds: 10,
		PeriodSeconds:       30,
		TimeoutSeconds:      5,
	}
	probe := buildProbe(pc, 8080)

	if probe.FailureThreshold != 3 {
		t.Fatalf("FailureThreshold = %d, want 3", probe.FailureThreshold)
	}
	if probe.SuccessThreshold != 1 {
		t.Fatalf("SuccessThreshold = %d, want 1", probe.SuccessThreshold)
	}
	if probe.HTTPGet == nil || probe.HTTPGet.Path != "/healthz" {
		t.Fatalf("expected HTTPGet probe with path /healthz")
	}
	if probe.HTTPGet.Port.IntValue() != 9090 {
		t.Fatalf("expected port 9090, got %d", probe.HTTPGet.Port.IntValue())
	}
}
