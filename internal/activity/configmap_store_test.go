package activity_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/MC-Meesh/mortise/internal/activity"
)

func newFakeClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func baseEvent(project string, i int) activity.Event {
	return activity.Event{
		Timestamp:    time.Unix(int64(i), 0).UTC(),
		Actor:        "jane@example.com",
		Action:       "app.deploy",
		ResourceKind: "App",
		ResourceName: fmt.Sprintf("app-%d", i),
		Project:      project,
		Message:      fmt.Sprintf("deployed #%d", i),
	}
}

func TestAppend_CreatesConfigMapOnFirstCall(t *testing.T) {
	c := newFakeClient(t)
	s := activity.NewConfigMapStore(c)

	ev := baseEvent("alpha", 1)
	if err := s.Append(context.Background(), ev); err != nil {
		t.Fatalf("append: %v", err)
	}

	var cm corev1.ConfigMap
	err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "project-alpha",
		Name:      "activity-alpha",
	}, &cm)
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if cm.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		t.Errorf("missing managed-by label: %v", cm.Labels)
	}
	if cm.Labels["mortise.dev/kind"] != "activity" {
		t.Errorf("missing kind label: %v", cm.Labels)
	}

	got, err := s.List(context.Background(), "alpha", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].ResourceName != "app-1" {
		t.Errorf("want app-1, got %q", got[0].ResourceName)
	}
}

func TestAppend_AppendsToExistingConfigMap(t *testing.T) {
	c := newFakeClient(t)
	s := activity.NewConfigMapStore(c)

	for i := 1; i <= 10; i++ {
		if err := s.Append(context.Background(), baseEvent("beta", i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got, err := s.List(context.Background(), "beta", 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("want 10 events, got %d", len(got))
	}
}

func TestAppend_TruncatesAtCap(t *testing.T) {
	c := newFakeClient(t)
	s := activity.NewConfigMapStore(c)

	total := activity.Cap + 1
	for i := 1; i <= total; i++ {
		if err := s.Append(context.Background(), baseEvent("gamma", i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got, err := s.List(context.Background(), "gamma", activity.Cap+10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != activity.Cap {
		t.Fatalf("want %d events after truncation, got %d", activity.Cap, len(got))
	}

	// Newest first.
	if got[0].ResourceName != fmt.Sprintf("app-%d", total) {
		t.Errorf("newest: want app-%d, got %q", total, got[0].ResourceName)
	}
	// Oldest that survives is #2 (#1 should be trimmed).
	oldest := got[len(got)-1]
	if oldest.ResourceName != "app-2" {
		t.Errorf("oldest: want app-2, got %q", oldest.ResourceName)
	}
	for _, e := range got {
		if e.ResourceName == "app-1" {
			t.Errorf("expected app-1 to be trimmed, found it in list")
		}
	}
}

func TestList_NewestFirst(t *testing.T) {
	c := newFakeClient(t)
	s := activity.NewConfigMapStore(c)

	for i := 1; i <= 3; i++ {
		if err := s.Append(context.Background(), baseEvent("delta", i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got, err := s.List(context.Background(), "delta", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	wantNames := []string{"app-3", "app-2", "app-1"}
	for i, want := range wantNames {
		if got[i].ResourceName != want {
			t.Errorf("index %d: want %q, got %q", i, want, got[i].ResourceName)
		}
	}
}

func TestList_MissingConfigMapReturnsEmpty(t *testing.T) {
	c := newFakeClient(t)
	s := activity.NewConfigMapStore(c)

	got, err := s.List(context.Background(), "ghost", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %d events", len(got))
	}
}

func TestList_HonorsLimit(t *testing.T) {
	c := newFakeClient(t)
	s := activity.NewConfigMapStore(c)

	for i := 1; i <= 20; i++ {
		if err := s.Append(context.Background(), baseEvent("eps", i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got, err := s.List(context.Background(), "eps", 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5, got %d", len(got))
	}
	if got[0].ResourceName != "app-20" {
		t.Errorf("want app-20 first, got %q", got[0].ResourceName)
	}
	if got[4].ResourceName != "app-16" {
		t.Errorf("want app-16 last, got %q", got[4].ResourceName)
	}
}

func TestAppend_MissingNamespaceIsNotAnError(t *testing.T) {
	// The fake client does not enforce namespace existence, so exercise the
	// NotFound path by pre-populating a client that returns IsNotFound on
	// Create. We simulate by using a client wrapper that rejects Creates
	// with NotFound — but the simpler approach: the fake client allows
	// creation in any namespace, so verify the code path by using a
	// separate test that injects a client returning IsNotFound.
	c := &notFoundOnCreateClient{Client: newFakeClient(t)}
	s := activity.NewConfigMapStore(c)

	err := s.Append(context.Background(), baseEvent("teardown", 1))
	if err != nil {
		t.Fatalf("expected nil on namespace not found, got %v", err)
	}
}

// notFoundOnCreateClient wraps a client and returns IsNotFound on Create,
// simulating the case where the project namespace has been deleted.
type notFoundOnCreateClient struct {
	client.Client
}

func (n *notFoundOnCreateClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return errors.NewNotFound(corev1.Resource("configmaps"), obj.GetName())
}
