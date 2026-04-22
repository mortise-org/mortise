/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

var _ = Describe("GitProvider Controller", func() {
	ctx := context.Background()

	// secretNS is the namespace we create test secrets in.
	const secretNS = "default"

	// makeSecret creates a simple secret in the given namespace.
	makeSecret := func(ns, name, key, value string) {
		s := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string][]byte{key: []byte(value)},
		}
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &corev1.Secret{})
		if errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, s)).To(Succeed())
		}
	}

	// makeProvider builds a GitProvider with three secretRefs all pointing to the
	// same secret / key for convenience in tests.
	makeProvider := func(name string, providerType mortisev1alpha1.GitProviderType, secretName string) *mortisev1alpha1.GitProvider {
		ref := mortisev1alpha1.SecretRef{Namespace: secretNS, Name: secretName, Key: "value"}
		return &mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: mortisev1alpha1.GitProviderSpec{
				Type:             providerType,
				Host:             "https://github.com",
				ClientID:         "test-id",
				WebhookSecretRef: &ref,
			},
		}
	}

	reconcile := func(name string) error {
		r := &GitProviderReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name},
		})
		return err
	}

	Context("when all secrets exist", func() {
		const gpName = "gp-ready"
		const secretName = "gp-secret-ready"

		BeforeEach(func() {
			makeSecret(secretNS, secretName, "value", "test-value")
			gp := makeProvider(gpName, mortisev1alpha1.GitProviderTypeGitHub, secretName)
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
		})

		AfterEach(func() {
			gp := &mortisev1alpha1.GitProvider{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: gpName}, gp); err == nil {
				Expect(k8sClient.Delete(ctx, gp)).To(Succeed())
			}
		})

		It("marks the GitProvider as Ready", func() {
			Expect(reconcile(gpName)).To(Succeed())

			var updated mortisev1alpha1.GitProvider
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gpName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.GitProviderPhaseReady))

			var availableCond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == "Available" {
					availableCond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(availableCond).NotTo(BeNil())
			Expect(availableCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("when a required secret is missing", func() {
		const gpName = "gp-missing-secret"

		BeforeEach(func() {
			gp := makeProvider(gpName, mortisev1alpha1.GitProviderTypeGitHub, "does-not-exist")
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
		})

		AfterEach(func() {
			gp := &mortisev1alpha1.GitProvider{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: gpName}, gp); err == nil {
				Expect(k8sClient.Delete(ctx, gp)).To(Succeed())
			}
		})

		It("marks the GitProvider as Failed", func() {
			Expect(reconcile(gpName)).To(Succeed())

			var updated mortisev1alpha1.GitProvider
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gpName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.GitProviderPhaseFailed))
		})
	})

	Context("when the secret exists but the key is absent", func() {
		const gpName = "gp-missing-key"
		const secretName = "gp-secret-missing-key"

		BeforeEach(func() {
			// Secret exists but key "value" is absent.
			s := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
				Data:       map[string][]byte{"other-key": []byte("x")},
			}
			Expect(k8sClient.Create(ctx, s)).To(Succeed())
			gp := makeProvider(gpName, mortisev1alpha1.GitProviderTypeGitHub, secretName)
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
		})

		AfterEach(func() {
			gp := &mortisev1alpha1.GitProvider{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: gpName}, gp); err == nil {
				Expect(k8sClient.Delete(ctx, gp)).To(Succeed())
			}
		})

		It("marks the GitProvider as Failed", func() {
			Expect(reconcile(gpName)).To(Succeed())

			var updated mortisev1alpha1.GitProvider
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gpName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.GitProviderPhaseFailed))

			var availableCond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == "Available" {
					availableCond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(availableCond).NotTo(BeNil())
			Expect(availableCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(availableCond.Reason).To(Equal("SecretNotFound"))
		})
	})

	Context("when the resource does not exist", func() {
		It("returns nil without error", func() {
			Expect(reconcile("does-not-exist")).To(Succeed())
		})
	})

	Context("reconciling each provider type", func() {
		for _, pt := range []mortisev1alpha1.GitProviderType{
			mortisev1alpha1.GitProviderTypeGitHub,
			mortisev1alpha1.GitProviderTypeGitLab,
			mortisev1alpha1.GitProviderTypeGitea,
		} {
			pt := pt // capture loop var
			name := "gp-type-" + string(pt)
			secretName := "gp-secret-type-" + string(pt)

			Context("type="+string(pt), func() {
				BeforeEach(func() {
					makeSecret(secretNS, secretName, "value", "test-value")
					gp := makeProvider(name, pt, secretName)
					Expect(k8sClient.Create(ctx, gp)).To(Succeed())
				})

				AfterEach(func() {
					gp := &mortisev1alpha1.GitProvider{}
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, gp); err == nil {
						Expect(k8sClient.Delete(ctx, gp)).To(Succeed())
					}
				})

				It("reaches Ready phase", func() {
					Expect(reconcile(name)).To(Succeed())
					var updated mortisev1alpha1.GitProvider
					Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, &updated)).To(Succeed())
					Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.GitProviderPhaseReady))
				})
			})
		}
	})
})
