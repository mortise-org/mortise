# Creating a Kubernetes cluster

Mortise needs a running Kubernetes cluster. If you already have one, skip to
[Installing Mortise](./install.md). If not, here are the quickest paths
depending on where you're running.

## Local development (macOS / Linux desktop)

**k3d** creates a lightweight k3s cluster inside Docker. Best for trying
Mortise on your laptop.

```bash
# Install k3d (requires Docker)
curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash

# Create a cluster
k3d cluster create mortise

# Verify
kubectl get nodes
```

Takes about 30 seconds. Your `kubectl` context is automatically set.

## Single server (VPS, NUC, Raspberry Pi)

**k3s** is a single-binary Kubernetes distribution. Runs on anything from a
Raspberry Pi to a cloud VPS.

```bash
# Install k3s
curl -sfL https://get.k3s.io | sh -

# k3s includes kubectl: copy the kubeconfig to the standard location
mkdir -p ~/.kube
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $(id -u):$(id -g) ~/.kube/config

# Verify
kubectl get nodes
```

Takes about 60 seconds. k3s includes Traefik as its default ingress
controller, which Mortise can use out of the box.

**Registry config for git-source builds:** k3s needs a one-time registry
mirror entry so kubelet can pull images built by Mortise. Add to
`/etc/rancher/k3s/registries.yaml` (create if missing) and restart k3s:

```yaml
mirrors:
  "localhost:30500":
    endpoint:
      - "http://localhost:30500"
```

See [Installing Mortise > Registry proxy](./install.md#registry-proxy-git-source-builds)
for details.

## Cloud Kubernetes (EKS, GKE, AKS)

If you have a cloud account, create a managed cluster using your provider's
CLI or console. The key requirement is a working `kubectl` connection.

**AWS EKS:**
```bash
eksctl create cluster --name mortise --region us-east-1 --nodes 2
```

**Google GKE:**
```bash
gcloud container clusters create mortise --zone us-central1-a --num-nodes 2
gcloud container clusters get-credentials mortise --zone us-central1-a
```

**Azure AKS:**
```bash
az aks create --resource-group mygroup --name mortise --node-count 2
az aks get-credentials --resource-group mygroup --name mortise
```

## Minimum requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPU | 2 cores | 4 cores |
| Memory | 2 GB | 4 GB |
| Disk | 10 GB | 20 GB+ |
| Kubernetes version | 1.27+ | 1.29+ |

These are for the platform itself. Your apps need additional resources on top.

## Next step

Once `kubectl get nodes` shows your node(s) as `Ready`, proceed to
[Installing Mortise](./install.md).
