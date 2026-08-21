package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

// The CAI-151 snapshot: what client-side `kubectl apply` writes for an App with
// plaintext env and credential literals.
const leakySnapshot = `{
  "apiVersion": "mortise.dev/v1alpha1",
  "kind": "App",
  "metadata": {"name": "demo-api", "namespace": "pj-demo"},
  "spec": {
    "source": {
      "type": "git",
      "image": "demo:1.2.3",
      "build": {"args": {"NPM_TOKEN": "npm_live_tok", "PUBLIC_FLAG": "on"}}
    },
    "sharedVars": [
      {"name": "SENTRY_DSN", "value": "https://abc123@sentry.io/42"}
    ],
    "configFiles": [
      {"path": "/etc/app/creds.ini", "content": "password = hunter3"}
    ],
    "credentials": [
      {"name": "password", "value": "hunter2"},
      {"name": "username", "value": "postgres"}
    ],
    "environments": [
      {
        "name": "production",
        "replicas": 2,
        "buildArgs": {"SENTRY_AUTH_TOKEN": "sntrys_live_xyz"},
        "env": [
          {"name": "NODE_ENV", "value": "production"},
          {"name": "STRIPE_SECRET_KEY", "value": "sk_live_abc123"},
          {"name": "FROM_SECRET", "valueFrom": {"secretRef": "app-credentials"}}
        ]
      },
      {
        "name": "staging",
        "env": [{"name": "LINEAR_API_KEY", "value": "lin_api_xyz789"}]
      }
    ]
  }
}`

func TestRedactLastApplied(t *testing.T) {
	out, changed, err := redactLastApplied(leakySnapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected the snapshot to be reported as changed")
	}

	// The annotation is a copy of the whole spec, so the fixture is the whole
	// spec -- scoping it to the fields the implementation handles is how the
	// first version of this hid four leaks.
	for _, secret := range []string{
		"hunter2", "sk_live_abc123", "lin_api_xyz789", "postgres",
		"npm_live_tok", "https://abc123@sentry.io/42", "hunter3", "sntrys_live_xyz",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("plaintext value %q survived redaction", secret)
		}
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("redacted output is not valid JSON: %v", err)
	}
	spec := got["spec"].(map[string]any)

	// Structure must survive. Not because kubectl deletion-detects list entries
	// by merge key -- it does not, an App is a CRD and gets an RFC 7386 merge
	// patch with atomic arrays -- but because the annotation is what makes
	// whole-field removal propagate at all.
	envs := spec["environments"].([]any)
	if len(envs) != 2 {
		t.Fatalf("expected 2 environments preserved, got %d", len(envs))
	}
	prod := envs[0].(map[string]any)
	if prod["name"] != "production" {
		t.Errorf("environment name not preserved, got %v", prod["name"])
	}
	if prod["replicas"].(float64) != 2 {
		t.Errorf("non-secret field replicas not preserved, got %v", prod["replicas"])
	}

	prodEnv := prod["env"].([]any)
	if len(prodEnv) != 3 {
		t.Fatalf("expected 3 env entries preserved, got %d", len(prodEnv))
	}
	names := make([]string, 0, len(prodEnv))
	for _, e := range prodEnv {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	for i, want := range []string{"NODE_ENV", "STRIPE_SECRET_KEY", "FROM_SECRET"} {
		if names[i] != want {
			t.Errorf("env entry name %d: expected %q, got %q", i, want, names[i])
		}
	}

	// A valueFrom entry has no plaintext to redact and must be left alone.
	fromSecret := prodEnv[2].(map[string]any)
	if _, leaked := fromSecret["value"]; leaked {
		t.Error("a valueFrom entry gained a value key")
	}
	if fromSecret["valueFrom"] == nil {
		t.Error("valueFrom was dropped")
	}

	// Non-secret spec fields are untouched.
	if spec["source"].(map[string]any)["image"] != "demo:1.2.3" {
		t.Error("spec.source.image was modified")
	}
}

func TestRedactLastApplied_Idempotent(t *testing.T) {
	once, changed, err := redactLastApplied(leakySnapshot)
	if err != nil || !changed {
		t.Fatalf("first pass: changed=%v err=%v", changed, err)
	}
	twice, changed, err := redactLastApplied(once)
	if err != nil {
		t.Fatalf("second pass errored: %v", err)
	}
	if changed {
		t.Error("re-redacting an already-redacted snapshot reported a change, which would spin the App watch")
	}
	if twice != once {
		t.Error("re-redacting altered the snapshot")
	}
}

