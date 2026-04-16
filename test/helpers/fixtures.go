package helpers

import (
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// LoadFixture reads a YAML fixture file and decodes it into an App.
func LoadFixture(t *testing.T, path string) *mortisev1alpha1.App {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", path, err)
	}

	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add scheme: %v", err)
	}

	codecs := serializer.NewCodecFactory(scheme)
	deserializer := codecs.UniversalDeserializer()

	obj, _, err := deserializer.Decode(data, nil, nil)
	if err != nil {
		t.Fatalf("failed to decode fixture %s: %v", path, err)
	}

	app, ok := obj.(*mortisev1alpha1.App)
	if !ok {
		t.Fatalf("fixture %s is not an App", path)
	}
	return app
}
