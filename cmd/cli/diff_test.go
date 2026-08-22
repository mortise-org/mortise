package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/envstore"
)

// Every test in this file builds the same shape: project `myproj` with one
// environment `production`, app `web`. Control ns pj-myproj, workload ns
// pj-myproj-production.
const (
	testProject   = "myproj"
	testApp       = "web"
	testEnv       = "production"
	testControlNs = "pj-myproj"
	testEnvNs     = "pj-myproj-production"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		batchv1.AddToScheme,
		mortisev1alpha1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func testProjectObj(autoRedeploy bool) *mortisev1alpha1.Project {
	return &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: testProject},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: testEnv}},
			AutoRedeploy: autoRedeploy,
		},
	}
}

func testAppObj(env []mortisev1alpha1.EnvVar, bindings []mortisev1alpha1.Binding) *mortisev1alpha1.App {
	return &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: testApp, Namespace: testControlNs},
		Spec: mortisev1alpha1.AppSpec{
			Source:       mortisev1alpha1.AppSource{Type: "image", Image: "nginx:1.27"},
			Environments: []mortisev1alpha1.Environment{{Name: testEnv, Env: env, Bindings: bindings}},
		},
	}
}

// envSecret builds the derived {app}-env Secret. sources maps a var name to an
// envstore source ("binding", "shared", "generated"); anything absent is a
// plain user var.
func envSecret(data map[string]string, sources map[string]string) *corev1.Secret {
	vars := make([]envstore.Env, 0, len(data))
	for k, v := range data {
		vars = append(vars, envstore.Env{Name: k, Value: v, Source: sources[k]})
	}
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: envstore.AppEnvSecretName(testApp), Namespace: testEnvNs},
		Data:       map[string][]byte{},
	}
	annotations := map[string][]string{}
	for _, v := range vars {
		s.Data[v.Name] = []byte(v.Value)
		switch v.Source {
		case "binding":
			annotations[envstore.AnnotationBindingKeys] = append(annotations[envstore.AnnotationBindingKeys], v.Name)
		case "generated":
			annotations[envstore.AnnotationGeneratedKeys] = append(annotations[envstore.AnnotationGeneratedKeys], v.Name)
		case "shared":
			annotations[envstore.AnnotationSharedKeys] = append(annotations[envstore.AnnotationSharedKeys], v.Name)
		}
	}
	if len(annotations) > 0 {
		s.Annotations = map[string]string{}
		for k, names := range annotations {
			s.Annotations[k] = strings.Join(names, ",")
		}
	}
	return s
}

// sharedEnvSecret builds the project's shared-env Secret in the workload
// namespace. Pods mount it alongside {app}-env.
func sharedEnvSecret(data map[string]string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: envstore.SharedEnvName, Namespace: testEnvNs},
		Data:       map[string][]byte{},
	}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

// withLastSpecEnv stamps the annotation the controller writes after applying
// spec env vars: var name -> the value it applied. Whether a spec/Secret
// mismatch is an unapplied spec change or a user override the platform is
// honouring is decided entirely by this annotation, so a test that means one
// of those two states says so here rather than in its assertions.
func withLastSpecEnv(t *testing.T, s *corev1.Secret, lastApplied map[string]string) *corev1.Secret {
	t.Helper()
	digests := make(map[string]string, len(lastApplied))
	for k, v := range lastApplied {
		digests[k] = specEnvDigest(v)
	}
	raw, err := json.Marshal(digests)
	if err != nil {
		t.Fatal(err)
	}
	return annotate(s, envstore.AnnotationLastSpecEnvDigest, string(raw))
}

// withLegacyLastSpecEnv stamps the pre-CAI-168 annotation, which held the
// applied values themselves rather than digests.
func withLegacyLastSpecEnv(t *testing.T, s *corev1.Secret, lastApplied map[string]string) *corev1.Secret {
	t.Helper()
	raw, err := json.Marshal(lastApplied)
	if err != nil {
		t.Fatal(err)
	}
	return annotate(s, envstore.AnnotationLastSpecEnv, string(raw))
}

func annotate(s *corev1.Secret, key, value string) *corev1.Secret {
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[key] = value
	return s
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
}

func runTestDiff(t *testing.T, c client.Reader, req diffRequest) *diffReport {
	t.Helper()
	if req.app == "" {
		req.app = testApp
	}
	if req.project == "" {
		req.project = testProject
	}
	rep, err := runDiff(context.Background(), c, req)
	if err != nil {
		t.Fatalf("runDiff: %v", err)
	}
	return rep
}

// findingFor returns the finding for name in the first environment.
func findingFor(t *testing.T, rep *diffReport, name string) diffFinding {
	t.Helper()
	if len(rep.Environments) == 0 {
		t.Fatal("report has no environments")
	}
	for _, f := range rep.Environments[0].Findings {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no finding for %q; findings: %+v", name, rep.Environments[0].Findings)
	return diffFinding{}
}

// The alarm state: the Secret still holds exactly what the CRD last applied
// (`info`), and the CRD now says something else. Nobody overrode anything —
// the spec change has not reached the Secret pods mount.
func TestDiff_SpecDiffersFromSecret(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}, nil),
		withLastSpecEnv(t, envSecret(map[string]string{"LOG_LEVEL": "info"}, nil),
			map[string]string{"LOG_LEVEL": "info"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "LOG_LEVEL")
	if f.Category != catSpecDiffers {
		t.Errorf("category: got %q, want %q", f.Category, catSpecDiffers)
	}
	if f.SpecDigest != digestValue("debug") {
		t.Errorf("spec digest: got %q, want %q", f.SpecDigest, digestValue("debug"))
	}
	if f.SecretDigest != digestValue("info") {
		t.Errorf("secret digest: got %q, want %q", f.SecretDigest, digestValue("info"))
	}
}

