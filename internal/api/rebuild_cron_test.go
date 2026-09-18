package api_test

import (
	"context"
	"net/http"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

// Redeploying a cron App used to fetch a Deployment that does not exist
// and fail NotFound (CAI-170). It now stamps the CronJob's job template, so
// the next scheduled run picks up the current env.
func TestRedeployCronApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	ctx := context.Background()

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Kind:         mortisev1alpha1.AppKindCron,
			Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "busybox:1.37"},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	envNs := constants.EnvNamespace("default", "production")
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: constants.CronJobName("nightly"), Namespace: envNs},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 3 * * *",
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "app", Image: "busybox:1.37"}}},
			}}},
		},
	}
	if err := k8sClient.Create(ctx, cj); err != nil {
		t.Fatalf("create cronjob: %v", err)
	}
	app.Status.Phase = mortisev1alpha1.AppPhaseReady
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{{Name: "production", Phase: mortisev1alpha1.AppPhaseReady, PendingEnvHash: "abc"}}
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update status: %v", err)
	}

	w := doRequest(h, http.MethodPost, "/api/projects/default/apps/nightly/redeploy?environment=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("redeploy cron app: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: cj.Name, Namespace: envNs}, cj); err != nil {
		t.Fatalf("get cronjob: %v", err)
	}
	ann := cj.Spec.JobTemplate.Spec.Template.Annotations
	if ann["mortise.dev/restartedAt"] == "" {
		t.Error("expected restartedAt on the job template")
	}
	if ann["mortise.dev/env-hash"] != "abc" {
		t.Errorf("expected the pending env hash on the job template, got %q", ann["mortise.dev/env-hash"])
	}
}
