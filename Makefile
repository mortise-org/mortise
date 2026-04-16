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
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	# Sync generated CRDs into the Helm chart so `helm install` ships the real schema.
	cp config/crd/bases/*.yaml charts/mortise/crds/

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

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

.PHONY: build-cli
build-cli: fmt vet ## Build CLI binary.
	go build -o bin/mortise ./cmd/cli

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

##@ Dev Cluster

DEV_CLUSTER ?= mortise-dev
DEV_IMG ?= mortise:dev

.PHONY: dev-up
dev-up: ## Create k3d dev cluster, build image, install Mortise
	@echo "==> Creating k3d cluster $(DEV_CLUSTER)..."
	@k3d cluster list | grep -q $(DEV_CLUSTER) || k3d cluster create $(DEV_CLUSTER) \
		--port "8090:80@loadbalancer" \
		--k3s-arg "--disable=traefik@server:0" \
		--wait
	@echo "==> Building Docker image..."
	$(CONTAINER_TOOL) build -t $(DEV_IMG) .
	@echo "==> Loading image into k3d..."
	k3d image import $(DEV_IMG) -c $(DEV_CLUSTER)
	@echo "==> Installing CRDs..."
	kubectl apply -f charts/mortise/crds/
	@echo "==> Installing Mortise via Helm..."
	helm upgrade --install mortise charts/mortise \
		--namespace mortise-system --create-namespace \
		--set image.repository=mortise \
		--set image.tag=dev \
		--set image.pullPolicy=Never \
		--wait --timeout 60s
	@echo ""
	@echo "✓ Mortise is running!"
	@echo "  API: kubectl port-forward -n mortise-system svc/mortise 8090:80"
	@echo "  Then open http://localhost:8090"

.PHONY: dev-down
dev-down: ## Delete k3d dev cluster
	k3d cluster delete $(DEV_CLUSTER)

##@ Integration Tests

INT_CLUSTER ?= mortise-int
INT_IMG ?= mortise:int

.PHONY: test-integration
test-integration: ## Create k3d cluster, install chart + test deps, run integration tests, tear down
	@echo "==> Deleting any stale k3d cluster from a prior run..."
	-k3d cluster delete $(INT_CLUSTER) >/dev/null 2>&1
	@echo "==> Creating k3d cluster $(INT_CLUSTER)..."
	# --config wires the containerd mirror rule that makes
	# registry.mortise-test-deps.svc:5000 pulls reachable from the node.
	k3d cluster create --config test/integration/k3d-config.yaml --wait
	@echo "==> Building Docker image..."
	$(CONTAINER_TOOL) build -t $(INT_IMG) .
	@echo "==> Loading image into k3d..."
	k3d image import $(INT_IMG) -c $(INT_CLUSTER)
	@echo "==> Installing CRDs..."
	kubectl apply -f charts/mortise/crds/
	@echo "==> Installing test-only dependencies (Zot, Gitea, BuildKit)..."
	kubectl create namespace mortise-system --dry-run=client -o yaml | kubectl apply -f -
	kubectl apply -f test/integration/manifests/
	@echo "==> Waiting for test dependencies to become ready..."
	kubectl -n mortise-test-deps rollout status deployment/registry  --timeout=120s
	kubectl -n mortise-test-deps rollout status deployment/gitea     --timeout=180s
	kubectl -n mortise-test-deps rollout status deployment/buildkitd --timeout=180s
	@echo "==> Installing Mortise via Helm..."
	helm upgrade --install mortise charts/mortise \
		--namespace mortise-system --create-namespace \
		--set image.repository=mortise \
		--set image.tag=int \
		--set image.pullPolicy=Never \
		--wait --timeout 120s
	@echo "==> Running integration tests..."
	go test -tags integration -count=1 -timeout 15m ./test/integration/... || { \
		k3d cluster delete $(INT_CLUSTER); exit 1; \
	}
	@echo "==> Tearing down cluster..."
	k3d cluster delete $(INT_CLUSTER)

.PHONY: test-integration-fast
test-integration-fast: ## Run integration tests against the existing dev cluster (requires make dev-up + chart installed)
	go test -tags integration -count=1 -timeout 5m ./test/integration/...

.PHONY: test-e2e
test-e2e: ## Run Playwright E2E suite against existing dev cluster.
	cd ui && npm install && npx playwright install --with-deps chromium && npm run test:e2e

.PHONY: dev-reload
dev-reload: ## Rebuild image, re-apply CRDs + chart, restart Mortise in existing cluster
	$(CONTAINER_TOOL) build -t $(DEV_IMG) .
	k3d image import $(DEV_IMG) -c $(DEV_CLUSTER)
	kubectl apply -f charts/mortise/crds/
	helm upgrade mortise charts/mortise \
		--namespace mortise-system \
		--set image.repository=mortise \
		--set image.tag=dev \
		--set image.pullPolicy=Never
	kubectl rollout restart deployment/mortise -n mortise-system
	kubectl rollout status deployment/mortise -n mortise-system --timeout 60s

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

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
