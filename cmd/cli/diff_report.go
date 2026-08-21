package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// catSpecDiffers: declared in both, different values. The Secret wins —
	// it is what pods mount.
	catSpecDiffers = "spec-differs-from-secret"
	// catMissingFromSecret: declared in the CRD, absent from the derived
	// Secret. Pods do not get this variable at all.
	catMissingFromSecret = "missing-from-secret"
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

// digestValue is the only way a value ever influences output.
func digestValue(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:digestLen]
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
	// problem is non-empty when the effective value could not be determined.
	problem string
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
		return specValue{binding: true}
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

	secretEntries := map[string]envstore.Env{}
	if rep.SecretExists {
		for _, e := range envstore.SecretToEnvs(&secret) {
			secretEntries[e.Name] = e
		}
	}

	// Layer 1 → layer 2.
	seen := map[string]bool{}
	if specEnv != nil {
		for _, ev := range specEnv.Env {
			seen[ev.Name] = true
			rep.Findings = append(rep.Findings, compareSpecVar(ctx, c, ev, envNs, bindingRefs, secretEntries))
		}
	}

	// Whatever is left in the Secret was not declared in spec.env.
	for name, e := range secretEntries {
		if seen[name] {
			continue
		}
		f := diffFinding{
			Name:         name,
			SecretDigest: digestValue(e.Value),
			Source:       e.Source,
		}
		switch e.Source {
		case "binding", "shared", "generated":
			f.Category = catDerived
			f.Detail = "written by the controller from " + derivedOrigin(e.Source) + "; not expected in spec.env"
		default:
			f.Category = catNotDeclaredInCRD
			f.Detail = "set through the API/UI, which writes the Secret directly and never the CRD"
		}
		rep.Findings = append(rep.Findings, f)
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
	secretEntries map[string]envstore.Env,
) diffFinding {
	sv := resolveSpecEnv(ctx, c, ev, envNs, bindingRefs)
	entry, inSecret := secretEntries[ev.Name]

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
		f.Detail = "declared in the CRD but absent from the derived Secret; pods do not get this variable"
		return f
	}

	f.SpecDigest = sv.digest
	if sv.digest == f.SecretDigest {
		f.Category = catInSync
		return f
	}
	f.Category = catSpecDiffers
	f.Detail = "the Secret is what pods mount, so the CRD value is not in effect"
	return f
}

func categoryRank(cat string) int {
	switch cat {
	case catSpecDiffers:
		return 0
	case catMissingFromSecret:
		return 1
	case catUnresolved:
		return 2
	case catNotDeclaredInCRD:
		return 3
	case catDerived:
		return 4
	case catInSync:
		return 5
	}
	return 6
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
			case lv.digest != fv.digest || lv.binding != fv.binding || lv.problem != fv.problem:
				changes = append(changes, crdChange{
					Environment: envName, Name: n, Change: "change",
					LiveDigest: lv.digest, FileDigest: fv.digest,
					Detail: strings.TrimSpace(specNote(lv) + " " + specNote(fv)),
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
		return "valueFrom.fromBinding; value not compared"
	}
	return ""
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
	catSpecDiffers:       "CRD and Secret disagree",
	catMissingFromSecret: "declared in the CRD, missing from the Secret",
	catUnresolved:        "declared in the CRD, value not resolvable",
	catNotDeclaredInCRD:  "in the Secret, not declared in the CRD (normal: set via API/UI)",
	catDerived:           "derived by the controller (expected)",
	catInSync:            "in sync",
}

// renderText writes the human-readable report. Every field it prints is a
// name, a namespace, a source label, or a digest.
func renderText(w io.Writer, rep *diffReport, showInSync bool) {
	fmt.Fprintf(w, "app %s  project %s\n", rep.App, rep.Project)
	fmt.Fprintln(w, "values are never shown; each column is a 12-hex-char sha256 prefix")

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
		for _, cat := range []string{catSpecDiffers, catMissingFromSecret, catUnresolved, catNotDeclaredInCRD, catDerived} {
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
