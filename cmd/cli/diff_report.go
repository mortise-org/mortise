package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
)

// envHashAnnotation is the pod-template annotation the App controller stamps
// with the hash of the env Secrets a workload was started with.
const envHashAnnotation = "mortise.dev/env-hash"

// digestLen is how much of a sha256 hex digest is printed. Short enough to
// scan by eye, long enough that two different values effectively never
// collide in one report.
const digestLen = 12

// Finding categories. These are part of the `-o json` contract.
const (
	// catInSync: declared in the CRD and present in the derived Secret with
	// the same value. Nothing to do.
	catInSync = "in-sync"
	// catSpecDiffers: declared in both with different values, and the Secret
	// still holds the value the CRD last applied. The spec has genuinely
	// moved and has not reached the Secret pods mount.
	catSpecDiffers = "spec-differs-from-secret"
	// catUserOverride: declared in both with different values, and the Secret
	// no longer holds what the CRD last applied. Someone set this through the
	// API/UI and the controller deliberately leaves such an override alone.
	// Normal and permanent by design.
	catUserOverride = "user-override"
	// catSpecShadowed: declared in both with different values, the Secret no
	// longer holds what the CRD last applied, and — unlike catUserOverride —
	// the Secret's entry is owned by the controller itself (binding, shared,
	// or generated), not a user. The controller rewrites that entry from its
	// source on every reconcile, so it always wins over the spec.env literal:
	// the spec value is not merely behind, it can never take effect while
	// this name belongs to that source. The API cannot have produced this
	// state (it rejects writes to a non-user-owned key), so it is always a
	// naming collision between spec.env and a binding/shared/generated var.
	catSpecShadowed = "spec-shadowed-by-controller"
	// catSpecDiffersUntracked: declared in both with different values, and the
	// Secret records nothing about what the CRD last applied, so the two cases
	// above cannot be told apart from the cluster.
	catSpecDiffersUntracked = "spec-differs-untracked"
	// catMissingFromSecret: declared in the CRD, absent from the derived
	// Secret.
	catMissingFromSecret = "missing-from-secret"
	// catSharedVarStale: present in the derived Secret from spec.sharedVars,
	// with a different value. sharedVars are seeded once and never re-applied.
	catSharedVarStale = "shared-var-not-updated"
	// catFromSharedEnv: absent from this app's Secret, present in the project's
	// shared-env Secret — which pods mount as well.
	catFromSharedEnv = "from-shared-env"
	// catNotDeclaredInCRD: present in the derived Secret with source "user",
	// absent from the CRD. Normal — the API writes user vars straight to the
	// Secret and never touches the CRD.
	catNotDeclaredInCRD = "not-declared-in-crd"
	// catDerived: present in the derived Secret with a source the controller
	// owns (binding / shared / generated). Expected to be absent from
	// spec.env.
	catDerived = "derived"
	// catUnresolved: declared in the CRD but its effective value could not be
	// determined, so it cannot be compared.
	catUnresolved = "unresolved"
)

