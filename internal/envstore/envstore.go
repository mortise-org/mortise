// Package envstore manages per-app and shared environment variable Secrets.
//
// Each app-environment has one Secret ({app}-env) in the workload namespace.
// Each project-environment has one shared Secret (shared-env) in the workload
// namespace. Deployments mount both via envFrom — shared first, app-specific
// second (app wins on conflict).
//
// Source annotations track where each key came from so the UI can show badges
// (e.g. "binding", "generated", "shared") without storing vars in multiple places.
package envstore

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// AppEnvSuffix is appended to the app name for the per-app env Secret.
	AppEnvSuffix = "-env"

	// SharedEnvName is the name of the shared env Secret per project-environment
	// in the workload namespace. Materialized by the controller from the
	// control-namespace source of truth.
	SharedEnvName = "shared-env"

	// SharedVarsSourceName is the name of the shared vars Secret in the control
	// namespace. This is the source of truth — the API reads/writes here, the
	// controller copies to SharedEnvName in each env namespace.
	SharedVarsSourceName = "shared-vars"

	// ManagedByLabel marks Secrets owned by Mortise.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "mortise"

	// Source annotations — comma-separated key lists tracking origin of each var.
	AnnotationBindingKeys   = "mortise.dev/binding-keys"
	AnnotationGeneratedKeys = "mortise.dev/generated-keys"
	AnnotationSharedKeys    = "mortise.dev/shared-keys"
)

// AppEnvSecretName returns the Secret name for an app's env vars.
func AppEnvSecretName(appName string) string {
	return appName + AppEnvSuffix
}

// EnvFromSources returns the envFrom entries for a Deployment container.
// Order: shared-env (lowest priority) then {app}-env (wins on conflict).
func EnvFromSources(appName string) []corev1.EnvFromSource {
	return []corev1.EnvFromSource{
		{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: SharedEnvName},
			Optional:             boolPtr(true),
		}},
		{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: AppEnvSecretName(appName)},
			Optional:             boolPtr(true),
		}},
	}
}

// Store is the read/write interface for env var Secrets.
type Store struct {
	Client client.Client
}

// Env represents a single env var with its source.
type Env struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"` // "user", "binding", "generated", "shared", ""
}

// validEnvVarName matches POSIX-compliant environment variable names.
var validEnvVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvVarName checks that a name is a valid POSIX env var name.
// This also prevents comma injection into source-tracking annotations.
func ValidateEnvVarName(name string) error {
	if !validEnvVarName.MatchString(name) {
		return fmt.Errorf("invalid env var name %q: must match [A-Za-z_][A-Za-z0-9_]*", name)
	}
	return nil
}

// validateEnvVars checks all env var names in a slice.
func validateEnvVars(vars []Env) error {
	for _, v := range vars {
		if err := ValidateEnvVarName(v.Name); err != nil {
			return err
		}
	}
	return nil
}

// Get reads all env vars from an app's env Secret.
func (s *Store) Get(ctx context.Context, namespace, appName string) ([]Env, error) {
	secret, err := s.getSecret(ctx, namespace, AppEnvSecretName(appName))
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secretToEnvs(secret), nil
}

// GetShared reads all env vars from the shared-env Secret.
func (s *Store) GetShared(ctx context.Context, namespace string) ([]Env, error) {
	secret, err := s.getSecret(ctx, namespace, SharedEnvName)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secretToEnvs(secret), nil
}

// Set writes env vars to an app's env Secret, creating it if needed.
// source indicates where the vars came from ("user", "binding", "generated").
// If source is empty, existing source annotations for those keys are preserved.
func (s *Store) Set(ctx context.Context, namespace, appName string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	name := AppEnvSecretName(appName)
	return s.upsertSecret(ctx, namespace, name, vars, labels)
}

// SetShared writes env vars to the shared-env Secret, creating it if needed.
func (s *Store) SetShared(ctx context.Context, namespace string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	return s.upsertSecret(ctx, namespace, SharedEnvName, vars, labels)
}

// Merge reads the existing Secret, merges in new vars (overwriting duplicates),
// and writes back. Returns the merged set.
func (s *Store) Merge(ctx context.Context, namespace, appName string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	name := AppEnvSecretName(appName)
	existing, err := s.getSecret(ctx, namespace, name)
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	merged := make(map[string]Env)
	if existing != nil {
		for _, e := range secretToEnvs(existing) {
			merged[e.Name] = e
		}
	}
	for _, e := range vars {
		merged[e.Name] = e
	}

	flat := make([]Env, 0, len(merged))
	for _, e := range merged {
		flat = append(flat, e)
	}
	return s.upsertSecret(ctx, namespace, name, flat, labels)
}

