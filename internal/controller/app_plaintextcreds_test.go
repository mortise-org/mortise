package controller

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestLooksLikeCredentialName(t *testing.T) {
	yes := []string{"STRIPE_SECRET_KEY", "SUPABASE_SERVICE_KEY", "ENCRYPTION_KEY", "LINKEDIN_CLIENT_SECRET", "GITHUB_TOKEN", "DB_PASSWORD", "SLACK_WEBHOOK_URL", "API_KEY", "secret"}
	no := []string{"PORT", "NODE_ENV", "LOG_LEVEL", "FRONTEND_URL", "STRIPE_PUBLISHABLE_KEY", "VAPID_PUBLIC_KEY", "KEYBOARD_LAYOUT", "MONKEY", "TOKENIZER_MODEL"}
	for _, n := range yes {
		if !looksLikeCredentialName(n) {
			t.Errorf("%s should look like a credential", n)
		}
	}
	for _, n := range no {
		if looksLikeCredentialName(n) {
			t.Errorf("%s should not look like a credential", n)
		}
	}
}

func TestSetPlaintextCredentialsCondition(t *testing.T) {
	app := &mortisev1alpha1.App{Spec: mortisev1alpha1.AppSpec{
		SharedVars: []mortisev1alpha1.EnvVar{{Name: "SENTRY_TOKEN", Value: "x"}, {Name: "LOG_LEVEL", Value: "info"}},
	}}
	envs := []mortisev1alpha1.Environment{{
		Name: "production",
		Env: []mortisev1alpha1.EnvVar{
			{Name: "STRIPE_SECRET_KEY", Value: "sk_live_1"},
			{Name: "PORT", Value: "3000"},
			{Name: "DB_PASSWORD", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "db-creds"}},
			{Name: "STRIPE_PUBLISHABLE_KEY", Value: "pk_live_1"},
		},
	}}
	var conds []metav1.Condition
	setPlaintextCredentialsCondition(&conds, app, envs, 7)
	c := meta.FindStatusCondition(conds, "PlaintextCredentials")
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != "LiteralLooksLikeCredential" || c.ObservedGeneration != 7 {
		t.Fatalf("got %+v", c)
	}
	for _, want := range []string{"sharedVars: SENTRY_TOKEN", "production: STRIPE_SECRET_KEY"} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message lacks %q: %s", want, c.Message)
		}
	}
	for _, not := range []string{"PORT", "DB_PASSWORD", "PUBLISHABLE", "sk_live", "LOG_LEVEL"} {
		if strings.Contains(c.Message, not) {
			t.Errorf("message must not contain %q: %s", not, c.Message)
		}
	}
	clean := []mortisev1alpha1.Environment{{Name: "production", Env: []mortisev1alpha1.EnvVar{{Name: "PORT", Value: "3000"}, {Name: "API_KEY", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "creds"}}}}}
	setPlaintextCredentialsCondition(&conds, &mortisev1alpha1.App{}, clean, 8)
	if meta.FindStatusCondition(conds, "PlaintextCredentials") != nil {
		t.Fatal("condition must clear once every credential-shaped var is a secretRef")
	}
}