// diffFinding is one env var's verdict. It carries names, digests, and
// sources — never a value.
type diffFinding struct {
	Name         string `json:"name"`
	Category     string `json:"category"`
	SpecDigest   string `json:"specDigest,omitempty"`
	SecretDigest string `json:"secretDigest,omitempty"`
	Source       string `json:"source,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

// rolloutReport is layer 3: whether the running workload was started with the
// environment that currently exists.
type rolloutReport struct {
	WorkloadKind  string `json:"workloadKind"`
	WorkloadName  string `json:"workloadName"`
	WorkloadFound bool   `json:"workloadFound"`
	// SecretEnvHash is the hash of the env Secrets as they exist right now.
	SecretEnvHash string `json:"secretEnvHash,omitempty"`
	// WorkloadEnvHash is the hash the running pod template was stamped with.
	WorkloadEnvHash       string `json:"workloadEnvHash,omitempty"`
	StatusPendingEnvHash  string `json:"statusPendingEnvHash,omitempty"`
	StatusDeployedEnvHash string `json:"statusDeployedEnvHash,omitempty"`
	InSync                bool   `json:"inSync"`
	AutoRedeploy          bool   `json:"autoRedeploy"`
	Detail                string `json:"detail"`
}

// envDiffReport is the per-environment result.
type envDiffReport struct {
	Environment       string        `json:"environment"`
	ControlNamespace  string        `json:"controlNamespace"`
	WorkloadNamespace string        `json:"workloadNamespace"`
	Enabled           bool          `json:"enabled"`
	SecretExists      bool          `json:"secretExists"`
	Findings          []diffFinding `json:"findings"`
	Rollout           rolloutReport `json:"rollout"`
}

// crdChange is one entry of the `-f` (dry-run) CRD-level comparison.
type crdChange struct {
	Environment string `json:"environment"`
	Name        string `json:"name"`
	// Change is one of "add", "remove", "change".
	Change     string `json:"change"`
	LiveDigest string `json:"liveDigest,omitempty"`
	FileDigest string `json:"fileDigest,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// diffReport is the whole result. Marshalled verbatim by `-o json`.
type diffReport struct {
	App          string          `json:"app"`
	Project      string          `json:"project"`
	DryRunFile   string          `json:"dryRunFile,omitempty"`
	CRDChanges   []crdChange     `json:"crdChanges,omitempty"`
	Environments []envDiffReport `json:"environments"`
}

// digestSalt randomises the printed digests once per invocation. They only
// have to be comparable inside a single report, and an unsalted sha256 of a
// low-entropy value (`true`, `production`, an account id) can be confirmed
// offline by anyone holding a report — which this output is meant to survive
// being pasted into a channel during an incident.
var digestSalt = newDigestSalt()

func newDigestSalt() []byte {
	b := make([]byte, 16)
	// crypto/rand.Read never returns an error; it crashes the program if the
	// system source fails, which is the right outcome here — carrying on with
	// an empty salt would silently restore the guessable digests.
	_, _ = rand.Read(b)
	return b
}

// digestValue is the only way a value ever influences output.
func digestValue(v string) string {
	h := sha256.New()
	h.Write(digestSalt)
	h.Write([]byte(v))
	return hex.EncodeToString(h.Sum(nil))[:digestLen]
}

// specEnvDigest is the form the controller stores in
// mortise.dev/last-spec-env-digest: a full, unsalted sha256 hex of the value.
// Separate from digestValue because it is only ever compared against what the
// controller wrote, never printed.
func specEnvDigest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// lastSpecDigests reads the controller's record of what the spec last applied
// to this Secret: env var name to the full sha256 of that value. Mirrors the
// App controller's readLastSpecEnv, legacy-annotation fallback included, so
// the CLI reads the signal exactly as the controller writes and acts on it.
func lastSpecDigests(secret *corev1.Secret) map[string]string {
	if raw := secret.Annotations[envstore.AnnotationLastSpecEnvDigest]; raw != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil
		}
		return m
	}
	// The legacy annotation holds values rather than digests. Hashing them
	// yields exactly what the digest annotation would have held, so Secrets
	// written before CAI-168 classify the same way.
	raw := secret.Annotations[envstore.AnnotationLastSpecEnv]
	if raw == "" {
		return nil
	}
	var legacy map[string]string
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return nil
	}
	m := make(map[string]string, len(legacy))
	for k, v := range legacy {
		m[k] = specEnvDigest(v)
	}
	return m
}

// tracksLastAppliedSpec reports whether the Secret still holds the value the
// controller last applied from the spec — the exact condition under which the
// controller would overwrite it on the next reconcile.
func tracksLastAppliedSpec(lastApplied, secretValue string) bool {
	return lastApplied != "" && lastApplied == specEnvDigest(secretValue)
}

// shortHash truncates a full env-hash for display.
func shortHash(h string) string {
	if h == "" {
		return "-"
	}
	if len(h) > digestLen {
		return h[:digestLen]
	}
	return h
}

