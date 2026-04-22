# Quickstart

Go from nothing to a deployed app in under 10 minutes.

## 1. Install Mortise

One command — installs k3s (or k3d on macOS + Docker Desktop), Helm, and
the full stack, then port-forwards the UI to `localhost:8090`:

```bash
curl -fsSL https://mortise.me/install | bash
```

**Already have a cluster?** Skip the installer and helm install directly:

```bash
helm repo add mortise https://mortise-org.github.io/mortise
helm repo update
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace
kubectl port-forward -n mortise-system svc/mortise 8090:80
```

See [Creating a cluster](./cluster-setup.md) if you need help getting a
cluster (k3s on a server, EKS, GKE, AKS).

## 2. Open the UI

Open **http://localhost:8090**.

## 3. Create your admin account

Enter an email and password. You'll see a getting-started checklist —
everything on it is optional. Click through to the dashboard.

## 4. Deploy your first app

Click into the **default** project, then click **Add**.

**Image deploy (simplest):** Pick "Docker Image", enter `nginx:1.27`,
name it `web`, click Create. Your app is running in about 10 seconds.

**Database:** Pick "Database", choose Postgres 16. One click, running
database with auto-generated credentials.

**From git:** Pick "Git Repository". If you haven't connected a git
provider yet, you'll see a prompt to do so in Settings — takes about
a minute with a personal access token.

## 5. Access your app

Click the app on the canvas to open the drawer. Click **Open** — Mortise
proxies to your running app through the API. No domain configuration needed.

If you want real URLs (e.g. `web.apps.example.com`), set a platform domain
in **Settings > Platform Domain**. See [Configuring your platform](./configuration.md)
for DNS setup details.

## Data persistence

Your data is safe across restarts. All platform state (apps, projects,
users, env vars, credentials) is stored in Kubernetes and survives pod
and node reboots. Built container images and the build cache are stored
on persistent volumes that also survive restarts.

**Disaster recovery** (server dies, disk fails): The persistent volumes
use your cluster's default storage, which on a single server is local disk.
To survive hardware failure, set up backups:

- **Simplest (k3s):** k3s auto-snapshots its database every 12 hours.
  Push snapshots to S3 with one command:
  ```bash
  k3s etcd-snapshot save --s3 --s3-bucket=mortise-backups \
    --s3-endpoint=s3.amazonaws.com
  ```
  This covers all platform state. See the
  [k3s docs](https://docs.k3s.io/cli/etcd-snapshot) for scheduling.

- **Full backup (including app data):** Use Velero to back up everything
  — Kubernetes objects and persistent volumes — to S3. See
  [Backup with Velero](./recipes/backup.md).

## What's next

- [Configuration guide](./configuration.md) — domain, git providers, HTTPS, storage
- [External CI deploys](./recipes/external-ci.md) — deploy from GitHub Actions or any CI
- [Cloudflare Tunnel](./recipes/cloudflare-tunnel.md) — access without a public IP
- [OIDC / SSO](./recipes/oidc.md) — single sign-on with Authentik, Keycloak, etc.
