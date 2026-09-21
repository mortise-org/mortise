package controller

import (
	"context"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/crdcheck"
)

// CAI-312: a 1.0.4-era served CRD silently pruned the 1.1.0 operator's
// status writes, visible only as an INFO log. The condition must name the
// stale CRDs and the fix; with current CRDs served it must be absent.
func TestSetCRDsCurrentCondition(t *testing.T) {
	if err := apiextensionsv1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}

	// Serve the embedded CRDs faithfully, except platformconfigs, served
	// with an empty status schema (the stale, pruning shape).
	crds, err := crdcheck.EmbeddedCRDs()
	if err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme.Scheme)
	for _, crd := range crds {
		if crd.Name == "platformconfigs.mortise.mortise.dev" {
			for i := range crd.Spec.Versions {
				crd.Spec.Versions[i].Schema = &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {}, "status": {}},
					},
				}
			}
		}
		builder = builder.WithObjects(crd)
	}
	c := builder.Build()

	r := &PlatformConfigReconciler{CRDReader: c}
	pc := &mortisev1alpha1.PlatformConfig{}

	if outdated := r.setCRDsCurrentCondition(context.Background(), pc); !outdated {
		t.Fatal("expected outdated=true against a stale platformconfigs CRD")
	}
	cond := meta.FindStatusCondition(pc.Status.Conditions, "CRDsCurrent")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "CRDsOutdated" {
		t.Fatalf("expected CRDsCurrent=False/CRDsOutdated, got %+v", cond)
	}
	if !strings.Contains(cond.Message, "platformconfigs.mortise.mortise.dev") ||
		!strings.Contains(cond.Message, "kubectl apply") {
		t.Fatalf("message must name the stale CRD and the fix, got: %s", cond.Message)
	}

	// Nil reader: check disabled, no condition, no requeue.
	pc2 := &mortisev1alpha1.PlatformConfig{}
	if outdated := (&PlatformConfigReconciler{}).setCRDsCurrentCondition(context.Background(), pc2); outdated {
		t.Fatal("nil CRDReader must disable the check")
	}
	if meta.FindStatusCondition(pc2.Status.Conditions, "CRDsCurrent") != nil {
		t.Fatal("nil CRDReader must not set the condition")
	}
}