// specValue is the effective value of a spec env var, reduced to a digest.
type specValue struct {
	digest string
	// binding is set when the value comes from the bindings resolver. The
	// diff does not reimplement bindings resolution, so there is no digest
	// to compare — only presence in the derived Secret is checked.
	binding bool
	// ref and key identify which binding a fromBinding var projects from.
	// They are part of what makes two bound vars different: repointing a var
	// at another binding changes the value the controller will resolve, and
	// with only `binding: true` to compare, that repoint is invisible. A
	// binding ref is a name, not a value, so carrying it leaks nothing.
	ref string
	key string
	// problem is non-empty when the effective value could not be determined.
	problem string
}

// envLayers is what the cluster says about one App × environment, keyed by var
// name. Assembled once per environment and read by the per-var comparison.
type envLayers struct {
	// app is the {app}-env Secret, mounted second so it wins on conflict.
	app map[string]envstore.Env
	// shared is the project's shared-env Secret, mounted first. Pods get it
	// too, so a name missing from {app}-env is not necessarily missing from
	// the container.
	shared map[string]envstore.Env
	// lastSpec maps a var name to the full sha256 of the value the controller
	// last applied from the spec (mortise.dev/last-spec-env-digest). It is the
	// only thing that distinguishes an unapplied spec change from a user
	// override the platform is deliberately honouring.
	lastSpec map[string]string
}

// resolveSpecEnv mirrors the semantics of the App controller's
// resolveEnvVarValue: literal value, bare-Secret-name secretRef keyed by the
// env var's own name and read from the workload namespace, or fromBinding.
// It never writes and never returns a value — only a digest of one.
func resolveSpecEnv(ctx context.Context, c client.Reader, ev mortisev1alpha1.EnvVar, envNs string, bindingRefs map[string]bool) specValue {
	hasValue := ev.Value != ""
	hasSecretRef := ev.ValueFrom != nil && ev.ValueFrom.SecretRef != ""
	hasFromBinding := ev.ValueFrom != nil && ev.ValueFrom.FromBinding != nil

	sources := 0
	for _, set := range []bool{hasValue, hasSecretRef, hasFromBinding} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return specValue{problem: "more than one of value, valueFrom.secretRef, valueFrom.fromBinding is set; the controller drops this variable"}
	}

	switch {
	case hasSecretRef:
		var secret corev1.Secret
		err := c.Get(ctx, types.NamespacedName{Name: ev.ValueFrom.SecretRef, Namespace: envNs}, &secret)
		if k8serrors.IsNotFound(err) {
			return specValue{problem: fmt.Sprintf("valueFrom.secretRef %q does not exist in %s", ev.ValueFrom.SecretRef, envNs)}
		}
		if err != nil {
			return specValue{problem: fmt.Sprintf("valueFrom.secretRef %q in %s could not be read: %v", ev.ValueFrom.SecretRef, envNs, err)}
		}
		raw, ok := secret.Data[ev.Name]
		if !ok {
			return specValue{problem: fmt.Sprintf("valueFrom.secretRef %q in %s has no key %q", ev.ValueFrom.SecretRef, envNs, ev.Name)}
		}
		return specValue{digest: digestValue(string(raw))}
	case hasFromBinding:
		ref := ev.ValueFrom.FromBinding.Ref
		if !bindingRefs[ref] {
			return specValue{problem: fmt.Sprintf("valueFrom.fromBinding.ref %q is not in this environment's bindings list", ref)}
		}
		return specValue{binding: true, ref: ref, key: ev.ValueFrom.FromBinding.Key}
	default:
		return specValue{digest: digestValue(ev.Value)}
	}
}

// findAppEnv returns the App's per-environment overrides for envName, or nil
// when the App declares none (Apps auto-participate in every project env).
func findAppEnv(app *mortisev1alpha1.App, envName string) *mortisev1alpha1.Environment {
	for i := range app.Spec.Environments {
		if app.Spec.Environments[i].Name == envName {
			return &app.Spec.Environments[i]
		}
	}
	return nil
}

