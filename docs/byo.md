# BYO Install Scenarios

Use these examples when you already run parts of the platform yourself. The
`mortise` umbrella chart can still create a `PlatformConfig` for you: set the
external endpoints under `platformConfig.*` and disable only the bundled
components you do not want.

You can also start from the commented example file:

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  -f charts/mortise/values-byo.yaml
```

## Managed Kubernetes with Own Ingress, cert-manager, and Registry

This fits EKS, GKE, AKS, or any cluster where ingress and cert-manager are
already installed. Mortise uses your registry and the bundled BuildKit.

Prereqs:

- An ingress controller such as nginx, ALB, or Gateway-backed ingress.
- A cert-manager `ClusterIssuer`, for example `letsencrypt-prod`.
- A wildcard DNS record for `*.apps.example.com` pointing at your ingress.
- A docker-registry Secret named `registry-pull` in `mortise-system`.

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set platformConfig.domain=apps.example.com \
  --set platformConfig.externalDomain=mortise.example.com \
  --set platformConfig.registry.url=https://ghcr.io/acme \
  --set platformConfig.registry.namespace=mortise \
  --set platformConfig.registry.pullSecretRef=registry-pull \
  --set platformConfig.tls.clusterIssuer=letsencrypt-prod \
  --set traefik.enabled=false \
  --set cert-manager.enabled=false \
  --set registry.enabled=false \
  --set buildkit.enabled=true \
  --set mortise-core.operator.ingressClassName=nginx \
  --set mortise-core.ingress.enabled=true \
  --set mortise-core.ingress.className=nginx \
  --set mortise-core.ingress.host=mortise.example.com
```

After install, create an image App with host `web.apps.example.com` or a
git-source App that builds through bundled BuildKit and pushes to your
registry.

## On-Prem Cluster with Existing Traefik

Use this when Traefik is already the cluster ingress controller, but you want
Mortise to install cert-manager, the registry, and BuildKit.

Prereqs:

- Existing Traefik `IngressClass` named `traefik`.
- Wildcard DNS for `*.apps.example.com` pointing at Traefik.
- Traefik JSON access logs enabled if you want observer traffic metrics.

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set platformConfig.domain=apps.example.com \
  --set platformConfig.externalDomain=mortise.example.com \
  --set platformConfig.tls.clusterIssuer=letsencrypt-prod \
  --set traefik.enabled=false \
  --set cert-manager.enabled=true \
  --set registry.enabled=true \
  --set buildkit.enabled=true \
  --set mortise-core.operator.ingressClassName=traefik \
  --set mortise-core.ingress.enabled=true \
  --set mortise-core.ingress.className=traefik \
  --set mortise-core.ingress.host=mortise.example.com
```

The bundled registry and BuildKit are addressed through in-cluster service
defaults because their external values are empty.

## Bare Operator with mortise-core

Use `mortise-core` when you provide every infrastructure dependency yourself.
This chart installs the operator, RBAC, Service, and CRDs, but intentionally
does not create a `PlatformConfig`.

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update

helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace \
  --set operator.ingressClassName=nginx \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.host=mortise.example.com
```

If your GitOps controller manages CRDs out-of-band:

```bash
helm install mortise mortise/mortise-core \
  --namespace mortise-system --create-namespace \
  --set crds.install=false
```

Apply the PlatformConfig yourself:

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
    buildkitAddr: tcp://buildkit.example.com:1234
  tls:
    certManagerClusterIssuer: letsencrypt-prod
```

With `domain: apps.example.com`, a public image App named `web` receives an
Ingress host under that domain, for example `http://web.apps.example.com` or
`https://web.apps.example.com` when TLS is configured.
