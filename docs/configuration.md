# Configuring your platform

After installing Mortise, you can deploy image-based apps with zero
configuration. This guide covers the optional settings that unlock
additional features: custom domains, git deploys, HTTPS, and more.

All configuration happens in **Settings** in the Mortise UI, or via the
`PlatformConfig` CRD / REST API.

## Platform domain

**What it does:** Gives your apps automatic URLs. When set to
`apps.example.com`, an app called `api` gets `api.apps.example.com`
in production and `api-staging.apps.example.com` in staging.

**What it also does:** Serves as the callback address for git webhooks.
When you push to a connected repo, your git host sends a notification to
`https://apps.example.com/api/webhooks/{provider}` to trigger a build.

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

### Webhook reachability

For automatic push-to-deploy, your git host needs to reach your domain:

- **github.com / gitlab.com:** Your domain must be reachable from the
  public internet. If you're behind NAT, use a Cloudflare Tunnel or
  similar.
- **Self-hosted Gitea / GitLab:** Only needs to reach Mortise over your
  local network. A LAN address like `apps.local` or `192.168.1.100`
  works fine.
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

## Environments

Every project starts with a **production** environment. You can add more
(staging, development, preview) in **Project Settings > Environments**.

Each environment is a separate, isolated space where your apps run. They
get their own copies of services, databases, and configuration. An app
deployed to staging doesn't affect production.

Environment-specific settings (replicas, resources, env vars, domains) can
be set per app in the app drawer.