func findEnvStatus(app *mortisev1alpha1.App, envName string) *mortisev1alpha1.EnvironmentStatus {
	for i := range app.Status.Environments {
		if app.Status.Environments[i].Name == envName {
			return &app.Status.Environments[i]
		}
	}
	return nil
}

// buildEnvDiff compares the three layers for one App × environment.
//
// specApp supplies layer 1 (the CRD spec). It is the live App normally and the
// App parsed from -f in dry-run mode. liveApp always supplies the status and
// the workload name. Read-only: every cluster call here is a Get.
func buildEnvDiff(
	ctx context.Context,
	c client.Reader,
	specApp, liveApp *mortisev1alpha1.App,
	project, envName string,
	autoRedeploy bool,
) (envDiffReport, error) {
	envNs := constants.EnvNamespace(project, envName)
	rep := envDiffReport{
		Environment:       envName,
		ControlNamespace:  constants.ControlNamespace(project),
		WorkloadNamespace: envNs,
		Enabled:           true,
		Findings:          []diffFinding{},
	}

	specEnv := findAppEnv(specApp, envName)
	if specEnv != nil && specEnv.Enabled != nil && !*specEnv.Enabled {
		rep.Enabled = false
	}

	bindingRefs := map[string]bool{}
	if specEnv != nil {
		for _, b := range specEnv.Bindings {
			bindingRefs[b.Ref] = true
		}
	}

	// Layer 2: the derived Secret pods actually mount.
	var secret corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Name: envstore.AppEnvSecretName(liveApp.Name), Namespace: envNs}, &secret)
	switch {
	case err == nil:
		rep.SecretExists = true
	case k8serrors.IsNotFound(err):
	default:
		return rep, fmt.Errorf("read env Secret %s/%s: %w", envNs, envstore.AppEnvSecretName(liveApp.Name), err)
	}

	layers := envLayers{app: map[string]envstore.Env{}, shared: map[string]envstore.Env{}}
	if rep.SecretExists {
		for _, e := range envstore.SecretToEnvs(&secret) {
			layers.app[e.Name] = e
		}
		layers.lastSpec = lastSpecDigests(&secret)
	}

	// The project's shared-env Secret is mounted by the same pods and folded
	// into the rollout hash below, so the report has to see it or its own two
	// layers disagree about what the app's environment contains.
	var sharedSecret corev1.Secret
	switch err := c.Get(ctx, types.NamespacedName{Name: envstore.SharedEnvName, Namespace: envNs}, &sharedSecret); {
	case err == nil:
		for _, e := range envstore.SecretToEnvs(&sharedSecret) {
			layers.shared[e.Name] = e
		}
	case k8serrors.IsNotFound(err):
	default:
		return rep, fmt.Errorf("read shared Secret %s/%s: %w", envNs, envstore.SharedEnvName, err)
	}

	// Layer 1 → layer 2.
	seen := map[string]bool{}
	if specEnv != nil {
		for _, ev := range specEnv.Env {
			seen[ev.Name] = true
			rep.Findings = append(rep.Findings, compareSpecVar(ctx, c, ev, envNs, bindingRefs, layers))
		}
	}

	// spec.sharedVars is seeded into the Secret only when the name is absent,
	// so an edited sharedVar never reaches pods. Reporting that as "derived by
	// the controller (expected)" would file a permanent divergence as normal.
	sharedVarSpec := map[string]string{}
	for _, sv := range specApp.Spec.SharedVars {
		sharedVarSpec[sv.Name] = digestValue(sv.Value)
	}

	// Whatever is left in the Secret was not declared in spec.env.
	for name, e := range layers.app {
		if seen[name] {
			continue
		}
		f := diffFinding{
			Name:         name,
			SecretDigest: digestValue(e.Value),
			Source:       e.Source,
		}
		specDigest, fromSharedVars := sharedVarSpec[name]
		switch {
		case e.Source == "shared" && fromSharedVars && specDigest != f.SecretDigest:
			f.Category = catSharedVarStale
			f.SpecDigest = specDigest
			f.Detail = "spec.sharedVars is seeded into the Secret only when the name is absent and never re-applied, so the Secret keeps the value it was first given"
		case e.Source == "binding", e.Source == "shared", e.Source == "generated":
			f.Category = catDerived
			f.Detail = "written by the controller from " + derivedOrigin(e.Source) + "; not expected in spec.env"
		default:
			f.Category = catNotDeclaredInCRD
			f.Detail = "set through the API/UI, which writes the Secret directly and never the CRD"
		}
		rep.Findings = append(rep.Findings, f)
	}

	// Names that exist only in shared-env. Pods get them; {app}-env does not
	// carry them, so nothing above would have mentioned them.
	for name, e := range layers.shared {
		if seen[name] {
			continue
		}
		if _, inApp := layers.app[name]; inApp {
			continue
		}
		rep.Findings = append(rep.Findings, diffFinding{
			Name:         name,
			Category:     catFromSharedEnv,
			SecretDigest: digestValue(e.Value),
			Source:       envstore.SharedEnvName,
			Detail:       "a project-level shared variable; pods mount shared-env alongside this app's own Secret",
		})
	}

	sort.Slice(rep.Findings, func(i, j int) bool {
		if rep.Findings[i].Category != rep.Findings[j].Category {
			return categoryRank(rep.Findings[i].Category) < categoryRank(rep.Findings[j].Category)
		}
		return rep.Findings[i].Name < rep.Findings[j].Name
	})

	rollout, err := buildRollout(ctx, c, liveApp, project, envName, envNs, autoRedeploy)
	if err != nil {
		return rep, err
	}
	rep.Rollout = rollout
	return rep, nil
}

