package helpers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

// DumpWorkloadState logs the Deployment, its pods (container states included),
// and the namespace's events. Called when a wait times out: the test's
// namespace is deleted by cleanup before CI's diagnostics run, so this is the
// only record of what the pods were doing.
func DumpWorkloadState(t *testing.T, k8sClient client.Client, ns, name string) {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	fmt.Fprintf(&b, "--- state of %s/%s at timeout ---\n", ns, name)

	var dep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &dep); err != nil {
		fmt.Fprintf(&b, "deployment: %v\n", err)
	} else {
		fmt.Fprintf(&b, "deployment: replicas=%d ready=%d updated=%d available=%d generation=%d observed=%d\n",
			dep.Status.Replicas, dep.Status.ReadyReplicas, dep.Status.UpdatedReplicas, dep.Status.AvailableReplicas, dep.Generation, dep.Status.ObservedGeneration)
		for _, c := range dep.Status.Conditions {
			fmt.Fprintf(&b, "  condition %s=%s %s: %s\n", c.Type, c.Status, c.Reason, c.Message)
		}
	}

	var pods corev1.PodList
	if err := k8sClient.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{constants.AppNameLabel: name}); err != nil {
		fmt.Fprintf(&b, "pods: %v\n", err)
	}
	for _, p := range pods.Items {
		fmt.Fprintf(&b, "pod %s phase=%s node=%s\n", p.Name, p.Status.Phase, p.Spec.NodeName)
		for _, c := range p.Status.Conditions {
			if c.Status != corev1.ConditionTrue {
				fmt.Fprintf(&b, "  condition %s=%s %s: %s\n", c.Type, c.Status, c.Reason, c.Message)
			}
		}
		for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
			state := "running"
			switch {
			case cs.State.Waiting != nil:
				state = "waiting " + cs.State.Waiting.Reason + ": " + cs.State.Waiting.Message
			case cs.State.Terminated != nil:
				state = fmt.Sprintf("terminated %s exit=%d: %s", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode, cs.State.Terminated.Message)
			}
			fmt.Fprintf(&b, "  container %s ready=%v restarts=%d %s\n", cs.Name, cs.Ready, cs.RestartCount, state)
		}
	}

	var events corev1.EventList
	if err := k8sClient.List(ctx, &events, client.InNamespace(ns)); err != nil {
		fmt.Fprintf(&b, "events: %v\n", err)
	}
	sort.Slice(events.Items, func(i, j int) bool { return events.Items[i].LastTimestamp.Before(&events.Items[j].LastTimestamp) })
	for _, e := range events.Items {
		fmt.Fprintf(&b, "event %s %s %s/%s %s: %s\n", e.LastTimestamp.Format("15:04:05"), e.Type, e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Reason, e.Message)
	}
	t.Log(b.String())
}

// DumpAppState logs the App's status and the workload state of every env it
// reports, for the same reason as DumpWorkloadState.
func DumpAppState(t *testing.T, k8sClient client.Client, ns, name string) {
	t.Helper()
	var app mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &app); err != nil {
		t.Logf("app %s/%s: %v", ns, name, err)
		return
	}
	status, _ := yaml.Marshal(app.Status)
	t.Logf("--- App %s/%s status at timeout ---\n%s", ns, name, status)
	project, ok := constants.ProjectFromControlNs(ns)
	if !ok {
		return
	}
	for _, es := range app.Status.Environments {
		DumpWorkloadState(t, k8sClient, constants.EnvNamespace(project, es.Name), name)
	}
}
