# Installing Mortise

Mortise installs onto an existing Kubernetes cluster via Helm. After install
you get a web UI, REST API, and CLI for deploying apps.

## Prerequisites

- A running Kubernetes cluster (k3s, k3d, EKS, GKE, AKS, RKE2, Talos, etc.)
- `kubectl` configured to talk to that cluster
- `helm` v3 installed

Don't have a cluster yet? See [Creating a cluster](./cluster-setup.md) for
a quick guide to getting one running on your machine or server.

## Two charts

Mortise publishes two Helm charts:

| Chart | What's in it | When to use |
|-------|-------------|-------------|
| **`mortise`** | Operator + Traefik + cert-manager + BuildKit + OCI registry + default PlatformConfig | Fresh cluster, homelab, or anywhere you want one command to get everything running |
| **`mortise-core`** | Operator only (CRDs, RBAC, Deployment, Service) | You already have an ingress controller, cert-manager, and/or your own build pipeline |

## Install (batteries included)

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise \
  --namespace mortise-system \
  --create-namespace
```

This deploys the operator, Traefik (ingress controller), cert-manager (TLS),
BuildKit (image builder), and an OCI registry into your cluster. If your
cluster already has any of these, disable them:

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system \
  --create-namespace \
  --set traefik.enabled=false \
  --set cert-manager.enabled=false
```

## Install (operator only)

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise-core \
  --namespace mortise-system \
  --create-namespace
```

You'll need to provide your own ingress controller, cert-manager, and
build infrastructure. See [Configuring your platform](./configuration.md)
for how to point the operator at an external BuildKit and registry.

## Accessing the UI

By default the service is `ClusterIP`. To access from your machine:

```bash
kubectl port-forward -n mortise-system svc/mortise 8090:80
```

Then open **http://localhost:8090**. You'll see the admin account creation
screen on first visit.

If you want to expose Mortise on a real domain (recommended for teams), create
an Ingress or use the chart's built-in ingress support:

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system \
  --create-namespace \
  --set mortise-core.ingress.enabled=true \
  --set mortise-core.ingress.host=mortise.yourdomain.com
```

## First-run setup

1. Open the UI and create your admin account (email + password)
2. You'll see a getting-started checklist — everything on it is optional
3. You're on the dashboard with a default project ready to go

You can deploy an image-based app immediately with zero additional
configuration. For git deploys, custom domains, and HTTPS, see
[Configuring your platform](./configuration.md).

## Using the CLI

Install the `mortise` CLI and log in:

```bash
mortise login
# Prompts for: server URL, email, password
```

Deploy your first app:

```bash
mortise app create --source image --image nginx:1.27 --name web
```

See the full CLI reference with `mortise --help`.

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
kubectl delete namespace mortise-system
```

If you used the batteries-included chart, also clean up the build infra:

```bash
kubectl delete namespace mortise-deps
```

This removes the operator and all platform resources. App namespaces
(`pj-*`) are owned by their Project CRDs — deleting the CRDs cascades
to the namespaces and everything inside them.
