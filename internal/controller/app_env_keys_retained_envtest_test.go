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

// Own file on purpose: specs added at the shared anchor in
// app_controller_test.go conflicted on every rebase.
var _ = Describe("EnvKeysRetained condition (CAI-154)", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"
	ctx := context.Background()

	AfterEach(func() { purgeAllAppsIn(ctx, namespace) })

	It("marks and reports a removed key that was kept because it had been edited out of band (CAI-154)", func() {
		appName := "prune-report"
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Network: mortisev1alpha1.NetworkConfig{Public: true},
				Environments: []mortisev1alpha1.Environment{{
					Name: "production",
					Env: []mortisev1alpha1.EnvVar{
						{Name: "TWITTER_REDIRECT_URI", Value: "https://x/cb"},
						{Name: "YOUTUBE_REDIRECT_URI", Value: "https://y/cb"},
						{Name: "KEEP_ME", Value: "1"},
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

		var sec corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-env", Namespace: envNsProduction}, &sec)).To(Succeed())
		sec.Data["YOUTUBE_REDIRECT_URI"] = []byte("https://y/other")
		Expect(k8sClient.Update(ctx, &sec)).To(Succeed())

		var fresh mortisev1alpha1.App
		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		fresh.Spec.Environments[0].Env = []mortisev1alpha1.EnvVar{{Name: "KEEP_ME", Value: "1"}}
		Expect(k8sClient.Update(ctx, &fresh)).To(Succeed())
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}

		envData := readAppEnvSecret(ctx, appName, envNsProduction)
		Expect(envData).NotTo(HaveKey("TWITTER_REDIRECT_URI"), "tracked the spec: pruned")
		Expect(envData).To(HaveKeyWithValue("YOUTUBE_REDIRECT_URI", "https://y/other"), "edited out of band: kept, by design")

		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		Expect(fresh.Status.Environments[0].RetainedEnvKeys).To(Equal([]string{"YOUTUBE_REDIRECT_URI"}))
		cond := meta.FindStatusCondition(fresh.Status.Conditions, "EnvKeysRetained")
		Expect(cond).NotTo(BeNil(), "an incomplete prune must be reported")
		Expect(cond.Reason).To(Equal("RemovedButKept"))
		Expect(cond.Message).To(ContainSubstring("production: YOUTUBE_REDIRECT_URI"))
		Expect(cond.Message).NotTo(ContainSubstring("y/other"), "names only")

		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		fresh.Spec.Environments[0].Env = append(fresh.Spec.Environments[0].Env, mortisev1alpha1.EnvVar{Name: "YOUTUBE_REDIRECT_URI", Value: "https://y/other"})
		Expect(k8sClient.Update(ctx, &fresh)).To(Succeed())
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(k8sClient.Get(ctx, req.NamespacedName, &fresh)).To(Succeed())
		Expect(fresh.Status.Environments[0].RetainedEnvKeys).To(BeEmpty())
		Expect(meta.FindStatusCondition(fresh.Status.Conditions, "EnvKeysRetained")).To(BeNil())
	})
})
