package helpers

import (
	"path/filepath"
	"runtime"

	"k8s.io/apimachinery/pkg/util/rand"
)

func FixturesDir() string {
	_, thisFile, _, _ := runtime.Caller(1)
	return filepath.Join(filepath.Dir(thisFile), "..", "fixtures")
}

func RandSuffix() string {
	return rand.String(6)
}
