# waitformeet
#
# The default target runs everything CI runs, so a green `make` means a green build.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := all

VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE        ?= ghcr.io/mrcat71/waitformeet
CHART_DIR    := deploy/helm/waitformeet
TYPESCRIPT   ?= typescript@6.0.3
LOCAL_DATA   ?= ./tmp-data

.PHONY: all
all: assets fmt vet lint test typecheck helm-lint ## Run the full local check suite

.PHONY: help
help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: assets
assets: ## Bundle the TypeScript into internal/web/static/dist (no Node needed)
	go run ./tools/assets

.PHONY: build
build: assets ## Build the binary
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
		-o bin/waitformeet ./cmd/waitformeet

.PHONY: fmt
fmt: ## Format Go sources
	gofmt -w -s $(shell find . -name '*.go' -not -path './vendor/*')

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: test
test: ## Run the tests
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Run the tests and open the coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: lint
lint: ## Run golangci-lint if it is installed
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, skipping (CI runs it)"; \
	fi

.PHONY: typecheck
typecheck: ## Type-check the TypeScript (needs Node; esbuild does not type-check)
	@if command -v npx >/dev/null 2>&1; then \
		npx --yes --package=$(TYPESCRIPT) tsc --noEmit -p tsconfig.json; \
		npx --yes --package=$(TYPESCRIPT) tsc --noEmit -p tsconfig.sw.json; \
	else \
		echo "npx not installed, skipping type check (CI runs it)"; \
	fi

.PHONY: assets-check
assets-check: assets ## Fail if the committed bundle is out of date
	@git diff --exit-code -- internal/web/static/dist \
		|| { echo "the committed bundle is stale; run 'make assets' and commit the result"; exit 1; }

.PHONY: run
run: assets ## Run locally against ./tmp-data with a throwaway admin
	WFM_DATA_DIR=$(LOCAL_DATA) \
	WFM_BASE_URL=http://localhost:8080 \
	WFM_COOKIE_SECURE=false \
	WFM_LOG_LEVEL=debug \
	WFM_SESSION_SECRET=local-development-session-secret-3 \
	WFM_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
	WFM_BOOTSTRAP_ADMIN_PASSWORD=local-development-password \
	go run ./cmd/waitformeet

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

.PHONY: helm-lint
helm-lint: ## Lint the chart
	@if command -v helm >/dev/null 2>&1; then \
		helm lint $(CHART_DIR); \
	else \
		echo "helm not installed, skipping"; \
	fi

.PHONY: helm-template
helm-template: ## Render the chart with the default values
	helm template waitformeet $(CHART_DIR)

.PHONY: tidy
tidy: ## Tidy the module
	go mod tidy

.PHONY: clean
clean: ## Remove build output and the local database
	rm -rf bin coverage.out $(LOCAL_DATA)
