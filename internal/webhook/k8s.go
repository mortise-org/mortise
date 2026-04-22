package webhook

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// K8sReader implements k8sReader using a controller-runtime client.
type K8sReader struct {
	client client.Client
}

// NewK8sReader returns a K8sReader backed by the given client.
func NewK8sReader(c client.Client) *K8sReader {
	return &K8sReader{client: c}
}

func (r *K8sReader) getGitProvider(ctx context.Context, name string) (*mortisev1alpha1.GitProvider, error) {
	var gp mortisev1alpha1.GitProvider
	if err := r.client.Get(ctx, types.NamespacedName{Name: name}, &gp); err != nil {
		return nil, fmt.Errorf("get GitProvider %q: %w", name, err)
	}
	return &gp, nil
}

// getProject fetches the cluster-scoped Project CR by name.
func (r *K8sReader) getProject(ctx context.Context, name string) (*mortisev1alpha1.Project, error) {
	var project mortisev1alpha1.Project
	if err := r.client.Get(ctx, types.NamespacedName{Name: name}, &project); err != nil {
		return nil, fmt.Errorf("get Project %q: %w", name, err)
	}
	return &project, nil
}

func (r *K8sReader) getSecret(ctx context.Context, namespace, name, key string) (string, error) {
	var s corev1.Secret
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	v, ok := s.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
	}
	return string(v), nil
}

// listGitApps returns all Apps across all namespaces that have source.type=git.
func (r *K8sReader) listGitApps(ctx context.Context) ([]mortisev1alpha1.App, error) {
	var all mortisev1alpha1.AppList
	if err := r.client.List(ctx, &all); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	out := make([]mortisev1alpha1.App, 0)
	for _, a := range all.Items {
		if a.Spec.Source.Type == mortisev1alpha1.SourceTypeGit {
			out = append(out, a)
		}
	}
	return out, nil
}

// patchAppRevision sets the mortise.dev/revision annotation on the given App
// using a strategic-merge patch so it triggers a reconcile without a full update.
func (r *K8sReader) patchAppRevision(ctx context.Context, app *mortisev1alpha1.App, sha string) error {
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{
				"mortise.dev/revision": sha,
			},
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal patch: %w", err)
	}
	return r.client.Patch(ctx, app, client.RawPatch(types.MergePatchType, data))
}

// listPreviewEnvironments returns all PreviewEnvironments in the given namespace.
func (r *K8sReader) listPreviewEnvironments(ctx context.Context, namespace string) ([]mortisev1alpha1.PreviewEnvironment, error) {
	var list mortisev1alpha1.PreviewEnvironmentList
	if err := r.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list preview environments in %s: %w", namespace, err)
	}
	return list.Items, nil
}

// createPreviewEnvironment creates a PreviewEnvironment CRD.
func (r *K8sReader) createPreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	return r.client.Create(ctx, pe)
}

// updatePreviewEnvironment updates a PreviewEnvironment CRD.
func (r *K8sReader) updatePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	return r.client.Update(ctx, pe)
}

// deletePreviewEnvironment deletes a PreviewEnvironment CRD.
func (r *K8sReader) deletePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	return r.client.Delete(ctx, pe)
}
