package api

import corev1 "k8s.io/api/core/v1"

// Exported test hooks. Only compiled into _test binaries, so these don't
// broaden the public surface of the api package.

// ParseLogLineForTest exposes parseLogLine for tests outside this package.
func ParseLogLineForTest(raw string) (ts, content string) {
	return parseLogLine(raw)
}

// SummarizePodForTest exposes summarizePod for tests outside this package.
func SummarizePodForTest(pod *corev1.Pod) PodSummary {
	s := summarizePod(pod)
	return PodSummary{
		Name:         s.Name,
		Phase:        s.Phase,
		RestartCount: s.RestartCount,
		Ready:        s.Ready,
		StartedAt:    s.StartedAt,
		CreatedAt:    s.CreatedAt,
	}
}

// PodSummary mirrors the internal podSummary for tests outside this package.
type PodSummary struct {
	Name         string
	Phase        string
	RestartCount int32
	Ready        bool
	StartedAt    string
	CreatedAt    string
}
