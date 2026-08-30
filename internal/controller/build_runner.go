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
	"os"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/git"
)

// toContextMode maps the CRD-level BuildContext to the build package's
// ContextMode. Unset in the CRD means auto-detect.
func toContextMode(c mortisev1alpha1.BuildContext) build.ContextMode {
	switch c {
	case mortisev1alpha1.BuildContextRoot:
		return build.ContextModeRoot
	case mortisev1alpha1.BuildContextSubdir:
		return build.ContextModeSubdir
	}
	return ""
}

// buildRunnerOptions controls the small set of per-caller differences between
// the app and preview-environment build flows.
type buildRunnerOptions struct {
	// logName is the logger name used for build-time messages (e.g. "build" or
	// "preview-build").
	logName string
	// tmpDirPrefix is passed to os.MkdirTemp (e.g. "mortise-build-*").
	tmpDirPrefix string
	// appendLog, when true, records each build log line into the tracker buffer.
	// The app reconciler enables this so the UI Build tab can stream lines.
	// The preview reconciler omits it — preview builds write no log ConfigMap.
	appendLog bool
	// onDone, if non-nil, is called after every terminal outcome (success or
	// failure). The app reconciler uses this to persist the build log ConfigMap.
	onDone func()
}

// runBuild is the shared goroutine body for both the app and preview-environment
// build flows. It clones the repo, submits the build to BuildClient, drains
// events, and records the outcome on the tracker. It is designed to run in its
// own goroutine; cancel is deferred to ensure the build context is always
// released.
func runBuild(
	ctx context.Context,
	cancel context.CancelFunc,
	t *buildTracker,
	p buildParams,
	gitClient git.GitClient,
	buildClient build.BuildClient,
	opts buildRunnerOptions,
) {
	defer cancel()
	if opts.onDone != nil {
		defer opts.onDone()
	}

	log := logf.Log.WithName(opts.logName).WithValues("app", p.appName, "namespace", p.namespace)

	cloneDir, err := os.MkdirTemp("", opts.tmpDirPrefix)
	if err != nil {
		t.setFailed(fmt.Sprintf("create temp dir: %v", err))
		return
	}
	defer os.RemoveAll(cloneDir)

	creds := git.GitCredentials{Token: p.token}
	if err := gitClient.Clone(ctx, p.repo, p.branch, cloneDir, creds); err != nil {
		t.setFailed(fmt.Sprintf("CloneFailed: %v", err))
		return
	}
	log.Info("cloned repo", "repo", p.repo, "branch", p.branch)
	if p.revision != "" && p.revision != p.branch {
		if err := gitClient.CheckoutRevision(ctx, cloneDir, p.revision); err != nil {
			t.setFailed(fmt.Sprintf("CheckoutRevisionFailed: %v", err))
			return
		}
		log.Info("checked out revision", "revision", p.revision)
	}

	dockerfileDir := cloneDir
	if p.path != "" {
		resolved, err := resolveSourceDir(cloneDir, p.path)
		if err != nil {
			t.setFailed(err.Error())
			return
		}
		dockerfileDir = resolved
	}

	req := build.BuildRequest{
		AppName:       p.appName,
		Namespace:     p.namespace,
		SourceDir:     cloneDir,
		DockerfileDir: dockerfileDir,
		Dockerfile:    p.dockerfile,
		BuildArgs:     withRevisionBuildArg(p.buildArgs, p.branch, p.revision),
		ContextMode:   toContextMode(p.buildContext),
		NoCache:       p.noCache,
		PushTarget:    p.imageRef.Full,
	}

	events, err := buildClient.Submit(ctx, req)
	if err != nil {
		t.setFailed(fmt.Sprintf("BuildSubmitFailed: %v", err))
		return
	}

	digest := ""
	var detectedPort int32
	for ev := range events {
		switch ev.Type {
		case build.EventLog:
			log.V(1).Info("build log", "line", ev.Line)
			if opts.appendLog {
				t.appendLog(ev.Line)
			}
		case build.EventSuccess:
			digest = ev.Digest
			detectedPort = ev.DetectedPort
			log.Info("build succeeded", "image", p.imageRef.Full, "digest", digest, "detectedPort", detectedPort)
		case build.EventFailure:
			t.setFailed(ev.Error)
			return
		}
	}

	pullImage := p.pullImageRef.Full
	if digest != "" {
		pullImage = p.pullImageRef.Registry + "/" + p.pullImageRef.Path + "@" + digest
	}
	t.setSucceeded(pullImage, digest, detectedPort)
}

// revisionEnvName is the build arg and container env var that carries the git
// revision an image was built from. Mortise is the component that always
// knows it, so it supplies it rather than every team wiring an ARG through
// their own CI (CAI-180).
const revisionEnvName = "MORTISE_REVISION"

// imageEnvName is the container env var that carries the resolved image
// reference the pod is running, digest included.
const imageEnvName = "MORTISE_IMAGE"

// withRevisionBuildArg adds MORTISE_REVISION to the user's build args. It is
// added here, at submission, rather than into BuildRunSpec.BuildArgs: the
// BuildRun input hash already covers the revision, and adding a derived arg
// to the spec would change every existing App's hash and force a rebuild
// on upgrade for no new information. A user-set value wins. When the
// revision is only the branch fallback there is no commit to report, so the
// arg is left absent rather than set to a branch name.
func withRevisionBuildArg(args map[string]string, branch, revision string) map[string]string {
	if revision == "" || revision == branch {
		return args
	}
	if _, set := args[revisionEnvName]; set {
		return args
	}
	out := make(map[string]string, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out[revisionEnvName] = revision
	return out
}