// The designed state, and the one that used to be reported as the top-priority
// alarm: the Secret no longer holds what the CRD applied, because someone set
// the var through the API/UI. `internal/api/env.go` permits that for a plain
// spec literal, and the controller then leaves the override in place forever
// on purpose. Reporting it as a fault means every healthy cluster leads its
// report with a red herring.
func TestDiff_UserOverrideIsNotTheAlarm(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}, nil),
		withLastSpecEnv(t, envSecret(map[string]string{"LOG_LEVEL": "trace"}, nil),
			map[string]string{"LOG_LEVEL": "debug"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "LOG_LEVEL")
	if f.Category != catUserOverride {
		t.Errorf("category: got %q, want %q", f.Category, catUserOverride)
	}
	if f.Category == catSpecDiffers {
		t.Error("an honoured override must not be reported as the CRD-not-in-effect alarm")
	}
	if categoryRank(f.Category) <= categoryRank(catSpecDiffers) {
		t.Errorf("an override must rank below the alarm, got rank %d vs %d",
			categoryRank(f.Category), categoryRank(catSpecDiffers))
	}
	for _, banned := range []string{"error", "invalid", "wrong", "drift"} {
		if strings.Contains(strings.ToLower(f.Detail), banned) {
			t.Errorf("detail %q implies the state is wrong (contains %q)", f.Detail, banned)
		}
	}
}

// The false-positive state CAI-157 fixes: the Secret's entry for this name is
// not a user override at all, it is owned by a binding — e.g. a `database`
// app's binding resolver writing DATABASE_URL (internal/bindings/resolver.go)
// collides with a spec.env literal of the same name. The controller rewrites
// that entry from the binding on every reconcile, so the spec literal is
// permanently dead, not merely superseded by a deliberate override.
// internal/api/env.go refuses to let the API write a controller-owned key, so
// this state could never have been produced "through the API/UI".
func TestDiff_ControllerOwnedSourceShadowsSpecIsNotUserOverride(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "DATABASE_URL", Value: "user-literal"}}, nil),
		withLastSpecEnv(t,
			envSecret(map[string]string{"DATABASE_URL": "bound-value"}, map[string]string{"DATABASE_URL": "binding"}),
			map[string]string{"DATABASE_URL": "previously-applied-literal"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "DATABASE_URL")
	if f.Category == catUserOverride {
		t.Error("a controller-owned Secret entry must not be classified as a user override")
	}
	if f.Category != catSpecShadowed {
		t.Errorf("category: got %q (%s), want %q", f.Category, f.Detail, catSpecShadowed)
	}
	if categoryRank(f.Category) >= categoryRank(catNotDeclaredInCRD) {
		t.Errorf("a permanently-shadowed spec value must rank above the normal-by-design categories, got rank %d vs catNotDeclaredInCRD's %d",
			categoryRank(f.Category), categoryRank(catNotDeclaredInCRD))
	}
	if strings.Contains(f.Detail, "API/UI") {
		t.Errorf("detail must not claim this was set through the API/UI, got %q", f.Detail)
	}
	if !strings.Contains(f.Detail, "binding") {
		t.Errorf("detail should name the owning source, got %q", f.Detail)
	}
}

// With no annotation there is nothing in the cluster that separates an
// unapplied spec change from an override, so the report says exactly that
// instead of picking the scarier of the two.
func TestDiff_UntrackedSpecMismatchIsUndetermined(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}, nil),
		envSecret(map[string]string{"LOG_LEVEL": "info"}, nil),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "LOG_LEVEL")
	if f.Category != catSpecDiffersUntracked {
		t.Errorf("category: got %q, want %q", f.Category, catSpecDiffersUntracked)
	}
	if f.Category == catSpecDiffers || f.Category == catUserOverride {
		t.Error("an undetermined mismatch must not be filed as either decided case")
	}
	if !strings.Contains(f.Detail, "last-spec-env-digest") {
		t.Errorf("detail should name the missing signal, got %q", f.Detail)
	}
}

// Secrets written before CAI-168 carry values, not digests, under the legacy
// annotation. The controller migrates by hashing them; so does the report, or
// every var on an un-reconciled cluster reads as undetermined.
func TestDiff_LegacyLastSpecEnvAnnotationIsUnderstood(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}, nil),
		withLegacyLastSpecEnv(t, envSecret(map[string]string{"LOG_LEVEL": "info"}, nil),
			map[string]string{"LOG_LEVEL": "info"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	if f := findingFor(t, rep, "LOG_LEVEL"); f.Category != catSpecDiffers {
		t.Errorf("category: got %q (%s), want %q", f.Category, f.Detail, catSpecDiffers)
	}
}

func TestDiff_InSync(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}, nil),
		envSecret(map[string]string{"LOG_LEVEL": "info"}, nil),
	)

	rep := runTestDiff(t, c, diffRequest{})
	if f := findingFor(t, rep, "LOG_LEVEL"); f.Category != catInSync {
		t.Errorf("category: got %q, want %q", f.Category, catInSync)
	}
}

func TestDiff_MissingFromSecret(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "NEW_FLAG", Value: "on"}}, nil),
		envSecret(map[string]string{"OTHER": "x"}, nil),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "NEW_FLAG")
	if f.Category != catMissingFromSecret {
		t.Errorf("category: got %q, want %q", f.Category, catMissingFromSecret)
	}
	if f.SecretDigest != "" {
		t.Errorf("secret digest should be empty, got %q", f.SecretDigest)
	}
}

