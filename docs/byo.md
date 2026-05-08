# Bring Your Own Infrastructure

This guide walks through installing Mortise when you already have some or all
of the infrastructure it normally bundles (ingress controller, cert-manager,
container registry, BuildKit).

**Prerequisites for all scenarios:**

```bash
kubectl auth can-i '*' '*' --all-namespaces   # cluster-admin access
helm version --short                           # Helm 3.12+
kubectl get storageclass                       # at least one StorageClass
```

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update
```

---

## Scenario 1: Managed Kubernetes with own ingress + cert-manager

**Setup:** EKS / GKE / AKS cluster. You already run an ingress controller
(nginx, ALB, etc.) and cert-manager. You want to use an external registry
(ECR, GHCR, or Harbor) but keep the bundled BuildKit for git-source builds.

### 1. Create a registry pull secret

Mortise needs to pull built images from your registry. Create a standard
`docker-registry` Secret:

```bash
kubectl create namespace mortise-system

kubectl create secret docker-registry registry-pull \
  --namespace mortise-system \
  --docker-server=https://ghcr.io \
  --docker-username=YOUR_USERNAME \
  --docker-password=YOUR_TOKEN
```

### 2. Install

```bash
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

### 3. Expose the Mortise UI (optional)

```bash
helm upgrade mortise mortise/mortise \
  --namespace mortise-system --reuse-values \
  --set mortise-core.ingress.enabled=true \
  --set mortise-core.ingress.className=nginx \
  --set mortise-core.ingress.host=mortise.example.com
```

### 4. Verify

```bash
kubectl -n mortise-system rollout status deploy/mortise
kubectl get platformconfig platform -o yaml
```

Deploy an image app through the UI or CLI. It should be reachable at
`http://<app>.apps.example.com` (or HTTPS if your ClusterIssuer is
configured).

---

## Scenario 2: On-prem cluster with Traefik already running

**Setup:** Bare-metal or VM-based cluster (k3s, RKE2, kubeadm). Traefik
is already running as the ingress controller. You want to use the chart's
cert-manager, bundled registry, and BuildKit.

### 1. Install

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set traefik.enabled=false \
  --set mortise-core.operator.ingressClassName=traefik \
  --set platformConfig.domain=apps.example.com \
  --set platformConfig.tls.clusterIssuer=letsencrypt-prod
```

Everything else stays at defaults: the chart deploys cert-manager, the
in-cluster OCI registry, and BuildKit.

### 2. Configure the registry proxy

Since the bundled registry is enabled, kubelet on each node needs to know
about the `localhost:30500` pull-through proxy. See
[Registry proxy](./install.md#registry-proxy-git-source-builds) for
per-distro instructions.

### 3. Create a ClusterIssuer (if you don't have one)

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: you@example.com
    privateKeySecretRef:
      name: letsencrypt-prod-key
    solvers:
      - http01:
          ingress:
            ingressClassName: traefik
```

```bash
kubectl apply -f clusterissuer.yaml
```

### 4. Verify

```bash
kubectl -n mortise-system rollout status deploy/mortise
kubectl -n mortise-deps get deploy           # registry + buildkitd
kubectl get platformconfig platform -o yaml
```

Deploy an image app and confirm it's reachable at
`http://<app>.apps.example.com`.

---

## Scenario 3: Bare operator (mortise-core only)

**Setup:** You provide everything: ingress controller, cert-manager,
container registry, and BuildKit. You only want the Mortise operator, CRDs,
and API.

### 1. Install mortise-core

```bash
helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace \
  --set operator.ingressClassName=nginx
```

`mortise-core` ships no PlatformConfig, no registry, no BuildKit, no
Traefik, no cert-manager.

**GitOps users:** If you manage CRDs out-of-band (Argo CD, Flux), skip
chart-managed CRDs:

```bash
helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace \
  --skip-crds
```

### 2. Create a registry pull secret

```bash
kubectl create secret docker-registry registry-pull \
  --namespace mortise-system \
  --docker-server=https://registry.example.com \
  --docker-username=YOUR_USERNAME \
  --docker-password=YOUR_TOKEN
```

### 3. Apply a PlatformConfig

`mortise-core` does not auto-create a PlatformConfig. Apply one manually:

```yaml
apiVersion: mortise.mortise.dev/v1alpha1
kind: PlatformConfig
metadata:
  name: platform
spec:
  domain: apps.example.com
  externalDomain: mortise.example.com
  registry:
    url: https://registry.example.com/mortise
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

### 4. Verify

```bash
kubectl -n mortise-system rollout status deploy/mortise
kubectl get platformconfig platform -o jsonpath='{.status.phase}'
# Should print: Ready
```

Deploy an image app. It should get an Ingress at
`<app>-<project>.apps.example.com` with a TLS cert from your ClusterIssuer.

---

## Values reference (BYO-specific)

These values live under `platformConfig` in the umbrella chart and control
what goes into the auto-created PlatformConfig resource. When a field is
empty and the corresponding bundled component is enabled, the in-cluster
default is used.

| Value | Default | Description |
|-------|---------|-------------|
| `platformConfig.registry.url` | `""` | External OCI registry URL (e.g. `https://ghcr.io/my-org`) |
| `platformConfig.registry.namespace` | `""` | Registry namespace for built images |
| `platformConfig.registry.insecureSkipTLSVerify` | `false` | Skip TLS verification (dev only) |
| `platformConfig.registry.pullSecretRef` | `""` | Name of an existing `docker-registry` Secret |
| `platformConfig.build.buildkitAddr` | `""` | External BuildKit address (e.g. `tcp://buildkit:1234`) |
| `platformConfig.tls.clusterIssuer` | `""` | cert-manager ClusterIssuer name |

An example values file with all BYO toggles is included in the chart:
[`values-byo.yaml`](../charts/mortise/values-byo.yaml).
