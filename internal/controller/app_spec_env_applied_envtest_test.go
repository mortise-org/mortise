package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// Own file on purpose: every condition added to updateStatus used to add
// its spec at the same anchor in app_controller_test.go, and rebases
// conflicted on every merge.
var _ = Describe("SpecEnvApplied condition (CAI-272)", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"
	ctx := context.Background()

	AfterEach(func() { purgeAllAppsIn(ctx, namespace) })

	It("reports spec keys the derived Secret no longer tracks (CAI-272)", func() {
		appName := "override-report"
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Network: mortisev1alpha1.NetworkConfig{Public: true},
				Environments: []mortisev1alpha1.Environment{{
					Name: "production",
					Env: []mortisev1alpha1.EnvVar{
						{Name: "ALLOWED_ORIGINS", Value: "https://auto.example"},
						{Name: "LOG_LEVEL", Value: "info"},
					},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

		reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace}}
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		var fresh mortisev1alpha1.App
		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		Expect(fresh.Status.Environments[0].OverriddenEnvKeys).To(BeEmpty())
		Expect(meta.FindStatusCondition(fresh.Status.Conditions, "SpecEnvApplied")).To(BeNil())

		var sec corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-env", Namespace: envNsProduction}, &sec)).To(Succeed())
		sec.Data["ALLOWED_ORIGINS"] = []byte("https://real.example")
		Expect(k8sClient.Update(ctx, &sec)).To(Succeed())

		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		fresh.Spec.Environments[0].Env[0].Value = "https://real.example,https://www.real.example"
		Expect(k8sClient.Update(ctx, &fresh)).To(Succeed())
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		envData := readAppEnvSecret(ctx, appName, envNsProduction)
		Expect(envData).To(HaveKeyWithValue("ALLOWED_ORIGINS", "https://real.example"), "override preserved by design")

		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		Expect(fresh.Status.Environments[0].OverriddenEnvKeys).To(Equal([]string{"ALLOWED_ORIGINS"}))
		cond := meta.FindStatusCondition(fresh.Status.Conditions, "SpecEnvApplied")
		Expect(cond).NotTo(BeNil(), "an ignored spec key must be reported")
		Expect(cond.Reason).To(Equal("KeysOverridden"))
		Expect(cond.Message).To(ContainSubstring("production: ALLOWED_ORIGINS"))
		Expect(cond.Message).NotTo(ContainSubstring("real.example"), "names only, never values")

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-env", Namespace: envNsProduction}, &sec)).To(Succeed())
		sec.Data["ALLOWED_ORIGINS"] = []byte("https://real.example,https://www.real.example")
		Expect(k8sClient.Update(ctx, &sec)).To(Succeed())
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		Expect(fresh.Status.Environments[0].OverriddenEnvKeys).To(BeEmpty())
		Expect(meta.FindStatusCondition(fresh.Status.Conditions, "SpecEnvApplied")).To(BeNil())
	})
})
