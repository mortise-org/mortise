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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/constants"
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
	ManagedByLabel = constants.ManagedByLabel
	ManagedByValue = constants.ManagedByValue

	// Source annotations — comma-separated key lists tracking origin of each var.
	AnnotationBindingKeys   = "mortise.dev/binding-keys"
	AnnotationGeneratedKeys = "mortise.dev/generated-keys"
	AnnotationSharedKeys    = "mortise.dev/shared-keys"

	// AnnotationLastSpecEnv stores the JSON-encoded CRD spec env VALUES that
	// were last applied by the controller.
	//
	// LEGACY. Superseded by AnnotationLastSpecEnvDigest: it held resolved
	// credential values in plaintext on the derived Secret, so a value moved
	// out of the CRD into a Secret was written straight back out one field
	// over (CAI-168). The controller reads it only to migrate, and deletes it
	// on the next write.
	//
	// Not marked Deprecated: the migration path must keep referencing it, and
	// a deprecation marker would make staticcheck flag the very code doing the
	// removal.
	AnnotationLastSpecEnv = "mortise.dev/last-spec-env"

	// AnnotationLastSpecEnvDigest stores a JSON map of env var name to a
	// SHA-256 digest of the value the controller last applied from the spec.
	//
	// A digest answers the only question this annotation exists to answer --
	// "does the live value still match what the spec last set?" -- exactly as
	// well as the value does, without persisting the credential.
	AnnotationLastSpecEnvDigest = "mortise.dev/last-spec-env-digest"
)

// AppEnvSecretName returns the Secret name for an app's env vars.
func AppEnvSecretName(appName string) string {
	return appName + AppEnvSuffix
}

// EnvFromSources returns the envFrom entries for a Deployment container.
// Order: shared-env (lowest priority) then {app}-env (wins on conflict).
func EnvFromSources(appName string) []corev1.EnvFromSource {
	names := envSourceNames(appName)
	sources := make([]corev1.EnvFromSource, 0, len(names))
	for _, name := range names {
		sources = append(sources, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: name},
			Optional:             boolPtr(true),
		}})
	}
	return sources
}

// envSourceNames is the one ordering of the Secrets a container's env is
// built from. Kubernetes resolves envFrom collisions in favour of the LATER
// entry, so {app}-env beats shared-env in the process. EnvFromSources and
// EnvHash both derive from this list: the hash used to merge in the
// opposite order, so for a key present in both Secrets the pod-template hash
// tracked a value the container was not running, and changing the value the
// container did run never rolled it (CAI-178).
func envSourceNames(appName string) []string {
	return []string{SharedEnvName, AppEnvSecretName(appName)}
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
// Set is a wholesale replace: callers must own the entire var set for this
// Secret. It is only safe while no partial writer (Apply/Merge-based) shares
// the Secret — a future partial co-writer would silently lose to it.
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
// SetShared is a wholesale replace under the same invariant as Set: safe only
// while no partial writer shares the shared-env Secret (MergeSharedSource is
// Apply-based and recomputes, so it does not conflict).
func (s *Store) SetShared(ctx context.Context, namespace string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	return s.upsertSecret(ctx, namespace, SharedEnvName, vars, labels)
}

// ErrSkip can be returned by an Apply mutate callback to abort the apply
// without writing anything (and without surfacing an error). Used to avoid
// creating a Secret that would be empty.
var ErrSkip = errors.New("envstore: skip apply")

// Apply atomically mutates an app's env Secret. mutate receives the current
// vars — freshly read on every conflict-retry attempt, nil if the Secret does
// not exist — and returns the full desired set. Because the result is
// recomputed from the latest read inside the retry loop, concurrent writers
// cannot lose each other's updates.
func (s *Store) Apply(ctx context.Context, namespace, appName string, labels map[string]string, mutate func(current []Env) ([]Env, error)) error {
	return s.applySecret(ctx, namespace, AppEnvSecretName(appName), labels, mutate)
}

// Merge reads the existing Secret, merges in new vars (overwriting duplicates),
// and writes back. Returns the merged set.
func (s *Store) Merge(ctx context.Context, namespace, appName string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	return s.Apply(ctx, namespace, appName, labels, func(current []Env) ([]Env, error) {
		return mergeEnvs(current, vars), nil
	})
}

// MergeShared is like Merge but for the shared-env Secret.
func (s *Store) MergeShared(ctx context.Context, namespace string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	return s.applySecret(ctx, namespace, SharedEnvName, labels, func(current []Env) ([]Env, error) {
		return mergeEnvs(current, vars), nil
	})
}

// ReplaceSource replaces all vars with the given source in an app's env Secret.
// Existing vars with a different source are preserved. If vars is empty, all
// vars with the given source are removed.
func (s *Store) ReplaceSource(ctx context.Context, namespace, appName, source string, vars []Env, labels map[string]string) error {
	if err := validateEnvVars(vars); err != nil {
		return err
	}
	return s.Apply(ctx, namespace, appName, labels, func(current []Env) ([]Env, error) {
		var kept []Env
		for _, e := range current {
			if e.Source != source {
				kept = append(kept, e)
			}
		}
		return append(kept, vars...), nil
	})
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
	if _, exists := secret.Data[key]; !exists {
		return nil
	}

	return UpdateWithConflictRetry(ctx, s.Client, types.NamespacedName{Namespace: namespace, Name: name}, func() *corev1.Secret {
		return &corev1.Secret{}
	}, func(secret *corev1.Secret) (bool, error) {
		if _, exists := secret.Data[key]; !exists {
			return false, nil
		}
		delete(secret.Data, key)
		removeKeyFromAnnotations(secret, key)
		return true, nil
	})
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
	return s.applySecret(ctx, controlNs, SharedVarsSourceName, labels, func(current []Env) ([]Env, error) {
		return mergeEnvs(current, vars), nil
	})
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
	return s.applySecret(ctx, namespace, name, labels, func([]Env) ([]Env, error) {
		return vars, nil
	})
}

// applySecret runs mutate against a fresh read of the Secret inside the
// conflict-retry loop: every attempt re-reads the latest vars and recomputes
// the desired set, so a retry after a conflict picks up the competing write
// instead of clobbering it with a stale precomputed result.
func (s *Store) applySecret(ctx context.Context, namespace, name string, labels map[string]string, mutate func(current []Env) ([]Env, error)) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var existing corev1.Secret
		getErr := s.Client.Get(ctx, key, &existing)
		if getErr != nil && !k8serrors.IsNotFound(getErr) {
			return fmt.Errorf("get env secret %s/%s: %w", namespace, name, getErr)
		}

		var current []Env
		if getErr == nil {
			current = secretToEnvs(&existing)
		}
		vars, err := mutate(current)
		if err != nil {
			if errors.Is(err, ErrSkip) {
				return nil
			}
			return err
		}
		if err := validateEnvVars(vars); err != nil {
			return err
		}
		desired := buildSecret(namespace, name, vars, labels)

		if k8serrors.IsNotFound(getErr) {
			if createErr := s.Client.Create(ctx, desired); createErr != nil {
				if k8serrors.IsAlreadyExists(createErr) {
					// Lost a create race — surface as a conflict so the retry
					// loop re-reads and applies as an update.
					return k8serrors.NewConflict(corev1.Resource("secrets"), name, createErr)
				}
				return fmt.Errorf("create env secret %s/%s: %w", namespace, name, createErr)
			}
			return nil
		}

		if !applyDesiredSecret(&existing, desired) {
			return nil
		}
		return s.Client.Update(ctx, &existing)
	})
}