// Pods mount shared-env as well as {app}-env, so "missing from the app's
// Secret" must not claim the container does not get the variable.
func TestDiff_MissingFromAppSecretCreditsSharedEnv(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "REGION", Value: "us-east"}}, nil),
		envSecret(map[string]string{"OTHER": "x"}, nil),
		sharedEnvSecret(map[string]string{"REGION": "eu-west"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "REGION")
	if f.Category != catMissingFromSecret {
		t.Errorf("category: got %q, want %q", f.Category, catMissingFromSecret)
	}
	if strings.Contains(f.Detail, "pods do not get this variable") {
		t.Errorf("detail claims pods get nothing while shared-env supplies the name: %q", f.Detail)
	}
	if !strings.Contains(f.Detail, "shared-env") {
		t.Errorf("detail should say where pods get the value instead, got %q", f.Detail)
	}
}

// A var that lives only in shared-env still reaches the container and is
// folded into the rollout hash, so leaving it out would make the report's two
// layers disagree about what the app's environment contains.
func TestDiff_SharedEnvOnlyVarIsReported(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj(nil, nil),
		envSecret(map[string]string{"OTHER": "x"}, nil),
		sharedEnvSecret(map[string]string{"SENTRY_ENV": "prod"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "SENTRY_ENV")
	if f.Category != catFromSharedEnv {
		t.Errorf("category: got %q, want %q", f.Category, catFromSharedEnv)
	}
	if f.SecretDigest != digestValue("prod") {
		t.Errorf("secret digest: got %q, want %q", f.SecretDigest, digestValue("prod"))
	}
	for _, banned := range []string{"error", "invalid", "wrong", "drift"} {
		if strings.Contains(strings.ToLower(f.Detail), banned) {
			t.Errorf("detail %q implies the state is wrong (contains %q)", f.Detail, banned)
		}
	}
}

// {app}-env wins at mount, so a name in both is already reported from the app
// Secret and must not also appear as a shared-env finding.
func TestDiff_SharedEnvDoesNotDuplicateAppSecretVars(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}, nil),
		envSecret(map[string]string{"LOG_LEVEL": "info"}, nil),
		sharedEnvSecret(map[string]string{"LOG_LEVEL": "debug"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	if n := len(rep.Environments[0].Findings); n != 1 {
		t.Fatalf("expected exactly one finding, got %d: %+v", n, rep.Environments[0].Findings)
	}
	if f := findingFor(t, rep, "LOG_LEVEL"); f.Category != catInSync {
		t.Errorf("category: got %q, want %q", f.Category, catInSync)
	}
}

// spec.sharedVars is seeded into the Secret only when the name is absent, so
// editing one never reaches pods. Filing that under "derived by the controller
// (expected)" would call a permanent divergence normal.
func TestDiff_StaleSharedVarIsReportedNotExpected(t *testing.T) {
	app := testAppObj(nil, nil)
	app.Spec.SharedVars = []mortisev1alpha1.EnvVar{{Name: "TEAM", Value: "platform"}}
	c := newFakeClient(t,
		testProjectObj(false), app,
		envSecret(map[string]string{"TEAM": "infra"}, map[string]string{"TEAM": "shared"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "TEAM")
	if f.Category != catSharedVarStale {
		t.Errorf("category: got %q (%s), want %q", f.Category, f.Detail, catSharedVarStale)
	}
	if f.SpecDigest != digestValue("platform") || f.SecretDigest != digestValue("infra") {
		t.Errorf("both sides should be digested: %+v", f)
	}
}

func TestDiff_MatchingSharedVarStaysDerived(t *testing.T) {
	app := testAppObj(nil, nil)
	app.Spec.SharedVars = []mortisev1alpha1.EnvVar{{Name: "TEAM", Value: "platform"}}
	c := newFakeClient(t,
		testProjectObj(false), app,
		envSecret(map[string]string{"TEAM": "platform"}, map[string]string{"TEAM": "shared"}),
	)

	rep := runTestDiff(t, c, diffRequest{})
	if f := findingFor(t, rep, "TEAM"); f.Category != catDerived {
		t.Errorf("category: got %q, want %q", f.Category, catDerived)
	}
}

// The API writes user vars straight into the derived Secret and never touches
// the CRD, so a Secret-only var is normal, not an error.
func TestDiff_SecretOnlyUserVarIsNotDeclaredInCRD(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj(nil, nil),
		envSecret(map[string]string{"SENTRY_DSN": "x"}, nil),
	)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "SENTRY_DSN")
	if f.Category != catNotDeclaredInCRD {
		t.Errorf("category: got %q, want %q", f.Category, catNotDeclaredInCRD)
	}
	for _, banned := range []string{"error", "invalid", "wrong", "drift"} {
		if strings.Contains(strings.ToLower(f.Detail), banned) {
			t.Errorf("detail %q implies the state is wrong (contains %q)", f.Detail, banned)
		}
	}
}

// Binding- and shared-sourced entries legitimately exist without appearing in
// spec.env. Reporting them as undeclared drift is the mistake that sank an
// earlier attempt at this command.
func TestDiff_BindingAndSharedEntriesAreNotDrift(t *testing.T) {
	c := newFakeClient(t,
		testProjectObj(false),
		testAppObj(nil, nil),
		envSecret(
			map[string]string{"PGHOST": "h", "LOG_LEVEL": "info", "PASSWORD": "p"},
			map[string]string{"PGHOST": "binding", "LOG_LEVEL": "shared", "PASSWORD": "generated"},
		),
	)

	rep := runTestDiff(t, c, diffRequest{})
	for _, name := range []string{"PGHOST", "LOG_LEVEL", "PASSWORD"} {
		f := findingFor(t, rep, name)
		if f.Category != catDerived {
			t.Errorf("%s: category got %q, want %q", name, f.Category, catDerived)
		}
	}
}

func TestDiff_SecretRefResolvesAgainstEnvNamespace(t *testing.T) {
	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-creds", Namespace: testEnvNs},
		Data:       map[string][]byte{"DB_PASSWORD": []byte("hunter2")},
	}
	app := testAppObj([]mortisev1alpha1.EnvVar{{
		Name:      "DB_PASSWORD",
		ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "web-creds"},
	}}, nil)

	c := newFakeClient(t,
		testProjectObj(false), app, creds,
		envSecret(map[string]string{"DB_PASSWORD": "hunter2"}, nil),
	)

	rep := runTestDiff(t, c, diffRequest{})
	if f := findingFor(t, rep, "DB_PASSWORD"); f.Category != catInSync {
		t.Errorf("category: got %q (%s), want %q", f.Category, f.Detail, catInSync)
	}
}

