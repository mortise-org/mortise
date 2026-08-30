package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// CAI-173: with the parent Project unresolvable from the cache, the status
// pass used to write an empty env list and Phase=Deploying over a Ready App,
// and nothing re-queued it. The reconcile must leave status alone and requeue.
var _ = Describe("status pass without a resolvable Project (CAI-173)", func() {
	const namespace = "pj-noproject"
	ctx := context.Background()

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, ns))).To(Succeed())
	})
	AfterEach(func() { purgeAllAppsIn(ctx, namespace) })

	It("keeps the App's env statuses and phase and requeues", func() {
		appName := "orphan"
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Environments: []mortisev1alpha1.Environment{{Name: "production"}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()
		app.Status.Phase = mortisev1alpha1.AppPhaseReady
		app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{{Name: "production", Phase: mortisev1alpha1.AppPhaseReady, ReadyReplicas: 1}}
		Expect(k8sClient.Status().Update(ctx, app)).To(Succeed())

		reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace}}
		res, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))

		var fresh mortisev1alpha1.App
		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		Expect(fresh.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
		Expect(fresh.Status.Environments).To(HaveLen(1))
	})
})
