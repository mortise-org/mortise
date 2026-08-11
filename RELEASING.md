# Releasing Mortise

## Versioning

Strict semver, `v` prefix on git tags. The tag is the source of truth: it
drives the chart version, chart `appVersion`, and container image tag.

| Artifact            | Where it lives                                    | Tag / version |
|---------------------|---------------------------------------------------|---------------|
| Container image     | `ghcr.io/mortise-org/mortise`                     | `<version>` (e.g. `0.1.0`) |
| `mortise` chart     | `https://mortise-org.github.io/mortise`           | `<version>`   |
| `mortise-core` chart | `https://mortise-org.github.io/mortise`           | `<version>`   |
| GitHub Release      | `github.com/mortise-org/mortise/releases`         | `v<version>`  |

Chart version, chart `appVersion`, and image tag are always the same number.
The `main` image tag is a floating dev build: never reference it from a
chart or from user-facing docs.

## Cutting a release

```bash
# On main, from a clean working tree
git pull
git tag v0.1.1
git push origin v0.1.1
```

That single push does everything:

1. **`image` job**: builds multi-arch (`linux/amd64` + `linux/arm64`) and
   pushes `ghcr.io/mortise-org/mortise:0.1.1` plus `0.1` (major.minor) tags.
2. **`chart-integration` job**: runs `make test-chart-integration` — the
   full umbrella chart deployed on a real k3d cluster (PVC persistence
   across pod restart, toggle lifecycle, standalone core, install
   script). This is the *behavioral* pre-publish gate: the chart job
   will not run if it fails. It pairs with the post-publish artifact
   verification below — this proves the chart behaves before publishing;
   that proves the published bytes are the ones this run packaged.
3. **`chart` job** (needs image + chart-integration): stamps `version:`
   and `appVersion:` in both `charts/mortise/Chart.yaml` and
   `charts/mortise-core/Chart.yaml` to `0.1.1`, packages both charts,
   merges them into the `gh-pages` branch `index.yaml`, and pushes.
4. **`release` job**: creates a GitHub Release at `v0.1.1` with auto-
   generated release notes.

Nothing else is required. Do not manually edit `Chart.yaml` version fields
or the gh-pages `index.yaml`.

## Dev builds

The release workflow only fires on `v*` tags — pushes to `main` do not
build or publish an image. For local development, use `make dev-up` (k3d +
Tilt) or `make build` to build a local image. If you need a specific
commit, cut a prerelease tag (e.g. `v0.2.0-rc.1`).

## Placeholder version policy

The in-repo charts carry the placeholder version `0.0.0-dev`. It is not a
release: CI stamps the real tag-derived version at release time, and
`release.yml` refuses to publish a chart still carrying the placeholder.
The placeholder is deliberately NOT a plausible release number — it used
to be `0.1.0`, which collided with the genuinely published `mortise-0.1.0`
artifact on gh-pages, making "am I looking at the dev tree or the 0.1.0
release?" unanswerable (the mechanism behind #446/#379's stale-template
reports). `release.yml` also refuses to overwrite a version that already
exists on gh-pages: published chart artifacts are immutable, same as
images.

## Hotfix / rollback

If a release is broken:

- **Roll back a Helm install**: `helm rollback mortise` or reinstall with
  `--version <prev>`. The old chart versions stay in `index.yaml` forever.
- **Roll back a container image**: pull the previous tag. Images are
  immutable once pushed, so `0.1.0` is always `0.1.0`.
- **Cut a fix release**: `git tag v0.1.2` and push. Don't delete or
  re-push the broken tag. Semver forbids re-using a version.

## What lives where

```
.github/workflows/release.yml   # the pipeline: edit here
charts/mortise/Chart.yaml       # version stamped by CI at tag time
charts/mortise-core/Chart.yaml  # version stamped by CI at tag time
charts/mortise-core/values.yaml # image.repository = ghcr.io/mortise-org/mortise
                                # image.tag        = <version>
```

The `scripts/install.sh` flow is separate from this convention: when run
from a repo clone, it builds `mortise:dev` locally and overrides the chart
image. When run via `curl | bash`, it falls through to the published
chart, which pulls the published image. Either way, it does not need
`helm dependency build` or registry access: the installer handles that.

## Prerequisites for the pipeline to work

All one-time, already done, only listed here so the next person
investigating a broken pipeline has the receipts:

- Repo is public (package visibility defaults follow source repo).
- Org setting **Settings → Packages → "Public packages"** = Allowed.
- Org setting **Settings → Packages → "Inherit access restrictions from
  source repositories"** = On.
- `gh-pages` branch exists and GitHub Pages is set to serve from it
  (`Settings → Pages → Source: gh-pages branch, / root`).
- Workflow permissions default (no special secrets: `GITHUB_TOKEN` is
  enough because we only write to `gh-pages` and push to `ghcr.io`
  under the same repo).
