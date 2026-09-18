package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// CAI-173: an App with a volume must not roll with two pods alive at once.
var _ = Describe("Deployment strategy for Apps with storage (CAI-173)", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"
	ctx := context.Background()

	AfterEach(func() { purgeAllAppsIn(ctx, namespace) })

	It("uses Recreate when the App declares storage and the default otherwise", func() {
		withVol := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "with-vol", Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Storage:      []mortisev1alpha1.VolumeSpec{{Name: "data", MountPath: "/data", Size: resource.MustParse("1Gi")}},
				Environments: []mortisev1alpha1.Environment{{Name: "production"}},
			},
		}
		noVol := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "no-vol", Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Environments: []mortisev1alpha1.Environment{{Name: "production"}},
			},
		}
		for _, app := range []*mortisev1alpha1.App{withVol, noVol} {
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func(a *mortisev1alpha1.App) { Expect(k8sClient.Delete(ctx, a)).To(Succeed()) }(app)
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace}}
			for i := 0; i < 2; i++ {
				_, err := reconciler.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
			}
		}

		var dep appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "with-vol", Namespace: envNsProduction}, &dep)).To(Succeed())
		Expect(dep.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "no-vol", Namespace: envNsProduction}, &dep)).To(Succeed())
		Expect(dep.Spec.Strategy.Type).NotTo(Equal(appsv1.RecreateDeploymentStrategyType))
	})
})
