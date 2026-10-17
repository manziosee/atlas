# Atlas developer commands. Run `make help`.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/manziosee/atlas/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)
IMAGE   ?= atlas

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## ---- Build & test ----------------------------------------------------------
.PHONY: build
build: ## Build atlas-node and atlas-cli into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

.PHONY: test
test: ## Run tests with the race detector
	go test -race -shuffle=on -count=1 ./...

.PHONY: cover
cover: ## Run tests with coverage report
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: fmt
fmt: ## Format code
	gofmt -s -w . && go run golang.org/x/tools/cmd/goimports@latest -local github.com/manziosee/atlas -w .

## ---- Security ----------------------------------------------------------------
.PHONY: vuln
vuln: ## Scan dependencies for known vulnerabilities (govulncheck)
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: secrets-scan
secrets-scan: ## Scan git history for leaked secrets (gitleaks)
	docker run --rm -v "$(CURDIR):/repo" ghcr.io/gitleaks/gitleaks:v8.28.0 git /repo --redact

.PHONY: image-scan
image-scan: docker ## Scan the container image for CVEs (grype)
	docker run --rm -v /var/run/docker.sock:/var/run/docker.sock anchore/grype:latest $(IMAGE):$(VERSION) --fail-on high

.PHONY: security
security: vuln secrets-scan image-scan ## Run every security check

## ---- Secrets & certs ---------------------------------------------------------
.PHONY: env
env: ## Create .env and generate missing secrets
	./scripts/gen-secrets.sh .env

.PHONY: certs
certs: ## Generate a local CA + server/client certs for mTLS
	./scripts/gen-certs.sh deploy/certs

## ---- Containers --------------------------------------------------------------
.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t $(IMAGE):$(VERSION) -t $(IMAGE):dev .

.PHONY: up
up: env ## Start the local 3-node cluster + Prometheus + Grafana
	docker compose up --build -d
	@echo "API: http://127.0.0.1:8081  Prometheus: http://127.0.0.1:9091  Grafana: http://127.0.0.1:3000"

.PHONY: down
down: ## Stop the local cluster
	docker compose down

.PHONY: chaos
chaos: ## Kill a random node, wait, bring it back (watch Grafana)
	./scripts/chaos.sh

## ---- Kubernetes --------------------------------------------------------------
.PHONY: k8s-render
k8s-render: ## Render the dev overlay to stdout
	kubectl kustomize deploy/k8s/overlays/dev

.PHONY: k8s-apply
k8s-apply: ## Apply the dev overlay to the current kube-context
	kubectl apply -k deploy/k8s/overlays/dev
