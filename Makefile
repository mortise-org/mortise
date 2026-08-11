# Image URL to use all building/pushing image targets
IMG ?= controller:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	@mkdir -p config/webhook
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases output:webhook:dir=config/webhook
	# Sync generated CRDs into the Helm chart so `helm install` ships the real schema.
	cp config/crd/bases/*.yaml charts/mortise-core/crds/

.PHONY: generate
generate: controller-gen generate-api ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: generate-api
generate-api: ## Regenerate OpenAPI spec from swag annotations.
	swag init --generalInfo main.go --dir ./cmd,./internal/api,./internal/webhook --output ./docs --outputTypes yaml --parseDependency --parseInternal
	cp docs/swagger.yaml internal/api/openapi.yaml

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest check-ui ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

.PHONY: check-ui
check-ui: ## Run svelte-check (Svelte + TypeScript diagnostics) against the UI.
	cd ui && npm install && npm run check

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build-ui
build-ui: ## Build the SvelteKit UI and copy into internal/ui/build for embedding.
	cd ui && npm install && npm run build
	rm -rf internal/ui/build
	cp -r ui/build internal/ui/build
	# .gitkeep preserves the directory on a fresh clone so //go:embed has a
	# target to attach to before `make build-ui` has been run.
	touch internal/ui/build/.gitkeep

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: build-observer
build-observer: fmt vet ## Build observer binary.
	go build -o bin/observer ./cmd/observer

.PHONY: build-cli
build-cli: fmt vet ## Build CLI binary.
	go build -o bin/mortise ./cmd/cli

CLI_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build-cli-all
build-cli-all: fmt vet ## Cross-compile CLI binary for all release platforms.
	@for platform in $(CLI_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "Building mortise-$${os}-$${arch}..."; \
		GOOS=$${os} GOARCH=$${arch} go build -o bin/mortise-$${os}-$${arch} ./cmd/cli; \
	done

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

##@ Dev Cluster

DEV_CLUSTER ?= mortise-dev
DEV_KUBE_CONTEXT ?= k3d-$(DEV_CLUSTER)
DEV_IMG ?= mortise:dev
DEV_OBSERVER_IMG ?= mortise-observer:dev
GITHUB_CLIENT_ID ?= Ov23lizLTd25E32VrWwl
K3D_RUNTIME_ULIMIT ?= nofile=1048576:1048576
# Single budget for every helm --wait in this repo (#443): hosted CI runners
# pull traefik/cert-manager/metrics-server images cold, and 180-300s budgets
# have expired in real runs.
HELM_WAIT_TIMEOUT ?= 600s
DEV_HELM_TIMEOUT ?= $(HELM_WAIT_TIMEOUT)
DEV_PORT_FORWARD_LOG ?= /tmp/$(DEV_CLUSTER)-port-forward.log
DEV_PORT_FORWARD_READY_URL ?= http://localhost:8090/api/auth/status

.PHONY: dev-up
dev-up: build-ui ## Create k3d dev cluster with build infra, install Mortise, port-forward
	@echo "==> Creating k3d cluster $(DEV_CLUSTER) (with registry mirror)..."
	@tmp_config=$$(mktemp); \
	trap 'rm -f "$$tmp_config"' EXIT; \
	sed 's/name: mortise-dev/name: $(DEV_CLUSTER)/' test/dev/k3d-config.yaml > "$$tmp_config"; \
	k3d cluster list | grep -q "^$(DEV_CLUSTER)\\b" || k3d cluster create \
		--config "$$tmp_config" --runtime-ulimit $(K3D_RUNTIME_ULIMIT) --wait
	@echo "==> Building Docker images..."
	$(CONTAINER_TOOL) build --target operator -t $(DEV_IMG) .
	$(CONTAINER_TOOL) build --target observer -t $(DEV_OBSERVER_IMG) .
	@echo "==> Loading images into k3d..."
	k3d image import $(DEV_IMG) $(DEV_OBSERVER_IMG) -c $(DEV_CLUSTER)
	@echo "==> Installing CRDs..."
	kubectl --context $(DEV_KUBE_CONTEXT) apply -f charts/mortise-core/crds/
	@echo "==> Deploying test infrastructure (registry, Gitea, BuildKit)..."
	kubectl --context $(DEV_KUBE_CONTEXT) apply -f test/integration/manifests/00-namespace.yaml \
		-f test/integration/manifests/10-registry.yaml \
		-f test/integration/manifests/20-gitea.yaml \
		-f test/integration/manifests/30-buildkit.yaml
	@echo "==> Waiting for test infrastructure to become ready..."
	kubectl --context $(DEV_KUBE_CONTEXT) -n mortise-test-deps rollout status deployment/registry  --timeout=120s
	kubectl --context $(DEV_KUBE_CONTEXT) -n mortise-test-deps rollout status deployment/gitea     --timeout=180s
	kubectl --context $(DEV_KUBE_CONTEXT) -n mortise-test-deps rollout status deployment/buildkitd --timeout=180s
	@echo "==> Fetching Helm chart dependencies..."
	helm repo add traefik https://traefik.github.io/charts --force-update
	helm repo add jetstack https://charts.jetstack.io --force-update
	helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ --force-update
	helm repo update
	helm dependency build charts/mortise
	@echo "==> Installing Mortise via Helm..."
	@dev_img_repo='$(DEV_IMG)'; \
	dev_img_repo=$${dev_img_repo%:*}; \
	dev_img_tag='$(DEV_IMG)'; \
	dev_img_tag=$${dev_img_tag##*:}; \
	helm --kube-context $(DEV_KUBE_CONTEXT) upgrade --install mortise charts/mortise \
		--namespace mortise-system --create-namespace \
		--skip-crds \
		--set mortise-core.image.repository=$$dev_img_repo \
		--set mortise-core.image.tag=$$dev_img_tag \
		--set mortise-core.image.pullPolicy=Never \
		--set observer.enabled=true \
		--set observer.image=$(DEV_OBSERVER_IMG) \
		--set observer.imagePullPolicy=Never \
		--set observer.metricsPollInterval=5s \
		--set registry.enabled=false \
		--set buildkit.enabled=false \
		--set platformConfig.enabled=false \
		--set cert-manager.enabled=false \
		--set metrics-server.args='{--kubelet-insecure-tls}' \
		--set mortise-core.github.clientID=$(GITHUB_CLIENT_ID) \
		--wait --timeout $(DEV_HELM_TIMEOUT)
	@echo "==> Applying dev PlatformConfig..."
	kubectl --context $(DEV_KUBE_CONTEXT) apply -f test/dev/platform-config.yaml
	@echo "==> Restarting operator to pick up config..."
	# The operator consumes PlatformConfig registry/build settings once at
	# boot (no hot reload; ConfigApplied=RestartRequired tracks drift).
	kubectl --context $(DEV_KUBE_CONTEXT) -n mortise-system rollout restart deployment/mortise
	kubectl --context $(DEV_KUBE_CONTEXT) -n mortise-system rollout status deployment/mortise --timeout=60s
	@echo "==> Starting port-forward..."
	@-pkill -f "[k]ubectl port-forward.*8090" >/dev/null 2>&1
	@if command -v setsid >/dev/null 2>&1; then \
		nohup setsid kubectl --context $(DEV_KUBE_CONTEXT) port-forward -n mortise-system svc/mortise 8090:80 >$(DEV_PORT_FORWARD_LOG) 2>&1 </dev/null & \
	else \
		nohup kubectl --context $(DEV_KUBE_CONTEXT) port-forward -n mortise-system svc/mortise 8090:80 >$(DEV_PORT_FORWARD_LOG) 2>&1 </dev/null & \
	fi
	@for i in $$(seq 1 15); do \
		if curl -fsS $(DEV_PORT_FORWARD_READY_URL) >/dev/null 2>&1; then \
			break; \
		fi; \
		if [ $$i -eq 15 ]; then \
			echo "ERROR: port-forward failed to become reachable; see $(DEV_PORT_FORWARD_LOG)"; \
			tail -n 20 $(DEV_PORT_FORWARD_LOG) >/dev/null 2>&1 && tail -n 20 $(DEV_PORT_FORWARD_LOG); \
			exit 1; \
		fi; \
		sleep 1; \
	done
	@echo ""
	@echo "✓ Mortise dev cluster is running at http://localhost:8090"
	@echo "  Test infra: Gitea + BuildKit + registry (with node-local mirror)"
	@echo "  Run 'make dev-reload' to rebuild and redeploy without recreating the cluster"
	@echo "  Run 'make dev-down' to tear down"

.PHONY: dev-down
dev-down: ## Delete k3d dev cluster and stop port-forward
	@-pkill -f "[k]ubectl port-forward.*8090" >/dev/null 2>&1
	k3d cluster delete $(DEV_CLUSTER)


.PHONY: dev-reset
dev-reset: ## Tear down dev cluster completely, rebuild, and start fresh
	-k3d cluster delete $(DEV_CLUSTER) >/dev/null 2>&1
	-pkill -f "[k]ubectl port-forward.*8090" >/dev/null 2>&1
	$(MAKE) dev-up

##@ Integration Tests

INT_CLUSTER ?= mortise-int
# Every kubectl/helm/go-test step below pins this context explicitly. The
# setup must never ride the ambient current-context: k3d cluster create
# SWITCHES current-context, so any concurrent cluster creation on the box
# (another crew's gate, a dev-up) would silently redirect an unpinned
# install or test suite into the wrong cluster mid-run.
INT_KUBE_CONTEXT ?= k3d-$(INT_CLUSTER)
INT_IMG ?= mortise:int
INT_OBSERVER_IMG ?= mortise-observer:int
# Test fixtures plus the umbrella chart's dependency images (traefik,
# metrics-server — versions from the packaged subcharts; cert-manager is
# disabled in the integration install). Pre-importing the chart deps keeps
# cold-registry pulls out of the helm --wait budget.
INT_DEP_IMAGES ?= nginx:1.27 postgres:16 redis:7 busybox:1.37 alpine:3.20 docker.io/traefik:v3.3.3 registry.k8s.io/metrics-server/metrics-server:v0.7.2

.PHONY: test-integration
test-integration: ## Create k3d cluster, install chart + test deps, run integration tests, tear down
	@echo "==> Deleting any stale k3d cluster from a prior run..."
	-k3d cluster delete $(INT_CLUSTER) >/dev/null 2>&1
	@echo "==> Creating k3d cluster $(INT_CLUSTER)..."
	# --config wires the containerd mirror rule that makes
	# registry.mortise-test-deps.svc:5000 pulls reachable from the node.
	# The config hardcodes metadata.name, so stamp INT_CLUSTER into it the
	# same way dev-up stamps DEV_CLUSTER — otherwise create ignores the
	# variable and concurrent gates collide on the shared name.
	@tmp_config=$$(mktemp); \
	trap 'rm -f "$$tmp_config"' EXIT; \
	sed 's/name: mortise-int/name: $(INT_CLUSTER)/' test/integration/k3d-config.yaml > "$$tmp_config"; \
	k3d cluster create --config "$$tmp_config" --runtime-ulimit $(K3D_RUNTIME_ULIMIT) --wait
	@echo "==> Building Docker images..."
	$(CONTAINER_TOOL) build --target operator -t $(INT_IMG) .
	$(CONTAINER_TOOL) build --target observer -t $(INT_OBSERVER_IMG) .
	@echo "==> Pre-pulling test images..."
	for img in $(INT_DEP_IMAGES); do $(CONTAINER_TOOL) pull $$img || exit 1; done
	@echo "==> Loading images into k3d..."
	# With Docker's containerd image store (default on fresh Docker 28+
	# installs), `docker save` of a pulled multi-arch image emits a tarball
	# referencing blobs it does not contain, the node-side ctr import fails
	# with "content digest not found" — and k3d still reports success and
	# exits 0, so the failure only surfaces later as helm --wait timing out
	# on ErrImageNeverPull pods (mo-29g). Import per-image with an explicit
	# platform there; keep the plain k3d import on the legacy store.
	@if $(CONTAINER_TOOL) info -f '{{json .DriverStatus}}' | grep -q io.containerd.snapshotter; then \
		echo "containerd image store detected: importing per-image via ctr"; \
		for img in $(INT_IMG) $(INT_OBSERVER_IMG) $(INT_DEP_IMAGES); do \
			echo "importing $$img"; \
			$(CONTAINER_TOOL) save --platform linux/amd64 $$img | docker exec -i k3d-$(INT_CLUSTER)-server-0 ctr -n k8s.io images import - || exit 1; \
		done; \
	else \
		k3d image import $(INT_IMG) $(INT_OBSERVER_IMG) $(INT_DEP_IMAGES) -c $(INT_CLUSTER); \
	fi
	@echo "==> Installing CRDs..."
	kubectl --context $(INT_KUBE_CONTEXT) apply -f charts/mortise-core/crds/
	@echo "==> Waiting for CRDs to be established..."
	kubectl --context $(INT_KUBE_CONTEXT) wait --for=condition=Established crd/platformconfigs.mortise.mortise.dev --timeout=30s
	@echo "==> Installing test-only dependencies (registry, Gitea, BuildKit)..."
	kubectl --context $(INT_KUBE_CONTEXT) create namespace mortise-system --dry-run=client -o yaml | kubectl --context $(INT_KUBE_CONTEXT) apply -f -
	kubectl --context $(INT_KUBE_CONTEXT) apply -f test/integration/manifests/
	@echo "==> Waiting for test dependencies to become ready..."
	kubectl --context $(INT_KUBE_CONTEXT) -n mortise-test-deps rollout status deployment/registry  --timeout=120s
	kubectl --context $(INT_KUBE_CONTEXT) -n mortise-test-deps rollout status deployment/gitea     --timeout=180s
	kubectl --context $(INT_KUBE_CONTEXT) -n mortise-test-deps rollout status deployment/buildkitd --timeout=180s
	@echo "==> Fetching Helm chart dependencies..."
	helm repo add traefik https://traefik.github.io/charts
	helm repo add jetstack https://charts.jetstack.io
	helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
	helm repo update
	helm dependency build charts/mortise
	@echo "==> Installing Mortise via Helm..."
	helm --kube-context $(INT_KUBE_CONTEXT) upgrade --install mortise charts/mortise \
		--namespace mortise-system --create-namespace \
		--skip-crds \
		--set mortise-core.image.repository=mortise \
		--set mortise-core.image.tag=int \
		--set mortise-core.image.pullPolicy=Never \
		--set observer.enabled=true \
		--set observer.image=$(INT_OBSERVER_IMG) \
		--set observer.imagePullPolicy=Never \
		--set observer.metricsPollInterval=5s \
		--set registry.enabled=false \
		--set buildkit.enabled=false \
		--set platformConfig.enabled=false \
		--set cert-manager.enabled=false \
		--set metrics-server.args='{--kubelet-insecure-tls}' \
		--wait --timeout $(HELM_WAIT_TIMEOUT)
	@echo "==> Running integration tests..."
	# The suite builds its client from KUBECONFIG's current context; a
	# standalone kubeconfig from k3d pins it to this run's cluster.
	KUBECONFIG=$$(k3d kubeconfig write $(INT_CLUSTER)) go test -v -parallel 25 -tags integration -count=1 -timeout 45m ./test/integration/... || \
		{ echo "==> Tests failed; leaving cluster for diagnostics (run: k3d cluster delete $(INT_CLUSTER))"; exit 1; }
	@echo "==> Tearing down cluster..."
	k3d cluster delete $(INT_CLUSTER)

.PHONY: test-integration-fast
test-integration-fast: ## Run integration tests against the existing dev cluster (requires make dev-up + chart installed)
	go test -tags integration -count=1 -timeout 5m ./test/integration/...

##@ CI

.PHONY: ci-local
ci-local: ## Run every CI gate locally — THE push gate. SKIP_INTEGRATION=1 for fast mode (skips the cluster-backed gates).
	scripts/ci-local.sh $(if $(SKIP_INTEGRATION),--skip-integration)

##@ Chart Tests

.PHONY: verify-chart-dependency-drift
verify-chart-dependency-drift: ## Verify vendored mortise-core matches source AND chart RBAC covers the generated role
	@echo "==> Verifying vendored mortise-core package matches local chart source..."
	@subchart_version=$$(awk -F': ' '/^version:/ {print $$2; exit}' charts/mortise-core/Chart.yaml); \
	tgz="charts/mortise/charts/mortise-core-$${subchart_version}.tgz"; \
	tmpdir=$$(mktemp -d); \
	rbac_manifest="$$tmpdir/rbac.yaml"; \
	trap 'rm -rf "$$tmpdir"' EXIT; \
	test -f "$$tgz"; \
	tar -xzf "$$tgz" -C "$$tmpdir"; \
	diff -u <(helm show chart charts/mortise-core) <(helm show chart "$$tgz"); \
	diff -qr charts/mortise-core/templates "$$tmpdir/mortise-core/templates"; \
	diff -qr charts/mortise-core/crds "$$tmpdir/mortise-core/crds"; \
	diff -q charts/mortise-core/values.yaml "$$tmpdir/mortise-core/values.yaml"; \
	helm template packaged-core "$$tgz" --show-only templates/rbac.yaml --namespace mortise-system > "$$rbac_manifest"; \
	awk '/^kind:/ {kind=$$2} (kind=="ClusterRole" || kind=="Role") && /^[[:space:]]*resources:/ {sub(/.*resources:/, ""); gsub(/[][",]/, " "); print}' "$$rbac_manifest" \
		| tr -s ' ' '\n' | sed '/^$$/d' | sort -u > "$$tmpdir/chart-resources.txt"; \
	awk '/^ *resources:/{f=1;next} /^ *verbs:/{f=0} f&&/^ *- /{sub(/^ *- */,"");print}' config/rbac/role.yaml \
		| sort -u > "$$tmpdir/generated-resources.txt"; \
	missing=$$(comm -23 "$$tmpdir/generated-resources.txt" "$$tmpdir/chart-resources.txt" | tr '\n' ' '); \
	if [ -n "$$(echo $$missing)" ]; then \
		echo "chart RBAC (packaged) is missing resources the generated role grants: $$missing" >&2; \
		echo "sync charts/mortise-core/templates/rbac.yaml with config/rbac/role.yaml and repack the vendored tgz" >&2; \
		exit 1; \
	fi