func TestDiff_SecretRefMissingKeyIsUnresolved(t *testing.T) {
	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-creds", Namespace: testEnvNs},
		Data:       map[string][]byte{"pw": []byte("hunter2")},
	}
	app := testAppObj([]mortisev1alpha1.EnvVar{{
		Name:      "DB_PASSWORD",
		ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "web-creds"},
	}}, nil)

	c := newFakeClient(t, testProjectObj(false), app, creds)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "DB_PASSWORD")
	if f.Category != catUnresolved {
		t.Errorf("category: got %q, want %q", f.Category, catUnresolved)
	}
	if !strings.Contains(f.Detail, "has no key") {
		t.Errorf("detail should name the missing key, got %q", f.Detail)
	}
}

func TestDiff_SecretRefMissingSecretIsUnresolved(t *testing.T) {
	app := testAppObj([]mortisev1alpha1.EnvVar{{
		Name:      "DB_PASSWORD",
		ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "nope"},
	}}, nil)
	c := newFakeClient(t, testProjectObj(false), app)

	rep := runTestDiff(t, c, diffRequest{})
	if f := findingFor(t, rep, "DB_PASSWORD"); f.Category != catUnresolved {
		t.Errorf("category: got %q, want %q", f.Category, catUnresolved)
	}
}

// A fromBinding var declared without the matching bindings[] entry is exactly
// what the controller rejects; the diff reports it rather than guessing.
func TestDiff_FromBindingWithoutBindingIsUnresolved(t *testing.T) {
	app := testAppObj([]mortisev1alpha1.EnvVar{{
		Name: "PGHOST",
		ValueFrom: &mortisev1alpha1.EnvVarSource{
			FromBinding: &mortisev1alpha1.BindingVarSource{Ref: "db", Key: "host"},
		},
	}}, nil)
	c := newFakeClient(t, testProjectObj(false), app)

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "PGHOST")
	if f.Category != catUnresolved {
		t.Errorf("category: got %q, want %q", f.Category, catUnresolved)
	}
	if !strings.Contains(f.Detail, "bindings list") {
		t.Errorf("detail should explain the missing bindings entry, got %q", f.Detail)
	}
}

// A properly-declared fromBinding var is resolved by the controller, not by
// diff, so its value is never compared — presence only.
func TestDiff_FromBindingWithBindingIsDerived(t *testing.T) {
	app := testAppObj(
		[]mortisev1alpha1.EnvVar{{
			Name: "PGHOST",
			ValueFrom: &mortisev1alpha1.EnvVarSource{
				FromBinding: &mortisev1alpha1.BindingVarSource{Ref: "db", Key: "host"},
			},
		}},
		[]mortisev1alpha1.Binding{{Ref: "db"}},
	)
	c := newFakeClient(t, testProjectObj(false), app,
		envSecret(map[string]string{"PGHOST": "db.internal"}, map[string]string{"PGHOST": "binding"}))

	rep := runTestDiff(t, c, diffRequest{})
	f := findingFor(t, rep, "PGHOST")
	if f.Category != catDerived {
		t.Errorf("category: got %q (%s), want %q", f.Category, f.Detail, catDerived)
	}
	if f.SpecDigest != "" {
		t.Errorf("a bound var must not carry a spec digest, got %q", f.SpecDigest)
	}
}

// --- layer 3 ---

func deploymentWithEnvHash(hash string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: testApp, Namespace: testEnvNs},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{envHashAnnotation: hash},
				},
			},
		},
	}
}

func TestDiff_RolloutInSyncWhenHashesMatch(t *testing.T) {
	secret := envSecret(map[string]string{"LOG_LEVEL": "info"}, nil)
	live := envstore.HashData(secret.Data)

	c := newFakeClient(t, testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}, nil),
		secret, deploymentWithEnvHash(live))

	rep := runTestDiff(t, c, diffRequest{})
	r := rep.Environments[0].Rollout
	if !r.InSync {
		t.Errorf("expected rollout in sync, got %+v", r)
	}
	if r.SecretEnvHash != live {
		t.Errorf("secret env hash: got %q, want %q", r.SecretEnvHash, live)
	}
}

func TestDiff_RolloutStaleWhenHashesDiffer(t *testing.T) {
	secret := envSecret(map[string]string{"LOG_LEVEL": "info"}, nil)
	c := newFakeClient(t, testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}, nil),
		secret, deploymentWithEnvHash("0000000000000000"))

	rep := runTestDiff(t, c, diffRequest{})
	r := rep.Environments[0].Rollout
	if r.InSync {
		t.Errorf("expected rollout out of sync, got %+v", r)
	}
	if !strings.Contains(r.Detail, "redeploy") {
		t.Errorf("detail should say a redeploy is needed, got %q", r.Detail)
	}
	// autoRedeploy defaults to false, so this state is expected rather than
	// broken and the output must say so.
	if !strings.Contains(r.Detail, "autoRedeploy") {
		t.Errorf("detail should mention autoRedeploy, got %q", r.Detail)
	}
}

