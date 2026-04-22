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

var _ = Describe("PlatformConfig Controller", func() {
	ctx := context.Background()

	const secretNS = "default"

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

	makePlatformConfig := func(name string) *mortisev1alpha1.PlatformConfig {
		return &mortisev1alpha1.PlatformConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: mortisev1alpha1.PlatformConfigSpec{
				Domain: "example.com",
			},
		}
	}

	doReconcile := func(name string) error {
		r := &PlatformConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name},
		})
		return err
	}

	deletePC := func(name string) {
		pc := &mortisev1alpha1.PlatformConfig{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, pc); err == nil {
			Expect(k8sClient.Delete(ctx, pc)).To(Succeed())
		}
	}

	Context("happy path — singleton named 'platform'", func() {
		const pcName = "platform"

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, makePlatformConfig(pcName))).To(Succeed())
		})

		AfterEach(func() { deletePC(pcName) })

		It("marks the PlatformConfig as Ready", func() {
			Expect(doReconcile(pcName)).To(Succeed())

			var updated mortisev1alpha1.PlatformConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PlatformConfigPhaseReady))

			var availableCond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == "Available" {
					availableCond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(availableCond).NotTo(BeNil())
			Expect(availableCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(availableCond.Reason).To(Equal("Reconciled"))
		})
	})

	// The "missing registry secret" test uses name "platform" because the
	// singleton check runs before the secret check — without the correct name,
	// we'd get InvalidName rather than SecretNotFound.
	Context("missing registry credentials secret", func() {
		const pcName = "platform"

		BeforeEach(func() {
			pc := makePlatformConfig(pcName)
			pc.Spec.Registry = mortisev1alpha1.RegistryConfig{
				URL: "registry.example.com",
				CredentialsSecretRef: &mortisev1alpha1.SecretRef{
					Namespace: secretNS,
					Name:      "does-not-exist",
					Key:       "username",
				},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
		})

		AfterEach(func() { deletePC(pcName) })

		It("marks the PlatformConfig as Failed with SecretNotFound", func() {
			Expect(doReconcile(pcName)).To(Succeed())

			var updated mortisev1alpha1.PlatformConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PlatformConfigPhaseFailed))

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

	Context("duplicate config — name is not 'platform'", func() {
		const pcName = "not-platform"
		const secretName = "pc-reg-secret-dupe"

		BeforeEach(func() {
			makeSecret(secretNS, secretName, "username", "admin")
			Expect(k8sClient.Create(ctx, makePlatformConfig(pcName))).To(Succeed())
		})

		AfterEach(func() { deletePC(pcName) })

		It("marks the PlatformConfig as Failed with InvalidName", func() {
			Expect(doReconcile(pcName)).To(Succeed())

			var updated mortisev1alpha1.PlatformConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PlatformConfigPhaseFailed))

			var availableCond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == "Available" {
					availableCond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(availableCond).NotTo(BeNil())
			Expect(availableCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(availableCond.Reason).To(Equal("InvalidName"))
		})
	})

	Context("missing observability logs adapter token secret", func() {
		const pcName = "platform"

		BeforeEach(func() {
			pc := makePlatformConfig(pcName)
			pc.Spec.Observability = mortisev1alpha1.ObservabilitySpec{
				LogsAdapterEndpoint: "http://observer:9091",
				LogsAdapterTokenSecretRef: &mortisev1alpha1.SecretRef{
					Namespace: secretNS,
					Name:      "missing-obs-secret",
					Key:       "token",
				},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
		})

		AfterEach(func() { deletePC(pcName) })

		It("marks the PlatformConfig as Failed with SecretNotFound", func() {
			Expect(doReconcile(pcName)).To(Succeed())

			var updated mortisev1alpha1.PlatformConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.PlatformConfigPhaseFailed))

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

	Context("resource does not exist", func() {
		It("returns nil without error", func() {
			Expect(doReconcile("does-not-exist")).To(Succeed())
		})
	})
})
