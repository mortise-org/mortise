package envstore

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// UpdateWithConflictRetry re-reads the latest object and retries the update
// when the API server rejects it with an optimistic-lock conflict. Backed by
// retry.DefaultRetry (5 attempts with jittered backoff).
func UpdateWithConflictRetry[T client.Object](
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	newObject func() T,
	mutate func(T) (bool, error),
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj := newObject()
		if err := c.Get(ctx, key, obj); err != nil {
			return err
		}

		changed, err := mutate(obj)
		if err != nil || !changed {
			return err
		}
		return c.Update(ctx, obj)
	})
}
