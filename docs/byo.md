# Bring Your Own Infrastructure

This guide covers installing Mortise when you already have some or all of the
infrastructure it normally bundles (ingress, cert-manager, registry, BuildKit).

For the default batteries-included install, see [install.md](./install.md).

---

## How BYO works

The `mortise` umbrella chart bundles Traefik, cert-manager, an OCI registry,
and BuildKit. Each component can be disabled with a toggle, and
`platformConfig.*` values let you point Mortise at your external replacements
without writing a PlatformConfig manifest by hand.

When a BYO value is set, it overrides the in-cluster default. When it's empty
and the bundled component is enabled, the in-cluster address is used
automatically.

---

## Scenario 1: Managed Kubernetes with own ingress + cert-manager

**Typical setup:** EKS / GKE / AKS with nginx or ALB ingress controller and
an existing cert-manager install. You bring your own registry (ECR, GHCR,
Harbor) and use the bundled BuildKit for git-source builds.

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set traefik.enabled=false \
  --set cert-manager.enabled=false \
  --set registry.enabled=false \
  --set mortise-core.operator.ingressClassName=nginx \
  --set platformConfig.domain=apps.example.com \
  --set platformConfig.registry.url=https://ghcr.io/my-org \
  --set platformConfig.registry.namespace=mortise \
  --set platformConfig.registry.pullSecretRef=ghcr-pull \
  --set platformConfig.tls.clusterIssuer=letsencrypt-prod
```

**What this does:**

- Disables Traefik, cert-manager, and the bundled registry
- Points App Ingresses at your nginx/ALB ingress class
- Configures PlatformConfig to push built images to GHCR and pull via an
  existing `docker-registry` Secret named `ghcr-pull`
- Uses your existing cert-manager ClusterIssuer for TLS
- Keeps the bundled BuildKit for building from git source

**Prerequisites:**

- An ingress controller running in the cluster
- cert-manager installed with a working ClusterIssuer
- A `docker-registry` Secret in the `mortise-system` namespace containing
  credentials for your registry
- DNS: wildcard `*.apps.example.com` pointing at your ingress controller

After install, verify with:

```bash
kubectl -n mortise-system rollout status deploy/mortise
kubectl get platformconfig platform -o yaml
```

Then follow the [Quickstart](./quickstart.md) to create your admin account.

---

## Scenario 2: On-prem cluster with Traefik already running

**Typical setup:** Bare-metal or on-prem k3s/RKE2 cluster where Traefik is
already the ingress controller. You want the chart's cert-manager, registry,
and BuildKit — just not a second Traefik.

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set traefik.enabled=false \
  --set mortise-core.operator.ingressClassName=traefik \
  --set platformConfig.domain=apps.internal.example.com
```

**What this does:**

- Disables the chart's Traefik (your existing one handles ingress)
- Points App Ingresses at your existing Traefik's ingress class
- Keeps everything else bundled: cert-manager, registry, BuildKit

**If you also have cert-manager already:**

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set traefik.enabled=false \
  --set cert-manager.enabled=false \
  --set mortise-core.operator.ingressClassName=traefik \
  --set platformConfig.domain=apps.internal.example.com \
  --set platformConfig.tls.clusterIssuer=letsencrypt-prod
```

**Note on git-source builds:** The bundled registry needs node-level
containerd configuration so kubelet can pull images from `localhost:30500`.
See [Registry proxy](./install.md#registry-proxy-git-source-builds) in the
install docs.

---

## Scenario 3: Bare operator (mortise-core)

**Typical setup:** You run everything yourself — ingress, cert-manager,
registry, BuildKit — and only want the Mortise operator (CRDs, RBAC,
Deployment).

```bash
helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace
```

`mortise-core` ships **no PlatformConfig**, no infrastructure. You must apply
one manually:

```yaml
apiVersion: mortise.mortise.dev/v1alpha1
kind: PlatformConfig
metadata:
  name: platform
spec:
  domain: apps.example.com
  externalDomain: mortise.example.com
  registry:
    url: https://registry.example.com
    namespace: mortise
    pullSecretName: registry-pull
  build:
    buildkitAddr: tcp://buildkitd.build-infra.svc:1234
  tls:
    certManagerClusterIssuer: letsencrypt-prod
```

```bash
kubectl apply -f platformconfig.yaml
```

See the [PlatformConfig CRD reference](./configuration.md) for all available
fields.

**GitOps users (Argo CD / Flux):** If you manage CRDs out-of-band, install
with `--skip-crds` so Helm doesn't touch them:

```bash
helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace \
  --skip-crds
```

Apply CRDs yourself from the chart's `crds/` directory or from the release
assets.

---

## Values reference (BYO-relevant)

All BYO values live under `platformConfig` in the umbrella chart:

| Value | Default | Description |
|---|---|---|
| `platformConfig.registry.url` | `""` (uses bundled) | External registry URL (e.g. `https://ghcr.io/my-org`) |
| `platformConfig.registry.namespace` | `mortise` | Registry namespace for app images |
| `platformConfig.registry.insecureSkipTLSVerify` | `false` | Skip TLS verification for the registry |
| `platformConfig.registry.pullSecretRef` | `""` | Name of an existing `docker-registry` Secret for pulling |
| `platformConfig.build.buildkitAddr` | `""` (uses bundled) | External BuildKit address (e.g. `tcp://buildkit:1234`) |
| `platformConfig.tls.clusterIssuer` | `""` | cert-manager ClusterIssuer name for App TLS |

Component toggles (disable bundled infra):

| Value | Default | Description |
|---|---|---|
| `traefik.enabled` | `true` | Deploy bundled Traefik |
| `cert-manager.enabled` | `true` | Deploy bundled cert-manager |
| `registry.enabled` | `true` | Deploy bundled OCI registry |
| `buildkit.enabled` | `true` | Deploy bundled BuildKit |

See [`values-byo.yaml`](../charts/mortise/values-byo.yaml) for a
commented example with every BYO toggle.

---

## Troubleshooting

**Apps fail to build (git source):** Check that `PlatformConfig.spec.build`
has a reachable BuildKit address. If using a BYO BuildKit, ensure it's
accessible from the `mortise-system` namespace.

**Images fail to pull:** Ensure the `pullSecretRef` Secret exists in
`mortise-system` and contains valid credentials. For private registries,
the operator projects this secret into app namespaces automatically.

**TLS not issued:** Verify your ClusterIssuer is working:
`kubectl get clusterissuer <name> -o yaml`. Check cert-manager logs if
challenges fail.

**Observer traffic empty with BYO Traefik:** Your Traefik must emit
JSON-formatted access logs. See
[Troubleshooting](./troubleshooting.md#observer-traffic-empty-with-byo-traefik).
