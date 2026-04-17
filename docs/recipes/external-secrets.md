# External Secrets Operator (ESO)

Mortise reads secrets from standard Kubernetes Secret resources. External
Secrets Operator syncs secrets from external stores (Vault, AWS Secrets
Manager, GCP Secret Manager, Azure Key Vault) into k8s Secrets that Mortise
consumes natively. No Mortise configuration changes are needed.

## Install ESO

```bash
helm repo add external-secrets https://charts.external-secrets.io
helm repo update

helm install external-secrets external-secrets/external-secrets \
  -n external-secrets --create-namespace \
  --set installCRDs=true
```

## Configure a SecretStore

### HashiCorp Vault

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: vault
spec:
  provider:
    vault:
      server: "https://vault.example.com"
      path: "secret"
      version: "v2"
      auth:
        kubernetes:
          mountPath: "kubernetes"
          role: "external-secrets"
```

### AWS Secrets Manager

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: aws-sm
spec:
  provider:
    aws:
      service: SecretsManager
      region: us-east-1
      auth:
        jwt:
          serviceAccountRef:
            name: external-secrets
            namespace: external-secrets
```

### GCP Secret Manager

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: gcp-sm
spec:
  provider:
    gcpsm:
      projectID: my-project
      auth:
        workloadIdentity:
          clusterLocation: us-central1
          clusterName: my-cluster
          serviceAccountRef:
            name: external-secrets
            namespace: external-secrets
```

## Create an ExternalSecret

Once the store is configured, create an ExternalSecret in the App's project
namespace. ESO will sync it into a k8s Secret that Mortise can reference.

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: my-app-db
  namespace: project-my-saas
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault
    kind: ClusterSecretStore
  target:
    name: my-app-db          # the k8s Secret name Mortise will read
  data:
    - secretKey: DATABASE_URL
      remoteRef:
        key: apps/my-app/database-url
```

## Reference from a Mortise App

Point the App's environment or binding at the Secret ESO created:

```yaml
spec:
  environments:
    - name: production
      env:
        - name: DATABASE_URL
          valueFrom:
            secretRef:
              name: my-app-db
              key: DATABASE_URL
```

Mortise treats this like any other k8s Secret reference. When ESO rotates
the secret, Mortise detects the change (via the env-hash annotation) and
rolls the Deployment.

## Further reading

- [External Secrets Operator docs](https://external-secrets.io/)
- [Vault provider](https://external-secrets.io/latest/provider/hashicorp-vault/)
- [AWS provider](https://external-secrets.io/latest/provider/aws-secrets-manager/)
- [GCP provider](https://external-secrets.io/latest/provider/google-secrets-manager/)
