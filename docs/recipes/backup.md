# Backup and Restore

Mortise stores all platform state in Kubernetes objects (CRDs, Secrets,
ConfigMaps) and persistent volumes (registry images, BuildKit cache, app
data). This guide covers how to back it all up.

## What to back up

| Data | Where it lives | Impact if lost |
|------|---------------|----------------|
| Apps, Projects, PlatformConfig | CRDs in etcd | All workload definitions gone |
| Users, JWT keys, git credentials | Secrets in `mortise-system` | Auth broken, must re-create accounts |
| App env vars, shared vars | Secrets in `pj-*` namespaces | Apps lose configuration |
| Built container images | PVC in `mortise-deps` | Git-source apps enter ImagePullBackOff until rebuilt |
| BuildKit layer cache | PVC in `mortise-deps` | Builds work but start from scratch (slower) |
| App persistent data (databases, uploads) | PVCs in `pj-*-*` namespaces | User data lost |

## Option 1: k3s etcd snapshots (simplest)

If you're running k3s, this is the easiest way to back up all Kubernetes
objects (CRDs, Secrets, ConfigMaps: everything except PVC data).

k3s auto-snapshots every 12 hours to `/var/lib/rancher/k3s/server/db/snapshots/`.
To push snapshots to S3:

```bash
# One-time snapshot to S3
k3s etcd-snapshot save --s3 \
  --s3-bucket=mortise-backups \
  --s3-region=us-east-1 \
  --s3-endpoint=s3.amazonaws.com \
  --s3-access-key=AKIAXXXXXXXX \
  --s3-secret-key=XXXXXXXX

# Schedule automatic S3 snapshots (add to /etc/rancher/k3s/config.yaml)
# etcd-snapshot-schedule-cron: "0 */6 * * *"
# etcd-snapshot-retention: 10
# etcd-s3: true
# etcd-s3-bucket: mortise-backups
# etcd-s3-region: us-east-1
```

**What this covers:** All platform state: users, apps, projects,
credentials, env vars, PlatformConfig. Everything except files on disk
(PVC data).

**What this doesn't cover:** PVC contents (registry images, BuildKit
cache, app database files). For those, see Option 2.

See the [k3s etcd-snapshot docs](https://docs.k3s.io/cli/etcd-snapshot).

## Option 2: Velero (full backup including PVCs)

Velero backs up both Kubernetes objects and persistent volume data to S3.

### Install Velero

```bash
# AWS S3
velero install \
  --provider aws \
  --plugins velero/velero-plugin-for-aws:v1.11.0 \
  --bucket mortise-backups \
  --backup-location-config region=us-east-1 \
  --secret-file ./credentials-velero \
  --use-node-agent \
  --default-volumes-to-fs-backup
```

For other providers (GCP, Azure, MinIO), see the
[Velero docs](https://velero.io/docs/).

### Schedule a backup

```bash
velero schedule create mortise-daily \
  --schedule="0 2 * * *" \
  --include-namespaces mortise-system,mortise-deps \
  --include-namespace-pattern "pj-*" \
  --include-cluster-scoped-resources \
    apps.mortise.dev,platformconfigs.mortise.dev,gitproviders.mortise.dev,projects.mortise.dev \
  --ttl 720h \
  --default-volumes-to-fs-backup
```

This backs up:
- `mortise-system`: operator, user accounts, JWT keys
- `mortise-deps`: registry images, BuildKit cache
- `pj-*`: all project namespaces (app definitions, env vars, workloads, PVCs)
- Cluster-scoped CRDs: Projects, PlatformConfig, GitProviders

### Restore

```bash
velero backup get
velero restore create --from-backup mortise-daily-20260413020000
```

After restoring, verify the operator is reconciling:

```bash
kubectl get apps -A
kubectl get pods -n mortise-system
```

## Homelab: MinIO as S3 target

For homelabs without cloud storage, run MinIO in-cluster:

```bash
helm repo add minio https://charts.min.io
helm install minio minio/minio \
  -n minio --create-namespace \
  --set rootUser=minioadmin \
  --set rootPassword=minioadmin \
  --set persistence.size=50Gi
```

Then point Velero (or k3s etcd-snapshot) at MinIO:

```bash
# Velero with MinIO
velero install \
  --provider aws \
  --plugins velero/velero-plugin-for-aws:v1.11.0 \
  --bucket mortise-backups \
  --backup-location-config \
    region=minio,s3ForcePathStyle=true,s3Url=http://minio.minio:9000 \
  --secret-file ./credentials-velero \
  --use-node-agent

# k3s with MinIO
k3s etcd-snapshot save --s3 \
  --s3-bucket=mortise-backups \
  --s3-endpoint=minio.minio:9000 \
  --s3-skip-ssl-verify \
  --s3-access-key=minioadmin \
  --s3-secret-key=minioadmin
```

Other cheap S3-compatible options: [Backblaze B2](https://www.backblaze.com/cloud-storage),
[Wasabi](https://wasabi.com/).

## Further reading

- [k3s etcd-snapshot](https://docs.k3s.io/cli/etcd-snapshot)
- [Velero documentation](https://velero.io/docs/)
- [Velero AWS plugin](https://github.com/vmware-tanzu/velero-plugin-for-aws)
