/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// The preview exclusion in updateStatus is a repair, not a guard: it can only
// ever undo an app-global BuildSucceeded=False that the env loop already
// persisted. Asserting on the App after Reconcile therefore proves nothing —
// the repair has run by then. This spec records every status write that
// reaches the API server during a single Reconcile, so the transient False
// that readers (API, UI) can observe mid-pass is visible to the test.
var _ = Describe("App Controller — preview build failure isolation", func() {
	const namespace = "pj-default-project"
	const previewEnv = "pr-6"
	const revision = "same-sha"

	AfterEach(func() {
		purgeAllAppsIn(context.Background(), namespace)
	})

	It("never writes an app-global BuildSucceeded=False when only a preview env's build failed", func() {
		ctx := context.Background()

		var proj mortisev1alpha1.Project
		Expect(k8sClient.Get(ctx, ctrlclient.ObjectKey{Name: "default-project"}, &proj)).To(Succeed())
		originalEnvs := proj.Spec.Environments
		proj.Spec.Environments = append(append([]mortisev1alpha1.ProjectEnvironment{}, originalEnvs...),
			mortisev1alpha1.ProjectEnvironment{Name: previewEnv, DisplayOrder: 9, Preview: true})
		Expect(k8sClient.Update(ctx, &proj)).To(Succeed())
		ensureNamespace(ctx, namespace+"-"+previewEnv)
		defer func() {
			var restore mortisev1alpha1.Project
			Expect(k8sClient.Get(ctx, ctrlclient.ObjectKey{Name: "default-project"}, &restore)).To(Succeed())
			restore.Spec.Environments = originalEnvs
			Expect(k8sClient.Update(ctx, &restore)).To(Succeed())
		}()

		app := makeGitSourceApp("preview-build-fail", namespace, "gh-preview")
		app.Annotations["mortise.dev/revision"] = revision
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		key := types.NamespacedName{Name: app.Name, Namespace: namespace}

		rb := &fakeRegistryBackend{}
		prodTarget, err := rb.PushTarget(app.Name, envImageTag(revision, "production"))
		Expect(err).NotTo(HaveOccurred())
		previewTarget, err := rb.PushTarget(app.Name, envImageTag(revision, previewEnv))
		Expect(err).NotTo(HaveOccurred())

		// Both BuildRuns are seeded terminal so the env loop takes its
		// already-built short-circuit and no build is actually launched.
		prodRun := &mortisev1alpha1.BuildRun{
			ObjectMeta: metav1.ObjectMeta{Name: "preview-build-fail-production-run", Namespace: namespace},
			Spec:       appBuildRunSpec(app, "production", "main", revision, prodTarget.Full, prodTarget.Full),
		}
		Expect(k8sClient.Create(ctx, prodRun)).To(Succeed())
		defer func() { Expect(k8sClient.Delete(ctx, prodRun)).To(Succeed()) }()
		prodRun.Status = mortisev1alpha1.BuildRunStatus{
			Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
			Image: prodTarget.Full,
		}
		Expect(k8sClient.Status().Update(ctx, prodRun)).To(Succeed())

		previewRun := &mortisev1alpha1.BuildRun{
			ObjectMeta: metav1.ObjectMeta{Name: "preview-build-fail-preview-run", Namespace: namespace},
			Spec:       appBuildRunSpec(app, previewEnv, "main", revision, previewTarget.Full, previewTarget.Full),
		}
		Expect(k8sClient.Create(ctx, previewRun)).To(Succeed())
		defer func() { Expect(k8sClient.Delete(ctx, previewRun)).To(Succeed()) }()
		previewRun.Status = mortisev1alpha1.BuildRunStatus{
			Phase:          mortisev1alpha1.BuildRunPhaseFailed,
			FailureReason:  "BuildFailed",
			FailureMessage: "invalid reference format",
		}
		Expect(k8sClient.Status().Update(ctx, previewRun)).To(Succeed())

		Expect(k8sClient.Get(ctx, key, app)).To(Succeed())
		app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
			{
				Name:           "production",
				LastBuiltSHA:   revision,
				LastBuiltImage: prodTarget.Full,
				CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  prodRun.Name,
					Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
				},
			},
			{
				Name: previewEnv,
				CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  previewRun.Name,
					Phase: mortisev1alpha1.BuildRunPhaseFailed,
				},
			},
		}
		Expect(k8sClient.Status().Update(ctx, app)).To(Succeed())

		var written []metav1.Condition
		base, err := ctrlclient.NewWithWatch(cfg, ctrlclient.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		recording := interceptor.NewClient(base, interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c ctrlclient.Client, subResourceName string, obj ctrlclient.Object, opts ...ctrlclient.SubResourceUpdateOption) error {
				if a, ok := obj.(*mortisev1alpha1.App); ok {
					if cond := meta.FindStatusCondition(a.Status.Conditions, "BuildSucceeded"); cond != nil {
						written = append(written, *cond)
					}
				}
				return c.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
		})

		// GitClient nil makes prepareGitSource a no-op, so the spec exercises
		// the env loop without standing up a GitProvider and token secret.
		r := &AppReconciler{
			Client:          recording,
			Scheme:          scheme.Scheme,
			BuildClient:     &fakeBuildClient{},
			RegistryBackend: rb,
			Builds:          &BuildTrackerStore{},
		}
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		for _, cond := range written {
			Expect(cond.Status).NotTo(Equal(metav1.ConditionFalse),
				"env loop persisted a preview-only build failure to the app-global condition: reason=%s message=%s",
				cond.Reason, cond.Message)
		}

		// The per-env result must still be recorded — the guard suppresses the
		// app-global write, not the projection.
		Expect(k8sClient.Get(ctx, key, app)).To(Succeed())
		es := envStatusFor(app, previewEnv)
		Expect(es).NotTo(BeNil())
		Expect(es.CurrentBuildRunRef).NotTo(BeNil())
		Expect(es.CurrentBuildRunRef.Phase).To(Equal(mortisev1alpha1.BuildRunPhaseFailed))
	})
})
