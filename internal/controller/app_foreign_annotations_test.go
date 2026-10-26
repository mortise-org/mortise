package controller

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// The mortise issue 631 regression: assigning desired.Annotations over the
// whole map wiped every annotation Mortise didn't set. On a PVC that removed
// pv.kubernetes.io/bind-completed, which the PV controller immediately
// restored, and the two controllers fought at ~40 writes/s forever — measured
// at exactly that rate on our own cluster. The reconciler must manage only its
// own keys and must not write a resource whose managed keys are already right.
var _ = Describe("Foreign annotations on managed resources (issue 631)", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"
	ctx := context.Background()

	AfterEach(func() { purgeAllAppsIn(ctx, namespace) })

	It("preserves foreign PVC annotations and stops writing a settled PVC", func() {
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "ann-pvc", Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Storage:      []mortisev1alpha1.VolumeSpec{{Name: "data", MountPath: "/data", Size: resource.MustParse("1Gi")}},
				Environments: []mortisev1alpha1.Environment{{Name: "production"}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()
		reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace}}
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}

		// What the PV controller does the moment a claim binds. envtest runs
		// no PV controller, so if the reconcile wipes this, nothing restores
		// it and the assertion below sees the user-visible half of the fight.
		pvcKey := types.NamespacedName{Name: pvcName(app.Name, "data"), Namespace: envNsProduction}
		var pvc corev1.PersistentVolumeClaim
		Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
		if pvc.Annotations == nil {
			pvc.Annotations = map[string]string{}
		}
		pvc.Annotations["pv.kubernetes.io/bind-completed"] = "yes"
		Expect(k8sClient.Update(ctx, &pvc)).To(Succeed())

		Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
		settledRV := pvc.ResourceVersion

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
		Expect(pvc.Annotations).To(HaveKeyWithValue("pv.kubernetes.io/bind-completed", "yes"),
			"the reconciler must not remove annotations it never set")
		Expect(pvc.ResourceVersion).To(Equal(settledRV),
			"a PVC whose managed keys are already right must not be written at all")
	})

	It("adds and removes env-spec PVC annotations without touching foreign ones", func() {
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "ann-env", Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Storage: []mortisev1alpha1.VolumeSpec{{Name: "data", MountPath: "/data", Size: resource.MustParse("1Gi")}},
				Environments: []mortisev1alpha1.Environment{{
					Name:        "production",
					Annotations: map[string]string{"backup.example.com/policy": "daily"},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()
		reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace}}
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}

		pvcKey := types.NamespacedName{Name: pvcName(app.Name, "data"), Namespace: envNsProduction}
		var pvc corev1.PersistentVolumeClaim
		Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
		Expect(pvc.Annotations).To(HaveKeyWithValue("backup.example.com/policy", "daily"))

		pvc.Annotations["pv.kubernetes.io/bind-completed"] = "yes"
		Expect(k8sClient.Update(ctx, &pvc)).To(Succeed())

		// Dropping the env annotation must remove it from the PVC — the
		// bookkeeping tells a formerly-managed key apart from a foreign one —
		// while the foreign key stays.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
		app.Spec.Environments[0].Annotations = nil
		Expect(k8sClient.Update(ctx, app)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
		Expect(pvc.Annotations).NotTo(HaveKey("backup.example.com/policy"),
			"a managed key dropped from the spec must be removed")
		Expect(pvc.Annotations).To(HaveKeyWithValue("pv.kubernetes.io/bind-completed", "yes"))
	})

	It("preserves foreign annotations on the Service and Deployment", func() {
		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: "ann-svc", Namespace: namespace},
			Spec: mortisev1alpha1.AppSpec{
				Source:       mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
				Network:      mortisev1alpha1.NetworkConfig{Public: false, Port: 80},
				Environments: []mortisev1alpha1.Environment{{Name: "production"}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()
		reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace}}
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}

		svcKey := types.NamespacedName{Name: app.Name, Namespace: envNsProduction}
		var svc corev1.Service
		Expect(k8sClient.Get(ctx, svcKey, &svc)).To(Succeed())
		if svc.Annotations == nil {
			svc.Annotations = map[string]string{}
		}
		svc.Annotations["external-dns.alpha.kubernetes.io/ttl"] = "60"
		Expect(k8sClient.Update(ctx, &svc)).To(Succeed())

		var dep = deploymentFor(ctx, app.Name, envNsProduction)
		if dep.Annotations == nil {
			dep.Annotations = map[string]string{}
		}
		dep.Annotations["deployment.kubernetes.io/revision"] = "3"
		dep.Annotations["fluxcd.io/ignore"] = "true"
		Expect(k8sClient.Update(ctx, dep)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, svcKey, &svc)).To(Succeed())
		Expect(svc.Annotations).To(HaveKeyWithValue("external-dns.alpha.kubernetes.io/ttl", "60"))

		dep = deploymentFor(ctx, app.Name, envNsProduction)
		Expect(dep.Annotations).To(HaveKeyWithValue("deployment.kubernetes.io/revision", "3"))
		Expect(dep.Annotations).To(HaveKeyWithValue("fluxcd.io/ignore", "true"),
			"the fix must cover foreign keys generally, not a hand-kept prefix list")
	})
})

