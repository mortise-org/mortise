# Troubleshooting

Known issues and fixes encountered when running Mortise on real clusters.

---

## BuildKit cache on NFS

**Symptom:** Git-source builds are extremely slow. BuildKit pod shows high
I/O wait. Builds that should take 30 seconds take 5+ minutes.

**Cause:** The default `buildkit.storage: pvc` creates a PersistentVolumeClaim
using the cluster's default StorageClass. If that default is an NFS-based
provisioner (e.g. `nfs-subdir-external-provisioner`), BuildKit's layer cache
performs terribly — it does massive small-file I/O that NFS is not designed for.

**Quick fix:** Switch BuildKit to `emptyDir`, which uses fast local node disk:

```yaml
# values.yaml override
buildkit:
  storage: emptyDir
```

```bash
helm upgrade mortise mortise/mortise \
  --namespace mortise-system \
  --set buildkit.storage=emptyDir
```

The tradeoff: the layer cache is lost on pod restart, so the first build after
a restart is a cold build. In practice this is still much faster than NFS-backed
cache because every layer operation hits local disk instead of the network.

**Longer-term:** The chart's PVC template does not currently expose a
`storageClassName` override. If your cluster has a local-disk StorageClass
(e.g. `local-path`, `openebs-hostpath`, `topolvm-provisioner`), you'd need
to either:

1. Make that StorageClass the cluster default, or
2. Add a `buildkit.storageClassName` value to the chart (not yet implemented —
   contributions welcome)

This would give you persistent cache on fast local disk — the best of both
worlds.

---

## Registry unreachable from kubelet

**Symptom:** Git-source apps build successfully but pods stay in
`ImagePullBackOff` or `ErrImagePull`. The error message says the registry
hostname can't be resolved or the connection is refused.

**Cause:** Mortise's bundled OCI registry runs as a ClusterIP service at
`registry.mortise-deps.svc:5000`. BuildKit can reach it (BuildKit runs as a
pod with access to cluster DNS), but kubelet/containerd runs on the **host
network** — it uses host DNS, not CoreDNS, so it can't resolve
`.svc` names. Even if DNS resolved, containerd defaults to HTTPS but the
registry is HTTP-only.

**How Mortise solves this:** The chart deploys a DaemonSet registry proxy on
every node. Each proxy is a `distribution/distribution` instance in
pull-through cache mode with a `hostPort` (default 30500). The operator
writes `localhost:30500/mortise/app:tag` into Deployment image specs, so
kubelet pulls from the node-local proxy, which forwards to the main registry
via cluster DNS.

**What you still need:** Most container runtimes need to be told that
`localhost:30500` is an HTTP registry. containerd has a built-in rule that
treats `localhost` as HTTP — but some distros (k3s, Talos, RKE2) configure
containerd with a `config_path` that overrides this default.

Add the appropriate snippet for your distro:

**k3s / RKE2** — `/etc/rancher/k3s/registries.yaml` (then restart k3s):
```yaml
mirrors:
  "localhost:30500":
    endpoint:
      - "http://localhost:30500"
```

**Talos** — machine config patch:
```yaml
machine:
  registries:
    mirrors:
      localhost:30500:
        endpoints:
          - http://localhost:30500
```

**kubeadm / vanilla containerd** —
`/etc/containerd/certs.d/localhost:30500/hosts.toml`:
```toml
server = "http://localhost:30500"

[host."http://localhost:30500"]
  capabilities = ["pull", "resolve"]
  plain-http = true
```

If you customised the port via `registry.proxy.hostPort` in your Helm
values, substitute that port in the snippets above.

**Why k3d dev clusters don't hit this:** k3d lets you inject containerd
registry config at cluster creation time. Mortise's k3d configs
(`test/integration/k3d-config.yaml`, `test/dev/k3d-config.yaml`) embed
mirror rules that rewrite the registry address, plus a `hostPort` on the
registry pod. The DaemonSet proxy generalises this approach to real clusters.

**External registries:** If you point `PlatformConfig.spec.registry.url` at
an external registry (GHCR, ECR, Harbor, etc.), none of this applies —
kubelet can already reach those registries over HTTPS with standard DNS.
