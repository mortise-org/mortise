# Bring Your Own Infrastructure

This guide walks through installing Mortise when you already have some or all
of the supporting infrastructure (ingress controller, cert-manager, container
registry, BuildKit). If you want everything bundled, see the
[standard install](./install.md).

Each scenario below ends with a working install where you can deploy an image
app and reach it at `http://<app>.<your-domain>`.

An example values file with every BYO toggle is available at
[`charts/mortise/values-byo.yaml`](../charts/mortise/values-byo.yaml).

---

## How the toggles work

The `mortise` umbrella chart bundles Traefik, cert-manager, an OCI registry,
BuildKit, and metrics-server. Each can be disabled independently:

| Toggle | Default | What it controls |
|--------|---------|------------------|
| `traefik.enabled` | `true` | Traefik ingress controller |
| `cert-manager.enabled` | `true` | cert-manager for TLS |
| `registry.enabled` | `true` | In-cluster OCI registry |
| `buildkit.enabled` | `true` | BuildKit for git-source builds |
| `metricsServer.enabled` | `true` | metrics-server for CPU/memory |

When you disable a component and have an external replacement, tell Mortise
where to find it via `platformConfig.*` values:

| Value | Purpose |
|-------|---------|
| `platformConfig.registry.url` | External registry URL (e.g. `https://ghcr.io/my-org`) |
| `platformConfig.registry.namespace` | Registry namespace (default: `mortise`) |
| `platformConfig.registry.insecureSkipTLSVerify` | Skip TLS verification (default: `false`) |
| `platformConfig.registry.pullSecretRef` | Name of an existing `docker-registry` Secret |
| `platformConfig.build.buildkitAddr` | External BuildKit address (e.g. `tcp://buildkit:1234`) |
| `platformConfig.tls.clusterIssuer` | cert-manager ClusterIssuer name (e.g. `letsencrypt-prod`) |

When a BYO value is empty and the corresponding component is enabled, the
chart uses the in-cluster address automatically. When both are empty and the
component is disabled, the field is omitted from PlatformConfig.

---

## Scenario 1: Managed Kubernetes with own ingress + cert-manager

**Setup:** EKS, GKE, or AKS cluster. You already have nginx (or ALB) as
your ingress controller and cert-manager installed. You want to use an
external registry (ECR, GHCR, or Harbor) but keep the bundled BuildKit.

### Prerequisites

```bash
# Verify your ingress controller is running
kubectl get ingressclass
# NAME    CONTROLLER                      PARAMETERS   AGE
# nginx   k8s.io/ingress-nginx            <none>       30d

# Verify cert-manager is installed
kubectl get pods -n cert-manager
# NAME                                      READY   STATUS
# cert-manager-...                          1/1     Running
# cert-manager-cainjector-...               1/1     Running
# cert-manager-webhook-...                  1/1     Running

# Verify you have a ClusterIssuer
kubectl get clusterissuer
# NAME                READY   AGE
# letsencrypt-prod    True    30d
```

### Create a registry pull secret

If your external registry requires authentication, create a pull secret that
Mortise can project into app namespaces:

```bash
kubectl create namespace mortise-system

kubectl create secret docker-registry registry-pull \
  --namespace mortise-system \
  --docker-server=ghcr.io \
  --docker-username=my-user \
  --docker-password=ghp_xxxxxxxxxxxx
```

### Install

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
  --set platformConfig.registry.pullSecretRef=registry-pull \
  --set platformConfig.tls.clusterIssuer=letsencrypt-prod
```

Or equivalently, save a values file:

```yaml
# values-eks.yaml
traefik:
  enabled: false
cert-manager:
  enabled: false
registry:
  enabled: false

mortise-core:
  operator:
    ingressClassName: nginx

platformConfig:
  domain: apps.example.com
  registry:
    url: https://ghcr.io/my-org
    namespace: mortise
    pullSecretRef: registry-pull
  tls:
    clusterIssuer: letsencrypt-prod
```

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  -f values-eks.yaml
```

### Verify

```bash
kubectl -n mortise-system rollout status deploy/mortise
kubectl get platformconfig platform -o yaml
```