func TestDiff_RolloutOmitsAutoRedeployNoteWhenEnabled(t *testing.T) {
	secret := envSecret(map[string]string{"LOG_LEVEL": "info"}, nil)
	c := newFakeClient(t, testProjectObj(true),
		testAppObj(nil, nil), secret, deploymentWithEnvHash("0000000000000000"))

	rep := runTestDiff(t, c, diffRequest{})
	if strings.Contains(rep.Environments[0].Rollout.Detail, "autoRedeploy") {
		t.Errorf("autoRedeploy=true should not carry the 'expected, not broken' note: %q",
			rep.Environments[0].Rollout.Detail)
	}
}

func TestDiff_RolloutReportsStatusHashes(t *testing.T) {
	app := testAppObj(nil, nil)
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{{
		Name:            testEnv,
		PendingEnvHash:  "aaaabbbbccccdddd",
		DeployedEnvHash: "1111222233334444",
	}}
	c := newFakeClient(t, testProjectObj(false), app)

	rep := runTestDiff(t, c, diffRequest{})
	r := rep.Environments[0].Rollout
	if r.StatusPendingEnvHash != "aaaabbbbccccdddd" || r.StatusDeployedEnvHash != "1111222233334444" {
		t.Errorf("status hashes not reported: %+v", r)
	}
}

func TestDiff_RolloutReportsMissingWorkload(t *testing.T) {
	c := newFakeClient(t, testProjectObj(false), testAppObj(nil, nil))

	rep := runTestDiff(t, c, diffRequest{})
	r := rep.Environments[0].Rollout
	if r.WorkloadFound {
		t.Error("expected no workload")
	}
	if r.WorkloadKind != "Deployment" {
		t.Errorf("workload kind: got %q, want Deployment", r.WorkloadKind)
	}
}

func TestDiff_CronAppReadsCronJobEnvHash(t *testing.T) {
	app := testAppObj(nil, nil)
	app.Spec.Kind = mortisev1alpha1.AppKindCron
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: testApp, Namespace: testEnvNs},
		Spec: batchv1.CronJobSpec{
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{envHashAnnotation: "deadbeefdeadbeef"},
						},
					},
				},
			},
		},
	}
	c := newFakeClient(t, testProjectObj(false), app, cj)

	rep := runTestDiff(t, c, diffRequest{})
	r := rep.Environments[0].Rollout
	if r.WorkloadKind != "CronJob" || !r.WorkloadFound {
		t.Fatalf("expected a found CronJob, got %+v", r)
	}
	if r.WorkloadEnvHash != "deadbeefdeadbeef" {
		t.Errorf("workload env hash: got %q", r.WorkloadEnvHash)
	}
}

// --- the names-and-digests guarantee ---

// TestDiff_NeverPrintsAValue is the load-bearing test for this command: the
// output is meant to be safe to paste into a channel during an incident.
func TestDiff_NeverPrintsAValue(t *testing.T) {
	// Every value below is a distinctive sentinel that must not appear in any
	// rendering of the report.
	values := []string{
		"sentinelLiteralValue1",
		"sentinelSecretValue2",
		"sentinelUserSetValue3",
		"sentinelBindingValue4",
		"sentinelSharedValue5",
		"sentinelCredsValue6",
		"sentinelFileValue7",
		"sentinelLegacyValue8",
		"sentinelSharedEnvValue9",
	}

	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-creds", Namespace: testEnvNs},
		Data:       map[string][]byte{"DB_PASSWORD": []byte("sentinelCredsValue6")},
	}
	app := testAppObj([]mortisev1alpha1.EnvVar{
		{Name: "LOG_LEVEL", Value: "sentinelLiteralValue1"},
		{Name: "DB_PASSWORD", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "web-creds"}},
	}, nil)
	// The legacy last-spec-env annotation holds plaintext values, and the
	// shared-env Secret is a second source of values the report now reads.
	// Both are digested on the way in or not printed at all.
	secret := withLegacyLastSpecEnv(t, envSecret(
		map[string]string{
			"LOG_LEVEL":   "sentinelSecretValue2",
			"UI_SET":      "sentinelUserSetValue3",
			"PGHOST":      "sentinelBindingValue4",
			"SHARED_FLAG": "sentinelSharedValue5",
		},
		map[string]string{"PGHOST": "binding", "SHARED_FLAG": "shared"},
	), map[string]string{"LOG_LEVEL": "sentinelLegacyValue8"})
	shared := sharedEnvSecret(map[string]string{"PROJECT_TIER": "sentinelSharedEnvValue9"})

	dir := t.TempDir()
	manifest := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(manifest, []byte(`apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: web
spec:
  source:
    type: image
    image: nginx:1.27
  environments:
    - name: production
      env:
        - name: LOG_LEVEL
          value: sentinelFileValue7
`), 0o600); err != nil {
		t.Fatal(err)
	}

	c := newFakeClient(t, testProjectObj(false), app, creds, secret, shared,
		deploymentWithEnvHash("0000000000000000"))

	for _, req := range []diffRequest{
		{app: testApp, project: testProject},
		{app: testApp, project: testProject, file: manifest},
	} {
		rep, err := runDiff(context.Background(), c, req)
		if err != nil {
			t.Fatalf("runDiff(%+v): %v", req, err)
		}
		for _, showAll := range []bool{false, true} {
			for _, format := range []string{"text", "json"} {
				var buf bytes.Buffer
				if err := writeDiff(&buf, rep, format, showAll); err != nil {
					t.Fatal(err)
				}
				out := buf.String()
				for _, v := range values {
					if strings.Contains(out, v) {
						t.Errorf("format=%s all=%v file=%q leaked value %q:\n%s",
							format, showAll, req.file, v, out)
					}
				}
			}
		}
	}
}

