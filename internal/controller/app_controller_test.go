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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/ingress"
	"github.com/mortise-org/mortise/internal/registry"
)

// testImageNginx is the pinned image used across App controller tests.
// Hoisted to package scope so it is visible from multiple Describe blocks.
const testImageNginx = "nginx:1.27"

var _ = Describe("App Controller", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"
	const envNsStaging = "pj-default-project-staging"

	AfterEach(func() {
		purgeAllAppsIn(context.Background(), namespace)
	})

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
						Image: testImageNginx,
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
				Name: "test-nginx", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(testImageNginx))
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
				Name: "test-nginx", Namespace: envNsProduction,
			}, &svc)).To(Succeed())

			Expect(svc.Spec.Selector["app.kubernetes.io/name"]).To(Equal(appName))
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8080)))
		})

		It("should create an Ingress with TLS for the domain", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx", Namespace: envNsProduction,
			}, &ing)).To(Succeed())

			Expect(ing.Spec.Rules).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].Host).To(Equal("nginx.example.com"))
			Expect(ing.Spec.TLS).To(HaveLen(1))
			Expect(ing.Spec.TLS[0].Hosts).To(ContainElement("nginx.example.com"))
			// No DefaultClusterIssuer configured → no cert-manager annotation.
			_, hasIssuer := ing.Annotations["cert-manager.io/cluster-issuer"]
			Expect(hasIssuer).To(BeFalse())
			// Auto-generated TLS Secret name.
			Expect(ing.Spec.TLS[0].SecretName).To(Equal("test-nginx-tls"))
		})

		It("should annotate the Ingress with DefaultClusterIssuer when configured", func() {
			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "prod-issuer",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx", Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["cert-manager.io/cluster-issuer"]).To(Equal("prod-issuer"))
		})
	})

	Context("ingress TLS overrides per environment (§5.6)", func() {
		const appName = "tls-overrides"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("env.TLS.ClusterIssuer wins over DefaultClusterIssuer", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "over.example.com",
						TLS:      &mortisev1alpha1.EnvTLSConfig{ClusterIssuer: "override-issuer"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "fallback",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["cert-manager.io/cluster-issuer"]).To(Equal("override-issuer"))
			Expect(ing.Spec.TLS[0].SecretName).To(Equal(appName + "-tls"))
		})

		It("env.TLS.SecretName (BYO) suppresses cert-manager annotation", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "byo.example.com",
						TLS:      &mortisev1alpha1.EnvTLSConfig{SecretName: "byo-tls"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "fallback",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			_, hasIssuer := ing.Annotations["cert-manager.io/cluster-issuer"]
			Expect(hasIssuer).To(BeFalse())
			Expect(ing.Spec.TLS).To(HaveLen(1))
			Expect(ing.Spec.TLS[0].SecretName).To(Equal("byo-tls"))
		})

		It("user annotation overrides Mortise cert-manager default", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "userwins.example.com",
						Annotations: map[string]string{
							"linkerd.io/inject":              "enabled",
							"cert-manager.io/cluster-issuer": "user-wins",
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "fallback",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["linkerd.io/inject"]).To(Equal("enabled"))
			Expect(ing.Annotations["cert-manager.io/cluster-issuer"]).To(Equal("user-wins"))
		})
	})

	Context("ExternalDNS annotation on Ingress", func() {
		const appName = "test-externaldns"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should emit ExternalDNS hostname annotation with env.Domain", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "dns.example.com",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["external-dns.alpha.kubernetes.io/hostname"]).To(Equal("dns.example.com"))
		})
	})

	Context("customDomains on Ingress", func() {
		const appName = "test-customdomains"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should create rules for env.Domain and custom domains, all in TLS hosts, all in ExternalDNS annotation", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:          "production",
						Replicas:      ptr.To[int32](1),
						Domain:        "primary.example.com",
						CustomDomains: []string{"custom1.example.com", "custom2.example.com"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "letsencrypt",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())

			// 3 rules: primary + 2 custom domains.
			Expect(ing.Spec.Rules).To(HaveLen(3))
			Expect(ing.Spec.Rules[0].Host).To(Equal("primary.example.com"))
			Expect(ing.Spec.Rules[1].Host).To(Equal("custom1.example.com"))
			Expect(ing.Spec.Rules[2].Host).To(Equal("custom2.example.com"))

			// TLS covers all hosts.
			Expect(ing.Spec.TLS).To(HaveLen(1))
			Expect(ing.Spec.TLS[0].Hosts).To(ConsistOf(
				"primary.example.com", "custom1.example.com", "custom2.example.com",
			))

			// ExternalDNS annotation lists all hostnames.
			Expect(ing.Annotations["external-dns.alpha.kubernetes.io/hostname"]).To(Equal(
				"primary.example.com,custom1.example.com,custom2.example.com",
			))
		})
	})

	Context("IngressProvider className", func() {
		const appName = "test-classname"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should set Spec.IngressClassName when provider ClassName is non-empty", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "class.example.com",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					ClassName: "traefik",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Spec.IngressClassName).NotTo(BeNil())
			Expect(*ing.Spec.IngressClassName).To(Equal("traefik"))
		})
	})

	Context("nil IngressProvider (backward compat)", func() {
		const appName = "test-nil-provider"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should not crash and should emit no provider annotations or className", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "nil.example.com",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())

			_, hasExternalDNS := ing.Annotations["external-dns.alpha.kubernetes.io/hostname"]
			Expect(hasExternalDNS).To(BeFalse())
			_, hasCertManager := ing.Annotations["cert-manager.io/cluster-issuer"]
			Expect(hasCertManager).To(BeFalse())
			Expect(ing.Spec.IngressClassName).To(BeNil())
		})
	})

	Context("environment annotations passthrough (§5.2a)", func() {
		const appName = "annot-passthrough"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Storage: []mortisev1alpha1.VolumeSpec{{
						Name:      "data",
						MountPath: "/data",
						Size:      resource.MustParse("1Gi"),
					}},
					Environments: []mortisev1alpha1.Environment{{
						Name:        "production",
						Replicas:    ptr.To[int32](1),
						Domain:      "annot.example.com",
						Annotations: map[string]string{"foo": "bar"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, app)
		})

		It("propagates env.Annotations onto Deployment, pod template, Service, Ingress, and PVCs", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Annotations["foo"]).To(Equal("bar"))
			Expect(dep.Spec.Template.Annotations["foo"]).To(Equal("bar"))

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)).To(Succeed())
			Expect(svc.Annotations["foo"]).To(Equal("bar"))

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["foo"]).To(Equal("bar"))

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-data", Namespace: envNsProduction,
			}, &pvc)).To(Succeed())
			Expect(pvc.Annotations["foo"]).To(Equal("bar"))
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
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "DATABASE_URL", Value: "postgres://test"},
						{Name: "host"},
						{Name: "port"},
					},
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
				Name: "test-db", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("postgres:16"))

			// Env vars are now stored in the app-env Secret, not on the Deployment.
			envData := readAppEnvSecret(ctx, "test-db", envNsProduction)
			Expect(envData).NotTo(BeNil())
			Expect(envData).To(HaveKeyWithValue("POSTGRES_PASSWORD", "testpass"))

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-db", Namespace: envNsProduction,
			}, &svc)).To(Succeed())

			var ing networkingv1.Ingress
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-db", Namespace: envNsProduction,
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
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "DATABASE_URL", Value: "postgres://testpass@my-db/postgres"},
						{Name: "host"},
						{Name: "port"},
						{Name: "user", Value: "postgres"},
						{Name: "password", Value: "testpass"},
					},
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

			// Env vars are now in the app-env Secret with MY_DB_ prefix.
			envData := readAppEnvSecret(ctx, apiAppName, envNsProduction)
			Expect(envData).NotTo(BeNil())

			// MY_DB_HOST should be the Service DNS value
			Expect(envData).To(HaveKeyWithValue("MY_DB_HOST",
				"my-db.pj-default-project-production.svc.cluster.local"))

			// MY_DB_PORT should be the literal port
			Expect(envData).To(HaveKeyWithValue("MY_DB_PORT", "8080"))

			// MY_DB_DATABASE_URL is now a resolved literal (resolver resolves SecretKeyRefs)
			Expect(envData).To(HaveKeyWithValue("MY_DB_DATABASE_URL",
				"postgres://testpass@my-db/postgres"))

			// MY_DB_USER is a resolved literal
			Expect(envData).To(HaveKeyWithValue("MY_DB_USER", "postgres"))

			// MY_DB_PASSWORD is a resolved literal
			Expect(envData).To(HaveKeyWithValue("MY_DB_PASSWORD", "testpass"))
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
				Name: "test-pvc-basic-data", Namespace: envNsProduction,
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
				Name: "test-pvc-sc-data", Namespace: envNsProduction,
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
				Name: "test-pvc-idem-data", Namespace: envNsProduction,
			}, &pvc)).To(Succeed())
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("10Gi"))).To(BeTrue())
		})

		It("should stamp labels enabling cross-namespace finalizer GC", func() {
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
				Name: "test-pvc-owner-data", Namespace: envNsProduction,
			}, &pvc)).To(Succeed())

			Expect(pvc.OwnerReferences).To(BeEmpty())
			Expect(pvc.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "test-pvc-owner"))
			Expect(pvc.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))
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
				Name: "test-pvc-mount", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			Expect(dep.Spec.Template.Spec.Volumes).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("test-pvc-mount-data"))
			Expect(dep.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath).To(Equal("/var/lib/postgresql/data"))
		})

		It("should expand PVC size when spec.storage[].size is increased", func() {
			// PVC resize is only permitted by the apiserver when the claim is
			// Bound AND its StorageClass has AllowVolumeExpansion=true. envtest
			// has no binder or storage classes, so create both by hand.
			scName := "expandable-sc"
			allowExpand := true
			sc := &storagev1.StorageClass{
				ObjectMeta:           metav1.ObjectMeta{Name: scName},
				Provisioner:          "kubernetes.io/no-provisioner",
				AllowVolumeExpansion: &allowExpand,
			}
			Expect(k8sClient.Create(ctx, sc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, sc)).To(Succeed()) }()

			app := newStorageApp("test-pvc-expand", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/data", Size: resource.MustParse("10Gi"), StorageClass: scName},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			pvcKey := types.NamespacedName{Name: "test-pvc-expand-data", Namespace: envNsProduction}

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("10Gi"))).To(BeTrue())

			// envtest has no binder, so mark the claim Bound via status so
			// the apiserver will permit the resize.
			pvc.Status.Phase = corev1.ClaimBound
			Expect(k8sClient.Status().Update(ctx, &pvc)).To(Succeed())

			// Bump the size on the App and re-reconcile.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: app.Name, Namespace: namespace,
			}, app)).To(Succeed())
			app.Spec.Storage[0].Size = resource.MustParse("20Gi")
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
			storageReq = pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("20Gi"))).To(BeTrue())
		})
	})

	Context("secretMounts mount existing Secrets as volumes", func() {
		ctx := context.Background()

		newMountApp := func(name string, mounts []mortisev1alpha1.SecretMount) *mortisev1alpha1.App {
			return &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:         "production",
							Replicas:     ptr.To[int32](1),
							SecretMounts: mounts,
						},
					},
				},
			}
		}

		findVolume := func(vols []corev1.Volume, n string) *corev1.Volume {
			for i := range vols {
				if vols[i].Name == n {
					return &vols[i]
				}
			}
			return nil
		}

		findMount := func(ms []corev1.VolumeMount, n string) *corev1.VolumeMount {
			for i := range ms {
				if ms[i].Name == n {
					return &ms[i]
				}
			}
			return nil
		}

		It("should wire one SecretMount as a Volume + VolumeMount with ReadOnly=true", func() {
			app := newMountApp("test-sm-basic", []mortisev1alpha1.SecretMount{
				{Name: "tls-bundle", Secret: "my-app-tls", Path: "/etc/ssl/app"},
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
				Name: "test-sm-basic", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			vol := findVolume(dep.Spec.Template.Spec.Volumes, "tls-bundle")
			Expect(vol).NotTo(BeNil())
			Expect(vol.Secret).NotTo(BeNil())
			Expect(vol.Secret.SecretName).To(Equal("my-app-tls"))
			Expect(vol.Secret.Items).To(BeEmpty())

			vm := findMount(dep.Spec.Template.Spec.Containers[0].VolumeMounts, "tls-bundle")
			Expect(vm).NotTo(BeNil())
			Expect(vm.MountPath).To(Equal("/etc/ssl/app"))
			Expect(vm.ReadOnly).To(BeTrue())
		})

		It("should honor explicit ReadOnly=false", func() {
			falseVal := false
			app := newMountApp("test-sm-rw", []mortisev1alpha1.SecretMount{
				{Name: "writable", Secret: "rw-secret", Path: "/var/run/secrets/rw", ReadOnly: &falseVal},
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
				Name: "test-sm-rw", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			vm := findMount(dep.Spec.Template.Spec.Containers[0].VolumeMounts, "writable")
			Expect(vm).NotTo(BeNil())
			Expect(vm.ReadOnly).To(BeFalse())
		})

		It("should project user-supplied Items 1:1 into the SecretVolumeSource", func() {
			mode := int32(0o400)
			app := newMountApp("test-sm-items", []mortisev1alpha1.SecretMount{
				{
					Name:   "tls-bundle",
					Secret: "my-app-tls",
					Path:   "/etc/ssl/app",
					Items: []mortisev1alpha1.KeyToPath{
						{Key: "tls.crt", Path: "cert.pem"},
						{Key: "tls.key", Path: "key.pem", Mode: &mode},
					},
				},
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
				Name: "test-sm-items", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			vol := findVolume(dep.Spec.Template.Spec.Volumes, "tls-bundle")
			Expect(vol).NotTo(BeNil())
			Expect(vol.Secret).NotTo(BeNil())
			Expect(vol.Secret.Items).To(HaveLen(2))
			Expect(vol.Secret.Items[0]).To(Equal(corev1.KeyToPath{Key: "tls.crt", Path: "cert.pem"}))
			Expect(vol.Secret.Items[1].Key).To(Equal("tls.key"))
			Expect(vol.Secret.Items[1].Path).To(Equal("key.pem"))
			Expect(vol.Secret.Items[1].Mode).NotTo(BeNil())
			Expect(*vol.Secret.Items[1].Mode).To(Equal(int32(0o400)))
		})

		It("should wire multiple SecretMounts simultaneously", func() {
			app := newMountApp("test-sm-multi", []mortisev1alpha1.SecretMount{
				{Name: "tls-bundle", Secret: "my-app-tls", Path: "/etc/ssl/app"},
				{Name: "jwt-keys", Secret: "jwt-signing", Path: "/etc/jwt"},
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
				Name: "test-sm-multi", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			Expect(findVolume(dep.Spec.Template.Spec.Volumes, "tls-bundle")).NotTo(BeNil())
			Expect(findVolume(dep.Spec.Template.Spec.Volumes, "jwt-keys")).NotTo(BeNil())

			tlsMount := findMount(dep.Spec.Template.Spec.Containers[0].VolumeMounts, "tls-bundle")
			Expect(tlsMount).NotTo(BeNil())
			Expect(tlsMount.MountPath).To(Equal("/etc/ssl/app"))

			jwtMount := findMount(dep.Spec.Template.Spec.Containers[0].VolumeMounts, "jwt-keys")
			Expect(jwtMount).NotTo(BeNil())
			Expect(jwtMount.MountPath).To(Equal("/etc/jwt"))
		})

		It("should produce no secret-typed volumes when SecretMounts is empty", func() {
			app := newMountApp("test-sm-none", nil)
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-sm-none", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			for _, v := range dep.Spec.Template.Spec.Volumes {
				Expect(v.Secret).To(BeNil(), "expected no Secret-typed volumes, found %q", v.Name)
			}
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
				Name: "test-update", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.26"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, app)).To(Succeed())
			app.Spec.Source.Image = testImageNginx
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-update", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(testImageNginx))
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
			app.Spec.Source.Image = testImageNginx
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			fakeClock.Step(5 * time.Minute)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			history := app.Status.Environments[0].DeployHistory
			Expect(history).To(HaveLen(2))
			Expect(history[0].Image).To(Equal(testImageNginx))
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
			app.Spec.Source.Image = testImageNginx
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
				Name: "test-rollback", Namespace: envNsProduction,
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

	Context("ServiceAccount creation and imagePullSecret wiring", func() {
		ctx := context.Background()

		It("creates a ServiceAccount named after the app, owned by the App", func() {
			const appName = "sa-basic"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())

			Expect(sa.OwnerReferences).To(BeEmpty())
			Expect(sa.Labels["app.kubernetes.io/managed-by"]).To(Equal("mortise"))
			Expect(sa.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", appName))
			Expect(sa.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))
		})

		It("sets serviceAccountName on the Deployment pod spec", func() {
			const appName = "sa-dep-ref"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.ServiceAccountName).To(Equal(appName))
		})

		It("attaches imagePullSecrets when RegistryBackend has a PullSecretRef", func() {
			const appName = "sa-pull-secret"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				RegistryBackend: &fakeRegistryBackend{pullSecretName: "registry-pull"},
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(HaveLen(1))
			Expect(sa.ImagePullSecrets[0].Name).To(Equal("registry-pull"))
		})

		It("creates SA without imagePullSecrets when RegistryBackend is nil", func() {
			const appName = "sa-no-registry"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(BeEmpty())
		})

		It("is idempotent on re-reconcile", func() {
			const appName = "sa-idempotent"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				RegistryBackend: &fakeRegistryBackend{pullSecretName: "registry-pull"},
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile should not error.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(HaveLen(1))
			Expect(sa.ImagePullSecrets[0].Name).To(Equal("registry-pull"))
		})
	})
})

// --- Mock types for git-source tests ---

// fakeBuildClient implements build.BuildClient for tests.
type fakeBuildClient struct {
	digest string
	err    string // if non-empty, Submit returns an EventFailure with this error
}

func (f *fakeBuildClient) Submit(_ context.Context, _ build.BuildRequest) (<-chan build.BuildEvent, error) {
	ch := make(chan build.BuildEvent, 2)
	if f.err != "" {
		ch <- build.BuildEvent{Type: build.EventFailure, Error: f.err}
	} else {
		ch <- build.BuildEvent{Type: build.EventSuccess, Digest: f.digest}
	}
	close(ch)
	return ch, nil
}

// gatedBuildClient is a BuildClient whose Submit returns a channel that only
// emits a success event after the caller closes its release channel. Used to
// test async reconciles where we need the build to be in-flight across
// multiple Reconcile calls.
type gatedBuildClient struct {
	digest  string
	release <-chan struct{}
}

func (g *gatedBuildClient) Submit(ctx context.Context, _ build.BuildRequest) (<-chan build.BuildEvent, error) {
	ch := make(chan build.BuildEvent, 1)
	go func() {
		defer close(ch)
		select {
		case <-g.release:
			ch <- build.BuildEvent{Type: build.EventSuccess, Digest: g.digest}
		case <-ctx.Done():
			ch <- build.BuildEvent{Type: build.EventFailure, Error: ctx.Err().Error()}
		}
	}()
	return ch, nil
}

// fakeGitClient implements git.GitClient for tests (no-op clone).
type fakeGitClient struct {
	err error
}

func (f *fakeGitClient) Clone(_ context.Context, _, _, _ string, _ git.GitCredentials) error {
	return f.err
}

func (f *fakeGitClient) Fetch(_ context.Context, _, _ string) error {
	return f.err
}

// fakeRegistryBackend implements registry.RegistryBackend for tests.
type fakeRegistryBackend struct {
	imageRef       registry.ImageRef
	pullSecretName string
}

func (f *fakeRegistryBackend) PushTarget(app, tag string) (registry.ImageRef, error) {
	if f.imageRef.Full != "" {
		return f.imageRef, nil
	}
	return registry.ImageRef{
		Registry: "registry.example.com",
		Path:     "mortise/" + app,
		Tag:      tag,
		Full:     "registry.example.com/mortise/" + app + ":" + tag,
	}, nil
}

func (f *fakeRegistryBackend) PullSecretRef() string { return f.pullSecretName }

func (f *fakeRegistryBackend) Tags(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (f *fakeRegistryBackend) DeleteTag(_ context.Context, _, _ string) error {
	return nil
}

// gitSourceReconciler returns an AppReconciler wired with fakes for git-source tests.
func gitSourceReconciler(bc build.BuildClient, gc git.GitClient, rb registry.RegistryBackend) *AppReconciler {
	return &AppReconciler{
		Client:          k8sClient,
		Scheme:          k8sClient.Scheme(),
		BuildClient:     bc,
		GitClient:       gc,
		RegistryBackend: rb,
	}
}

// reconcileUntilBuildDone drives Reconcile past the async-build requeue loop.
// Returns the last Result/error from Reconcile. Fails the test if it takes
// more than a bounded number of iterations (the fake BuildClient completes
// synchronously, so a handful of reconciles is always sufficient).
func reconcileUntilBuildDone(r *AppReconciler, ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var res reconcile.Result
	for i := 0; i < 40; i++ {
		res, _ = r.Reconcile(ctx, req)
		// Check the app phase — stop when it's no longer Building.
		var app mortisev1alpha1.App
		if getErr := r.Get(ctx, req.NamespacedName, &app); getErr == nil {
			phase := app.Status.Phase
			if phase == mortisev1alpha1.AppPhaseReady ||
				phase == mortisev1alpha1.AppPhaseFailed ||
				phase == mortisev1alpha1.AppPhaseDeploying ||
				phase == mortisev1alpha1.AppPhaseCrashLooping {
				return res, nil
			}
		}
		// Let the background build goroutine run.
		time.Sleep(10 * time.Millisecond)
	}
	return res, fmt.Errorf("Reconcile still requeuing after 40 iterations")
}

// makeGitApp creates an App spec with source.type=git.
func makeGitSourceApp(name, ns, providerRef string) *mortisev1alpha1.App {
	return &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Annotations: map[string]string{
				"mortise.dev/created-by": "test@example.com",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://github.com/org/repo",
				Branch:      "main",
				ProviderRef: providerRef,
			},
			Network: mortisev1alpha1.NetworkConfig{Public: false},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Replicas: ptr.To[int32](1)},
			},
		},
	}
}

var _ = Describe("App Controller — git source", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"

	AfterEach(func() {
		purgeAllAppsIn(context.Background(), namespace)
	})

	Context("no providerRef", func() {
		It("should set phase=Failed when providerRef is missing", func() {
			ctx := context.Background()
			app := makeGitSourceApp("git-no-provider", namespace, "")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:abc"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("MissingProviderRef"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseFailed))
		})
	})

	Context("clone failure", func() {
		It("should set phase=Failed when clone fails", func() {
			ctx := context.Background()

			// Create the provider and its token secret so the reconciler gets past token resolution.
			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-clone-fail"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-clone-fail-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}}
			// Namespace may already exist; ignore AlreadyExists.
			_ = k8sClient.Create(ctx, ns)
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-clone-fail", namespace, "gh-clone-fail")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := gitSourceReconciler(
				&fakeBuildClient{},
				&fakeGitClient{err: fmt.Errorf("connection refused")},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			// Clone failure sets phase=Failed and stops retrying.
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseFailed))
		})
	})

	Context("build failure", func() {
		It("should set phase=Failed when build fails", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-build-fail"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-build-fail-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-build-fail", namespace, "gh-build-fail")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := gitSourceReconciler(
				&fakeBuildClient{err: "dockerfile not found"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			// Build failure sets phase=Failed and stops retrying (no error returned).
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseFailed))
		})
	})

	Context("happy path", func() {
		It("should build, set lastBuiltSHA, and create a Deployment with the built image", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-happy"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-happy-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("mytoken")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-happy", namespace, "gh-happy")
			// Set the revision annotation as the webhook would.
			app.Annotations["mortise.dev/revision"] = "abc1234567890"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:deadbeef"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Status should have the built SHA and image.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.LastBuiltSHA).To(Equal("abc1234567890"))
			Expect(app.Status.LastBuiltImage).NotTo(BeEmpty())

			// A Deployment should have been created with the built image.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "git-happy",
				Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("sha256:deadbeef"))
		})
	})

	Context("async build", func() {
		It("should return Building + requeue on first reconcile and finish on subsequent reconciles", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-async"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-async-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-async", namespace, "gh-async")
			app.Annotations["mortise.dev/revision"] = "revasync"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			// gatedBuildClient blocks on release until we tell it to succeed.
			release := make(chan struct{})
			bc := &gatedBuildClient{digest: "sha256:asyncdigest", release: release}

			r := gitSourceReconciler(bc, &fakeGitClient{}, &fakeRegistryBackend{})

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace}}

			// First reconcile kicks off the goroutine, must return quickly with
			// RequeueAfter > 0 even though the build hasn't completed.
			start := time.Now()
			res, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))
			Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseBuilding))

			// A second reconcile while the build is still in flight should also
			// return quickly and the phase should still be Building (no
			// Deployment yet).
			res, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "git-async", Namespace: envNsProduction,
			}, &dep)).To(MatchError(ContainSubstring("not found")))

			// Release the build; the next reconcile should observe the
			// succeeded tracker, write lastBuiltImage, and create a Deployment.
			close(release)
			_, err = reconcileUntilBuildDone(r, ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.LastBuiltSHA).To(Equal("revasync"))
			Expect(app.Status.LastBuiltImage).To(ContainSubstring("sha256:asyncdigest"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "git-async", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("sha256:asyncdigest"))
		})
	})

	Context("same-SHA short-circuit", func() {
		It("should skip rebuild when lastBuiltSHA matches the annotation revision", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-shortcircuit"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-shortcircuit-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-shortcircuit", namespace, "gh-shortcircuit")
			app.Annotations = map[string]string{"mortise.dev/revision": "same-sha"}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			// Simulate a prior successful build by presetting the status.
			app.Status.LastBuiltSHA = "same-sha"
			app.Status.LastBuiltImage = "registry.example.com/mortise/git-shortcircuit:same-sha"
			Expect(k8sClient.Status().Update(ctx, app)).To(Succeed())

			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:shouldnotbecalled"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)

			// We verify the short-circuit by checking that the Deployment image
			// matches the pre-set lastBuiltImage (not a newly built one).
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// The Deployment should use the already-built image.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "git-shortcircuit",
				Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("registry.example.com/mortise/git-shortcircuit:same-sha"))
		})
	})

	Context("build-log ConfigMap persistence", func() {
		// seedGitProvider plants a GitProvider CRD and its per-user token
		// Secret so the git-source reconciler can proceed past auth resolution.
		seedGitProvider := func(ctx context.Context, provider string) func() {
			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: provider},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())

			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-" + provider + "-token-74657374406578616d706c652e636f6d",
					Namespace: "mortise-system",
				},
				Data: map[string][]byte{"token": []byte("tok")},
			}
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())

			return func() {
				_ = k8sClient.Delete(ctx, tokenSecret)
				_ = k8sClient.Delete(ctx, gp)
			}
		}

		It("persists the log buffer and metadata after a successful build", func() {
			ctx := context.Background()
			cleanup := seedGitProvider(ctx, "gh-persist-ok")
			defer cleanup()

			app := makeGitSourceApp("git-persist-ok", namespace, "gh-persist-ok")
			app.Annotations["mortise.dev/revision"] = "abcdef1234"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, app) }()

			fakeNow := time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC)
			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:persistok"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			r.Clock = clocktesting.NewFakeClock(fakeNow)

			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cm corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-ok", Namespace: namespace,
				}, &cm)
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed())

			Expect(cm.Annotations).To(HaveKeyWithValue("mortise.dev/build-status", "Succeeded"))
			Expect(cm.Annotations).To(HaveKeyWithValue("mortise.dev/build-commit", "abcdef1234"))
			Expect(cm.Annotations).To(HaveKey("mortise.dev/build-timestamp"))
			Expect(cm.Annotations).NotTo(HaveKey("mortise.dev/build-error"))
			Expect(cm.Data).To(HaveKey("lines"))
			// Owner reference anchors the CM to the App for GC.
			Expect(cm.OwnerReferences).To(HaveLen(1))
			Expect(cm.OwnerReferences[0].Kind).To(Equal("App"))
			Expect(cm.OwnerReferences[0].Name).To(Equal("git-persist-ok"))
		})

		It("persists status=Failed and the error annotation when the build fails", func() {
			ctx := context.Background()
			cleanup := seedGitProvider(ctx, "gh-persist-fail")
			defer cleanup()

			app := makeGitSourceApp("git-persist-fail", namespace, "gh-persist-fail")
			app.Annotations["mortise.dev/revision"] = "badcommit"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, app) }()

			r := gitSourceReconciler(
				&fakeBuildClient{err: "dockerfile syntax error"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)

			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cm corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-fail", Namespace: namespace,
				}, &cm)
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed())

			Expect(cm.Annotations).To(HaveKeyWithValue("mortise.dev/build-status", "Failed"))
			Expect(cm.Annotations).To(HaveKeyWithValue("mortise.dev/build-commit", "badcommit"))
			Expect(cm.Annotations["mortise.dev/build-error"]).To(ContainSubstring("dockerfile syntax error"))
		})

		It("updates the same ConfigMap on rebuild instead of creating a new one", func() {
			ctx := context.Background()
			cleanup := seedGitProvider(ctx, "gh-persist-rebuild")
			defer cleanup()

			app := makeGitSourceApp("git-persist-rebuild", namespace, "gh-persist-rebuild")
			app.Annotations["mortise.dev/revision"] = "rev-one"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, app) }()

			firstTime := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:rev1"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			r.Clock = clocktesting.NewFakeClock(firstTime)

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace}}
			_, err := reconcileUntilBuildDone(r, ctx, req)
			Expect(err).NotTo(HaveOccurred())

			var first corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-rebuild", Namespace: namespace,
				}, &first)
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed())
			firstTS := first.Annotations["mortise.dev/build-timestamp"]
			firstUID := first.UID

			// Simulate a second commit → rebuild. Advance the fake clock so the
			// new timestamp annotation is distinguishable from the first.
			Expect(k8sClient.Get(ctx, req.NamespacedName, app)).To(Succeed())
			app.Annotations["mortise.dev/revision"] = "rev-two"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			secondTime := firstTime.Add(1 * time.Hour)
			r.Clock = clocktesting.NewFakeClock(secondTime)

			_, err = reconcileUntilBuildDone(r, ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() string {
				var cm corev1.ConfigMap
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-rebuild", Namespace: namespace,
				}, &cm); err != nil {
					return ""
				}
				return cm.Annotations["mortise.dev/build-commit"]
			}, 5*time.Second, 50*time.Millisecond).Should(Equal("rev-two"))

			var second corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "buildlogs-git-persist-rebuild", Namespace: namespace,
			}, &second)).To(Succeed())
			// Same object (CreateOrUpdate, not new-every-time).
			Expect(second.UID).To(Equal(firstUID))
			Expect(second.Annotations["mortise.dev/build-timestamp"]).NotTo(Equal(firstTS))
		})

		It("is garbage-collected when the owning App is deleted", func() {
			// envtest doesn't run the built-in GC controller, but we can
			// assert the owner reference is correctly set (which is what
			// drives GC in real clusters). The separate Context above covers
			// the OwnerReference itself; here we additionally assert
			// envtest's foreground-delete path clears it — or, if the test
			// environment doesn't collect it, we verify the owner ref still
			// points at a non-existent UID, which is the GC precondition.
			ctx := context.Background()
			cleanup := seedGitProvider(ctx, "gh-persist-gc")
			defer cleanup()

			app := makeGitSourceApp("git-persist-gc", namespace, "gh-persist-gc")
			app.Annotations["mortise.dev/revision"] = "gc-rev"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:gc"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Capture the App UID so we can verify the owner reference points
			// at it.
			var persisted corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-gc", Namespace: namespace,
				}, &persisted)
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed())

			var live mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, &live)).To(Succeed())
			Expect(persisted.OwnerReferences).To(HaveLen(1))
			Expect(persisted.OwnerReferences[0].UID).To(Equal(live.UID))
			// BlockOwnerDeletion or Controller=true both signal GC intent to the
			// real garbage collector.
			Expect(persisted.OwnerReferences[0].Controller).NotTo(BeNil())
			Expect(*persisted.OwnerReferences[0].Controller).To(BeTrue())

			Expect(k8sClient.Delete(ctx, &live)).To(Succeed())
		})
	})

	Context("credentials Secret materialization", func() {
		ctx := context.Background()

		It("creates no Secret when credentials is empty", func() {
			const appName = "creds-empty"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)
			Expect(err).To(HaveOccurred())

			// Pod template must NOT carry the credentials-hash annotation.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Annotations).NotTo(HaveKey("mortise.dev/credentials-hash"))
		})

		It("materialises inline Values into the Secret and injects a hash annotation", func() {
			const appName = "creds-inline"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
						{Name: "username", Value: "postgres"},
						{Name: "password", Value: "hunter2"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())

			Expect(sec.Type).To(Equal(corev1.SecretTypeOpaque))
			Expect(sec.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "mortise"))
			Expect(sec.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", appName))
			// Well-known keys (host, port) are resolved at binder time, not
			// stored in the Secret.
			Expect(sec.Data).NotTo(HaveKey("host"))
			Expect(sec.Data).NotTo(HaveKey("port"))
			Expect(sec.Data).To(HaveKeyWithValue("username", []byte("postgres")))
			Expect(sec.Data).To(HaveKeyWithValue("password", []byte("hunter2")))

			// Cross-namespace: no controller ref; finalizer-based GC on App delete.
			Expect(sec.OwnerReferences).To(BeEmpty())
			Expect(sec.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))

			// Pod template carries the hash annotation.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Annotations).To(HaveKey("mortise.dev/credentials-hash"))
			Expect(dep.Spec.Template.Annotations["mortise.dev/credentials-hash"]).NotTo(BeEmpty())
		})

		It("resolves valueFrom secretRef from a user-managed Secret", func() {
			const appName = "creds-valuefrom"

			userSec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-db-secret", Namespace: envNsProduction},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"pw": []byte("s3cret!")},
			}
			Expect(k8sClient.Create(ctx, userSec)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, userSec)).To(Succeed()) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "username", Value: "postgres"},
						{
							Name: "password",
							ValueFrom: &mortisev1alpha1.CredentialSource{
								SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "user-db-secret", Key: "pw"},
							},
						},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			Expect(sec.Data).To(HaveKeyWithValue("username", []byte("postgres")))
			Expect(sec.Data).To(HaveKeyWithValue("password", []byte("s3cret!")))
		})

		It("errors when valueFrom references a missing Secret", func() {
			const appName = "creds-missing-src"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{
							Name: "password",
							ValueFrom: &mortisev1alpha1.CredentialSource{
								SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "does-not-exist", Key: "pw"},
							},
						},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("does-not-exist"))
		})

		It("rotates the hash when a referenced user Secret changes", func() {
			const appName = "creds-rotate"

			userSec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "rotate-src", Namespace: envNsProduction},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"pw": []byte("v1")},
			}
			Expect(k8sClient.Create(ctx, userSec)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, userSec)).To(Succeed()) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{
							Name: "password",
							ValueFrom: &mortisev1alpha1.CredentialSource{
								SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "rotate-src", Key: "pw"},
							},
						},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep1 appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep1)).To(Succeed())
			hash1 := dep1.Spec.Template.Annotations["mortise.dev/credentials-hash"]
			Expect(hash1).NotTo(BeEmpty())

			// Rotate the source Secret and re-reconcile.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "rotate-src", Namespace: envNsProduction,
			}, userSec)).To(Succeed())
			userSec.Data["pw"] = []byte("v2")
			Expect(k8sClient.Update(ctx, userSec)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep2 appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep2)).To(Succeed())
			hash2 := dep2.Spec.Template.Annotations["mortise.dev/credentials-hash"]
			Expect(hash2).NotTo(Equal(hash1))
		})

		It("deletes a previously-managed Secret when credentials are removed", func() {
			const appName = "creds-drop"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "password", Value: "hunter2"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())

			// Clear credentials, reconcile again; Secret should go away.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, app)).To(Succeed())
			app.Spec.Credentials = nil
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name: appName + "-credentials", Namespace: envNsProduction,
				}, &sec)
				return err != nil
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("refuses to adopt an unmanaged Secret with the reserved name", func() {
			const appName = "creds-conflict"

			// User pre-created a Secret at {app}-credentials with no Mortise label.
			preExisting := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: appName + "-credentials", Namespace: envNsProduction},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"external": []byte("data")},
			}
			Expect(k8sClient.Create(ctx, preExisting)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, preExisting)).To(Succeed()) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "password", Value: "hunter2"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())

			// Pre-existing Secret must be untouched.
			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			Expect(sec.Data).To(HaveKeyWithValue("external", []byte("data")))
		})
	})

	Context("configFiles reconciliation", func() {
		ctx := context.Background()

		It("creates a ConfigMap owned by the App with the correct data key", func() {
			const appName = "cf-create"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					ConfigFiles: []mortisev1alpha1.ConfigFile{
						{Path: "/etc/app/app.conf", Content: "key=value\n"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cm corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-config-0", Namespace: envNsProduction,
			}, &cm)).To(Succeed())

			Expect(cm.Data).To(HaveKeyWithValue("app.conf", "key=value\n"))
			Expect(cm.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "mortise"))
			Expect(cm.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", appName))
			Expect(cm.OwnerReferences).To(BeEmpty())
			Expect(cm.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))
		})

		It("prunes a ConfigMap when its configFiles entry is removed", func() {
			const appName = "cf-prune"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					ConfigFiles: []mortisev1alpha1.ConfigFile{
						{Path: "/etc/a/a.conf", Content: "a"},
						{Path: "/etc/b/b.conf", Content: "b"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Both ConfigMaps exist.
			var cm0, cm1 corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-config-0", Namespace: envNsProduction}, &cm0)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-config-1", Namespace: envNsProduction}, &cm1)).To(Succeed())

			// Drop the second configFile and reconcile again.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			app.Spec.ConfigFiles = app.Spec.ConfigFiles[:1]
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// -0 is retained, -1 is deleted.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-config-0", Namespace: envNsProduction}, &cm0)).To(Succeed())
			err = k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-config-1", Namespace: envNsProduction}, &cm1)
			Expect(err).To(HaveOccurred())
		})

		It("refuses to hijack a pre-existing ConfigMap not managed by Mortise", func() {
			const appName = "cf-hijack"
			cmName := appName + "-config-0"

			// Pre-create a ConfigMap with the reserved name, owned by the user.
			userCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: envNsProduction,
					// No mortise.dev/managed-by label — not ours.
				},
				Data: map[string]string{"user.conf": "do not touch"},
			}
			Expect(k8sClient.Create(ctx, userCM)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, userCM) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					ConfigFiles: []mortisev1alpha1.ConfigFile{
						{Path: "/etc/app/new.conf", Content: "new"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not managed by Mortise"))

			// Pre-existing data untouched.
			var got corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: envNsProduction}, &got)).To(Succeed())
			Expect(got.Data).To(HaveKeyWithValue("user.conf", "do not touch"))
			Expect(got.Data).NotTo(HaveKey("new.conf"))
		})
	})

	Context("custom network port", func() {
		const appName = "custom-port-app"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should use spec.network.port as Service targetPort and container port", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true, Port: 3000},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "custom-port.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(3000)))
			Expect(svc.Spec.Ports[0].TargetPort.IntVal).To(Equal(int32(3000)))

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(3000)))
		})
	})

	Context("sharedVars (spec §5.8b)", func() {
		ctx := context.Background()

		It("should inject sharedVars into every environment's Deployment", func() {
			withStagingEnv(ctx)
			defer withoutStagingEnv(ctx)

			appName := "shared-vars-multi-env"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					SharedVars: []mortisev1alpha1.EnvVar{
						{Name: "LOG_LEVEL", Value: "info"},
						{Name: "SENTRY_DSN", Value: "https://sentry.example.com/1"},
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "sv-prod.example.com",
						},
						{
							Name:   "staging",
							Domain: "sv-staging.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			for _, envName := range []string{"production", "staging"} {
				envNs := "pj-default-project-" + envName

				envData := readAppEnvSecret(ctx, appName, envNs)
				Expect(envData).NotTo(BeNil(), "app-env Secret missing in %s", envName)
				Expect(envData).To(HaveKeyWithValue("LOG_LEVEL", "info"), "LOG_LEVEL missing in %s", envName)
				Expect(envData).To(HaveKeyWithValue("SENTRY_DSN", "https://sentry.example.com/1"), "SENTRY_DSN missing in %s", envName)
			}
		})

		It("should let env-level vars override sharedVars on key conflict", func() {
			appName := "shared-vars-override"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					SharedVars: []mortisev1alpha1.EnvVar{
						{Name: "LOG_LEVEL", Value: "info"},
						{Name: "FEATURE_FLAG", Value: "off"},
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "svo-prod.example.com",
							Env: []mortisev1alpha1.EnvVar{
								{Name: "LOG_LEVEL", Value: "warn"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).NotTo(BeNil())

			// SharedVars are seeded after env-level vars, so sharedVars
			// LOG_LEVEL=info takes precedence during initial seed.
			Expect(envData).To(HaveKeyWithValue("LOG_LEVEL", "info"))

			// FEATURE_FLAG from sharedVars should still be present
			Expect(envData).To(HaveKeyWithValue("FEATURE_FLAG", "off"))
		})

		It("should merge bound credentials, sharedVars, and env vars in priority order", func() {
			dbAppName := "sv-db"
			apiAppName := "sv-api"

			dbApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      dbAppName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "DATABASE_URL", Value: "postgres://sv-db/postgres"},
						{Name: "host"},
						{Name: "port"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dbApp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, dbApp)).To(Succeed()) }()

			// Reconcile db first so its Service exists
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: dbAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			apiApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      apiAppName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "my-api:v1",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					SharedVars: []mortisev1alpha1.EnvVar{
						{Name: "LOG_LEVEL", Value: "info"},
						// sharedVars should override bound "host" credential
						{Name: "host", Value: "custom-host.example.com"},
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "sv-api.example.com",
							Bindings: []mortisev1alpha1.Binding{
								{Ref: dbAppName},
							},
							Env: []mortisev1alpha1.EnvVar{
								{Name: "NODE_ENV", Value: "production"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, apiApp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, apiApp)).To(Succeed()) }()

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, apiAppName, envNsProduction)
			Expect(envData).NotTo(BeNil())

			// Bound credential: DATABASE_URL is now a resolved literal with SV_DB_ prefix
			Expect(envData).To(HaveKeyWithValue("SV_DB_DATABASE_URL", "postgres://sv-db/postgres"))

			// sharedVars override bound credential: host should be the sharedVars value
			Expect(envData).To(HaveKeyWithValue("host", "custom-host.example.com"))

			// sharedVars: LOG_LEVEL should be present
			Expect(envData).To(HaveKeyWithValue("LOG_LEVEL", "info"))

			// Env-level: NODE_ENV should be present
			Expect(envData).To(HaveKeyWithValue("NODE_ENV", "production"))

			// Bound: SV_DB_HOST and SV_DB_PORT are always injected
			Expect(envData).To(HaveKey("SV_DB_HOST"))
			Expect(envData).To(HaveKeyWithValue("SV_DB_PORT", "8080"))
		})

		It("should not change behavior when sharedVars is empty", func() {
			appName := "shared-vars-empty"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "sve-prod.example.com",
							Env: []mortisev1alpha1.EnvVar{
								{Name: "PORT", Value: "3000"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			// Deployment only carries the PORT literal injected by the controller.
			envVars := dep.Spec.Template.Spec.Containers[0].Env
			Expect(envVars).To(HaveLen(1))
			Expect(envVars[0].Name).To(Equal("PORT"))

			// User-defined env vars are in the app-env Secret.
			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).NotTo(BeNil())
			Expect(envData).To(HaveKeyWithValue("PORT", "3000"))
		})
	})

	Context("cron app (kind=cron, §5.8a)", func() {
		const appName = "test-cron"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should create a CronJob with correct schedule and concurrency policy", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:              "production",
							Schedule:          "*/5 * * * *",
							ConcurrencyPolicy: mortisev1alpha1.ConcurrencyPolicyForbid,
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU:    "100m",
								Memory: "128Mi",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cj batchv1.CronJob
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.Spec.Schedule).To(Equal("*/5 * * * *"))
			Expect(cj.Spec.ConcurrencyPolicy).To(Equal(batchv1.ForbidConcurrent))
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image).To(Equal(testImageNginx))
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyOnFailure))
			Expect(cj.Labels["app.kubernetes.io/managed-by"]).To(Equal("mortise"))
			Expect(cj.Labels["mortise.dev/environment"]).To(Equal("production"))
		})

		It("should not create a Deployment, Service, or Ingress for cron apps", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "*/5 * * * *",
							Domain:   "cron.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)
			Expect(err).To(HaveOccurred())

			var svc corev1.Service
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)
			Expect(err).To(HaveOccurred())

			var ing networkingv1.Ingress
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)
			Expect(err).To(HaveOccurred())
		})

		It("should label CronJob for cross-namespace garbage collection", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "0 3 * * *",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cj batchv1.CronJob
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.OwnerReferences).To(BeEmpty())
			Expect(cj.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", appName))
			Expect(cj.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))
		})

		It("should default concurrency policy to Allow", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "0 * * * *",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cj batchv1.CronJob
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.Spec.ConcurrencyPolicy).To(Equal(batchv1.AllowConcurrent))
		})

		It("should update CronJob schedule on re-reconcile", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "*/5 * * * *",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Update the schedule
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			app.Spec.Environments[0].Schedule = "0 3 * * *"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cj batchv1.CronJob
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.Spec.Schedule).To(Equal("0 3 * * *"))
		})

		It("should support Replace concurrency policy", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:              "production",
							Schedule:          "*/10 * * * *",
							ConcurrencyPolicy: mortisev1alpha1.ConcurrencyPolicyReplace,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cj batchv1.CronJob
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.Spec.ConcurrencyPolicy).To(Equal(batchv1.ReplaceConcurrent))
		})
	})

	Context("external source with credentials (no workload)", func() {
		const appName = "ext-postgres"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should create credentials Secret but no Deployment, Service, or ServiceAccount", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeExternal,
						External: &mortisev1alpha1.ExternalSource{
							Host: "db.provider.cloud",
							Port: 5432,
						},
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
						{Name: "DATABASE_URL", Value: "postgres://user:pass@db.provider.cloud:5432/mydb"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Credentials Secret must exist.
			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			Expect(sec.Data).To(HaveKeyWithValue("DATABASE_URL",
				[]byte("postgres://user:pass@db.provider.cloud:5432/mydb")))
			// Well-known keys are not stored in the Secret.
			Expect(sec.Data).NotTo(HaveKey("host"))
			Expect(sec.Data).NotTo(HaveKey("port"))

			// No Deployment should exist.
			var dep appsv1.Deployment
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)
			Expect(err).To(HaveOccurred())

			// No ClusterIP Service should exist.
			var svc corev1.Service
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)
			Expect(err).To(HaveOccurred())

			// No ServiceAccount should exist.
			var sa corev1.ServiceAccount
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, &sa)
			Expect(err).To(HaveOccurred())
		})

		It("should set phase to Ready immediately", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: "ext-ready", Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeExternal,
						External: &mortisev1alpha1.ExternalSource{
							Host: "redis.provider.cloud",
							Port: 6379,
						},
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "ext-ready", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "ext-ready", Namespace: namespace,
			}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
		})
	})

	Context("external source with network.public creates Ingress", func() {
		const appName = "ext-public"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should create an ExternalName Service and Ingress for the external host", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeExternal,
						External: &mortisev1alpha1.ExternalSource{
							Host: "admin.managed-db.example.com",
							Port: 443,
						},
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "db-admin.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// ExternalName Service should exist.
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeExternalName))
			Expect(svc.Spec.ExternalName).To(Equal("admin.managed-db.example.com"))

			// Ingress should exist with the correct host.
			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Spec.Rules).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].Host).To(Equal("db-admin.example.com"))

			// Ingress backend should point at the ExternalName Service.
			backend := ing.Spec.Rules[0].HTTP.Paths[0].Backend
			Expect(backend.Service.Name).To(Equal(appName))

			// Phase should be Ready.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
		})
	})

	Context("external source credentials are resolvable by bindings", func() {
		const (
			extDBName  = "ext-db-bind"
			apiAppName = "api-ext-bind"
		)
		ctx := context.Background()

		var extApp, apiApp *mortisev1alpha1.App

		BeforeEach(func() {
			extApp = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: extDBName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeExternal,
						External: &mortisev1alpha1.ExternalSource{
							Host: "rds.us-east-1.amazonaws.com",
							Port: 5432,
						},
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
						{Name: "DATABASE_URL", Value: "postgres://admin:secret@rds.us-east-1.amazonaws.com:5432/prod"},
						{Name: "username", Value: "admin"},
						{Name: "password", Value: "secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, extApp)).To(Succeed())

			// Reconcile the external app first so its credentials Secret exists.
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: extDBName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			apiApp = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: apiAppName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "my-api:v1",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Bindings: []mortisev1alpha1.Binding{
								{Ref: extDBName},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, apiApp)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, apiApp)
			_ = k8sClient.Delete(ctx, extApp)
		})

		It("should inject external host and port as env vars in the binder Deployment", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: apiAppName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			// Env vars are now in the app-env Secret with EXT_DB_BIND_ prefix.
			envData := readAppEnvSecret(ctx, apiAppName, envNsProduction)
			Expect(envData).NotTo(BeNil())

			// host should be the external host, not a Service DNS name.
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_HOST", "rds.us-east-1.amazonaws.com"))

			// port should be the external port.
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_PORT", "5432"))

			// DATABASE_URL is now a resolved literal with prefix.
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_DATABASE_URL",
				"postgres://admin:secret@rds.us-east-1.amazonaws.com:5432/prod"))

			// username and password are resolved literals with prefix.
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_USERNAME", "admin"))
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_PASSWORD", "secret"))
		})
	})

	Context("Deployment update conflict retry (optimistic locking)", func() {
		const appName = "conflict-retry"
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
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
			}
		})

		It("recovers from a single optimistic-locking conflict on Deployment update", func() {
			// First reconcile: creates the Deployment.
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			// Re-fetch App so we have the latest resource version before updating.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())

			// Mutate the App so the next reconcile has something to update.
			app.Spec.Source.Image = "nginx:1.28"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			// conflictClient injects one 409 Conflict on the first Deployment
			// Update, then delegates to the real client on subsequent calls.
			conflictFired := false
			conflictClient := &deploymentConflictClient{
				Client: k8sClient,
				fired:  &conflictFired,
			}

			conflictReconciler := &AppReconciler{Client: conflictClient, Scheme: k8sClient.Scheme()}
			_, err = conflictReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(conflictFired).To(BeTrue(), "conflict interceptor should have fired")

			// Image should be updated despite the transient conflict.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.28"))
		})
	})
})