// applyDesiredSecret copies desired data, source annotations, and labels onto
// existing, reporting whether anything changed. Non-Mortise annotations and
// labels on existing are preserved.
func applyDesiredSecret(existing, desired *corev1.Secret) bool {
	changed := false
	if !secretDataEqual(existing.Data, desired.Data) {
		existing.Data = desired.Data
		changed = true
	}

	if existing.Annotations == nil {
		existing.Annotations = make(map[string]string)
	}
	for _, key := range []string{AnnotationBindingKeys, AnnotationGeneratedKeys, AnnotationSharedKeys} {
		if _, ok := existing.Annotations[key]; ok {
			delete(existing.Annotations, key)
			changed = true
		}
	}
	for k, v := range desired.Annotations {
		if existing.Annotations[k] != v {
			existing.Annotations[k] = v
			changed = true
		}
	}

	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	for k, v := range desired.Labels {
		if existing.Labels[k] != v {
			existing.Labels[k] = v
			changed = true
		}
	}

	return changed
}

// mergeEnvs overlays vars onto current, overwriting duplicates by name.
func mergeEnvs(current, vars []Env) []Env {
	merged := make(map[string]Env, len(current)+len(vars))
	for _, e := range current {
		merged[e.Name] = e
	}
	for _, e := range vars {
		merged[e.Name] = e
	}
	flat := make([]Env, 0, len(merged))
	for _, e := range merged {
		flat = append(flat, e)
	}
	return flat
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

// SecretToEnvs decodes a Secret using envstore's source annotations.
func SecretToEnvs(secret *corev1.Secret) []Env {
	return secretToEnvs(secret)
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

func secretDataEqual(existing, desired map[string][]byte) bool {
	if len(existing) == 0 && len(desired) == 0 {
		return true
	}
	if len(existing) != len(desired) {
		return false
	}
	for key, desiredValue := range desired {
		existingValue, ok := existing[key]
		if !ok || string(existingValue) != string(desiredValue) {
			return false
		}
	}
	return true
}

func boolPtr(b bool) *bool { return &b }

// HashData produces a sha256 over the sorted key=value pairs.
// Key sorting is load-bearing: Go maps randomise iteration order, and an
// unstable hash would cause gratuitous pod restarts on every reconcile.
func HashData(data map[string][]byte) string {
	if len(data) == 0 {
		return ""
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{'='})
		h.Write(data[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// EnvHash is the hash the controller stamps onto the pod template as
// `mortise.dev/env-hash`: a HashData over the merged contents of the app's
// {app}-env Secret and the project's shared-env Secret in the workload
// namespace. Missing Secrets contribute nothing. Read-only.
func EnvHash(ctx context.Context, reader client.Reader, appName, envNs string) string {
	combined := make(map[string][]byte)
	// Same order as the container's envFrom: a later Secret overwrites an
	// earlier one here exactly as it does in the process.
	for _, name := range envSourceNames(appName) {
		var sec corev1.Secret
		if err := reader.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &sec); err != nil {
			continue
		}
		for k, v := range sec.Data {
			combined[k] = v
		}
	}
	return HashData(combined)
}