func derivedOrigin(source string) string {
	switch source {
	case "binding":
		return "this environment's bindings"
	case "shared":
		return "shared variables"
	case "generated":
		return "generated credentials"
	}
	return source
}

func compareSpecVar(
	ctx context.Context,
	c client.Reader,
	ev mortisev1alpha1.EnvVar,
	envNs string,
	bindingRefs map[string]bool,
	layers envLayers,
) diffFinding {
	sv := resolveSpecEnv(ctx, c, ev, envNs, bindingRefs)
	entry, inSecret := layers.app[ev.Name]

	f := diffFinding{Name: ev.Name}
	if inSecret {
		f.SecretDigest = digestValue(entry.Value)
		f.Source = entry.Source
	}

	switch {
	case sv.problem != "":
		f.Category = catUnresolved
		f.Detail = sv.problem
		return f
	case sv.binding:
		// Bindings resolution is not reimplemented here; only presence is
		// checked, so a bound var never shows up as a value mismatch.
		if !inSecret {
			f.Category = catMissingFromSecret
			f.Detail = "declared as valueFrom.fromBinding but not present in the derived Secret"
			return f
		}
		f.Category = catDerived
		f.Detail = "valueFrom.fromBinding; value not compared (bindings are resolved by the controller)"
		return f
	case !inSecret:
		f.Category = catMissingFromSecret
		f.SpecDigest = sv.digest
		f.Detail = "declared in the CRD but absent from the app's derived Secret"
		if _, inShared := layers.shared[ev.Name]; inShared {
			f.Detail += "; pods get the project's shared-env value for this name instead"
		} else {
			f.Detail += "; pods do not get this variable"
		}
		return f
	}

	f.SpecDigest = sv.digest
	if sv.digest == f.SecretDigest {
		f.Category = catInSync
		return f
	}

	// A spec/Secret mismatch is two unrelated states wearing the same face.
	// The controller re-seeds a spec value only while the Secret still tracks
	// the last one it applied; past that it leaves the override in place
	// forever, on purpose. Reporting both as one alarm made the platform's
	// designed behaviour the report's top finding, so classify on the
	// annotation rather than assume.
	lastApplied, tracked := layers.lastSpec[ev.Name]
	switch {
	case !tracked:
		f.Category = catSpecDiffersUntracked
		f.Detail = "the Secret carries no last-spec-env-digest entry for this variable, so an unapplied spec change and a deliberate override are indistinguishable from here"
	case tracksLastAppliedSpec(lastApplied, entry.Value):
		f.Category = catSpecDiffers
		f.Detail = "the Secret still holds the value the CRD last applied, so this spec change has not reached it; the Secret is what pods mount, so the CRD value is not in effect"
	case entry.Source == "user" || entry.Source == "":
		f.Category = catUserOverride
		f.Detail = "the value was set through the API/UI after the CRD last applied its own; the controller keeps an override in place, so the Secret winning here is the intended behaviour"
	default:
		// entry.Source is binding/shared/generated: the API refuses to write a
		// key it doesn't already own (internal/api/env.go), so this was never
		// "set through the API/UI" — that detail would be false. The
		// controller itself is the one overwriting the spec value, every
		// reconcile, forever.
		f.Category = catSpecShadowed
		f.Detail = fmt.Sprintf("this key is owned by the %s source, which the controller rewrites into the Secret on every reconcile; the spec.env value can never take effect while that source claims the name", entry.Source)
	}
	return f
}