func TestMergeManagedAnnotations(t *testing.T) {
	t.Run("foreign keys survive, desired keys apply", func(t *testing.T) {
		got, changed := mergeManagedAnnotations(
			map[string]string{"pv.kubernetes.io/bind-completed": "yes"},
			map[string]string{"backup.example.com/policy": "daily"},
		)
		if !changed {
			t.Fatal("expected a change")
		}
		if got["pv.kubernetes.io/bind-completed"] != "yes" {
			t.Error("foreign key wiped")
		}
		if got["backup.example.com/policy"] != "daily" {
			t.Error("desired key not applied")
		}
		if got[managedAnnotationsKey] != "backup.example.com/policy" {
			t.Errorf("bookkeeping = %q", got[managedAnnotationsKey])
		}
	})

	t.Run("nothing desired, nothing managed: no change", func(t *testing.T) {
		existing := map[string]string{"pv.kubernetes.io/bind-completed": "yes"}
		got, changed := mergeManagedAnnotations(existing, nil)
		if changed {
			t.Fatalf("expected no change, got %v", got)
		}
	})

	t.Run("a dropped managed key is removed; foreign keys stay", func(t *testing.T) {
		existing := map[string]string{
			"backup.example.com/policy":       "daily",
			"pv.kubernetes.io/bind-completed": "yes",
			managedAnnotationsKey:             "backup.example.com/policy",
		}
		got, changed := mergeManagedAnnotations(existing, nil)
		if !changed {
			t.Fatal("expected a change")
		}
		if _, ok := got["backup.example.com/policy"]; ok {
			t.Error("dropped managed key kept")
		}
		if got["pv.kubernetes.io/bind-completed"] != "yes" {
			t.Error("foreign key wiped")
		}
		if _, ok := got[managedAnnotationsKey]; ok {
			t.Error("empty bookkeeping must be removed")
		}
	})

	t.Run("settled state reports no change", func(t *testing.T) {
		existing := map[string]string{
			"backup.example.com/policy":       "daily",
			"pv.kubernetes.io/bind-completed": "yes",
			managedAnnotationsKey:             "backup.example.com/policy",
		}
		if _, changed := mergeManagedAnnotations(existing, map[string]string{"backup.example.com/policy": "daily"}); changed {
			t.Error("settled state must not report a change")
		}
	})

	t.Run("empty everything stays nil", func(t *testing.T) {
		got, changed := mergeManagedAnnotations(nil, nil)
		if changed || got != nil {
			t.Errorf("got %v changed=%v", got, changed)
		}
	})
}

func deploymentFor(ctx context.Context, name, ns string) *appsv1.Deployment {
	var dep appsv1.Deployment
	ExpectWithOffset(1, k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &dep)).To(Succeed())
	return &dep
}
