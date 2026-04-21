package api

import (
	"io"

	corev1 "k8s.io/api/core/v1"
)

// SwapRandReader replaces the entropy source used by generateRandomHex.
// Test-only; returns a function that restores the previous reader. Exposed
// via export_test.go so the api_test package can exercise failure modes.
func SwapRandReader(r io.Reader) func() {
	orig := randReader
	randReader = r
	return func() { randReader = orig }
}

func ParseLogLineForTest(raw string) (string, string) {
	return parseLogLine(raw)
}

func SummarizePodForTest(pod *corev1.Pod) podSummary {
	return summarizePod(pod)
}
