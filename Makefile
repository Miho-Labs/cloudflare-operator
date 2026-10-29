# Image URL to use for docker build/push
IMG ?= ghcr.io/miho-labs/cloudflare-operator:dev
CHART := charts/cloudflare-operator
# OCI registry the chart is pushed to by `make helm-push` and the release workflow.
CHART_REGISTRY ?= oci://ghcr.io/miho-labs/charts
CHART_VERSION ?= $(shell sed -n 's/^version: //p' $(CHART)/Chart.yaml)
GOFLAGS ?= -mod=mod
export GOFLAGS

.PHONY: all
all: build

##@ Development

.PHONY: generate
generate: ## Generate DeepCopy methods.
	go tool controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: manifests
manifests: ## Generate CRDs (into the Helm chart) and RBAC rules.
	go tool controller-gen crd rbac:roleName=cloudflare-operator paths="./api/..." paths="./internal/..." \
		output:crd:dir=$(CHART)/crds output:rbac:dir=config/rbac
	go run ./hack/helmrbac -in config/rbac/role.yaml -out $(CHART)/templates/clusterrole.yaml

.PHONY: fmt
fmt: ## Run gofmt.
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: ## Run unit tests (fake Kubernetes client + fake Cloudflare API).
	go test ./... -coverprofile cover.out

.PHONY: verify
verify: generate manifests fmt vet ## Regenerate everything and fail if the tree is dirty.
	@git diff --exit-code || (echo "generated files are out of date; run 'make generate manifests fmt'" && exit 1)

##@ Build

.PHONY: build
build: generate ## Build the manager binary.
	CGO_ENABLED=0 go build -o bin/manager ./cmd

.PHONY: run
run: generate ## Run the manager against the current kubeconfig.
	go run ./cmd --leader-elect=false

.PHONY: docker-build
docker-build: ## Build the container image.
	docker build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push the container image.
	docker push $(IMG)

##@ Deployment

.PHONY: helm-lint
helm-lint: ## Lint the chart.
	helm lint $(CHART)

.PHONY: helm-template
helm-template: ## Render the chart to stdout.
	helm template cloudflare-operator $(CHART) --namespace cloudflare-operator

.PHONY: helm-package
helm-package: ## Package the chart into dist/ with CHART_VERSION.
	helm package $(CHART) --version $(CHART_VERSION) --app-version $(CHART_VERSION) --destination dist

.PHONY: helm-push
helm-push: helm-package ## Push the packaged chart to CHART_REGISTRY (run `helm registry login ghcr.io` first).
	helm push dist/cloudflare-operator-$(CHART_VERSION).tgz $(CHART_REGISTRY)

.PHONY: install
install: ## Install the operator with Helm into the cloudflare-operator namespace.
	helm upgrade --install cloudflare-operator $(CHART) --namespace cloudflare-operator --create-namespace --set image.tag=$(word 2,$(subst :, ,$(IMG)))

.PHONY: uninstall
uninstall: ## Uninstall the operator.
	helm uninstall cloudflare-operator --namespace cloudflare-operator

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
