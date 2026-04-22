# Quickstart

**Prereq: Mortise is installed.** If you haven't installed yet, start at
[Install](./install.md): then come back here. This guide takes you from
"installer finished" to a running app in about 5 minutes.

## 1. Open the UI

The installer port-forwards the UI automatically. Open:

> **http://localhost:8090**

Installed via Helm directly? Port-forward yourself:

```bash
kubectl port-forward -n mortise-system svc/mortise 8090:80
```

## 2. Create your admin account

Enter an email and password on the first-run screen. You'll land on a
getting-started checklist. Skip it if you want; nothing on it is required
to deploy an app.

## 3. Create a project, then deploy your first app

Click **New Project**, enter a name, open that project, then click **Add**.

**Image deploy (simplest path):** Pick "Docker Image", enter `nginx:1.27`,
name it `web`, click Create. Your app is running in ~10 seconds.

**Database:** Pick "Database", choose Postgres 16. One click, running
database with auto-generated credentials and a service URL ready to bind
from other apps.

**Git repo:** Pick "Git Repository". If you haven't connected a git
provider, Settings will prompt you to authorize one (device flow for
GitHub, personal access token for GitLab/Gitea). Takes about a minute.

## 4. Access your app

Click the app on the canvas to open the drawer, then click **Open**: the
operator proxies to your pod through the API. No domain or TLS needed to
poke at it.

Want real URLs like `web.apps.example.com`? Set a platform domain in
**Settings → Platform Domain**. See
[Configuring your platform](./configuration.md) for DNS details.

## 5. Bind services together

Open an app → drawer → **Bindings** tab → Add. Pick the service you want
(e.g. the Postgres app you created). Mortise injects `DATABASE_URL`,
`DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_USERNAME`, and
`DATABASE_PASSWORD` env vars on your app: no secret wiring.

## Data persistence

Platform state (apps, projects, users, env vars, credentials) lives in
Kubernetes and survives pod and node reboots. Built images and the build
cache are on PVCs that also survive restarts.

**Disaster recovery** (hardware failure): the PVCs use your cluster's
default storage class: local disk on a single-node install. To survive
that, set up backups:

- **k3s quick path:** k3s snapshots its etcd every 12 hours; push them to
  S3 with `k3s etcd-snapshot save --s3 --s3-bucket=mortise-backups`. See
  the [k3s docs](https://docs.k3s.io/cli/etcd-snapshot).
- **Full backup (including app volumes):** Velero → S3. See
  [Backup recipe](./recipes/backup.md).

## Next steps

- [Configuration](./configuration.md): domain, git providers, HTTPS, storage
- [External CI deploys](./recipes/external-ci.md): deploy from GitHub Actions or any CI
- [Cloudflare Tunnel](./recipes/cloudflare-tunnel.md): expose without a public IP
- [Auth status](./recipes/oidc.md): current authentication support and roadmap note
