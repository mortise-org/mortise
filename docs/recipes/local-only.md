# Local-Only Deployment (No DNS / No TLS / No Ingress)

Mortise works fully without Traefik, DNS, or TLS by using `kubectl
port-forward` to access apps directly. This is useful for:

- Local development on k3d / kind / minikube
- Testing without DNS configuration
- Air-gapped or LAN-only environments
- Evaluating Mortise before committing to domain setup

---

## Install with ingress disabled

### Quick install

The standard quick install works out of the box for local use — it
installs Traefik but you can access everything via `localhost:8090`
without DNS:

```bash
curl -fsSL https://mortise.me/install | bash
```

### Helm (minimal, no ingress/TLS)

If you want the lightest possible install:

```bash
helm install mortise mortise/mortise \
  --namespace mortise-system --create-namespace \
  --set traefik.enabled=false \
  --set cert-manager.enabled=false
```

Apps won't get automatic public URLs, but they'll build and deploy
normally. You access them via port-forward.

---

## Accessing the Mortise UI

```bash
kubectl port-forward -n mortise-system svc/mortise 8090:80
# open http://localhost:8090
```

---

## Accessing deployed apps

Every app gets a Service in its environment namespace. The namespace
follows the pattern `pj-{project}-{env}` and the service is named
after the app.

```bash
# Format: kubectl port-forward -n pj-{project}-{env} svc/{app} {local-port}:80
kubectl port-forward -n pj-my-project-production svc/my-app 3000:80
# open http://localhost:3000
```

To find the exact service name and port:

```bash
kubectl get svc -n pj-my-project-production
```

---

## Skipping the domain step in the wizard

When you first open the Mortise UI, the setup wizard asks for a platform
domain. This step is optional — you can leave it blank or skip it
entirely. Apps will still build and deploy; they just won't get
automatic `{app}.{domain}` URLs.

You can set the domain later in **Settings > Platform** when you're
ready to configure DNS.

---

## Using the CLI with port-forward

The Mortise CLI connects to the API at whatever address you specify
during `mortise login`:

```bash
mortise login --server http://localhost:8090
```

All CLI commands (`mortise app list`, `mortise deploy`, etc.) work
normally over the port-forwarded connection.

---

## Limitations

- **No automatic URLs:** Without an ingress controller and DNS, apps
  have no public hostname. Each app requires a manual port-forward.
- **No webhooks:** Git push-to-deploy requires the git provider to
  reach your cluster via HTTPS. Without a public URL, webhooks won't
  arrive. You can still trigger deploys manually via the UI or CLI.
- **No TLS:** Port-forwarded connections are plain HTTP over localhost.
  This is fine for local development but not for production.
- **One port-forward per app:** Each app you want to access
  simultaneously needs its own local port.

---

## When to upgrade to full DNS

Move to the full setup when you want:
- Automatic `{app}.{domain}` URLs
- Push-to-deploy via git webhooks
- TLS certificates
- Preview environments (require webhook delivery)

See [Configuration > Platform domain](../configuration.md) to add a
domain later without reinstalling.