// categoryRank orders findings by how much they want a human. The states that
// are normal by design sort below the ones that are not, so a healthy app
// never leads its report with an alarm.
func categoryRank(cat string) int {
	switch cat {
	// catSpecShadowed leads the report: it is the only state where the spec
	// value is not merely stale or pending but permanently unreachable — the
	// controller itself rewrites over it every reconcile, and until CAI-157
	// that was misreported as the benign, do-nothing catUserOverride. Ranking
	// it above catSpecDiffers (which self-heals on the next reconcile) says
	// plainly that this one won't.
	case catSpecShadowed:
		return 0
	case catSpecDiffers:
		return 1
	case catSharedVarStale:
		return 2
	case catMissingFromSecret:
		return 3
	case catUnresolved:
		return 4
	case catSpecDiffersUntracked:
		return 5
	case catNotDeclaredInCRD:
		return 6
	case catUserOverride:
		return 7
	case catFromSharedEnv:
		return 8
	case catDerived:
		return 9
	case catInSync:
		return 10
	}
	return 11
}

// buildRollout is layer 3. It compares the pod template's mortise.dev/env-hash
// against a freshly computed hash of the env Secrets, using the same function
// the controller stamps with.
func buildRollout(
	ctx context.Context,
	c client.Reader,
	app *mortisev1alpha1.App,
	project, envName, envNs string,
	autoRedeploy bool,
) (rolloutReport, error) {
	r := rolloutReport{AutoRedeploy: autoRedeploy}
	r.SecretEnvHash = envstore.EnvHash(ctx, c, app.Name, envNs)

	if app.Spec.Kind == mortisev1alpha1.AppKindCron {
		r.WorkloadKind = "CronJob"
		r.WorkloadName = constants.CronJobName(app.Name)
		var cj batchv1.CronJob
		err := c.Get(ctx, types.NamespacedName{Name: r.WorkloadName, Namespace: envNs}, &cj)
		switch {
		case err == nil:
			r.WorkloadFound = true
			r.WorkloadEnvHash = cj.Spec.JobTemplate.Spec.Template.Annotations[envHashAnnotation]
		case k8serrors.IsNotFound(err):
		default:
			return r, fmt.Errorf("read CronJob %s/%s: %w", envNs, r.WorkloadName, err)
		}
	} else {
		r.WorkloadKind = "Deployment"
		r.WorkloadName = constants.DeploymentName(app.Name)
		var dep appsv1.Deployment
		err := c.Get(ctx, types.NamespacedName{Name: r.WorkloadName, Namespace: envNs}, &dep)
		switch {
		case err == nil:
			r.WorkloadFound = true
			r.WorkloadEnvHash = dep.Spec.Template.Annotations[envHashAnnotation]
		case k8serrors.IsNotFound(err):
		default:
			return r, fmt.Errorf("read Deployment %s/%s: %w", envNs, r.WorkloadName, err)
		}
	}

	if es := findEnvStatus(app, envName); es != nil {
		r.StatusPendingEnvHash = es.PendingEnvHash
		r.StatusDeployedEnvHash = es.DeployedEnvHash
	}

	switch {
	case !r.WorkloadFound:
		r.Detail = "no " + r.WorkloadKind + " in " + envNs + " yet; nothing is running this environment"
	case r.WorkloadEnvHash == r.SecretEnvHash:
		r.InSync = true
		r.Detail = "pods were started with the environment that currently exists"
	default:
		r.Detail = "pods are running an older environment; redeploy to apply the current one"
		if !autoRedeploy {
			r.Detail += " (Project.spec.autoRedeploy is false, so this is expected, not broken)"
		}
	}
	return r, nil
}

