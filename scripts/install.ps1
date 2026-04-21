# quick-mortise: single-command installer for the Mortise PaaS (Windows)
# Usage: powershell -ExecutionPolicy Bypass -File install.ps1
#
# Idempotent - safe to run multiple times.
# Requires: Docker Desktop for Windows

$ErrorActionPreference = "Stop"

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
$CERT_MANAGER_VERSION = if ($env:CERT_MANAGER_VERSION) { $env:CERT_MANAGER_VERSION } else { "v1.17.1" }
$HELM_VERSION = if ($env:HELM_VERSION) { $env:HELM_VERSION } else { "v3.17.3" }
$MORTISE_CHART_REPO = if ($env:MORTISE_CHART_REPO) { $env:MORTISE_CHART_REPO } else { "https://mc-meesh.github.io/mortise" }
$MORTISE_CHART_VERSION = if ($env:MORTISE_CHART_VERSION) { $env:MORTISE_CHART_VERSION } else { "" }
$MORTISE_NAMESPACE = "mortise-system"
$DEPS_NAMESPACE = "mortise-deps"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
function Info($msg)  { Write-Host "[mortise] $msg" -ForegroundColor Blue }
function Warn($msg)  { Write-Host "[mortise] $msg" -ForegroundColor Yellow }
function Fatal($msg) { Write-Host "[mortise] $msg" -ForegroundColor Red; exit 1 }

function CommandExists($cmd) {
    $null -ne (Get-Command $cmd -ErrorAction SilentlyContinue)
}

function HasChocolatey() { CommandExists "choco" }

# ---------------------------------------------------------------------------
# Step 0: Detect architecture
# ---------------------------------------------------------------------------
function Detect-Platform {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    switch ($arch) {
        "X64"   { $script:ARCH = "amd64" }
        "Arm64" { $script:ARCH = "arm64" }
        default { Fatal "Unsupported architecture: $arch" }
    }
    Info "Detected platform: windows/$script:ARCH"
}

# ---------------------------------------------------------------------------
# Step 1: Check Docker Desktop
# ---------------------------------------------------------------------------
function Check-Docker {
    if (-not (CommandExists "docker")) {
        Fatal "Docker Desktop is required. Install from https://docker.com/products/docker-desktop"
    }

    try {
        docker info 2>&1 | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "not running" }
    } catch {
        Fatal "Docker Desktop is not running. Start Docker Desktop and try again."
    }
    Info "Docker Desktop is running"
}

# ---------------------------------------------------------------------------
# Step 2: Install k3d
# ---------------------------------------------------------------------------
function Install-K3d {
    # If kubectl is available and a cluster is reachable, skip.
    if ((CommandExists "kubectl") -and ((kubectl cluster-info 2>&1) -and $LASTEXITCODE -eq 0)) {
        Info "Existing Kubernetes cluster detected, skipping k3d install"
        return
    }

    if (-not (CommandExists "k3d")) {
        Info "Installing k3d..."
        if (HasChocolatey) {
            choco install k3d -y
        } else {
            # Direct download via PowerShell
            Invoke-WebRequest -Uri "https://raw.githubusercontent.com/k3d-io/k3d/main/install.ps1" -OutFile "$env:TEMP\install-k3d.ps1"
            & "$env:TEMP\install-k3d.ps1"
            Remove-Item "$env:TEMP\install-k3d.ps1" -ErrorAction SilentlyContinue
        }

        if (-not (CommandExists "k3d")) {
            Fatal "k3d installation failed"
        }
    } else {
        Info "k3d is already installed, skipping"
    }

    # Create cluster if it doesn't already exist.
    $clusters = k3d cluster list -o json 2>$null | ConvertFrom-Json
    $exists = $clusters | Where-Object { $_.name -eq "mortise" }
    if ($exists) {
        Info "k3d cluster 'mortise' already exists, skipping creation"
    } else {
        Info "Creating k3d cluster 'mortise'..."
        k3d cluster create mortise --port "80:80@loadbalancer" --port "443:443@loadbalancer" --wait
        if ($LASTEXITCODE -ne 0) { Fatal "Failed to create k3d cluster" }
    }

    Info "Waiting for k3d cluster to be ready..."
    kubectl wait --for=condition=Ready node --all --timeout=120s
    if ($LASTEXITCODE -ne 0) { Fatal "Timed out waiting for cluster to be ready" }
    Info "k3d cluster is ready"
}