// The report is meant to survive being pasted into a channel. An unsalted
// sha256 of a low-entropy value (`true`, `production`, an account id) does
// not: anyone holding the report can confirm a guess offline.
func TestDiff_DigestsAreSaltedNotPlainSHA256(t *testing.T) {
	for _, v := range []string{"true", "production", "info"} {
		if digestValue(v) == specEnvDigest(v)[:digestLen] {
			t.Errorf("digest of %q is a plain sha256 prefix; a guess can be confirmed offline", v)
		}
	}
	first, second := newDigestSalt(), newDigestSalt()
	if string(first) == string(second) {
		t.Error("the salt must be random per invocation")
	}
	// Within one report equal values must still produce equal digests, which
	// is the only property the output promises.
	a, b := digestValue("same"), digestValue("same")
	if a != b {
		t.Error("digests must be stable within one invocation")
	}
}

func TestDiff_TextOutputCarriesNamesAndDigests(t *testing.T) {
	c := newFakeClient(t, testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}, nil),
		withLastSpecEnv(t, envSecret(map[string]string{"LOG_LEVEL": "info"}, nil),
			map[string]string{"LOG_LEVEL": "info"}))

	rep := runTestDiff(t, c, diffRequest{})
	var buf bytes.Buffer
	if err := writeDiff(&buf, rep, "text", false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"LOG_LEVEL", digestValue("debug"), digestValue("info"), testEnvNs, testControlNs} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDiff_JSONOutputIsStructured(t *testing.T) {
	c := newFakeClient(t, testProjectObj(false),
		testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}, nil),
		withLastSpecEnv(t, envSecret(map[string]string{"LOG_LEVEL": "info"}, nil),
			map[string]string{"LOG_LEVEL": "info"}))

	rep := runTestDiff(t, c, diffRequest{})
	var buf bytes.Buffer
	if err := writeDiff(&buf, rep, "json", false); err != nil {
		t.Fatal(err)
	}
	var decoded diffReport
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if decoded.App != testApp || decoded.Project != testProject {
		t.Errorf("unexpected header: %+v", decoded)
	}
	if len(decoded.Environments) != 1 || len(decoded.Environments[0].Findings) != 1 {
		t.Fatalf("unexpected environments: %+v", decoded.Environments)
	}
	if decoded.Environments[0].Findings[0].Category != catSpecDiffers {
		t.Errorf("category: %+v", decoded.Environments[0].Findings[0])
	}
}

func TestDiff_UnknownOutputFormatIsRejected(t *testing.T) {
	if err := writeDiff(&bytes.Buffer{}, &diffReport{}, "yaml", false); err == nil {
		t.Fatal("expected an error for an unknown output format")
	}
}

// --- operational errors ---

func TestDiff_MissingAppIsAnError(t *testing.T) {
	c := newFakeClient(t, testProjectObj(false))
	_, err := runDiff(context.Background(), c, diffRequest{app: "nope", project: testProject})
	if err == nil {
		t.Fatal("expected an error for a missing app")
	}
	if !strings.Contains(err.Error(), testControlNs) {
		t.Errorf("error should name the control namespace, got %v", err)
	}
}

func TestDiff_MissingProjectIsAnError(t *testing.T) {
	c := newFakeClient(t)
	_, err := runDiff(context.Background(), c, diffRequest{app: testApp, project: "gone"})
	if err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("expected a project-not-found error, got %v", err)
	}
}

// runDiff takes a client.Reader, so it cannot write by construction. This
// pins the observable half of that: nothing in the cluster changes.
func TestDiff_DoesNotWriteToTheCluster(t *testing.T) {
	app := testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}, nil)
	secret := envSecret(map[string]string{"LOG_LEVEL": "info"}, nil)
	c := newFakeClient(t, testProjectObj(false), app, secret, deploymentWithEnvHash("abc"))

	before := resourceVersions(t, c)
	runTestDiff(t, c, diffRequest{})
	after := resourceVersions(t, c)

	if len(before) != len(after) {
		t.Fatalf("object count changed: %v -> %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed resourceVersion %s -> %s", k, v, after[k])
		}
	}
}

func resourceVersions(t *testing.T, c client.Client) map[string]string {
	t.Helper()
	out := map[string]string{}
	ctx := context.Background()

	var apps mortisev1alpha1.AppList
	if err := c.List(ctx, &apps); err != nil {
		t.Fatal(err)
	}
	for i := range apps.Items {
		out["App/"+apps.Items[i].Namespace+"/"+apps.Items[i].Name] = apps.Items[i].ResourceVersion
	}
	var secrets corev1.SecretList
	if err := c.List(ctx, &secrets); err != nil {
		t.Fatal(err)
	}
	for i := range secrets.Items {
		out["Secret/"+secrets.Items[i].Namespace+"/"+secrets.Items[i].Name] = secrets.Items[i].ResourceVersion
	}
	var deps appsv1.DeploymentList
	if err := c.List(ctx, &deps); err != nil {
		t.Fatal(err)
	}
	for i := range deps.Items {
		out["Deployment/"+deps.Items[i].Namespace+"/"+deps.Items[i].Name] = deps.Items[i].ResourceVersion
	}
	return out
}