.PHONY: test-charts
test-charts: ## Lint and template-test both Helm charts (no cluster required)
	@echo "==> Linting mortise-core..."
	helm lint charts/mortise-core
	@echo "==> Linting mortise (umbrella)..."
	$(MAKE) verify-chart-dependency-drift
	helm dependency build charts/mortise
	helm lint charts/mortise
	@echo "==> Template: umbrella defaults (all enabled, PVC storage)..."
	helm template test charts/mortise --namespace mortise-system >/dev/null
	@echo "==> Verifying PVCs render by default..."
# grep -q exits on the first match while helm is still writing; under
# `pipefail` that surfaces as SIGPIPE (exit 141), and for the negated
# assertions below `!` would flip that failure into a false pass.
	helm template test charts/mortise --namespace mortise-system \
		--show-only templates/registry.yaml | grep "PersistentVolumeClaim" >/dev/null
	helm template test charts/mortise --namespace mortise-system \
		--show-only templates/buildkit.yaml | grep "PersistentVolumeClaim" >/dev/null
	@echo "==> Template: all infra disabled (operator only)..."
	helm template test charts/mortise --namespace mortise-system \
		--set traefik.enabled=false \
		--set cert-manager.enabled=false \
		--set buildkit.enabled=false \
		--set registry.enabled=false \
		--set platformConfig.enabled=false \
		| grep "kind: Deployment" >/dev/null
	@echo "==> Verify disabled components don't render..."
	! helm template test charts/mortise --namespace mortise-system \
		--set traefik.enabled=false \
		--set cert-manager.enabled=false \
		--set buildkit.enabled=false \
		--set registry.enabled=false \
		--set platformConfig.enabled=false \
		| grep "buildkitd" >/dev/null
	@echo "==> Template: emptyDir fallback..."
	! helm template test charts/mortise --namespace mortise-system \
		--set registry.storage=emptyDir \
		--set buildkit.storage=emptyDir \
		--show-only templates/registry.yaml | grep "PersistentVolumeClaim" >/dev/null
	@echo "==> Template: mortise-core standalone..."
	helm template test charts/mortise-core --namespace mortise-system >/dev/null
	@echo "==> Verifying operator GOMEMLIMIT tracks the memory limit..."
	helm template test charts/mortise-core --namespace mortise-system \
		--show-only templates/deployment.yaml | grep "GOMEMLIMIT" >/dev/null
	@echo "==> Verifying registry + proxy securityContexts render by default..."
	helm template test charts/mortise --namespace mortise-system \
		--show-only templates/registry.yaml | grep "runAsNonRoot" >/dev/null
	helm template test charts/mortise --namespace mortise-system \
		--show-only templates/registry-proxy.yaml | grep "runAsNonRoot" >/dev/null
	@echo "==> Verifying buildkit is privileged by default but overridable..."
	helm template test charts/mortise --namespace mortise-system \
		--show-only templates/buildkit.yaml | grep "privileged: true" >/dev/null
	! helm template test charts/mortise --namespace mortise-system \
		--set buildkit.privileged=false \
		--show-only templates/buildkit.yaml | grep "privileged: true" >/dev/null
	! helm template test charts/mortise --namespace mortise-system \
		--set buildkit.securityContext.runAsUser=1000 \
		--show-only templates/buildkit.yaml | grep "privileged: true" >/dev/null
	@echo "==> Verifying deps namespace PSA labels + createNamespace toggle..."
	helm template test charts/mortise --namespace mortise-system \
		--show-only templates/namespace.yaml \
		| grep "pod-security.kubernetes.io/enforce: privileged" >/dev/null