# ---------------------------------------------------------------------------
# Step 3: Install Helm
# ---------------------------------------------------------------------------
function Install-Helm {
    if (CommandExists "helm") {
        Info "Helm is already installed, skipping"
        return
    }

    Info "Installing Helm $HELM_VERSION..."
    if (HasChocolatey) {
        choco install kubernetes-helm -y
    } else {
        # Direct download
        $helmUrl = "https://get.helm.sh/helm-${HELM_VERSION}-windows-${script:ARCH}.zip"
        $zipPath = "$env:TEMP\helm.zip"
        $extractPath = "$env:TEMP\helm"
        Invoke-WebRequest -Uri $helmUrl -OutFile $zipPath
        Expand-Archive -Path $zipPath -DestinationPath $extractPath -Force
        $helmBin = Get-ChildItem -Path $extractPath -Recurse -Filter "helm.exe" | Select-Object -First 1
        $destDir = "$env:LOCALAPPDATA\helm"
        New-Item -ItemType Directory -Path $destDir -Force | Out-Null
        Copy-Item -Path $helmBin.FullName -Destination "$destDir\helm.exe" -Force
        # Add to PATH for this session.
        $env:PATH = "$destDir;$env:PATH"
        Remove-Item $zipPath -ErrorAction SilentlyContinue
        Remove-Item $extractPath -Recurse -ErrorAction SilentlyContinue
    }

    if (-not (CommandExists "helm")) {
        Fatal "Helm installation failed"
    }
    Info "Helm installed"
}

# ---------------------------------------------------------------------------
# Step 4: Install cert-manager
# ---------------------------------------------------------------------------
function Install-CertManager {
    $ns = kubectl get namespace cert-manager 2>&1
    if ($LASTEXITCODE -eq 0) {
        Info "cert-manager namespace exists, skipping install"
    } else {
        Info "Installing cert-manager $CERT_MANAGER_VERSION..."
        kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
        if ($LASTEXITCODE -ne 0) { Fatal "Failed to install cert-manager" }
    }

    Info "Waiting for cert-manager to be ready..."
    kubectl -n cert-manager rollout status deployment/cert-manager --timeout=120s
    kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=120s
    kubectl -n cert-manager rollout status deployment/cert-manager-cainjector --timeout=120s
    Info "cert-manager is ready"
}

# ---------------------------------------------------------------------------
# Step 5: Deploy build infrastructure (BuildKit + OCI registry)
# ---------------------------------------------------------------------------
function Deploy-BuildInfra {
    Info "Creating $DEPS_NAMESPACE namespace..."
    kubectl create namespace $DEPS_NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

    Info "Deploying OCI registry..."
    @"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
  namespace: $DEPS_NAMESPACE
  labels:
    app.kubernetes.io/name: registry
    app.kubernetes.io/managed-by: quick-mortise
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: registry
  template:
    metadata:
      labels:
        app.kubernetes.io/name: registry
    spec:
      containers:
        - name: registry
          image: distribution/distribution:2.8.3
          imagePullPolicy: IfNotPresent
          env:
            - name: REGISTRY_HTTP_ADDR
              value: "0.0.0.0:5000"
            - name: REGISTRY_STORAGE_DELETE_ENABLED
              value: "true"
          ports:
            - name: http
              containerPort: 5000
          readinessProbe:
            httpGet:
              path: /v2/
              port: 5000
            initialDelaySeconds: 2
            periodSeconds: 2
            failureThreshold: 30
          volumeMounts:
            - name: data
              mountPath: /var/lib/registry
      volumes:
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: registry
  namespace: $DEPS_NAMESPACE
  labels:
    app.kubernetes.io/managed-by: quick-mortise
spec:
  selector:
    app.kubernetes.io/name: registry
  ports:
    - name: http
      port: 5000
      targetPort: 5000
"@ | kubectl apply -f -

    Info "Deploying BuildKit..."
    @"
apiVersion: v1
kind: ConfigMap
metadata:
  name: buildkitd-config
  namespace: $DEPS_NAMESPACE
  labels:
    app.kubernetes.io/managed-by: quick-mortise
data:
  buildkitd.toml: |
    debug = false
    [grpc]
      address = ["tcp://0.0.0.0:1234"]
    [registry."registry.${DEPS_NAMESPACE}.svc:5000"]
      http = true
      insecure = true
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: buildkitd
  namespace: $DEPS_NAMESPACE
  labels:
    app.kubernetes.io/name: buildkitd
    app.kubernetes.io/managed-by: quick-mortise
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: buildkitd
  template:
    metadata:
      labels:
        app.kubernetes.io/name: buildkitd
    spec:
      containers:
        - name: buildkitd
          image: moby/buildkit:v0.29.0
          imagePullPolicy: IfNotPresent
          args:
            - --addr
            - tcp://0.0.0.0:1234
            - --config
            - /etc/buildkit/buildkitd.toml
          readinessProbe:
            exec:
              command: ["buildctl", "--addr", "tcp://127.0.0.1:1234", "debug", "workers"]
            initialDelaySeconds: 5
            periodSeconds: 3
            failureThreshold: 30
          ports:
            - name: grpc
              containerPort: 1234
          securityContext:
            privileged: true
          volumeMounts:
            - name: config
              mountPath: /etc/buildkit
            - name: data
              mountPath: /var/lib/buildkit
      volumes:
        - name: config
          configMap:
            name: buildkitd-config
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: buildkitd
  namespace: $DEPS_NAMESPACE
  labels:
    app.kubernetes.io/managed-by: quick-mortise
spec:
  selector:
    app.kubernetes.io/name: buildkitd
  ports:
    - name: grpc
      port: 1234
      targetPort: 1234
"@ | kubectl apply -f -

    Info "Waiting for registry to be ready..."
    kubectl -n $DEPS_NAMESPACE rollout status deployment/registry --timeout=120s
    Info "Waiting for BuildKit to be ready..."
    kubectl -n $DEPS_NAMESPACE rollout status deployment/buildkitd --timeout=180s
    Info "Build infrastructure is ready"
}