// buildCRDChanges compares the live App's spec.env against the App parsed from
// -f, per environment, as effective-value digests. Names and digests only.
func buildCRDChanges(
	ctx context.Context,
	c client.Reader,
	liveApp, fileApp *mortisev1alpha1.App,
	project string,
	envNames []string,
) []crdChange {
	changes := []crdChange{}
	for _, envName := range envNames {
		envNs := constants.EnvNamespace(project, envName)
		live := specDigests(ctx, c, liveApp, envName, envNs)
		file := specDigests(ctx, c, fileApp, envName, envNs)

		names := map[string]bool{}
		for n := range live {
			names[n] = true
		}
		for n := range file {
			names[n] = true
		}
		ordered := make([]string, 0, len(names))
		for n := range names {
			ordered = append(ordered, n)
		}
		sort.Strings(ordered)

		for _, n := range ordered {
			lv, inLive := live[n]
			fv, inFile := file[n]
			switch {
			case !inLive:
				changes = append(changes, crdChange{Environment: envName, Name: n, Change: "add", FileDigest: fv.digest, Detail: specNote(fv)})
			case !inFile:
				changes = append(changes, crdChange{Environment: envName, Name: n, Change: "remove", LiveDigest: lv.digest, Detail: specNote(lv)})
			case lv.digest != fv.digest || lv.binding != fv.binding ||
				lv.ref != fv.ref || lv.key != fv.key || lv.problem != fv.problem:
				changes = append(changes, crdChange{
					Environment: envName, Name: n, Change: "change",
					LiveDigest: lv.digest, FileDigest: fv.digest,
					Detail: changeNote(lv, fv),
				})
			}
		}
	}
	return changes
}

func specNote(sv specValue) string {
	if sv.problem != "" {
		return "unresolved: " + sv.problem
	}
	if sv.binding {
		return "valueFrom.fromBinding " + bindingTarget(sv) + "; value not compared"
	}
	return ""
}

// changeNote describes one "change" row. A binding repoint is the case the
// two digests cannot express — both sides are empty — so it is spelled out.
func changeNote(lv, fv specValue) string {
	if lv.binding && fv.binding {
		return "valueFrom.fromBinding " + bindingTarget(lv) + " -> " + bindingTarget(fv)
	}
	return strings.TrimSpace(specNote(lv) + " " + specNote(fv))
}

func bindingTarget(sv specValue) string {
	if sv.key == "" {
		return "ref " + sv.ref
	}
	return "ref " + sv.ref + " key " + sv.key
}

func specDigests(ctx context.Context, c client.Reader, app *mortisev1alpha1.App, envName, envNs string) map[string]specValue {
	out := map[string]specValue{}
	env := findAppEnv(app, envName)
	if env == nil {
		return out
	}
	bindingRefs := map[string]bool{}
	for _, b := range env.Bindings {
		bindingRefs[b.Ref] = true
	}
	for _, ev := range env.Env {
		out[ev.Name] = resolveSpecEnv(ctx, c, ev, envNs, bindingRefs)
	}
	return out
}

// --- rendering ---

