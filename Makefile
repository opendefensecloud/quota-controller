# Include ODC common make targets
DEV_KIT_VERSION := v1.0.10
-include common.mk
common.mk:
	curl --fail -sSL https://raw.githubusercontent.com/opendefensecloud/dev-kit/$(DEV_KIT_VERSION)/common.mk -o common.mk.download && \
	mv common.mk.download $@

DOCKER ?= docker
ENVTEST_K8S_VERSION ?= 1.31.0

IMG_REGISTRY ?= ghcr.io/opendefensecloud
IMG_TAG ?= latest
CONTROLLER_IMG ?= $(IMG_REGISTRY)/quota-controller:$(IMG_TAG)
WEBHOOK_IMG ?= $(IMG_REGISTRY)/quota-webhook:$(IMG_TAG)

TIMESTAMP := $(shell date '+%Y%m%d%H%M%S')
DEV_TAG ?= dev.$(TIMESTAMP)
export DEV_TAG

LICENSE := apache
LICENSE_COMMENT := BWI GmbH and Quota Controller contributors

##@ Development

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate deepcopy methods.
	$(CONTROLLER_GEN) object paths="./api/..."

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Generate CRDs into config/crds.
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:dir=config/crds

.PHONY: fmt
fmt: $(ADDLICENSE) $(GOLANGCI_LINT) ## Add license headers and format code.
	$(MAKE) addlicense license=$(LICENSE) comment='$(LICENSE_COMMENT)' pattern='*\.go'
	$(GO) fmt ./...
	$(GOLANGCI_LINT) run --fix

.PHONY: lint
lint: lint-no-golangci golangci-lint ## Run all linters.

.PHONY: lint-no-golangci
lint-no-golangci: ## Run linters except golangci-lint (license headers).
	$(MAKE) addlicense-check license=$(LICENSE) comment='$(LICENSE_COMMENT)' pattern='*\.go'

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

##@ Build

.PHONY: build
build: generate ## Build the controller and webhook binaries.
	$(GO) build -o $(LOCALBIN)/quota-controller ./cmd/controller/
	$(GO) build -o $(LOCALBIN)/quota-webhook ./cmd/webhook/

.PHONY: run-controller
run-controller: generate ## Run the controller from source.
	$(GO) run ./cmd/controller/

.PHONY: run-webhook
run-webhook: generate ## Run the webhook server from source.
	$(GO) run ./cmd/webhook/

.PHONY: docker-build
docker-build: ## Build the Docker image.
	$(DOCKER) build --platform linux/amd64,linux/arm64 -t $(CONTROLLER_IMG) .

.PHONY: docker-push
docker-push: ## Push the Docker image.
	$(DOCKER) push $(CONTROLLER_IMG)

.PHONY: helm-package
helm-package: manifests ## Package Helm chart.
	helm package charts/quota-controller

##@ Testing

.PHONY: test
test: $(SETUP_ENVTEST) ## Run unit + integration tests (excludes e2e). envtest binaries via setup-envtest.
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use -p path $(ENVTEST_K8S_VERSION))" $(GO) test -race ./... $(testargs)

.PHONY: test-e2e
test-e2e: ## Run e2e tests (requires the e2e build tag; needs kind + kcp + helm).
	$(GO) test -tags e2e -timeout 30m ./test/e2e/ $(testargs)

.PHONY: clean-e2e
clean-e2e: ## Remove the kind cluster created by e2e tests.
	-$(KIND) delete cluster --name quota-e2e 2>/dev/null
