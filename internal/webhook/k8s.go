package webhook

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
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
