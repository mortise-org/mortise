//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestStorageProvisionsPVCAndMount(t *testing.T) {
	t.Parallel()
	projectName := "stor-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-postgres.yaml"))
	app.Namespace = ns

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("failed to create App: %v", err)
	}

	appName := app.Name
	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)
	resourceName := appName
	pvcName := appName + "-" + app.Spec.Storage[0].Name
	mountPath := app.Spec.Storage[0].MountPath
	wantSize := app.Spec.Storage[0].Size

	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var pvc corev1.PersistentVolumeClaim
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      pvcName,
			Namespace: envNs,
		}, &pvc) == nil
	})

	var pvc corev1.PersistentVolumeClaim
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      pvcName,
		Namespace: envNs,
	}, &pvc); err != nil {
		t.Fatalf("get PVC %s: %v", pvcName, err)
	}

	gotSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if gotSize.Cmp(wantSize) != 0 {
		t.Errorf("PVC storage request: got %s, want %s", gotSize.String(), wantSize.String())
	}
	if wantSize.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Errorf("fixture sanity: expected 1Gi, got %s", wantSize.String())
	}

	helpers.AssertDeploymentExists(t, k8sClient, envNs, resourceName)

	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      resourceName,
		Namespace: envNs,
	}, &dep); err != nil {
		t.Fatalf("get Deployment %s: %v", resourceName, err)
	}

	var foundPVCVol bool
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == pvcName {
			foundPVCVol = true
			break
		}
	}
	if !foundPVCVol {
		t.Errorf("Deployment %s has no volume referencing PVC %s; volumes=%+v",
			resourceName, pvcName, dep.Spec.Template.Spec.Volumes)
	}

	if len(dep.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("Deployment has no containers")
	}
	var foundMount bool
	for _, m := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.MountPath == mountPath {
			foundMount = true
			break
		}
	}
	if !foundMount {
		t.Errorf("container has no volumeMount at %s; mounts=%+v",
			mountPath, dep.Spec.Template.Spec.Containers[0].VolumeMounts)
	}

	t.Run("PVC garbage-collected when App is deleted", func(t *testing.T) {
		if err := k8sClient.Delete(context.Background(), app); err != nil {
			t.Fatalf("delete App: %v", err)
		}

		helpers.RequireEventually(t, 2*time.Minute, func() bool {
			var p corev1.PersistentVolumeClaim
			err := k8sClient.Get(context.Background(), types.NamespacedName{
				Name:      pvcName,
				Namespace: envNs,
			}, &p)
			return apierrors.IsNotFound(err)
		})
	})
}
