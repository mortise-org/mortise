package envstore

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maxConflictRetries = 3

// UpdateWithConflictRetry re-reads the latest object and retries the update
// when the API server rejects it with an optimistic-lock conflict.
func UpdateWithConflictRetry[T client.Object](
	ctx context.Context,
	c client.Client,
	key types.NamespacedName,
	newObject func() T,
	mutate func(T) (bool, error),
) error {
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		obj := newObject()
		if err := c.Get(ctx, key, obj); err != nil {
			return err
		}

		changed, err := mutate(obj)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}

		if err := c.Update(ctx, obj); err != nil {
			if !apierrors.IsConflict(err) || attempt == maxConflictRetries-1 {
				return err
			}
			continue
		}
		return nil
	}
	return nil
}
