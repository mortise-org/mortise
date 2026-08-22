package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

// diffScheme carries only the kinds `mortise diff` reads.
func diffScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		batchv1.AddToScheme,
		mortisev1alpha1.AddToScheme,
	} {
		if err := add(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// newKubeClient resolves a cluster connection exactly as kubectl does:
// KUBECONFIG / ~/.kube/config / in-cluster, with --kubeconfig and --context
// overriding. `mortise diff` deliberately does not go through the Mortise
// HTTP API — it reads the cluster directly, read-only.
func newKubeClient(kubeconfig, kubecontext string) (client.Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("resolving kubeconfig: %w", err)
	}
	scheme, err := diffScheme()
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

func newDiffCmd() *cobra.Command {
	var project, envName, output, file, kubeconfig, kubecontext string
	var showAll bool

	cmd := &cobra.Command{
		Use:   "diff <app>",
		Short: "Show where an app's environment configuration disagrees with itself",
		Long: `diff compares an App's environment configuration across three layers:

  1. the CRD spec        spec.environments[].env on the App (namespace pj-{project})
  2. the derived Secret  {app}-env in pj-{project}-{env}, which pods mount via envFrom
  3. the running pods    the mortise.dev/env-hash the workload was started with

Only names, sources, and 12-hex-char digests are printed. No variable value
ever reaches the output, so the result is safe to paste into a channel during
an incident. The digests are salted per invocation — they answer "are these
two the same value" within one report, and do not compare across runs.

diff talks to Kubernetes directly using the standard kubeconfig resolution,
not the Mortise HTTP API, and never writes to the cluster.

With -f it becomes a dry run: it reports what applying that App manifest would
change, relative to what is live, in the same names-and-digests form.

Findings are information, not failures: the exit status is non-zero only when
the command could not do its job (no cluster, no such app).`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			p := project
			if p == "" {
				p = cfg.Project()
			}
			c, err := newKubeClient(kubeconfig, kubecontext)
			if err != nil {
				return err
			}
			rep, err := runDiff(cmd.Context(), c, diffRequest{
				app:     args[0],
				project: p,
				env:     envName,
				file:    file,
			})
			if err != nil {
				return err
			}
			return writeDiff(cmd.OutOrStdout(), rep, output, showAll)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&envName, "env", "", "Environment name (default: every environment on the project)")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	cmd.Flags().StringVarP(&file, "file", "f", "", "Dry run: an App manifest to compare against what is live")
	cmd.Flags().BoolVar(&showAll, "all", false, "List in-sync variables too")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file")
	cmd.Flags().StringVar(&kubecontext, "context", "", "Kubeconfig context to use")
	return cmd
}

func writeDiff(w io.Writer, rep *diffReport, output string, showAll bool) error {
	switch output {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case "text", "":
		renderText(w, rep, showAll)
		return nil
	default:
		return fmt.Errorf("unknown output format %q: want text or json", output)
	}
}

type diffRequest struct {
	app     string
	project string
	env     string
	file    string
}

// runDiff performs every cluster read and assembles the report. Returns an
// error only for operational failures — drift findings live in the report.
func runDiff(ctx context.Context, c client.Reader, req diffRequest) (*diffReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	var proj mortisev1alpha1.Project
	if err := c.Get(ctx, client.ObjectKey{Name: req.project}, &proj); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("project %q not found", req.project)
		}
		return nil, fmt.Errorf("read Project %q: %w", req.project, err)
	}

	controlNs := constants.ControlNamespace(req.project)
	var liveApp mortisev1alpha1.App
	if err := c.Get(ctx, client.ObjectKey{Name: req.app, Namespace: controlNs}, &liveApp); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("app %q not found in project %q (namespace %s)", req.app, req.project, controlNs)
		}
		return nil, fmt.Errorf("read App %s/%s: %w", controlNs, req.app, err)
	}

	specApp := &liveApp
	if req.file != "" {
		fileApp, err := loadAppManifest(req.file)
		if err != nil {
			return nil, err
		}
		if fileApp.Name != req.app {
			return nil, fmt.Errorf("%s declares App %q, but the command names %q", req.file, fileApp.Name, req.app)
		}
		if fileApp.Namespace != "" && fileApp.Namespace != controlNs {
			return nil, fmt.Errorf("%s targets namespace %q, but project %q uses %q", req.file, fileApp.Namespace, req.project, controlNs)
		}
		specApp = fileApp
	}

	envNames := resolveEnvNames(&proj, specApp, &liveApp, req.env)
	if len(envNames) == 0 {
		return nil, fmt.Errorf("project %q declares no environments", req.project)
	}

	rep := &diffReport{App: req.app, Project: req.project, Environments: []envDiffReport{}}
	if req.file != "" {
		rep.DryRunFile = req.file
		rep.CRDChanges = buildCRDChanges(ctx, c, &liveApp, specApp, req.project, envNames)
	}
	for _, envName := range envNames {
		er, err := buildEnvDiff(ctx, c, specApp, &liveApp, req.project, envName, proj.Spec.AutoRedeploy)
		if err != nil {
			return nil, err
		}
		rep.Environments = append(rep.Environments, er)
	}
	return rep, nil
}

// resolveEnvNames returns the environments to report on. The Project is the
// authority — Apps auto-participate in every project environment — with the
// App's own entries folded in so an App naming an env the Project has dropped
// still shows up.
func resolveEnvNames(proj *mortisev1alpha1.Project, specApp, liveApp *mortisev1alpha1.App, only string) []string {
	if only != "" {
		return []string{only}
	}
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		names = append(names, n)
	}
	for _, e := range proj.Spec.Environments {
		add(e.Name)
	}
	extra := []string{}
	for _, app := range []*mortisev1alpha1.App{specApp, liveApp} {
		for _, e := range app.Spec.Environments {
			if !seen[e.Name] {
				extra = append(extra, e.Name)
			}
		}
	}
	sort.Strings(extra)
	for _, n := range extra {
		add(n)
	}
	if len(names) == 0 {
		add(constants.DefaultProjectEnvironment)
	}
	return names
}

// loadAppManifest reads a single App manifest for -f. Anything that is not a
// Mortise App is rejected rather than silently ignored.
func loadAppManifest(path string) (*mortisev1alpha1.App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var app mortisev1alpha1.App
	if err := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096).Decode(&app); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if app.Kind != "" && app.Kind != "App" {
		return nil, fmt.Errorf("%s contains kind %q, want App", path, app.Kind)
	}
	if app.Name == "" {
		return nil, fmt.Errorf("%s has no metadata.name", path)
	}
	return &app, nil
}