var categoryHeadings = map[string]string{
	catSpecDiffers:          "CRD and Secret disagree",
	catSharedVarStale:       "spec.sharedVars changed, the Secret was not updated",
	catMissingFromSecret:    "declared in the CRD, missing from the Secret",
	catUnresolved:           "declared in the CRD, value not resolvable",
	catSpecDiffersUntracked: "CRD and Secret disagree, cause undetermined",
	catNotDeclaredInCRD:     "in the Secret, not declared in the CRD (normal: set via API/UI)",
	catUserOverride:         "overridden via API/UI (normal: the platform honours overrides)",
	catSpecShadowed:         "spec.env value shadowed by a controller-owned source (spec is never applied)",
	catFromSharedEnv:        "from the project's shared-env Secret (normal)",
	catDerived:              "derived by the controller (expected)",
	catInSync:               "in sync",
}

// renderText writes the human-readable report. Every field it prints is a
// name, a namespace, a source label, or a digest.
func renderText(w io.Writer, rep *diffReport, showInSync bool) {
	fmt.Fprintf(w, "app %s  project %s\n", rep.App, rep.Project)
	fmt.Fprintln(w, "values are never shown; digests are salted per run and compare only within this report")

	if rep.DryRunFile != "" {
		fmt.Fprintf(w, "\ndry run against %s — CRD-level changes an apply would make:\n", rep.DryRunFile)
		if len(rep.CRDChanges) == 0 {
			fmt.Fprintln(w, "  none")
		} else {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "  ENV\tNAME\tCHANGE\tLIVE\tFILE\tNOTE")
			for _, ch := range rep.CRDChanges {
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
					ch.Environment, ch.Name, ch.Change, orDash(ch.LiveDigest), orDash(ch.FileDigest), ch.Detail)
			}
			_ = tw.Flush()
		}
		fmt.Fprintln(w, "\nthe comparison below uses the file's spec as layer 1 against the live cluster:")
	}

	for i := range rep.Environments {
		e := &rep.Environments[i]
		fmt.Fprintf(w, "\nenvironment %s   control ns %s   workload ns %s\n",
			e.Environment, e.ControlNamespace, e.WorkloadNamespace)
		if !e.Enabled {
			fmt.Fprintln(w, "  (this App is opted out of this environment: spec.environments[].enabled=false)")
		}
		if !e.SecretExists {
			fmt.Fprintf(w, "  no %s Secret in %s\n", envstore.AppEnvSecretName(rep.App), e.WorkloadNamespace)
		}

		byCat := map[string][]diffFinding{}
		for _, f := range e.Findings {
			byCat[f.Category] = append(byCat[f.Category], f)
		}
		for _, cat := range []string{
			catSpecShadowed, catSpecDiffers, catSharedVarStale, catMissingFromSecret, catUnresolved,
			catSpecDiffersUntracked, catNotDeclaredInCRD, catUserOverride,
			catFromSharedEnv, catDerived,
		} {
			renderCategory(w, cat, byCat[cat])
		}
		if inSync := byCat[catInSync]; len(inSync) > 0 {
			if showInSync {
				renderCategory(w, catInSync, inSync)
			} else {
				fmt.Fprintf(w, "\n  %s (%d) — use --all to list\n", categoryHeadings[catInSync], len(inSync))
			}
		}

		fmt.Fprintln(w, "\n  rollout")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "    current Secret env-hash\t%s\n", shortHash(e.Rollout.SecretEnvHash))
		fmt.Fprintf(tw, "    %s pod-template env-hash\t%s\n", e.Rollout.WorkloadKind, shortHash(e.Rollout.WorkloadEnvHash))
		fmt.Fprintf(tw, "    App status pending/deployed\t%s / %s\n",
			shortHash(e.Rollout.StatusPendingEnvHash), shortHash(e.Rollout.StatusDeployedEnvHash))
		_ = tw.Flush()
		fmt.Fprintf(w, "    %s\n", e.Rollout.Detail)
	}
}

func renderCategory(w io.Writer, cat string, findings []diffFinding) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(w, "\n  %s (%d)\n", categoryHeadings[cat], len(findings))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "    NAME\tSPEC\tSECRET\tSOURCE\tNOTE")
	for _, f := range findings {
		fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\t%s\n",
			f.Name, orDash(f.SpecDigest), orDash(f.SecretDigest), orDash(f.Source), f.Detail)
	}
	_ = tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