# --show-only errors when the template renders empty, so `!` asserts absence.
	! helm template test charts/mortise --namespace mortise-system \
		--set buildInfra.createNamespace=false \
		--show-only templates/namespace.yaml >/dev/null 2>&1
	@echo "==> Verifying system namespace toggle (default off)..."
	! helm template test charts/mortise --namespace mortise-system \
		--show-only charts/mortise-core/templates/system-namespace.yaml >/dev/null 2>&1
	helm template test charts/mortise --namespace mortise-system \
		--set mortise-core.systemNamespace.create=true \
		--show-only charts/mortise-core/templates/system-namespace.yaml \
		| grep "pod-security.kubernetes.io/enforce: baseline" >/dev/null
	@echo ""
	@echo "All chart lint and template tests passed."

CHART_CLUSTER ?= mortise-chart

.PHONY: test-chart-integration
test-chart-integration: build-ui ## [Release only] Full chart integration: k3d + PVC persistence + install script (~10min)
	bash test/chart/run.sh

E2E_PORT ?= 8091
E2E_EMAIL ?= admin@local
E2E_PASSWORD ?= admin123

.PHONY: test-e2e
test-e2e: ## Run Playwright E2E suite against the dev cluster (requires make dev-up).
	@kubectl --context $(DEV_KUBE_CONTEXT) -n mortise-system rollout status deployment/mortise --timeout=30s
	@cd ui && npm ci --silent && npx playwright install chromium
	@# The port-forward runs under a supervisor loop: kubectl port-forward
	@# exits on the first dropped backend connection, and a mid-suite death
	@# fails every in-flight test with ERR_EMPTY_RESPONSE. setsid gives the
	@# loop its own process group so the trap's group-kill reaps the loop and
	@# whatever kubectl it currently has, without touching this recipe shell.
	@# macOS has no setsid: there the loop instead stops cooperatively via a
	@# sentinel file (created by the trap), after pkill -P reaps the kubectl
	@# it currently has — no respawn race, no group kill needed.
	@SETSID="$$(command -v setsid || true)"; \
	PF_STOP="$$(mktemp -u)"; \
	$$SETSID bash -c 'while [ ! -e "$$0" ]; do kubectl --context $(DEV_KUBE_CONTEXT) port-forward -n mortise-system svc/mortise $(E2E_PORT):80 >/dev/null 2>&1; sleep 1; done' "$$PF_STOP" & PF_PID=$$!; \
	trap "touch $$PF_STOP; kill -- -$$PF_PID 2>/dev/null || pkill -P $$PF_PID 2>/dev/null || true; wait $$PF_PID 2>/dev/null || true; rm -f $$PF_STOP" EXIT; \
	echo "==> Waiting for API at http://localhost:$(E2E_PORT)..."; \
	for i in $$(seq 1 30); do \
		curl -sf http://localhost:$(E2E_PORT)/api/auth/status >/dev/null 2>&1 && break; \
		sleep 1; \
	done; \
	curl -sf http://localhost:$(E2E_PORT)/api/auth/status >/dev/null 2>&1 || { echo "ERROR: API not reachable"; exit 1; }; \
	echo "==> Bootstrapping admin account..."; \
	curl -sf -X POST http://localhost:$(E2E_PORT)/api/auth/setup \
		-H "Content-Type: application/json" \
		-d '{"email":"$(E2E_EMAIL)","password":"$(E2E_PASSWORD)"}' >/dev/null 2>&1 || true; \
	echo "==> Running Playwright tests..."; \
	cd ui && MORTISE_BASE_URL=http://localhost:$(E2E_PORT) \
		MORTISE_ADMIN_EMAIL=$(E2E_EMAIL) \
		MORTISE_ADMIN_PASSWORD=$(E2E_PASSWORD) \
		npx playwright test

