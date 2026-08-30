# Configuring your platform

After installing Mortise, you can deploy image-based apps with zero
configuration. This guide covers the optional settings that unlock
additional features: custom domains, git deploys, HTTPS, and more.

All configuration happens in **Settings** in the Mortise UI, or via the
`PlatformConfig` CRD / REST API.

## Platform domain

**What it does:** Gives your apps automatic URLs. When set to
`apps.example.com`, an app called `api` in project `backend` gets
`api-backend.apps.example.com` in production and
`api-backend-staging.apps.example.com` in staging. The project name is
included in the domain to prevent collisions when different projects have
apps with the same name.

**What it does NOT do:** This is the domain used to generate app
subdomains. It is not necessarily where Mortise itself is reachable. If
the Mortise UI/API lives at a different address (e.g. behind a tunnel,
on a different port, or at an IP), set the **External Domain** field
instead. See [External domain](#external-domain) below.

**When you need it:**
- You want apps to be reachable at real URLs (not just through the UI's
  built-in proxy)
- You want automatic push-to-deploy from git

**When you don't:**
- You're only deploying image-source apps and accessing them via
  `kubectl port-forward` during initial setup
- You plan to set up a domain later and just want to verify the operator
  is working first

### Setting it up

1. **Pick a domain** you control. A subdomain works best:
   `apps.example.com`, `deploy.mycompany.io`, etc.

2. **Create a DNS record** pointing at your cluster:

   | Scenario | Record type | Name | Value |
   |----------|------------|------|-------|
   | Single server (k3s) | A (wildcard) | `*.apps.example.com` | Your server's public IP |
   | Cloud load balancer (EKS, GKE) | CNAME (wildcard) | `*.apps.example.com` | Your load balancer hostname (e.g. `abc123.elb.amazonaws.com`) |
   | Behind Cloudflare Tunnel | CNAME (wildcard) | `*.apps.example.com` | Your tunnel ID `.cfargotunnel.com` |
   | LAN only (no public DNS) | A (wildcard) | `*.apps.local` | Your server's LAN IP (e.g. `192.168.1.100`). See [Internal DNS guide](./recipes/internal-dns.md) |

   **How to find your cluster's address:**
   - **k3s on a VPS/server:** Your server's public IP (check your hosting
     provider's dashboard, or run `curl -4 ifconfig.me`)
   - **k3s at home:** Your machine's LAN IP (`ip addr` or `ifconfig`). If
     you want public access from outside your network, set up port
     forwarding on your router (ports 80/443 → your server) or use a
     [Cloudflare Tunnel](./recipes/cloudflare-tunnel.md)
   - **EKS/GKE/AKS:** The external hostname or IP of your ingress
     controller's load balancer:
     ```bash
     kubectl get svc -n mortise-system
     # Look for the EXTERNAL-IP or hostname on the LoadBalancer service
     ```

   Most DNS providers (Cloudflare, Route53, DigitalOcean DNS, Namecheap)
   support wildcard records. A wildcard (`*.apps.example.com`) routes all
   subdomains to your cluster, so you don't need to create a new record
   every time you deploy an app.

   **Cloudflare Tunnel users:** You don't need a wildcard DNS record at all.
   The tunnel config routes traffic directly. Set a wildcard hostname in your
   tunnel's public hostname rules (`*.apps.example.com → http://traefik.mortise-system:80`)
   and Cloudflare handles the rest. See [Cloudflare Tunnel](./recipes/cloudflare-tunnel.md).

   **Optional: ExternalDNS.** If you prefer Mortise to create per-app DNS
   records automatically (instead of a wildcard), you can install
   [ExternalDNS](https://github.com/kubernetes-sigs/external-dns). Mortise
   annotates every app's Ingress with the hostname: ExternalDNS reads
   that annotation and creates the DNS record at your provider. This is a
   power-user option; most setups work fine with a wildcard record.

3. **Enter the domain** in Settings > Platform Domain and save.

### Domain template

The default domain pattern is:

```
{{.App}}-{{.Project}}{{if ne .Env "production"}}-{{.Env}}{{end}}.{{.Domain}}
```

This produces domains like `api-backend.apps.example.com` for production
and `api-backend-staging.apps.example.com` for staging.

You can customize the pattern by setting `domainTemplate` on your
PlatformConfig:

```yaml
apiVersion: mortise.mortise.dev/v1alpha1
kind: PlatformConfig
metadata:
  name: platform
spec:
  domain: apps.example.com
  domainTemplate: "{{.App}}.{{.Domain}}"  # restore old {app}.{domain} pattern
```

Available template variables:

| Variable | Description | Example value |
|----------|-------------|---------------|
| `{{.App}}` | App name | `api` |
| `{{.Project}}` | Project name | `backend` |
| `{{.Env}}` | Environment name | `production`, `staging` |
| `{{.Domain}}` | Platform domain | `apps.example.com` |

A few useful patterns:

| Pattern | Production result | Staging result |
|---------|-------------------|----------------|
| Default (see above) | `api-backend.apps.example.com` | `api-backend-staging.apps.example.com` |
| `{{.App}}.{{.Domain}}` | `api.apps.example.com` | `api.apps.example.com` |
| `{{.App}}.{{.Project}}.{{.Domain}}` | `api.backend.apps.example.com` | `api.backend.apps.example.com` |

Templates that omit `{{.Env}}` produce the same domain for every
environment. Use the `{{if ne .Env "production"}}` conditional from the
default template to differentiate environments while keeping production
domains clean.

If two apps in different projects produce the same hostname, the operator
rejects the second with a `DomainCollision` status condition. The default
template avoids this by including the project name.

### External domain

**What it does:** Tells Mortise where it is publicly reachable — the
address that git hosts, CI systems, and deploy tokens use to reach the
Mortise API. Webhook callbacks are sent to
`https://{externalDomain}/api/webhooks/{provider}`.

**When you need it:**
- Your app subdomain (`apps.example.com`) is different from where
  Mortise itself is reachable (e.g. `mortise.example.com`,
  `deploy.internal.mycompany.io`)
- You're running behind a Cloudflare Tunnel, reverse proxy, or NAT
  where the Mortise API is on a different hostname or port than the
  wildcard used for app routing

**It is required for automatic webhook registration.** When it is
empty, the App controller skips webhook registration and records a
`WebhookConfigured=False` condition; pushes then need a manual redeploy.
The app wildcard domain is not assumed to route to the Mortise API, so
Mortise never registers callbacks against it. If your platform domain
does also serve the Mortise API, set `externalDomain` to the same value.

Set it in **Settings > External Domain** in the UI, or via the
PlatformConfig CRD:

```yaml
spec:
  domain: apps.example.com          # used for app subdomains
  externalDomain: mortise.example.com  # where Mortise API is reachable
```

### Webhook reachability

For automatic push-to-deploy, your git host needs to reach the
**external domain** (or platform domain if external domain is not set):

- **github.com / gitlab.com:** The external domain must be reachable
  from the public internet. If you're behind NAT, use a Cloudflare
  Tunnel or similar.
- **Self-hosted Gitea / GitLab:** Only needs to reach Mortise over your
  local network. A LAN address like `deploy.local` or
  `192.168.1.100` works fine.
- **No webhooks:** You can always trigger deploys manually via the
  CLI (`mortise deploy`) or the deploy API. Webhooks are a convenience,
  not a requirement.

## Git provider

**What it does:** Connects Mortise to GitHub, GitLab, or Gitea so you
can deploy directly from a git repository with automatic push-to-deploy.

**When you need it:** You want to pick a repo and branch in the UI, and
have pushes automatically trigger builds and deploys.

**When you don't:** You're deploying pre-built container images, using
Docker Compose templates, or triggering deploys from your own CI via the
deploy webhook/API.

### Connecting a provider

Go to **Settings > Git Providers > Add Connection**.

**Option 1: Personal access token (all providers)**

The simplest method. Generate a token on your git host and paste it in.

| Provider | Where to create | Required scopes |
|----------|----------------|-----------------|
| GitHub | github.com > Settings > Developer settings > Personal access tokens | `repo`, `admin:repo_hook`, `read:org` |
| GitLab | gitlab.com (or your instance) > Preferences > Access Tokens | `api` |
| Gitea | Your instance > Settings > Applications > Access Tokens | `repo` (or all) |

**Option 2: Device flow (GitHub only)**

Click "Device Flow" and you'll get a one-time code. Open github.com/login/device
in your browser, paste the code, and authorize. Mortise polls until you're done.

This requires a GitHub OAuth App client ID. If the Helm chart was installed
with `github.clientID` set, it works automatically. If not, you can create
an OAuth App on GitHub (Settings > Developer settings > OAuth Apps) and
add the client ID in the Mortise Helm values.

### Per-user tokens

Git tokens are per-user, not per-platform. Each user on your Mortise
instance connects their own account. This means each user's API calls
use their own rate limits and permissions.

## HTTPS and TLS

**What it does:** Automatic TLS certificates for your app URLs via
cert-manager and an ACME provider (Let's Encrypt, ZeroSSL, etc.).

**When you need it:** You want `https://` URLs for your apps.

**Prerequisites:** cert-manager must be installed in your cluster. If you
used the Mortise Helm chart's bundled dependencies, it's already there.
Otherwise:

```bash
helm repo add jetstack https://charts.jetstack.io
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true
```

Create a ClusterIssuer (one-time):

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: your-email@example.com
    privateKeySecretRef:
      name: letsencrypt-prod-key
    solvers:
      - http01:
          ingress: {}
```

Then set the issuer name in **Settings > TLS** (e.g. `letsencrypt-prod`).

## Storage

**What it does:** Sets the default storage class for persistent volumes
(databases, file uploads, etc.).

Most clusters already have a default storage class. Mortise uses it
automatically. Only change this if you want to override it: for example,
to use a specific NFS provisioner or a particular cloud disk type.

Check your cluster's available storage classes:

```bash
kubectl get storageclass
```

The one marked `(default)` is what Mortise will use unless you override it.

## Image registry

Mortise includes a bundled OCI registry for storing images built from git
source. A DaemonSet proxy runs on every node so kubelet can pull images
via `localhost:30500` without needing cluster-internal DNS resolution.

Most container runtimes need a small config snippet to allow HTTP pulls
from localhost. See [Installing Mortise > Registry proxy](./install.md#registry-proxy-git-source-builds)
for per-distro instructions.

**Changing the proxy port:** Set `registry.proxy.hostPort` in your Helm
values (default is `30500`). The bundled chart value lives at
`charts/mortise/values.yaml` under `registry.proxy.hostPort`.

The same value is used to generate `PlatformConfig.spec.registry.pullURL`
(`localhost:<hostPort>`), so kubelet pulls and deployed image refs stay in
sync.

**Using an external registry:** If you want builds pushed to an external
registry (Docker Hub, GitHub Container Registry, Harbor, ECR, etc.),
set `registry.enabled: false` in your Helm values and configure
`PlatformConfig.spec.registry` to point at your registry. The DaemonSet
proxy is not deployed when the bundled registry is disabled.

**Registry and build changes require an operator restart.** The operator
reads `spec.registry` and `spec.build` once at startup. After editing
them, restart it:

```
kubectl -n mortise-system rollout restart deployment/mortise
```

The PlatformConfig's status reports this state: `ConfigApplied=False`
with reason `RestartRequired` means the running operator booted with
different settings (or started before the PlatformConfig existed) and a
restart is needed. `RegistryPullConfig=False` with reason
`PullURLMissing` means `spec.registry.url` is a cluster-internal address
(`*.svc` / `*.svc.cluster.local`) but `spec.registry.pullURL` is empty —
kubelet cannot resolve cluster-internal DNS, so fresh deploys would fail
to pull. Check with:

```
kubectl get platformconfig platform -o jsonpath='{.status.conditions}'
```

## Environments

Every project starts with a **production** environment. You can add more
(staging, development, preview) in **Project Settings > Environments**.

Each environment is a separate, isolated space where your apps run. They
get their own copies of services, databases, and configuration. An app
deployed to staging doesn't affect production.

Environment-specific settings (replicas, resources, env vars, domains) can
be set per app in the app drawer.

### Cloning an environment

You can create a new environment by cloning an existing one. Cloning copies
the full configuration for every app in the project:

- **CRD-level overrides:** replicas, resources, probes, schedule, annotations
- **Environment variables:** both CRD-declared vars and vars set through the
  UI/API (stored in Secrets)
- **Bindings:** binding references are copied, but binding-sourced env vars
  are excluded — the controller re-resolves them in the new namespace

Clone via the API:

```bash
curl -s -X POST "$BASE/api/projects/$PROJECT/environments/$SOURCE_ENV/clone" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"staging\",\"displayOrder\":1}" | jq
```

If the target environment already exists on the project, the API returns
`409 Conflict` instead of treating the call as a retry-safe replay.

### Preview environments

When preview environments are enabled on a project (Project Settings >
Preview), opening a pull request creates a `pr-{number}` clone
environment on the project. The clone copies configuration from the
source environment (defaults to "staging"):

- **Per-app env vars** from the source env's Secret
- **Shared env vars** from the source env's `shared-env` Secret
- **Per-app overrides** (replicas, resources, bindings, probes) cloned from the source env
- **Branch override** set to the PR branch for each git-source app

Every app in the project fans out into the preview environment through
the normal deployment path — previews are real environments, not a
separate system. You can edit env vars, replicas, and other settings on
a preview environment the same way you would any other environment;
edits are preserved across rebuilds.

When the PR closes, the preview environment and its namespace are
deleted. There is no TTL — previews exist until the PR is closed.

To configure which environment previews clone from, set the source
environment in Project Settings > Preview.

## Security profile

By default Mortise leaves the generated pod's `securityContext` unset, so
images that run as root keep working. A cluster that enforces the
PodSecurity `restricted` profile on a namespace rejects such pods.

```yaml
spec:
  securityProfile: restricted
```

sets `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, drops all
capabilities, and uses the `RuntimeDefault` seccomp profile — the
`restricted` minimum. The image must run as a non-root user (a `USER`
instruction, or an unprivileged variant such as
`nginxinc/nginx-unprivileged`); otherwise the kubelet refuses to start
the container with `container has runAsNonRoot and image will run as
root`. There is no cluster-wide default: opt in per App.
