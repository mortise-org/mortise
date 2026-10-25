# Cloudflare Tunnel: Access Mortise Without a Public IP

Cloudflare Tunnel (cloudflared) creates an outbound-only connection from
your cluster to Cloudflare's edge. Traffic reaches your Mortise apps via
Cloudflare without exposing any ports or requiring a static IP. Ideal for
homelabs and NAT'd networks.

## Prerequisites

- A Cloudflare account with a domain.
- A Cloudflare Tunnel token. Create one at
  [Cloudflare Zero Trust > Networks > Tunnels](https://one.dash.cloudflare.com/).

## Create the tunnel

1. In the Cloudflare dashboard, create a new tunnel. Name it (e.g., `mortise`).
2. Copy the tunnel token.
3. Add a public hostname rule:
   - Subdomain: `*` (wildcard) or specific subdomains
   - Domain: `yourdomain.com`
   - Service: `http://mortise-traefik.mortise-deps:80` (or your ingress
     service). The umbrella chart installs its Traefik into `mortise-deps`
     with the release-prefixed name `mortise-traefik`, not into
     `mortise-system`.

## Deploy cloudflared

Create a Secret with the tunnel token:

```bash
kubectl create secret generic cloudflared-token \
  -n mortise-system \
  --from-literal=token=<your-tunnel-token>
```

Deploy cloudflared as a Deployment:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cloudflared
  namespace: mortise-system
spec:
  replicas: 2
  selector:
    matchLabels:
      app: cloudflared
  template:
    metadata:
      labels:
        app: cloudflared
    spec:
      containers:
        - name: cloudflared
          image: cloudflare/cloudflared:2024.12.2
          # Do not pass the token as `--token $(TUNNEL_TOKEN)`: the kubelet
          # expands it into the container's argv, where any process on the
          # node can read it via `ps`. cloudflared picks up the TUNNEL_TOKEN
          # environment variable on its own.
          args:
            - tunnel
            - --no-autoupdate
            - run
          env:
            - name: TUNNEL_TOKEN
              valueFrom:
                secretKeyRef:
                  name: cloudflared-token
                  key: token
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 200m
              memory: 128Mi
```

```bash
kubectl apply -f cloudflared.yaml
```

## Alternatively: deploy as a Mortise App

Since cloudflared is just a container, you can deploy it as a Mortise App:

```bash
mortise app create --name cloudflared \
  --project infra \
  --image cloudflare/cloudflared:2024.12.2
```

cloudflared only makes outbound connections, so it needs no ingress or
domain. Open the app, and under **Settings → Networking** turn off **Public**.
Then set the tunnel token in the **Variables** tab.

## DNS setup

If you configured a wildcard hostname in the tunnel, Cloudflare
automatically proxies `*.yourdomain.com` through the tunnel to your
cluster's ingress. No additional DNS records are needed: the tunnel
config handles routing.

For non-wildcard setups, add CNAME records in Cloudflare DNS pointing
to the tunnel:

```
app.yourdomain.com  CNAME  <tunnel-id>.cfargotunnel.com
```

ExternalDNS is **not required** for Cloudflare Tunnel setups. The tunnel
itself handles traffic routing.

## Verify

```bash
# Check cloudflared pods are running
kubectl get pods -n mortise-system -l app=cloudflared

# Check tunnel status in Cloudflare dashboard
# Visit your app's URL: it should resolve through the tunnel
curl -I https://myapp.yourdomain.com
```

## Further reading

- [Cloudflare Tunnel docs](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
- [cloudflared on Kubernetes](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/deploy-tunnels/deployment-guides/kubernetes/)
