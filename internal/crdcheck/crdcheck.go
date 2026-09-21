// Package crdcheck compares the CRD schemas this binary was built against
// with the schemas the cluster actually serves. Helm does not upgrade
// crds/, and an operator writing through older CRDs has its new fields
// silently pruned by the API server — v1.1.0 shipped with its version
// report invisible for exactly this reason, flagged only by an INFO log
// line (CAI-312). The embedded copies are written by `make manifests` from
// the same source as the chart's crds/, so they are the schema this binary
// expects by construction, with no per-release table to maintain.
package crdcheck

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

//go:embed crds/*.yaml
var embedded embed.FS

// Expected holds, per CRD name, the dotted property paths its schema must
// serve, per version.
type Expected struct {
	// Paths maps "<crd name>/<version>" to the sorted property paths of the
	// embedded schema.
	Paths map[string][]string
}

// Load parses the embedded CRDs. It fails only on a build problem (a
// malformed embedded file), never on cluster state.
func Load() (*Expected, error) {
	entries, err := embedded.ReadDir("crds")
	if err != nil {
		return nil, err
	}
	exp := &Expected{Paths: map[string][]string{}}
	for _, e := range entries {
		raw, err := embedded.ReadFile("crds/" + e.Name())
		if err != nil {
			return nil, err
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			return nil, fmt.Errorf("embedded CRD %s: %w", e.Name(), err)
		}
		for _, v := range crd.Spec.Versions {
			if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
				continue
			}
			paths := schemaPaths("", v.Schema.OpenAPIV3Schema)
			sort.Strings(paths)
			exp.Paths[crd.Name+"/"+v.Name] = paths
		}
	}
	if len(exp.Paths) == 0 {
		return nil, fmt.Errorf("no CRD schemas embedded; make manifests not run?")
	}
	return exp, nil
}

// Names returns the CRD names the binary expects, sorted.
func (e *Expected) Names() []string {
	seen := map[string]bool{}
	for k := range e.Paths {
		seen[strings.SplitN(k, "/", 2)[0]] = true
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// MissingIn reports the property paths the served CRD lacks relative to the
// embedded schema, per version the binary expects. A version absent from
// the served CRD counts as all of its paths missing.
func (e *Expected) MissingIn(served *apiextensionsv1.CustomResourceDefinition) []string {
	servedPaths := map[string]map[string]bool{}
	for _, v := range served.Spec.Versions {
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		set := map[string]bool{}
		for _, p := range schemaPaths("", v.Schema.OpenAPIV3Schema) {
			set[p] = true
		}
		servedPaths[v.Name] = set
	}

	var missing []string
	for key, want := range e.Paths {
		parts := strings.SplitN(key, "/", 2)
		if parts[0] != served.Name {
			continue
		}
		got := servedPaths[parts[1]]
		for _, p := range want {
			if !got[p] {
				missing = append(missing, parts[1]+":"+p)
			}
		}
	}
	sort.Strings(missing)
	return missing
}

// schemaPaths flattens a structural schema into dotted property paths.
// Array item properties are traversed under "[]".
func schemaPaths(prefix string, s *apiextensionsv1.JSONSchemaProps) []string {
	var out []string
	for name, child := range s.Properties {
		p := name
		if prefix != "" {
			p = prefix + "." + name
		}
		out = append(out, p)
		out = append(out, schemaPaths(p, &child)...)
	}
	if s.Items != nil && s.Items.Schema != nil {
		itemPrefix := prefix + "[]"
		out = append(out, schemaPaths(itemPrefix, s.Items.Schema)...)
	}
	return out
}

// yamlUnmarshal is exposed for the package's own tests.
func yamlUnmarshal(raw []byte, out any) error { return yaml.Unmarshal(raw, out) }

// EmbeddedCRDs parses and returns the embedded CRDs, for tests that need to
// serve them back faithfully.
func EmbeddedCRDs() ([]*apiextensionsv1.CustomResourceDefinition, error) {
	entries, err := embedded.ReadDir("crds")
	if err != nil {
		return nil, err
	}
	var out []*apiextensionsv1.CustomResourceDefinition
	for _, e := range entries {
		raw, err := embedded.ReadFile("crds/" + e.Name())
		if err != nil {
			return nil, err
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			return nil, err
		}
		out = append(out, &crd)
	}
	return out, nil
}