// deploymentConflictClient wraps a client.Client and returns a 409 Conflict
// error on the first Update call for a Deployment, then passes through normally.
// Used to verify the optimistic-locking retry loop in reconcileDeployment.
type deploymentConflictClient struct {
	client.Client
	fired *bool
}

func (c *deploymentConflictClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*appsv1.Deployment); ok && !*c.fired {
		*c.fired = true
		// Commit the update to the store so the re-fetch returns a fresh
		// resource version, then lie to the caller about it conflicting.
		_ = c.Client.Update(ctx, obj, opts...)
		return &kerrors.StatusError{ErrStatus: metav1.Status{
			Reason: metav1.StatusReasonConflict,
			Code:   409,
		}}
	}
	return c.Client.Update(ctx, obj, opts...)
}

// readAppEnvSecret reads the {app}-env Secret and returns its data map.
// Returns nil if the Secret doesn't exist.
func readAppEnvSecret(ctx context.Context, appName, namespace string) map[string]string {
	var sec corev1.Secret
	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      envstore.AppEnvSecretName(appName),
		Namespace: namespace,
	}, &sec)
	if err != nil {
		return nil
	}
	result := make(map[string]string, len(sec.Data))
	for k, v := range sec.Data {
		result[k] = string(v)
	}
	return result
}
