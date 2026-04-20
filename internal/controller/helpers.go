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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// validateSecretRef returns an error if the secret does not exist or the key is absent.
func validateSecretRef(ctx context.Context, c client.Client, ref mortisev1alpha1.SecretRef, desc string) error {
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	if err := c.Get(ctx, key, &secret); err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("%s: secret %s/%s not found", desc, ref.Namespace, ref.Name)
		}
		return fmt.Errorf("%s: get secret %s/%s: %w", desc, ref.Namespace, ref.Name, err)
	}
	if _, ok := secret.Data[ref.Key]; !ok {
		return fmt.Errorf("%s: key %q not present in secret %s/%s", desc, ref.Key, ref.Namespace, ref.Name)
	}
	return nil
}