// MergeShared is like Merge but for the shared-env Secret.
func (s *Store) MergeShared(ctx context.Context, namespace string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	existing, err := s.getSecret(ctx, namespace, SharedEnvName)
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	merged := make(map[string]Env)
	if existing != nil {
		for _, e := range secretToEnvs(existing) {
			merged[e.Name] = e
		}
	}
	for _, e := range vars {
		merged[e.Name] = e
	}

	flat := make([]Env, 0, len(merged))
	for _, e := range merged {
		flat = append(flat, e)
	}
	return s.upsertSecret(ctx, namespace, SharedEnvName, flat, labels)
}

// ReplaceSource replaces all vars with the given source in an app's env Secret.
// Existing vars with a different source are preserved. If vars is empty, all
// vars with the given source are removed.
func (s *Store) ReplaceSource(ctx context.Context, namespace, appName, source string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	name := AppEnvSecretName(appName)
	existing, err := s.getSecret(ctx, namespace, name)
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	var kept []Env
	if existing != nil {
		for _, e := range secretToEnvs(existing) {
			if e.Source != source {
				kept = append(kept, e)
			}
		}
	}
	kept = append(kept, vars...)
	return s.upsertSecret(ctx, namespace, name, kept, labels)
}

// Delete removes a key from an app's env Secret.
func (s *Store) Delete(ctx context.Context, namespace, appName, key string) error {
	name := AppEnvSecretName(appName)
	secret, err := s.getSecret(ctx, namespace, name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	delete(secret.Data, key)
	removeKeyFromAnnotations(secret, key)
	return s.Client.Update(ctx, secret)
}

// SecretExists reports whether the app's env Secret exists in the namespace,
// regardless of whether it contains any data. This lets callers distinguish
// "Secret not yet created" from "Secret exists but user cleared all vars."
func (s *Store) SecretExists(ctx context.Context, namespace, appName string) (bool, error) {
	name := AppEnvSecretName(appName)
	var existing corev1.Secret
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &existing)
	if err == nil {
		return true, nil
	}
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// EnsureExists creates the env Secret if it doesn't exist (empty).
func (s *Store) EnsureExists(ctx context.Context, namespace, appName string, labels map[string]string) error {
	name := AppEnvSecretName(appName)
	var existing corev1.Secret
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &existing)
	if err == nil {
		return nil // already exists
	}
	if !k8serrors.IsNotFound(err) {
		return err
	}
	// Handle race: another controller might have created it while we were checking.
	if err := s.Client.Create(ctx, buildSecret(namespace, name, nil, labels)); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil // another controller created it - that's fine
		}
		return err
	}
	return nil
}

// GetSharedSource reads shared vars from the control-namespace source of truth.
func (s *Store) GetSharedSource(ctx context.Context, controlNs string) ([]Env, error) {
	secret, err := s.getSecret(ctx, controlNs, SharedVarsSourceName)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secretToEnvs(secret), nil
}

// SetSharedSource writes shared vars to the control-namespace source of truth.
func (s *Store) SetSharedSource(ctx context.Context, controlNs string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	return s.upsertSecret(ctx, controlNs, SharedVarsSourceName, vars, labels)
}

// MergeSharedSource merges shared vars into the control-namespace source.
func (s *Store) MergeSharedSource(ctx context.Context, controlNs string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	existing, err := s.getSecret(ctx, controlNs, SharedVarsSourceName)
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	merged := make(map[string]Env)
	if existing != nil {
		for _, e := range secretToEnvs(existing) {
			merged[e.Name] = e
		}
	}
	for _, e := range vars {
		merged[e.Name] = e
	}
	flat := make([]Env, 0, len(merged))
	for _, e := range merged {
		flat = append(flat, e)
	}
	return s.upsertSecret(ctx, controlNs, SharedVarsSourceName, flat, labels)
}

// EnsureSharedExists creates the shared-env Secret if it doesn't exist.
func (s *Store) EnsureSharedExists(ctx context.Context, namespace string, labels map[string]string) error {
	var existing corev1.Secret
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: SharedEnvName}, &existing)
	if err == nil {
		return nil
	}
	if !k8serrors.IsNotFound(err) {
		return err
	}
	// Handle race: another controller might have created it while we were checking.
	if err := s.Client.Create(ctx, buildSecret(namespace, SharedEnvName, nil, labels)); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// --- internal helpers ---