.PHONY: dev-reload
dev-reload: build-ui ## Rebuild image, re-apply CRDs + chart, restart Mortise in existing cluster
	$(CONTAINER_TOOL) build --target operator -t $(DEV_IMG) .
	$(CONTAINER_TOOL) build --target observer -t $(DEV_OBSERVER_IMG) .
	k3d image import $(DEV_IMG) $(DEV_OBSERVER_IMG) -c $(DEV_CLUSTER)
	kubectl --context $(DEV_KUBE_CONTEXT) apply -f charts/mortise-core/crds/
	@dev_img_repo='$(DEV_IMG)'; \
	dev_img_repo=$${dev_img_repo%:*}; \
	dev_img_tag='$(DEV_IMG)'; \
	dev_img_tag=$${dev_img_tag##*:}; \
	helm --kube-context $(DEV_KUBE_CONTEXT) upgrade mortise charts/mortise \
		--namespace mortise-system \
		--set mortise-core.image.repository=$$dev_img_repo \
		--set mortise-core.image.tag=$$dev_img_tag \
		--set mortise-core.image.pullPolicy=Never \
		--set observer.enabled=true \
		--set observer.image=$(DEV_OBSERVER_IMG) \
		--set observer.imagePullPolicy=Never \
		--set observer.metricsPollInterval=5s \
		--set registry.enabled=false \
		--set buildkit.enabled=false \
		--set platformConfig.enabled=false \
		--set cert-manager.enabled=false \
		--set metrics-server.args='{--kubelet-insecure-tls}'
	# Restart required: PlatformConfig registry/build settings load at boot only.
	kubectl --context $(DEV_KUBE_CONTEXT) rollout restart deployment/mortise -n mortise-system
	kubectl --context $(DEV_KUBE_CONTEXT) rollout status deployment/mortise -n mortise-system --timeout 60s
	@-pkill -f "[k]ubectl port-forward.*8090" >/dev/null 2>&1
	@if command -v setsid >/dev/null 2>&1; then \
		nohup setsid kubectl --context $(DEV_KUBE_CONTEXT) port-forward -n mortise-system svc/mortise 8090:80 >$(DEV_PORT_FORWARD_LOG) 2>&1 </dev/null & \
	else \
		nohup kubectl --context $(DEV_KUBE_CONTEXT) port-forward -n mortise-system svc/mortise 8090:80 >$(DEV_PORT_FORWARD_LOG) 2>&1 </dev/null & \
	fi
	@for i in $$(seq 1 15); do \
		if curl -fsS $(DEV_PORT_FORWARD_READY_URL) >/dev/null 2>&1; then \
			break; \
		fi; \
		if [ $$i -eq 15 ]; then \
			echo "ERROR: port-forward failed to become reachable; see $(DEV_PORT_FORWARD_LOG)"; \
			tail -n 20 $(DEV_PORT_FORWARD_LOG) >/dev/null 2>&1 && tail -n 20 $(DEV_PORT_FORWARD_LOG); \
			exit 1; \
		fi; \
		sleep 1; \
	done
	@echo "Reloaded — http://localhost:8090"

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build --target operator -t ${IMG} .

.PHONY: docker-build-observer
docker-build-observer: ## Build docker image with the observer.
	$(CONTAINER_TOOL) build --target observer -t ${IMG}-observer .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name capybara-builder
	$(CONTAINER_TOOL) buildx use capybara-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm capybara-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.1

#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.8.0
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
