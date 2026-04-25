# Internal DNS: Reaching Apps on Your LAN Without Public DNS

Mortise assigns each app a domain like `myapp.apps.local`. For that to
resolve on your network, something needs to answer DNS queries for
`*.apps.local` with your cluster's IP address. This guide covers the
most common ways to set that up on a private network.

All approaches follow the same pattern:

1. Find your cluster's ingress IP (the machine or load balancer running
   Traefik or your ingress controller).
2. Configure a wildcard DNS record: `*.{your-platform-domain}` → that IP.
3. Set the platform domain in Mortise (Settings > Platform Domain).

## Find your ingress IP

```bash
# k3s single node: your machine's LAN IP
hostname -I | awk '{print $1}'

# k3s/k3d with Traefik (bundled chart):
kubectl get svc traefik -n mortise-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}'

# If that's empty (common on bare-metal), use the node IP:
kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}'
```

In the examples below, we'll use `192.168.1.50` as the ingress IP and
`apps.local` as the platform domain. Substitute your own values.

---

## Pi-hole

Pi-hole is the most common DNS server in homelabs. If you already run
one, this takes 30 seconds.

1. Open Pi-hole admin (usually `http://pi.hole/admin`).
2. Go to **Local DNS > DNS Records**.
3. Add: `apps.local` → `192.168.1.50`.
4. Go to **Local DNS > CNAME Records**.
5. Add: `*.apps.local` → `apps.local`.

Every device on your network that uses Pi-hole as its DNS server will
now resolve `myapp.apps.local` to your cluster.

> **Note:** Pi-hole's CNAME wildcard support depends on the underlying
> dnsmasq. If wildcards don't work on your version, use the dnsmasq
> method below instead (Pi-hole uses dnsmasq under the hood).

## AdGuard Home

1. Open AdGuard Home admin.
2. Go to **Filters > DNS rewrites**.
3. Add a rewrite: `*.apps.local` → `192.168.1.50`.

AdGuard supports wildcard rewrites natively. One rule covers all apps.

## dnsmasq (standalone or inside Pi-hole)

If you run dnsmasq directly, or your Pi-hole wildcard isn't working:

```bash
# Add to /etc/dnsmasq.d/mortise.conf (or /etc/dnsmasq.conf)
address=/apps.local/192.168.1.50
```

```bash
sudo systemctl restart dnsmasq
```

The `address=` directive is a wildcard by default: `apps.local` and
every subdomain under it resolve to the specified IP.

## Unbound

If you run Unbound as a recursive resolver:

```yaml
# Add to /etc/unbound/unbound.conf.d/mortise.conf
server:
    local-zone: "apps.local." redirect
    local-data: "apps.local. IN A 192.168.1.50"
```

```bash
sudo systemctl restart unbound
```

The `redirect` zone type makes all names under `apps.local` return the
same A record.

## CoreDNS (in-cluster)

If you prefer DNS resolution within the cluster itself (useful when
other in-cluster services need to reach apps by their public hostname):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  mortise.server: |
    apps.local {
      template IN A {
        answer "{{ .Name }} 60 IN A 192.168.1.50"
      }
    }
```

Restart CoreDNS to pick up the custom config. Note: this only affects
in-cluster DNS resolution. Devices on your LAN still need one of the
other methods.

## Router DNS (UniFi, OPNsense, pfSense, OpenWrt)

Most homelab routers can serve as a local DNS server. The exact steps
vary:

**UniFi (Network controller):**
Settings > Networks > (your network) > DHCP > DNS Server → set to the
UniFi gateway IP if not already. Then Settings > Gateway > DNS >
Static DNS entries: add `*.apps.local` → `192.168.1.50`. (Exact menu
paths vary by UniFi OS version.)

**OPNsense / pfSense:**
Services > Unbound DNS > Host Overrides. Add an entry for `apps.local`
pointing to `192.168.1.50`. Enable "Wildcard" if the option exists.
Otherwise, add Unbound custom config (Advanced > Custom options):
```
server:
local-zone: "apps.local." redirect
local-data: "apps.local. IN A 192.168.1.50"
```

**OpenWrt:**
OpenWrt uses dnsmasq by default. SSH in and add to `/etc/dnsmasq.conf`:
```
address=/apps.local/192.168.1.50
```
Then `/etc/init.d/dnsmasq restart`.

## /etc/hosts (single machine, no DNS server)

The simplest option when you're the only user and don't want to run a
DNS server. Add entries to your machine's hosts file:

```bash
# Linux / macOS: /etc/hosts
# Windows: C:\Windows\System32\drivers\etc\hosts

192.168.1.50  myapp.apps.local
192.168.1.50  postgres.apps.local
192.168.1.50  frontend.apps.local
```

Downsides: you must add a line per app, it doesn't work on other devices,
and you'll forget to update it when you add new apps. Use this as a
stopgap, not a long-term solution.

## Tailscale / MagicDNS

If your cluster node and your devices are all on Tailscale:

1. In the Tailscale admin console, go to **DNS > Nameservers**.
2. Add a **Split DNS** entry: domain `apps.local`, nameserver = your
   Pi-hole/AdGuard/dnsmasq IP on the Tailscale network (the `100.x.y.z`
   address).
3. Or, if you don't run a separate DNS server, use Tailscale's `--accept-dns`
   and advertise the cluster node's Tailscale IP as the target for
   `*.apps.local` via a custom BIND/dnsmasq on that node.

This lets you reach your apps from anywhere on your tailnet, not just
your home LAN.

## Choosing a platform domain name

For internal-only use, pick a domain that won't collide with real
internet domains:

| Domain | Notes |
|--------|-------|
| `*.apps.local` | `.local` is reserved for mDNS but works fine with explicit DNS servers. Avoid if you use Avahi/Bonjour. |
| `*.apps.home.arpa` | IETF-reserved for home networks ([RFC 8375](https://www.rfc-editor.org/rfc/rfc8375)). The "correct" choice. |
| `*.apps.internal` | `.internal` is IANA-reserved for private use ([RFC 6762 successor](https://www.iana.org/domains/reserved)). Clean and unlikely to collide. |
| `*.apps.yourdomain.com` | Use a subdomain of a real domain you own. Works even if the subdomain only resolves internally. No collision risk. |

Avoid `.dev` (HSTS-preloaded, browsers force HTTPS), `.test`, `.example`,
or `.localhost` (all have special browser/OS behavior).

## TLS on internal networks

By default, internal apps are served over plain HTTP. If you want HTTPS:

**Option A: Self-signed CA.** Create a local CA, configure cert-manager
with a CA ClusterIssuer, and install the CA cert on your devices. This
is the most common homelab approach.

**Option B: Let's Encrypt with DNS-01.** If your platform domain is a
subdomain of a real domain you own (e.g. `apps.yourdomain.com`), you
can use DNS-01 challenges to get real certs even for internal-only
services. Configure a cert-manager ClusterIssuer with your DNS provider's
API credentials (Cloudflare, Route53, etc.).

**Option C: No TLS.** For home use, HTTP is fine. Browsers will show a
"not secure" warning but everything works.

## Verify it works

After configuring DNS and setting the platform domain in Mortise:

```bash
# Check DNS resolution from your machine:
nslookup myapp.apps.local
# Should return 192.168.1.50

# Check the Ingress exists in your cluster:
kubectl get ingress -A | grep myapp

# Hit the app directly:
curl -H "Host: myapp.apps.local" http://192.168.1.50/
```

If `nslookup` resolves but the app doesn't load, the issue is ingress
routing (check `kubectl describe ingress` and your ingress controller
logs), not DNS.