func (s *Store) getSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	var secret corev1.Secret
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret)
	if err != nil {
		return nil, err
	}
	return &secret, nil
}

func (s *Store) upsertSecret(ctx context.Context, namespace, name string, vars []Env, labels map[string]string) error {
	desired := buildSecret(namespace, name, vars, labels)

	var existing corev1.Secret
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &existing)
	if k8serrors.IsNotFound(err) {
		if err := s.Client.Create(ctx, desired); err != nil {
			if k8serrors.IsAlreadyExists(err) {
				goto update
			}
			return fmt.Errorf("create env secret %s/%s: %w", namespace, name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get env secret %s/%s: %w", namespace, name, err)
	}

update:
	// Re-fetch to ensure we have the latest version before updating.
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &existing); err != nil {
		return fmt.Errorf("re-get env secret %s/%s: %w", namespace, name, err)
	}

	existing.Data = desired.Data
	// Replace mortise source-tracking annotations entirely from desired,
	// preserving any non-mortise annotations on the existing Secret.
	if existing.Annotations == nil {
		existing.Annotations = make(map[string]string)
	}
	for k := range existing.Annotations {
		if strings.HasPrefix(k, "mortise.dev/") {
			delete(existing.Annotations, k)
		}
	}
	for k, v := range desired.Annotations {
		existing.Annotations[k] = v
	}
	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	for k, v := range desired.Labels {
		existing.Labels[k] = v
	}
	return s.Client.Update(ctx, &existing)
}

func buildSecret(namespace, name string, vars []Env, extraLabels map[string]string) *corev1.Secret {
	data := make(map[string][]byte, len(vars))
	bindingKeys := []string{}
	generatedKeys := []string{}
	sharedKeys := []string{}

	for _, e := range vars {
		data[e.Name] = []byte(e.Value)
		switch e.Source {
		case "binding":
			bindingKeys = append(bindingKeys, e.Name)
		case "generated":
			generatedKeys = append(generatedKeys, e.Name)
		case "shared":
			sharedKeys = append(sharedKeys, e.Name)
		}
	}

	labels := map[string]string{
		ManagedByLabel: ManagedByValue,
	}
	for k, v := range extraLabels {
		labels[k] = v
	}

	annotations := map[string]string{}
	if len(bindingKeys) > 0 {
		sort.Strings(bindingKeys)
		annotations[AnnotationBindingKeys] = strings.Join(bindingKeys, ",")
	}
	if len(generatedKeys) > 0 {
		sort.Strings(generatedKeys)
		annotations[AnnotationGeneratedKeys] = strings.Join(generatedKeys, ",")
	}
	if len(sharedKeys) > 0 {
		sort.Strings(sharedKeys)
		annotations[AnnotationSharedKeys] = strings.Join(sharedKeys, ",")
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Data: data,
	}
}

func secretToEnvs(secret *corev1.Secret) []Env {
	bindingSet := parseKeySet(secret.Annotations[AnnotationBindingKeys])
	generatedSet := parseKeySet(secret.Annotations[AnnotationGeneratedKeys])
	sharedSet := parseKeySet(secret.Annotations[AnnotationSharedKeys])

	envs := make([]Env, 0, len(secret.Data))
	for k, v := range secret.Data {
		source := "user"
		if bindingSet[k] {
			source = "binding"
		} else if generatedSet[k] {
			source = "generated"
		} else if sharedSet[k] {
			source = "shared"
		}
		envs = append(envs, Env{Name: k, Value: string(v), Source: source})
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].Name < envs[j].Name })
	return envs
}

func parseKeySet(csv string) map[string]bool {
	if csv == "" {
		return nil
	}
	set := make(map[string]bool)
	for _, k := range strings.Split(csv, ",") {
		if k = strings.TrimSpace(k); k != "" {
			set[k] = true
		}
	}
	return set
}

func removeKeyFromAnnotations(secret *corev1.Secret, key string) {
	for _, ann := range []string{AnnotationBindingKeys, AnnotationGeneratedKeys, AnnotationSharedKeys} {
		if csv, ok := secret.Annotations[ann]; ok {
			keys := strings.Split(csv, ",")
			filtered := keys[:0]
			for _, k := range keys {
				if strings.TrimSpace(k) != key {
					filtered = append(filtered, k)
				}
			}
			if len(filtered) == 0 {
				delete(secret.Annotations, ann)
			} else {
				secret.Annotations[ann] = strings.Join(filtered, ",")
			}
		}
	}
}

func boolPtr(b bool) *bool { return &b }
