# Backup with Velero

Velero backs up Kubernetes resources and persistent volumes. This recipe
covers backing up Mortise's state: CRDs, Secrets, and PersistentVolumes.

## Install Velero

```bash
# Example: AWS S3 backend
velero install \
  --provider aws \
  --plugins velero/velero-plugin-for-aws:v1.11.0 \
  --bucket mortise-backups \
  --backup-location-config region=us-east-1 \
  --secret-file ./credentials-velero \
  --use-node-agent \
  --default-volumes-to-fs-backup
```

For other providers (GCP, Azure, MinIO, NFS), see the
[Velero docs](https://velero.io/docs/).

## What to back up

Mortise's state lives in:

| Resource | Why |
|----------|-----|
| CRDs (App, PlatformConfig, GitProvider, Project) | Workload definitions |
| Secrets in `mortise-system` and `project-*` namespaces | Credentials, tokens, env vars |
| PersistentVolumeClaims | App data (databases, uploads) |
| ConfigMaps in `mortise-system` | Operator configuration |

## Schedule a backup

```bash
# Back up all Mortise-related namespaces daily, retain for 30 days
velero schedule create mortise-daily \
  --schedule="0 2 * * *" \
  --include-namespaces mortise-system \
  --include-namespace-pattern "project-*" \
  --include-cluster-scoped-resources \
    apps.mortise.dev,platformconfigs.mortise.dev,gitproviders.mortise.dev,projects.mortise.dev \
  --ttl 720h \
  --default-volumes-to-fs-backup
```

## Restore

```bash
# List available backups
velero backup get

# Restore from a specific backup
velero restore create --from-backup mortise-daily-20260413020000
```

After restoring, verify the operator is reconciling:

```bash
kubectl get apps -A
kubectl get pods -n mortise-system
```

## Homelab: MinIO as backup target

For homelabs without cloud storage, use MinIO:

```bash
helm repo add minio https://charts.min.io
helm install minio minio/minio \
  -n minio --create-namespace \
  --set rootUser=minioadmin \
  --set rootPassword=minioadmin \
  --set persistence.size=50Gi

# Configure Velero to use MinIO
velero install \
  --provider aws \
  --plugins velero/velero-plugin-for-aws:v1.11.0 \
  --bucket mortise-backups \
  --backup-location-config \
    region=minio,s3ForcePathStyle=true,s3Url=http://minio.minio:9000 \
  --secret-file ./credentials-velero \
  --use-node-agent
```

## Further reading

- [Velero documentation](https://velero.io/docs/)
- [Velero AWS plugin](https://github.com/vmware-tanzu/velero-plugin-for-aws)
- [MinIO Helm chart](https://min.io/docs/minio/kubernetes/upstream/)
