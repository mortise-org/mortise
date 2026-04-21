package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

func TestAutoDefaultDomain(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mortisev1alpha1.AddToScheme(scheme)

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Domain: "example.com",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pc).Build()
	r := &AppReconciler{Client: c}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "pj-myproject"},
	}

	tests := []struct {
		env  string
		want string
	}{
		{"production", "web.example.com"},
		{"staging", "web-staging.example.com"},
		{"preview", "web-preview.example.com"},
	}

	for _, tt := range tests {
		got := r.autoDefaultDomain(context.Background(), app, tt.env)
		if got != tt.want {
			t.Errorf("autoDefaultDomain(%q) = %q, want %q", tt.env, got, tt.want)
		}
	}
}

func TestAutoDefaultDomainNoPlatformConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mortisev1alpha1.AddToScheme(scheme)

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AppReconciler{Client: c}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "pj-myproject"},
	}

	got := r.autoDefaultDomain(context.Background(), app, "production")
	if got != "" {
		t.Errorf("expected empty domain when no PlatformConfig, got %q", got)
	}
}

func TestAutoDefaultDomainEmptyDomain(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = mortisev1alpha1.AddToScheme(scheme)

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec:       mortisev1alpha1.PlatformConfigSpec{},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pc).Build()
	r := &AppReconciler{Client: c}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "pj-myproject"},
	}

	got := r.autoDefaultDomain(context.Background(), app, "production")
	if got != "" {
		t.Errorf("expected empty domain when PlatformConfig has no domain, got %q", got)
	}
}
