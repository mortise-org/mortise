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

Create the ExternalSecret in the App's **per-environment workload namespace**
(`pj-{project}-{environment}`), not the project's control namespace. Mortise
resolves `secretRef` from the environment namespace, so a Secret sitting in
`pj-my-saas` is never found.

That means one ExternalSecret per environment the App runs in — production and
staging each need their own, since each has its own namespace.

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: my-app-db
  namespace: pj-my-saas-production   # the ENV namespace, not pj-my-saas
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault
    kind: ClusterSecretStore
  target:
    name: my-app-db          # the k8s Secret name Mortise will read
  data:
    # secretKey MUST equal the environment variable name Mortise projects it
    # into. Mortise's secretRef has no `key` field: it reads the key whose name
    # matches the variable.
    - secretKey: DATABASE_URL
      remoteRef:
        key: apps/my-app/database-url
```

## Reference from a Mortise App

Point the App's environment or binding at the Secret ESO created:

`secretRef` is a **bare Secret name**, not an object. This differs from
core-v1's `secretKeyRef` and is the most common mistake here — the object form
below the fold fails validation, because the CRD declares `secretRef` as a
string.

```yaml
spec:
  environments:
    - name: production
      env:
        - name: DATABASE_URL
          valueFrom:
            secretRef: my-app-db      # a string. The key read from the Secret
                                      # is always the variable's own name.
```

## What happens when ESO rotates the secret

Two steps, and only the first is automatic:

1. **The value reaches Mortise.** Changing the referenced Secret triggers an
   App reconcile, and the operator re-resolves the reference into the derived
   `{app}-env` Secret.
2. **The value reaches the running pods — only if you ask for it.** Environment
   variables are delivered via `envFrom` on that Secret, which a running
   container reads once at start. The rollout is triggered by the
   `mortise.dev/env-hash` pod-template annotation, and that hash is **frozen
   unless the parent Project sets `autoRedeploy: true`**, which is not the
   default.

So with default settings a rotated secret is picked up by the platform and
**the pods keep serving the old value** until someone redeploys. Set
`autoRedeploy: true` on the Project for hands-off rotation, or redeploy the App
as the last step of your rotation procedure.

Verify with a digest rather than by reading the value:

```bash
kubectl exec <pod> -n pj-my-saas-production -- printenv DATABASE_URL \
  | sha256sum | cut -c1-12
```

## Further reading

- [External Secrets Operator docs](https://external-secrets.io/)
- [Vault provider](https://external-secrets.io/latest/provider/hashicorp-vault/)
- [AWS provider](https://external-secrets.io/latest/provider/aws-secrets-manager/)
- [GCP provider](https://external-secrets.io/latest/provider/google-secrets-manager/)
