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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
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

	Context("PVC reconciliation from spec.storage", func() {
		ctx := context.Background()

		newStorageApp := func(name string, vols []mortisev1alpha1.VolumeSpec) *mortisev1alpha1.App {
			return &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Storage: vols,
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
						},
					},
				},
			}
		}

		It("should create a PVC with correct size and access mode", func() {
			app := newStorageApp("test-pvc-basic", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/data", Size: resource.MustParse("10Gi")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-basic-data", Namespace: namespace,
			}, &pvc)).To(Succeed())

			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("10Gi"))).To(BeTrue())
		})

		It("should create a PVC with custom storage class and access mode", func() {
			app := newStorageApp("test-pvc-sc", []mortisev1alpha1.VolumeSpec{
				{
					Name:         "data",
					MountPath:    "/data",
					Size:         resource.MustParse("5Gi"),
					StorageClass: "fast-ssd",
					AccessMode:   "ReadWriteMany",
				},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-sc-data", Namespace: namespace,
			}, &pvc)).To(Succeed())

			Expect(pvc.Spec.StorageClassName).NotTo(BeNil())
			Expect(*pvc.Spec.StorageClassName).To(Equal("fast-ssd"))
			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteMany))
		})

		It("should be idempotent on re-reconcile with same size", func() {
			app := newStorageApp("test-pvc-idem", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/data", Size: resource.MustParse("10Gi")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Reconcile again with unchanged size — should not error
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-idem-data", Namespace: namespace,
			}, &pvc)).To(Succeed())
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("10Gi"))).To(BeTrue())
		})

		It("should set owner reference for garbage collection", func() {
			app := newStorageApp("test-pvc-owner", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/data", Size: resource.MustParse("1Gi")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-owner-data", Namespace: namespace,
			}, &pvc)).To(Succeed())

			Expect(pvc.OwnerReferences).To(HaveLen(1))
			Expect(pvc.OwnerReferences[0].Name).To(Equal("test-pvc-owner"))
			Expect(*pvc.OwnerReferences[0].Controller).To(BeTrue())
		})

		It("should wire PVC into Deployment volume mounts", func() {
			app := newStorageApp("test-pvc-mount", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/var/lib/postgresql/data", Size: resource.MustParse("10Gi")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-mount-production", Namespace: namespace,
			}, &dep)).To(Succeed())

			Expect(dep.Spec.Template.Spec.Volumes).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("test-pvc-mount-data"))
			Expect(dep.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath).To(Equal("/var/lib/postgresql/data"))
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

	Context("deploy history tracking", func() {
		const appName = "test-history"
		ctx := context.Background()

		var (
			app        *mortisev1alpha1.App
			reconciler *AppReconciler
			fakeClock  *clocktesting.FakeClock
		)

		BeforeEach(func() {
			fakeClock = clocktesting.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			reconciler = &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Clock: fakeClock}

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

		It("should record one deploy history entry on first reconcile", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments).To(HaveLen(1))
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(1))
			Expect(app.Status.Environments[0].DeployHistory[0].Image).To(Equal("nginx:1.26"))
		})

		It("should not duplicate entry on re-reconcile with same image", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Re-fetch to get status with deploy history before second reconcile.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())

			fakeClock.Step(time.Minute)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(1))
		})

		It("should add a second entry when image changes, newest first", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Re-fetch, update image, reconcile again.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			app.Spec.Source.Image = "nginx:1.27"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			fakeClock.Step(5 * time.Minute)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			history := app.Status.Environments[0].DeployHistory
			Expect(history).To(HaveLen(2))
			Expect(history[0].Image).To(Equal("nginx:1.27"))
			Expect(history[1].Image).To(Equal("nginx:1.26"))
		})

		It("should cap deploy history at 20 entries", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			for i := 1; i <= 25; i++ {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
				app.Spec.Source.Image = fmt.Sprintf("nginx:1.%d", i)
				Expect(k8sClient.Update(ctx, app)).To(Succeed())

				fakeClock.Step(time.Minute)
				_, err = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(20))
			// Newest first: most recent image should be at index 0.
			Expect(app.Status.Environments[0].DeployHistory[0].Image).To(Equal("nginx:1.25"))
		})
	})

	Context("rollback", func() {
		const appName = "test-rollback"
		ctx := context.Background()

		var (
			app        *mortisev1alpha1.App
			reconciler *AppReconciler
			fakeClock  *clocktesting.FakeClock
		)

		BeforeEach(func() {
			fakeClock = clocktesting.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			reconciler = &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Clock: fakeClock}

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

		It("should rollback Deployment to a previous image", func() {
			// First reconcile with nginx:1.26.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Update to nginx:1.27 and reconcile.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			app.Spec.Source.Image = "nginx:1.27"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			fakeClock.Step(time.Minute)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Re-fetch to get updated status.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(2))

			// Rollback to index 1 (nginx:1.26).
			err = reconciler.RollbackDeployment(ctx, app, "production", 1)
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-rollback-production", Namespace: namespace,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.26"))
		})

		It("should return error for invalid history index", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			err = reconciler.RollbackDeployment(ctx, app, "production", 5)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("out of range"))
		})

		It("should return error for unknown environment", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			err = reconciler.RollbackDeployment(ctx, app, "staging", 0)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
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
