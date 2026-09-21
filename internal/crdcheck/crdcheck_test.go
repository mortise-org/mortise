package crdcheck

import (
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestLoadEmbedsAllCRDs(t *testing.T) {
	exp, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	names := exp.Names()
	if len(names) < 7 {
		t.Fatalf("expected the 7 Mortise CRDs embedded, got %v", names)
	}
	for _, n := range names {
		if !strings.HasSuffix(n, ".mortise.mortise.dev") {
			t.Fatalf("unexpected CRD name %q", n)
		}
	}
	// A field this operator writes must be among the expected paths, or the
	// whole check is vacuous.
	found := false
	for key, paths := range exp.Paths {
		if !strings.HasPrefix(key, "platformconfigs.") {
			continue
		}
		for _, p := range paths {
			if p == "status.operatorVersion" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("embedded platformconfigs schema lacks status.operatorVersion; embed out of date?")
	}
}

func TestMissingInReportsPrunedFields(t *testing.T) {
	exp, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// A served platformconfigs CRD whose v1alpha1 schema has a spec but a
	// status with no properties — the v1.0.4-era shape that pruned
	// status.operatorVersion writes silently (CAI-312).
	served := &apiextensionsv1.CustomResourceDefinition{}
	served.Name = "platformconfigs.mortise.mortise.dev"
	served.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
		Name: "v1alpha1",
		Schema: &apiextensionsv1.CustomResourceValidation{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"spec":   {},
					"status": {},
				},
			},
		},
	}}

	missing := exp.MissingIn(served)
	if len(missing) == 0 {
		t.Fatal("expected missing fields against a stripped served schema")
	}
	want := "v1alpha1:status.operatorVersion"
	found := false
	for _, m := range missing {
		if m == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %q among missing, got %v", want, missing[:min(len(missing), 5)])
	}
}

func TestMissingInAcceptsOwnSchema(t *testing.T) {
	exp, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Serve back exactly what we embed: nothing may be reported missing.
	raw, err := embedded.ReadFile("crds/mortise.mortise.dev_platformconfigs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yamlUnmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	if missing := exp.MissingIn(&crd); len(missing) != 0 {
		t.Fatalf("self-comparison reported missing fields: %v", missing[:min(len(missing), 5)])
	}
}