The PlatformConfig should show your external registry URL and ClusterIssuer.

---

## Scenario 2: On-prem cluster with Traefik already running

**Setup:** Bare-metal or VM-based cluster (RKE2, Talos, kubeadm) where
Traefik is already your ingress controller. You want Mortise to use the
chart's bundled cert-manager, registry, and BuildKit.

### Prerequisites

```bash
# Verify Traefik is running and has an IngressClass
kubectl get ingressclass
# NAME      CONTROLLER                      PARAMETERS   AGE
# traefik   traefik.io/ingress-controller   <none>       60d
```

### Install

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set traefik.enabled=false \
  --set mortise-core.operator.ingressClassName=traefik \
  --set platformConfig.domain=apps.example.com \
  --set platformConfig.tls.clusterIssuer=letsencrypt-prod
```

Everything else (cert-manager, registry, BuildKit) is bundled. The operator
uses your existing Traefik for routing.

### Configure containerd for the bundled registry

Since you're using the bundled registry with BuildKit, kubelet needs to
know about the localhost registry proxy. See
[Registry proxy setup](./install.md#registry-proxy-git-source-builds)
for distro-specific instructions.

### Verify

```bash
kubectl -n mortise-system rollout status deploy/mortise
kubectl -n mortise-deps get deploy
# NAME        READY   UP-TO-DATE   AVAILABLE
# buildkitd   1/1     1            1
# registry    1/1     1            1
```

---

## Scenario 3: Bare operator (mortise-core)

**Setup:** You provide everything — ingress controller, cert-manager,
registry, and BuildKit. You only want the Mortise operator, CRDs, and API.

### Install the operator

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace \
  --set operator.ingressClassName=nginx
```

`mortise-core` ships no PlatformConfig. You must create one manually.

### Apply PlatformConfig

```yaml
# platformconfig.yaml
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
    buildkitAddr: tcp://buildkitd.build-system.svc:1234

  tls:
    certManagerClusterIssuer: letsencrypt-prod
```

```bash
kubectl apply -f platformconfig.yaml
```

### GitOps users (Argo CD / Flux)

If you manage CRDs outside of Helm (e.g. via a separate Argo CD Application
or Flux Kustomization), skip CRD installation with `--skip-crds`:

```bash
helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace \
  --skip-crds \
  --set operator.ingressClassName=nginx
```

Apply CRDs from your GitOps pipeline instead. The raw CRD manifests are in
`charts/mortise-core/crds/`.

### Verify

```bash
kubectl -n mortise-system rollout status deploy/mortise
kubectl get platformconfig platform -o jsonpath='{.status.phase}'
# Ready
```

---

## Mixing bundled and external components

You can mix freely. Common combinations:

| You have | Disable | Set |
|----------|---------|-----|
| Own ingress, nothing else | `traefik.enabled=false` | `mortise-core.operator.ingressClassName=<class>` |
| Own ingress + cert-manager | `traefik.enabled=false`, `cert-manager.enabled=false` | `ingressClassName`, `platformConfig.tls.clusterIssuer` |
| Own registry (ECR/GHCR) | `registry.enabled=false` | `platformConfig.registry.url`, `.pullSecretRef` |
| Own BuildKit | `buildkit.enabled=false` | `platformConfig.build.buildkitAddr` |
| Own everything | Use `mortise-core` chart | Apply PlatformConfig manually |

---

## Troubleshooting

**PlatformConfig shows empty registry/build after install:**
Check that you set the `platformConfig.*` values. Run
`helm get values mortise -n mortise-system` to verify.

**Apps fail to build (git source) after disabling bundled BuildKit:**
Verify `platformConfig.build.buildkitAddr` is reachable from the operator
pod. The operator connects to BuildKit over gRPC.

**Image pulls fail after pointing to external registry:**
Ensure the `pullSecretRef` secret exists in `mortise-system` and contains
valid credentials. The operator copies it into app namespaces.

**TLS certificates not issued:**
Verify `platformConfig.tls.clusterIssuer` matches an existing
`ClusterIssuer` and that cert-manager is healthy.

See [Troubleshooting](./troubleshooting.md) for more.
