package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// Own file on purpose: specs added at the shared anchor in
// app_controller_test.go conflicted on every rebase.
//
// CAI-314: with autoRedeploy off the pod-template env-hash is frozen, and a
// `kubectl rollout restart` — whose new pods read the current Secret — never
// re-stamped it, so EnvRolledOut=False sat permanently on pods that were
// demonstrably current (production postlab-api). The status pass must adopt
// the pending hash once the template carries a restart marker newer than the
// env Secrets' last write and the roll has settled.
var _ = Describe("EnvRolledOut after an out-of-band restart (CAI-314)", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"
	ctx := context.Background()

	AfterEach(func() { purgeAllAppsIn(ctx, namespace) })

	It("clears RedeployPending when a kubectl rollout restart postdates the env change", func() {
		appName := "restart-clears"
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Network: mortisev1alpha1.NetworkConfig{Public: true},
				Environments: []mortisev1alpha1.Environment{{
					Name: "production",
					Env:  []mortisev1alpha1.EnvVar{{Name: "API_KEY", Value: "old"}},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())

		reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace}}
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		// Rotate the value. No Project exists, so autoRedeploy is off, the
		// pod-template hash freezes, and the condition appears (CAI-153).
		var fresh mortisev1alpha1.App
		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		fresh.Spec.Environments[0].Env[0].Value = "new"
		Expect(k8sClient.Update(ctx, &fresh)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		cond := meta.FindStatusCondition(fresh.Status.Conditions, "EnvRolledOut")
		Expect(cond).NotTo(BeNil(), "precondition: the divergence must be reported first")
		Expect(cond.Reason).To(Equal("RedeployPending"))

		// The user runs `kubectl rollout restart deploy/<app>`: kubectl
		// stamps its restartedAt on the pod template. The new pods read the
		// current Secret, so from here the frozen hash is a false claim.
		var dep appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: envNsProduction}, &dep)).To(Succeed())
		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = map[string]string{}
		}
		dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] =
			time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
		Expect(k8sClient.Update(ctx, &dep)).To(Succeed())

		// envtest runs no deployment controller; settle the rollout by hand.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: envNsProduction}, &dep)).To(Succeed())
		dep.Status.ObservedGeneration = dep.Generation
		dep.Status.Replicas = 1
		dep.Status.UpdatedReplicas = 1
		dep.Status.AvailableReplicas = 1
		dep.Status.ReadyReplicas = 1
		Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		// The reconcile may write the Deployment (bumping generation); settle
		// again so the status pass sees a finished roll, as production would.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: envNsProduction}, &dep)).To(Succeed())
		if dep.Status.ObservedGeneration != dep.Generation {
			dep.Status.ObservedGeneration = dep.Generation
			Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())
		}
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		Expect(meta.FindStatusCondition(fresh.Status.Conditions, "EnvRolledOut")).To(BeNil(),
			"a restart after the env change proves the pods current; the condition must clear")
		es := fresh.Status.Environments[0]
		Expect(es.DeployedEnvHash).To(Equal(es.PendingEnvHash))
	})

	It("keeps RedeployPending when the restart predates the env change", func() {
		appName := "stale-restart-keeps"
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Network: mortisev1alpha1.NetworkConfig{Public: true},
				Environments: []mortisev1alpha1.Environment{{
					Name: "production",
					Env:  []mortisev1alpha1.EnvVar{{Name: "API_KEY", Value: "old"}},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())

		reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace}}
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		// A restart that happened BEFORE the env change proves nothing.
		var dep appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: envNsProduction}, &dep)).To(Succeed())
		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = map[string]string{}
		}
		dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] =
			time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		Expect(k8sClient.Update(ctx, &dep)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: envNsProduction}, &dep)).To(Succeed())
		dep.Status.ObservedGeneration = dep.Generation
		dep.Status.Replicas = 1
		dep.Status.UpdatedReplicas = 1
		dep.Status.AvailableReplicas = 1
		dep.Status.ReadyReplicas = 1
		Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())

		var fresh mortisev1alpha1.App
		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		fresh.Spec.Environments[0].Env[0].Value = "new"
		Expect(k8sClient.Update(ctx, &fresh)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: envNsProduction}, &dep)).To(Succeed())
		if dep.Status.ObservedGeneration != dep.Generation {
			dep.Status.ObservedGeneration = dep.Generation
			Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())
		}
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		cond := meta.FindStatusCondition(fresh.Status.Conditions, "EnvRolledOut")
		Expect(cond).NotTo(BeNil(), "a pre-change restart must not clear the divergence")
		Expect(cond.Reason).To(Equal("RedeployPending"))
	})
})
