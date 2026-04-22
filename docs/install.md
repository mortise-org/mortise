# Installing Mortise

Two ways to install, depending on where you're starting from.

| Path | When to use | Jump to |
|---|---|---|
| **Quick** | You don't have a cluster yet, or you want a throwaway local one | [Quick install](#quick-install-one-command) |
| **Helm** | You already have a Kubernetes cluster (EKS, GKE, AKS, Talos, k3s, etc.) | [Helm install](#helm-install-existing-cluster) |

Both end in the same place: operator running, UI reachable at `localhost:8090`.

After install, go to the [Quickstart](./quickstart.md) to create your admin
account and deploy your first app.

---

## Quick install (one command)

The installer provisions a local Kubernetes cluster (k3s on Linux, k3d on
macOS and Windows) and installs the full Mortise stack via Helm.

### macOS

```bash
curl -fsSL https://mortise.me/install | bash
```

Prereq: **Docker Desktop** running. The installer uses k3d (k3s inside
Docker) and drops the `mortise` CLI on your `PATH`.

### Linux

```bash
curl -fsSL https://mortise.me/install | bash
```

Same command. Uses **k3s** natively (no Docker needed) and requires `sudo`.
If `kubectl cluster-info` already succeeds, the installer detects your
existing cluster and skips the k3s step.

### Windows

```powershell
iwr -useb https://mortise.me/install.ps1 | iex
```

Windows 10+, PowerShell. Prereq: **Docker Desktop** running. Uses k3d under
the hood.

### What the installer does

1. Installs **Kubernetes**: k3s (Linux) or k3d (macOS / Windows), unless an
   existing cluster is detected.
2. Installs **Helm** if not present.
3. `helm repo add` the Mortise chart repo.
4. `helm install` the batteries-included `mortise` chart (operator, Traefik,
   cert-manager, BuildKit, OCI registry, default PlatformConfig).
5. Port-forwards the UI to `http://localhost:8090`.
6. Builds the CLI from source (if `go` is present locally).

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

If you already run Kubernetes, skip the installer and deploy with Helm
directly. Works on EKS, GKE, AKS, RKE2, Talos, k3s, OpenShift, or any
CNCF-conformant distribution.

This section is longer than the quick install on purpose. When you bring
your own cluster there are more decisions (ingress, TLS, storage,
registry) and a real cluster makes those decisions visible.

### Prereqs

Before running `helm install`, confirm:

```bash
# 1. You can talk to the cluster as an admin.
kubectl auth can-i '*' '*' --all-namespaces     # expect "yes"

# 2. Helm 3 is installed.
helm version --short                             # expect v3.12+

# 3. At least one StorageClass exists (default, or you'll set one).
kubectl get storageclass

# 4. Enough headroom. Rough baseline for the batteries-included chart:
#    ~800m CPU + 1Gi RAM for operator + Traefik + cert-manager +
#    BuildKit + registry together. Builds can burst higher.
kubectl top nodes                                # if metrics-server is present
```

You should also decide **before** running Helm:

| Decision | Chart default | Alternative |
|---|---|---|
| Ingress controller | Chart deploys Traefik (`traefik.enabled=true`) | Use your own (nginx, HAProxy, ALB). Set `--set traefik.enabled=false`. |
| TLS | Chart deploys cert-manager (`cert-manager.enabled=true`) | Use your own cert-manager install. Set `--set cert-manager.enabled=false`. |
| Container registry | Chart deploys an in-cluster registry | Use ECR, GHCR, or Harbor. Set `--set registry.enabled=false` and set the URL in PlatformConfig. |
| BuildKit | Chart deploys BuildKit | Use an existing BuildKit endpoint. Set `--set buildkit.enabled=false` and set the addr in PlatformConfig. |

If you disable components, you'll configure Mortise to point at your
external ones via `PlatformConfig`. See
[Configuring your platform](./configuration.md).

### Two charts

| Chart | Contains | When to pick |
|---|---|---|
| **`mortise`** | operator + Traefik + cert-manager + BuildKit + registry + default PlatformConfig | Fresh cluster where you want it all bundled. |
| **`mortise-core`** | operator only (CRDs, RBAC, Deployment, Service). No infra. | You run your own ingress, cert-manager, registry, and buildkit. |

### Install (batteries-included)

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

Common first-install overrides:

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set platformConfig.domain=apps.example.com \
  --set mortise-core.operator.ingressClassName=nginx \
  --set traefik.enabled=false \
  --set cert-manager.enabled=true
```

### Install (operator-only)

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace
```

`mortise-core` ships **no PlatformConfig**. You must apply one yourself
pointing at your registry, BuildKit, and TLS ClusterIssuer before any App
will build:

```yaml
apiVersion: mortise.mortise.dev/v1alpha1
kind: PlatformConfig
metadata:
  name: platform
spec:
  domain: apps.example.com
  registry:
    url: https://ghcr.io/my-org
    pullSecretRef:
      name: ghcr-pull
  build:
    buildkitAddr: tcp://buildkit.mortise-build.svc:1234
  tls:
    certManagerClusterIssuer: letsencrypt-prod
  ingress:
    className: nginx
```

A step-by-step BYO walkthrough is
[tracked in #86](https://github.com/mortise-org/mortise/issues/86).

### What Helm creates

A fresh `mortise` chart install creates:

| Namespace | Contents |
|---|---|
| `mortise-system` | Operator `Deployment/mortise`, `Service/mortise`, `ServiceAccount/mortise-controller`, cert-manager (if `cert-manager.enabled`), Traefik (if `traefik.enabled`). |
| `mortise-deps` | `Deployment/registry`, `Deployment/buildkitd`, their `PersistentVolumeClaim`s if `storage=pvc`. |
| cluster-scoped | CRDs (`Project`, `App`, `PlatformConfig`, `GitProvider`, `PreviewEnvironment`, `ProjectMember`), `ClusterRole` / `ClusterRoleBinding`, default `PlatformConfig/platform`. |

Your app workloads will land in per-project namespaces (`pj-{project}` for
control, `pj-{project}-{env}` for workloads), created by the operator when
you create a Project in the UI.

### Verify the install

```bash
# Operator ready
kubectl -n mortise-system rollout status deploy/mortise

# CRDs registered
kubectl get crds | grep mortise.mortise.dev

# Platform configured
kubectl get platformconfig platform -o jsonpath='{.status}'

# Deps (registry + BuildKit) healthy
kubectl -n mortise-deps get deploy
```

### Access the UI

**Quick smoke test:**

```bash
kubectl port-forward -n mortise-system svc/mortise 8090:80
# open http://localhost:8090
```

**Production exposure** (recommended once you have a domain):

```bash
helm upgrade mortise mortise/mortise \
  --namespace mortise-system --reuse-values \
  --set mortise-core.ingress.enabled=true \
  --set mortise-core.ingress.className=traefik \
  --set mortise-core.ingress.host=mortise.example.com
```

Point a DNS record at your ingress controller's external IP or hostname,
and cert-manager will issue a TLS cert if you have a ClusterIssuer
configured.

## Registry proxy (git-source builds)

If you plan to deploy from git source, the bundled registry needs one small
piece of node-level configuration. Mortise deploys a DaemonSet proxy on
every node so kubelet can pull built images from `localhost:30500`, but most
container runtimes need to be told that this is an HTTP endpoint.

`helm install` prints the exact snippet for your distro. If you missed it:

**k3s / RKE2** -- add to `/etc/rancher/k3s/registries.yaml` and restart k3s:
```yaml
mirrors:
  "localhost:30500":
    endpoint:
      - "http://localhost:30500"
```

**Talos** -- add to your machine config:
```yaml
machine:
  registries:
    mirrors:
      localhost:30500:
        endpoints:
          - http://localhost:30500
```

**kubeadm** -- create `/etc/containerd/certs.d/localhost:30500/hosts.toml`:
```toml
server = "http://localhost:30500"

[host."http://localhost:30500"]
  capabilities = ["pull", "resolve"]
  plain-http = true
```

**Not needed when:** you're only deploying pre-built images, or you've
pointed `PlatformConfig.spec.registry.url` at an external registry (GHCR,
ECR, Harbor, etc.).

See [Troubleshooting > Registry unreachable from kubelet](./troubleshooting.md#registry-unreachable-from-kubelet)
for details on why this is necessary.

## First-run setup

### Setting up TLS for apps

Once deployed, apps served at `{app}.{platformDomain}` get TLS
automatically if you:

1. Have a `ClusterIssuer` in the cluster (e.g. Let's Encrypt HTTP-01).
2. Reference it in `PlatformConfig`:
   ```bash
   kubectl patch platformconfig platform --type merge \
     -p '{"spec":{"tls":{"certManagerClusterIssuer":"letsencrypt-prod"}}}'
   ```
3. Have a wildcard DNS record `*.apps.example.com` pointing at your
   ingress controller (or per-app records).

See [Configuration](./configuration.md) for the full TLS / DNS matrix.

### Most-used values

```yaml
# Minimum safe values for an existing cluster with its own ingress + cert-manager
traefik:
  enabled: false
cert-manager:
  enabled: false

mortise-core:
  operator:
    ingressClassName: nginx         # matches your existing ingress controller
  ingress:
    enabled: true
    className: nginx
    host: mortise.example.com

platformConfig:
  enabled: true
  domain: apps.example.com          # wildcard {app}.apps.example.com
```

Save as `values.yaml` and run `helm install -f values.yaml`.

### Upgrading

```bash
helm repo update
helm upgrade mortise mortise/mortise -n mortise-system
```

**CRD note:** `helm upgrade` does not update CRD definitions by default
(Helm's long-standing safety behavior). If a release adds or changes CRD
fields, re-apply them explicitly:

```bash
helm pull mortise/mortise --untar
kubectl apply -f mortise/charts/mortise-core/crds/
```

Release cadence and versioning are documented in
[RELEASING.md](../RELEASING.md). Every `v*` git tag publishes an image +
both charts + a GitHub Release simultaneously, so the chart version always
matches the image tag.

## Helm values reference

### mortise (batteries included)

All values are optional. A bare `helm install` with no overrides works.

| Value | Default | Description |
|-------|---------|-------------|
| `traefik.enabled` | `true` | Deploy Traefik ingress controller |
| `cert-manager.enabled` | `true` | Deploy cert-manager for TLS |
| `buildkit.enabled` | `true` | Deploy BuildKit for git-source builds |
| `buildkit.image` | `moby/buildkit:v0.29.0` | BuildKit image |
| `buildkit.privileged` | `true` | Run BuildKit privileged (required for most setups) |
| `buildkit.storage` | `pvc` | `pvc` for persistent, `emptyDir` for ephemeral |
| `buildkit.storageSize` | `10Gi` | PVC size for BuildKit layer cache |
| `registry.enabled` | `true` | Deploy OCI registry for built images |
| `registry.image` | `distribution/distribution:2.8.3` | Registry image |
| `registry.storage` | `pvc` | `pvc` for persistent, `emptyDir` for ephemeral |
| `registry.storageSize` | `10Gi` | PVC size for registry image storage |
| `registry.proxy.hostPort` | `30500` | Node port for the registry DaemonSet proxy |
| `metricsServer.enabled` | `true` | Deploy metrics-server for real-time CPU/memory |
| `observer.enabled` | `true` | Deploy built-in observer for log/metrics history |
| `observer.storage` | `emptyDir` | `emptyDir` or `pvc` for observer SQLite data |
| `observer.storageSize` | `2Gi` | PVC size (when `observer.storage=pvc`) |
| `observer.retention.metrics` | `72h` | How long to keep metrics history |
| `observer.retention.logs` | `48h` | How long to keep log history |
| `platformConfig.enabled` | `true` | Auto-create default PlatformConfig |
| `platformConfig.domain` | `""` | Platform domain for app URLs |
| `buildInfra.namespace` | `mortise-deps` | Namespace for BuildKit + registry |

Operator values are nested under `mortise-core.`:

| Value | Default | Description |
|-------|---------|-------------|
| `mortise-core.image.repository` | `mortise` | Operator image |
| `mortise-core.image.tag` | `dev` | Image tag |
| `mortise-core.replicaCount` | `1` | Operator replicas |
| `mortise-core.api.port` | `8090` | API server port |
| `mortise-core.service.type` | `ClusterIP` | Service type |
| `mortise-core.ingress.enabled` | `false` | Create an Ingress for the Mortise UI/API |
| `mortise-core.ingress.host` | `""` | Hostname for the Ingress |
| `mortise-core.operator.ingressClassName` | `""` | IngressClass for app Ingresses |
| `mortise-core.github.clientID` | `""` | GitHub OAuth App client ID (optional) |

### mortise-core (operator only)

| Value | Default | Description |
|-------|---------|-------------|
| `image.repository` | `mortise` | Operator image |
| `image.tag` | `dev` | Image tag |
| `replicaCount` | `1` | Operator replicas |
| `api.port` | `8090` | API server port |
| `service.type` | `ClusterIP` | Service type |
| `ingress.enabled` | `false` | Create an Ingress for the Mortise UI/API |
| `ingress.host` | `""` | Hostname for the Ingress |
| `operator.ingressClassName` | `""` | IngressClass for app Ingresses |
| `github.clientID` | `""` | GitHub OAuth App client ID (optional) |
| `resources.requests.cpu` | `200m` | CPU request |
| `resources.requests.memory` | `128Mi` | Memory request |
| `resources.limits.cpu` | `2000m` | CPU limit |
| `resources.limits.memory` | `512Mi` | Memory limit |

## Persistence and storage

The chart creates PersistentVolumeClaims for the OCI registry (built
images) and BuildKit (layer cache). This requires a default StorageClass
in your cluster.

**Most clusters already have one:**
- k3s/k3d: `local-path` (data at `/var/lib/rancher/k3s/storage/`)
- EKS: `gp2` or `gp3`
- GKE: `standard` or `premium-rwo`
- AKS: `managed-premium` or `managed-csi`

Check yours with `kubectl get storageclass`. The one marked `(default)` is
what Mortise uses.

**No StorageClass?** Either install one
([local-path-provisioner](https://github.com/rancher/local-path-provisioner)
is the simplest for single-node setups) or opt into ephemeral storage:

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set registry.storage=emptyDir \
  --set buildkit.storage=emptyDir
```

With `emptyDir`, built images are lost on pod restart. Image-only deploys
(no git source) are unaffected since they pull from external registries.

**Backups:** Platform state (apps, projects, users, credentials) is stored
in Kubernetes objects that live in etcd and survive reboots. For disaster
recovery, see [Backup with Velero](./recipes/backup.md) or use
[k3s etcd snapshots](https://docs.k3s.io/cli/etcd-snapshot) for the
simplest approach on k3s.

## Uninstall

```bash
helm uninstall mortise -n mortise-system
kubectl delete ns mortise-system mortise-deps

# CRDs are intentionally kept on uninstall. Remove explicitly if you want:
kubectl delete crd -l app.kubernetes.io/part-of=mortise
```

---

## Don't have a cluster?

See [Creating a cluster](./cluster-setup.md) for quick paths to k3s, k3d,
or a managed cluster (EKS, GKE, AKS).