func TestRedactLastApplied_NothingToDo(t *testing.T) {
	t.Run("a spec with no literals is unchanged", func(t *testing.T) {
		clean := `{"spec":{"environments":[{"name":"production","env":[` +
			`{"name":"FROM_SECRET","valueFrom":{"secretRef":"creds"}}]}]}}`
		_, changed, err := redactLastApplied(clean)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected no change for a snapshot with no plaintext values")
		}
	})

	t.Run("a snapshot with no spec is unchanged", func(t *testing.T) {
		_, changed, err := redactLastApplied(`{"metadata":{"name":"demo"}}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected no change for a snapshot with no spec")
		}
	})

	t.Run("an empty value is left alone", func(t *testing.T) {
		_, changed, err := redactLastApplied(
			`{"spec":{"environments":[{"name":"p","env":[{"name":"EMPTY","value":""}]}]}}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected no change for an already-empty value")
		}
	})

	t.Run("unparseable JSON errors rather than guessing", func(t *testing.T) {
		if _, _, err := redactLastApplied(`{not json`); err == nil {
			t.Error("expected an error for unparseable JSON")
		}
	})
}

func TestRedactAppLastApplied_OnObject(t *testing.T) {
	scheme := gcTestScheme(t)
	key := types.NamespacedName{Name: "demo-api", Namespace: constants.ControlNamespace("demo")}

	newApp := func(annotations map[string]string) *mortisev1alpha1.App {
		return &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{
				Name:        key.Name,
				Namespace:   key.Namespace,
				Annotations: annotations,
			},
		}
	}

	t.Run("redacts a leaky annotation in place", func(t *testing.T) {
		app := newApp(map[string]string{lastAppliedAnnotation: leakySnapshot})
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}

		if err := r.redactAppLastApplied(context.Background(), key); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var got mortisev1alpha1.App
		if err := c.Get(context.Background(), key, &got); err != nil {
			t.Fatalf("get app: %v", err)
		}
		raw := got.Annotations[lastAppliedAnnotation]
		for _, secret := range []string{"hunter2", "sk_live_abc123", "lin_api_xyz789"} {
			if strings.Contains(raw, secret) {
				t.Errorf("plaintext value %q still present on the object", secret)
			}
		}
		if !strings.Contains(raw, "STRIPE_SECRET_KEY") {
			t.Error("env entry name was lost from the annotation")
		}
	})

	// The annotation is rewritten by every client-side apply, so the operator
	// re-redacts on the next reconcile. That must be a no-op once clean: this
	// runs on every reconcile and an unconditional write would spin the App
	// watch, which this controller has been bitten by before.
	t.Run("a second pass writes nothing", func(t *testing.T) {
		app := newApp(map[string]string{lastAppliedAnnotation: leakySnapshot})
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}
		ctx := context.Background()

		if err := r.redactAppLastApplied(ctx, key); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		var afterFirst mortisev1alpha1.App
		if err := c.Get(ctx, key, &afterFirst); err != nil {
			t.Fatalf("get app: %v", err)
		}

		if err := r.redactAppLastApplied(ctx, key); err != nil {
			t.Fatalf("second pass: %v", err)
		}
		var afterSecond mortisev1alpha1.App
		if err := c.Get(ctx, key, &afterSecond); err != nil {
			t.Fatalf("get app: %v", err)
		}

		if afterFirst.ResourceVersion != afterSecond.ResourceVersion {
			t.Errorf("second pass wrote to the object (resourceVersion %s -> %s); "+
				"an unconditional write here would spin the App watch",
				afterFirst.ResourceVersion, afterSecond.ResourceVersion)
		}
	})

	t.Run("an App with no annotation is not written", func(t *testing.T) {
		app := newApp(nil)
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}
		ctx := context.Background()

		var before mortisev1alpha1.App
		if err := c.Get(ctx, key, &before); err != nil {
			t.Fatalf("get app: %v", err)
		}
		if err := r.redactAppLastApplied(ctx, key); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var after mortisev1alpha1.App
		if err := c.Get(ctx, key, &after); err != nil {
			t.Fatalf("get app: %v", err)
		}
		if before.ResourceVersion != after.ResourceVersion {
			t.Error("an App with no last-applied annotation was written to")
		}
	})

	t.Run("a deleted App is not an error", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &AppReconciler{Client: c, Scheme: scheme}
		if err := r.redactAppLastApplied(context.Background(), key); err != nil {
			t.Errorf("expected a missing App to be tolerated, got %v", err)
		}
	})
}
