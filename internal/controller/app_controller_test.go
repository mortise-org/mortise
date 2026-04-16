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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

var _ = Describe("App Controller", func() {
	const namespace = "default"

	Context("image source with one environment", func() {
		const appName = "test-nginx"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "nginx:1.27",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](2),
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU:    "100m",
								Memory: "128Mi",
							},
							Domain: "nginx.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should create a Deployment with correct spec", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx-production", Namespace: namespace,
			}, &dep)).To(Succeed())

			Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.27"))
			Expect(dep.Labels["app.kubernetes.io/managed-by"]).To(Equal("mortise"))
			Expect(dep.Labels["mortise.dev/environment"]).To(Equal("production"))
		})

		It("should create a Service targeting the Deployment", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx-production", Namespace: namespace,
			}, &svc)).To(Succeed())

			Expect(svc.Spec.Selector["app.kubernetes.io/name"]).To(Equal(appName))
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(80)))
		})

		It("should create an Ingress with TLS for the domain", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx-production", Namespace: namespace,
			}, &ing)).To(Succeed())

			Expect(ing.Spec.Rules).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].Host).To(Equal("nginx.example.com"))
			Expect(ing.Spec.TLS).To(HaveLen(1))
			Expect(ing.Spec.TLS[0].Hosts).To(ContainElement("nginx.example.com"))
			Expect(ing.Annotations["cert-manager.io/cluster-issuer"]).To(Equal("letsencrypt-prod"))
		})
	})

	Context("image source with no domain (private service)", func() {
		const appName = "test-db"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network:     mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []string{"DATABASE_URL", "host", "port"},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Env: []mortisev1alpha1.EnvVar{
								{Name: "POSTGRES_PASSWORD", Value: "testpass"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should create Deployment and Service but no Ingress", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-db-production", Namespace: namespace,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("postgres:16"))
			Expect(dep.Spec.Template.Spec.Containers[0].Env).To(ContainElement(
				corev1.EnvVar{Name: "POSTGRES_PASSWORD", Value: "testpass"},
			))

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-db-production", Namespace: namespace,
			}, &svc)).To(Succeed())

			var ing networkingv1.Ingress
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-db-production", Namespace: namespace,
			}, &ing)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("bindings resolution", func() {
		const (
			dbAppName  = "my-db"
			apiAppName = "my-api"
		)
		ctx := context.Background()

		var dbApp, apiApp *mortisev1alpha1.App

		BeforeEach(func() {
			dbApp = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      dbAppName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network:     mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []string{"DATABASE_URL", "host", "port", "user", "password"},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Env: []mortisev1alpha1.EnvVar{
								{Name: "POSTGRES_PASSWORD", Value: "testpass"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dbApp)).To(Succeed())

			// Reconcile db app first so its Service exists
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: dbAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			apiApp = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      apiAppName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "my-api:latest",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Bindings: []mortisev1alpha1.Binding{
								{Ref: dbAppName},
							},
							Domain: "api.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, apiApp)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, apiApp)).To(Succeed())
			Expect(k8sClient.Delete(ctx, dbApp)).To(Succeed())
		})

		It("should inject bound credentials as env vars in the binder Deployment", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "my-api-production", Namespace: namespace,
			}, &dep)).To(Succeed())

			envVars := dep.Spec.Template.Spec.Containers[0].Env

			// host should be a literal Service DNS value
			hostVar := findEnvVar(envVars, "host")
			Expect(hostVar).NotTo(BeNil())
			Expect(hostVar.Value).To(Equal("my-db-production.default.svc.cluster.local"))

			// port should be a literal value
			portVar := findEnvVar(envVars, "port")
			Expect(portVar).NotTo(BeNil())
			Expect(portVar.Value).To(Equal("80"))

			// DATABASE_URL should be a secretKeyRef
			dbURLVar := findEnvVar(envVars, "DATABASE_URL")
			Expect(dbURLVar).NotTo(BeNil())
			Expect(dbURLVar.ValueFrom).NotTo(BeNil())
			Expect(dbURLVar.ValueFrom.SecretKeyRef.Name).To(Equal("my-db-credentials"))
			Expect(dbURLVar.ValueFrom.SecretKeyRef.Key).To(Equal("DATABASE_URL"))

			// user should be a secretKeyRef
			userVar := findEnvVar(envVars, "user")
			Expect(userVar).NotTo(BeNil())
			Expect(userVar.ValueFrom).NotTo(BeNil())
			Expect(userVar.ValueFrom.SecretKeyRef.Name).To(Equal("my-db-credentials"))

			// password should be a secretKeyRef
			passVar := findEnvVar(envVars, "password")
			Expect(passVar).NotTo(BeNil())
			Expect(passVar.ValueFrom).NotTo(BeNil())
			Expect(passVar.ValueFrom.SecretKeyRef.Name).To(Equal("my-db-credentials"))
		})
	})

	Context("updating an existing App", func() {
		const appName = "test-update"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "nginx:1.26",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should update Deployment when image changes", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-update-production", Namespace: namespace,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.26"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, app)).To(Succeed())
			app.Spec.Source.Image = "nginx:1.27"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-update-production", Namespace: namespace,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.27"))
		})
	})
})

func findEnvVar(envVars []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			return &envVars[i]
		}
	}
	return nil
}
