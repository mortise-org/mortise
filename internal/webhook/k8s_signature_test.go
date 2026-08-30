package webhook

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestSetWebhookSignatureCondition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	gp := &mortisev1alpha1.GitProvider{ObjectMeta: metav1.ObjectMeta{Name: "github"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gp).WithStatusSubresource(gp).Build()
	r := NewK8sReader(c)
	ctx := context.Background()
	now := time.Date(2026, 8, 30, 3, 3, 0, 0, time.UTC)

	if err := r.setWebhookSignatureCondition(ctx, "github", true, now); err != nil {
		t.Fatal(err)
	}
	var got mortisev1alpha1.GitProvider
	if err := c.Get(ctx, types.NamespacedName{Name: "github"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, WebhookSignatureCondition)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "SignatureMismatch" {
		t.Fatalf("got %+v", cond)
	}
	if !strings.Contains(cond.Message, "2026-08-30T03:03:00Z") || !strings.Contains(cond.Message, "/api/webhooks/github") {
		t.Fatalf("message: %s", cond.Message)
	}

	if err := r.setWebhookSignatureCondition(ctx, "github", false, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: "github"}, &got); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(got.Status.Conditions, WebhookSignatureCondition) != nil {
		t.Fatal("a verified delivery must clear the condition")
	}
}
