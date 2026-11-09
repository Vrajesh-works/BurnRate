# BurnRate developer commands. `make help` lists them.
SHELL := /usr/bin/env bash
VERSION ?= $(shell awk '/^appVersion:/ {gsub(/"/, "", $$2); print $$2}' deploy/helm/burnrate/Chart.yaml)
IMAGE ?= burnrate:$(VERSION)
CHART := deploy/helm/burnrate
SLOTH_IMAGE ?= ghcr.io/slok/sloth:v0.16.0
PROMTOOL_IMAGE ?= prom/prometheus:v3.16.0

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/burnrate ./cmd/burnrate

.PHONY: run
run: ## Run the service locally on :8080
	go run ./cmd/burnrate

.PHONY: test
test: ## Unit and HTTP tests (RACE=1 adds the race detector, needs cgo)
	go test $(if $(RACE),-race,) -count=1 -coverprofile=coverage.out ./...

.PHONY: lint
lint: ## gofmt and go vet (CI also runs golangci-lint)
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...

.PHONY: image
image: ## Build the container image
	docker build -t $(IMAGE) --build-arg VERSION=$(VERSION) .

.PHONY: helm-lint
helm-lint: ## Lint and render the chart with features on and off
	helm lint $(CHART)
	helm lint $(CHART) -f $(CHART)/values-kind.yaml
	helm template burnrate $(CHART) >/dev/null
	helm template burnrate $(CHART) -f $(CHART)/values-kind.yaml >/dev/null
	helm template burnrate $(CHART) --set autoscaling.enabled=false,podDisruptionBudget.enabled=false >/dev/null
	helm lint deploy/argocd/apps

.PHONY: slo
slo: ## Regenerate Prometheus SLO rules from slo/burnrate.sloth.yaml
	docker run --rm -u $$(id -u):$$(id -g) -v $(CURDIR):/work -w /work $(SLOTH_IMAGE) \
		generate -i slo/burnrate.sloth.yaml -o $(CHART)/files/slo-rules.yaml

.PHONY: slo-check
slo-check: slo ## Fail if the committed SLO rules are stale or invalid
	git diff --exit-code -- $(CHART)/files/slo-rules.yaml
	docker run --rm -v $(CURDIR)/$(CHART)/files:/f:ro --entrypoint promtool $(PROMTOOL_IMAGE) check rules /f/slo-rules.yaml

.PHONY: up
up: ## Full stack on kind: Prometheus, Grafana, OTel, Argo Rollouts, BurnRate
	scripts/kind-up.sh

.PHONY: up-minimal
up-minimal: ## Minimal kind stack: metrics-server and BurnRate (what CI runs)
	scripts/kind-up.sh --minimal

.PHONY: gitops
gitops: ## Install Argo CD and sync everything from Git (REVISION=main)
	scripts/gitops-up.sh $(REVISION)

.PHONY: down
down: ## Delete the kind cluster
	scripts/kind-down.sh

.PHONY: compose
compose: ## Run the stack with Docker Compose (no Kubernetes)
	docker compose up --build

.PHONY: smoke
smoke: ## Smoke test through a port-forward to the Service
	@kubectl -n burnrate port-forward svc/burnrate 18080:80 >/dev/null 2>&1 & pf=$$!; \
	trap 'kill $$pf' EXIT; \
	for i in $$(seq 1 30); do curl -fs localhost:18080/healthz >/dev/null && break; sleep 1; done; \
	scripts/smoke.sh http://localhost:18080

.PHONY: chaos
chaos: ## Kill a pod and record recovery time (FORCE=1 for SIGKILL)
	scripts/chaos/pod-kill.sh $(if $(FORCE),--force,)

.PHONY: load
load: ## k6 load test in-cluster; records HPA scale-out
	scripts/load.sh

.PHONY: canary
canary: ## Deploy a failing canary and record the automatic rollback
	scripts/canary-demo.sh

.PHONY: e2e
e2e: up-minimal smoke chaos ## Minimal cluster, smoke test and pod-kill chaos

.PHONY: clean
clean: ## Remove build and experiment output
	go clean
	rm -f bin/burnrate coverage.out