// --- environment selection ---

func TestDiff_ReportsEveryProjectEnvironment(t *testing.T) {
	proj := testProjectObj(false)
	proj.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "production"}, {Name: "staging"}}
	c := newFakeClient(t, proj, testAppObj(nil, nil))

	rep := runTestDiff(t, c, diffRequest{})
	if len(rep.Environments) != 2 {
		t.Fatalf("expected 2 environments, got %d: %+v", len(rep.Environments), rep.Environments)
	}
	if rep.Environments[1].WorkloadNamespace != "pj-myproj-staging" {
		t.Errorf("workload namespace: got %q", rep.Environments[1].WorkloadNamespace)
	}
}

func TestDiff_EnvFlagNarrowsToOneEnvironment(t *testing.T) {
	proj := testProjectObj(false)
	proj.Spec.Environments = []mortisev1alpha1.ProjectEnvironment{{Name: "production"}, {Name: "staging"}}
	c := newFakeClient(t, proj, testAppObj(nil, nil))

	rep := runTestDiff(t, c, diffRequest{env: "staging"})
	if len(rep.Environments) != 1 || rep.Environments[0].Environment != "staging" {
		t.Fatalf("expected only staging, got %+v", rep.Environments)
	}
}

func TestDiff_OptedOutEnvironmentIsFlagged(t *testing.T) {
	no := false
	app := testAppObj(nil, nil)
	app.Spec.Environments[0].Enabled = &no
	c := newFakeClient(t, testProjectObj(false), app)

	rep := runTestDiff(t, c, diffRequest{})
	if rep.Environments[0].Enabled {
		t.Error("expected the environment to be flagged as opted out")
	}
}

// --- dry run ---

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDryRun_ReportsAddChangeRemove(t *testing.T) {
	app := testAppObj([]mortisev1alpha1.EnvVar{
		{Name: "KEEP", Value: "same"},
		{Name: "CHANGED", Value: "old"},
		{Name: "REMOVED", Value: "bye"},
	}, nil)
	c := newFakeClient(t, testProjectObj(false), app)

	manifest := writeManifest(t, `apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: web
spec:
  source:
    type: image
    image: nginx:1.27
  environments:
    - name: production
      env:
        - name: KEEP
          value: same
        - name: CHANGED
          value: new
        - name: ADDED
          value: hello
`)

	rep := runTestDiff(t, c, diffRequest{file: manifest})
	got := map[string]crdChange{}
	for _, ch := range rep.CRDChanges {
		got[ch.Name] = ch
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 changes, got %+v", rep.CRDChanges)
	}
	if got["ADDED"].Change != "add" || got["ADDED"].FileDigest != digestValue("hello") {
		t.Errorf("ADDED: %+v", got["ADDED"])
	}
	if got["REMOVED"].Change != "remove" || got["REMOVED"].LiveDigest != digestValue("bye") {
		t.Errorf("REMOVED: %+v", got["REMOVED"])
	}
	ch := got["CHANGED"]
	if ch.Change != "change" || ch.LiveDigest != digestValue("old") || ch.FileDigest != digestValue("new") {
		t.Errorf("CHANGED: %+v", ch)
	}
	if _, ok := got["KEEP"]; ok {
		t.Error("an unchanged var must not be reported as a change")
	}
}

// A bound var carries no digest to compare, so without the ref on the
// comparison a manifest that repoints DB_URL at a different backing service
// produced no change at all and the dry run printed "none".
func TestDryRun_BindingRepointIsAChange(t *testing.T) {
	app := testAppObj(
		[]mortisev1alpha1.EnvVar{{
			Name: "DB_URL",
			ValueFrom: &mortisev1alpha1.EnvVarSource{
				FromBinding: &mortisev1alpha1.BindingVarSource{Ref: "db-a", Key: "url"},
			},
		}},
		[]mortisev1alpha1.Binding{{Ref: "db-a"}, {Ref: "db-b"}},
	)
	c := newFakeClient(t, testProjectObj(false), app)

	manifest := writeManifest(t, `apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: web
spec:
  source:
    type: image
    image: nginx:1.27
  environments:
    - name: production
      bindings:
        - ref: db-a
        - ref: db-b
      env:
        - name: DB_URL
          valueFrom:
            fromBinding:
              ref: db-b
              key: url
`)

	rep := runTestDiff(t, c, diffRequest{file: manifest})
	if len(rep.CRDChanges) != 1 {
		t.Fatalf("expected the repoint to be reported, got %+v", rep.CRDChanges)
	}
	ch := rep.CRDChanges[0]
	if ch.Name != "DB_URL" || ch.Change != "change" {
		t.Errorf("unexpected change: %+v", ch)
	}
	if !strings.Contains(ch.Detail, "db-a") || !strings.Contains(ch.Detail, "db-b") {
		t.Errorf("detail should name both bindings, got %q", ch.Detail)
	}
}

// Repointing only the key inside the same binding is a change too.
func TestDryRun_BindingKeyChangeIsAChange(t *testing.T) {
	app := testAppObj(
		[]mortisev1alpha1.EnvVar{{
			Name: "DB_URL",
			ValueFrom: &mortisev1alpha1.EnvVarSource{
				FromBinding: &mortisev1alpha1.BindingVarSource{Ref: "db", Key: "url"},
			},
		}},
		[]mortisev1alpha1.Binding{{Ref: "db"}},
	)
	c := newFakeClient(t, testProjectObj(false), app)

	manifest := writeManifest(t, `apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: web
spec:
  source:
    type: image
    image: nginx:1.27
  environments:
    - name: production
      bindings:
        - ref: db
      env:
        - name: DB_URL
          valueFrom:
            fromBinding:
              ref: db
              key: readonly_url
`)

	rep := runTestDiff(t, c, diffRequest{file: manifest})
	if len(rep.CRDChanges) != 1 {
		t.Fatalf("expected the key change to be reported, got %+v", rep.CRDChanges)
	}
	if !strings.Contains(rep.CRDChanges[0].Detail, "readonly_url") {
		t.Errorf("detail should name the new key, got %q", rep.CRDChanges[0].Detail)
	}
}