# ---------------------------------------------------------------------------
# Step 6: Install Mortise operator via Helm
# ---------------------------------------------------------------------------
function Install-Mortise {
    Info "Adding Mortise Helm repository..."
    helm repo add mortise $MORTISE_CHART_REPO 2>$null
    helm repo update mortise

    $versionFlag = @()
    if ($MORTISE_CHART_VERSION -ne "") {
        $versionFlag = @("--version", $MORTISE_CHART_VERSION)
    }

    Info "Installing Mortise operator..."
    helm upgrade --install mortise mortise/mortise `
        --namespace $MORTISE_NAMESPACE --create-namespace `
        --wait --timeout 120s `
        @versionFlag

    if ($LASTEXITCODE -ne 0) { Fatal "Failed to install Mortise operator" }
    Info "Mortise operator installed"
}

# ---------------------------------------------------------------------------
# Step 7: Create default PlatformConfig
# ---------------------------------------------------------------------------
function Create-PlatformConfig {
    $existing = kubectl get platformconfigs.mortise.mortise.dev platform 2>&1
    if ($LASTEXITCODE -eq 0) {
        Info "PlatformConfig 'platform' already exists, skipping"
        return
    }

    $buildPlatform = if ($script:ARCH -eq "arm64") { "linux/arm64" } else { "linux/amd64" }

    Info "Creating default PlatformConfig (build platform: $buildPlatform)..."
    @"
apiVersion: mortise.mortise.dev/v1alpha1
kind: PlatformConfig
metadata:
  name: platform
spec:
  domain: mortise.local
  registry:
    url: http://registry.${DEPS_NAMESPACE}.svc:5000
    namespace: mortise
    insecureSkipTLSVerify: true
  build:
    buildkitAddr: tcp://buildkitd.${DEPS_NAMESPACE}.svc:1234
    defaultPlatform: "$buildPlatform"
"@ | kubectl apply -f -

    if ($LASTEXITCODE -ne 0) { Fatal "Failed to create PlatformConfig" }
    Info "PlatformConfig created"
}

# ---------------------------------------------------------------------------
# Step 8: Wait for operator and print success
# ---------------------------------------------------------------------------
function Wait-AndPrintSuccess {
    Info "Waiting for Mortise operator to be ready..."
    kubectl -n $MORTISE_NAMESPACE rollout status deployment/mortise --timeout=120s

    # Restart operator so it picks up the PlatformConfig.
    kubectl -n $MORTISE_NAMESPACE rollout restart deployment/mortise
    kubectl -n $MORTISE_NAMESPACE rollout status deployment/mortise --timeout=60s

    $nodeIp = kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type==\"InternalIP\")].address}' 2>$null
    if (-not $nodeIp) { $nodeIp = "localhost" }

    $svcPort = kubectl -n $MORTISE_NAMESPACE get svc mortise -o jsonpath='{.spec.ports[0].port}' 2>$null
    if (-not $svcPort) { $svcPort = "80" }

    Write-Host ""
    Write-Host "============================================" -ForegroundColor Green
    Write-Host "  Mortise is installed and running!" -ForegroundColor Green
    Write-Host "============================================" -ForegroundColor Green
    Write-Host ""
    Write-Host "  Operator namespace : $MORTISE_NAMESPACE"
    Write-Host "  Build infra        : $DEPS_NAMESPACE"
    Write-Host "  Node IP            : $nodeIp"
    Write-Host ""
    Write-Host "  To access the Mortise UI, create an Ingress or run:"
    Write-Host "    kubectl port-forward -n $MORTISE_NAMESPACE svc/mortise ${svcPort}:80"
    Write-Host "    then open http://localhost:$svcPort"
    Write-Host ""
    Write-Host "  To set a real domain, edit the PlatformConfig:"
    Write-Host "    kubectl edit platformconfigs.mortise.mortise.dev platform"
    Write-Host ""
    Write-Host "  Docs: https://mortise.dev/docs"
    Write-Host ""
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
Info "Starting Mortise installation..."
Detect-Platform
Check-Docker
Install-K3d
Install-Helm
Install-CertManager
Deploy-BuildInfra
Install-Mortise
Create-PlatformConfig
Wait-AndPrintSuccess
