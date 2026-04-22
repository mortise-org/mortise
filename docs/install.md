# Installing Mortise

Two ways to install, depending on where you're starting from.

| Path | When to use | Jump to |
|---|---|---|
| **Quick** | You don't have a cluster yet, or you want a throwaway local one | [Quick install](#quick-install-one-command) |
| **Helm** | You already have a Kubernetes cluster (EKS, GKE, AKS, Talos, k3s, etc.) | [Helm install](#helm-install-existing-cluster) |

Both end in the same place: operator running, UI at `localhost:8090`.

After install, go to the [Quickstart](./quickstart.md) to create your
admin account and deploy your first app.

---

## Quick install (one command)

The installer provisions a local Kubernetes cluster (k3s on Linux, k3d on
macOS + Windows) and installs the full Mortise stack via Helm.

### macOS

```bash
curl -fsSL https://mortise.me/install | bash
```

Prereq: **Docker Desktop** running. The installer uses k3d (k3s-in-Docker)
and drops the `mortise` CLI on your `PATH`.

### Linux

```bash
curl -fsSL https://mortise.me/install | bash
```

Same command. Uses **k3s** natively (no Docker needed) and requires
`sudo`. If `kubectl cluster-info` already succeeds, the installer detects
your existing cluster and skips the k3s step.

### Windows

```powershell
iwr -useb https://mortise.me/install.ps1 | iex
```

Windows 10+, PowerShell. Prereq: **Docker Desktop** running. Uses k3d
under the hood.

### What the installer does

1. Installs **Kubernetes** — k3s (Linux) or k3d (macOS/Windows), unless
   an existing cluster is detected
2. Installs **Helm** if not present
3. `helm repo add` the Mortise chart repo
4. `helm install` the batteries-included `mortise` chart —
   operator + Traefik (or uses the cluster's existing one) + cert-manager
   + BuildKit + OCI registry + default PlatformConfig
5. Port-forwards the UI to `http://localhost:8090`
6. Builds the CLI from source (if `go` is present locally)

All steps are idempotent. Run it again to upgrade.

### Configuration

Override any default via env var before running:

```bash
HELM_VERSION=v3.17.3 \
MORTISE_CHART_VERSION=0.1.0 \
MORTISE_CHART_REPO=https://mortise-org.github.io/mortise \
  curl -fsSL https://mortise.me/install | bash
```

---

## Helm install (existing cluster)

If `kubectl get nodes` already works, skip the installer and go straight
to Helm. Works identically on EKS, GKE, AKS, RKE2, Talos, k3s, or any
CNCF-conformant distribution.

### Two charts

| Chart | Contains | When to pick |
|---|---|---|
| **`mortise`** | operator + Traefik + cert-manager + BuildKit + registry + default PlatformConfig | Fresh cluster where you want it all bundled |
| **`mortise-core`** | operator only (CRDs, RBAC, Deployment, Service) | You run your own ingress + cert-manager + registry + buildkit |

### Batteries-included

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace
```

If your cluster already has any of the bundled pieces, disable them
individually:

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set traefik.enabled=false \
  --set cert-manager.enabled=false
```

### Operator-only

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace
```

You'll need to create a `PlatformConfig` pointing at your own ingress
class, TLS ClusterIssuer, registry, and BuildKit. See
[Configuring your platform](./configuration.md) for the full schema.
A BYO docs walkthrough is [tracked in issue #86](https://github.com/mortise-org/mortise/issues/86).

### Access the UI

```bash
kubectl port-forward -n mortise-system svc/mortise 8090:80
# open http://localhost:8090
```

Or expose it properly once you've set a platform domain — see
[Configuration](./configuration.md).

---

## Don't have a cluster?

See [Creating a cluster](./cluster-setup.md) for quick paths to k3s, k3d,
or a managed cluster (EKS, GKE, AKS).

## Uninstall

```bash
# Helm path
helm uninstall mortise -n mortise-system
kubectl delete ns mortise-system mortise-deps

# Quick-install path on macOS/Windows
k3d cluster delete mortise

# Quick-install path on Linux
/usr/local/bin/k3s-uninstall.sh
```

## Upgrading

```bash
helm repo update
helm upgrade mortise mortise/mortise -n mortise-system
```

Release cadence and versioning are documented in
[RELEASING.md](../RELEASING.md) — every `v*` git tag publishes a new
image + chart + GitHub Release simultaneously, so the chart version
always matches the image tag.