// An unchanged bound var is still not a change.
func TestDryRun_UnchangedBindingIsNotAChange(t *testing.T) {
	env := []mortisev1alpha1.EnvVar{{
		Name: "DB_URL",
		ValueFrom: &mortisev1alpha1.EnvVarSource{
			FromBinding: &mortisev1alpha1.BindingVarSource{Ref: "db", Key: "url"},
		},
	}}
	c := newFakeClient(t, testProjectObj(false),
		testAppObj(env, []mortisev1alpha1.Binding{{Ref: "db"}}))

	manifest := writeManifest(t, `apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: web
spec:
  source:
    type: image
    image: nginx:1.27
  environments:
    - name: production
      bindings:
        - ref: db
      env:
        - name: DB_URL
          valueFrom:
            fromBinding:
              ref: db
              key: url
`)

	if rep := runTestDiff(t, c, diffRequest{file: manifest}); len(rep.CRDChanges) != 0 {
		t.Errorf("expected no changes, got %+v", rep.CRDChanges)
	}
}

// In dry-run mode the file's spec becomes layer 1, so the layer 1 -> 2
// comparison answers "what would the pods disagree with after this apply".
func TestDryRun_UsesTheFileSpecAsLayerOne(t *testing.T) {
	app := testAppObj([]mortisev1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}, nil)
	// The Secret holds exactly what the live CRD applied, so the file's `debug`
	// is an unapplied spec change rather than an override of one.
	c := newFakeClient(t, testProjectObj(false), app,
		withLastSpecEnv(t, envSecret(map[string]string{"LOG_LEVEL": "info"}, nil),
			map[string]string{"LOG_LEVEL": "info"}))

	manifest := writeManifest(t, `apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: web
spec:
  source:
    type: image
    image: nginx:1.27
  environments:
    - name: production
      env:
        - name: LOG_LEVEL
          value: debug
`)

	// Without the file the live spec matches the Secret exactly.
	if f := findingFor(t, runTestDiff(t, c, diffRequest{}), "LOG_LEVEL"); f.Category != catInSync {
		t.Fatalf("precondition: expected in-sync, got %q", f.Category)
	}

	rep := runTestDiff(t, c, diffRequest{file: manifest})
	if f := findingFor(t, rep, "LOG_LEVEL"); f.Category != catSpecDiffers {
		t.Errorf("category: got %q, want %q", f.Category, catSpecDiffers)
	}
	if rep.DryRunFile != manifest {
		t.Errorf("report should record the manifest path, got %q", rep.DryRunFile)
	}
}

func TestDryRun_RejectsAMismatchedAppName(t *testing.T) {
	c := newFakeClient(t, testProjectObj(false), testAppObj(nil, nil))
	manifest := writeManifest(t, `apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: other
spec:
  source:
    type: image
    image: nginx:1.27
`)
	_, err := runDiff(context.Background(), c, diffRequest{app: testApp, project: testProject, file: manifest})
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Fatalf("expected a name mismatch error, got %v", err)
	}
}

func TestDryRun_RejectsAMismatchedNamespace(t *testing.T) {
	c := newFakeClient(t, testProjectObj(false), testAppObj(nil, nil))
	manifest := writeManifest(t, `apiVersion: mortise.dev/v1alpha1
kind: App
metadata:
  name: web
  namespace: pj-someone-else
spec:
  source:
    type: image
    image: nginx:1.27
`)
	_, err := runDiff(context.Background(), c, diffRequest{app: testApp, project: testProject, file: manifest})
	if err == nil || !strings.Contains(err.Error(), "pj-someone-else") {
		t.Fatalf("expected a namespace mismatch error, got %v", err)
	}
}

func TestDryRun_RejectsANonAppManifest(t *testing.T) {
	c := newFakeClient(t, testProjectObj(false), testAppObj(nil, nil))
	manifest := writeManifest(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: web
`)
	_, err := runDiff(context.Background(), c, diffRequest{app: testApp, project: testProject, file: manifest})
	if err == nil || !strings.Contains(err.Error(), "ConfigMap") {
		t.Fatalf("expected a kind error, got %v", err)
	}
}

func TestDryRun_MissingFileIsAnError(t *testing.T) {
	c := newFakeClient(t, testProjectObj(false), testAppObj(nil, nil))
	_, err := runDiff(context.Background(), c, diffRequest{
		app: testApp, project: testProject, file: filepath.Join(t.TempDir(), "absent.yaml"),
	})
	if err == nil {
		t.Fatal("expected an error for a missing manifest")
	}
}

// --- wiring ---

func TestDiffCmd_IsRegistered(t *testing.T) {
	for _, c := range newRootCmd().Commands() {
		if c.Name() == "diff" {
			if !strings.HasPrefix(c.Use, "diff <app>") {
				t.Errorf("unexpected use string: %q", c.Use)
			}
			return
		}
	}
	t.Fatal("diff is not registered on the root command")
}

func TestDiffCmd_HasTheDocumentedFlags(t *testing.T) {
	cmd := newDiffCmd()
	for _, name := range []string{"project", "env", "output", "file", "all", "kubeconfig", "context"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
}
