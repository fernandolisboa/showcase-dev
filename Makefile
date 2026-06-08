# Showcase control plane — developer tasks. See README.md and docs/adr/0009.
.DEFAULT_GOAL := help
SHELL := /bin/bash
BIN := bin/controlplane
# Explicit roots so Go tooling never descends into web/node_modules (some npm
# packages ship stray .go files).
GO_PKGS := ./cmd/... ./internal/...
# Platform-owned egress-proxy sidecar image tag (ADR-0011); must match the
# control plane's EGRESS_PROXY_IMAGE (internal/config default).
EGRESS_PROXY_IMAGE ?= showcase-dev/egress-proxy:latest

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- Go (control plane) ------------------------------------------------------
.PHONY: go-build go-test integration go-lint tidy
go-build: ## Build the control-plane binary (embeds current internal/web/dist)
	go build -o $(BIN) ./cmd/controlplane
go-test: ## Run Go unit tests
	go test -race $(GO_PKGS)
integration: ## Run integration tests (requires Docker; builder tests need rootless BuildKit)
	go test -tags integration -timeout 600s $(GO_PKGS)
go-lint: ## Vet Go (plus golangci-lint if installed)
	go vet $(GO_PKGS)
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run $(GO_PKGS) || echo "golangci-lint not installed; ran go vet only"
tidy: ## Tidy go.mod
	go mod tidy

# --- Web (React UI) ----------------------------------------------------------
.PHONY: web-install web-build web-test web-lint web-dev
web-install: ## Install web dependencies (npm ci)
	cd web && npm ci
web-build: ## Build the React app into internal/web/dist (embedded by Go)
	cd web && npm run build
web-test: ## Run web tests
	cd web && npm run test
web-lint: ## Lint the web app
	cd web && npm run lint
web-dev: ## Run the Vite dev server (proxies /api + /healthz to :8080)
	cd web && npm run dev

# --- Combined ----------------------------------------------------------------
.PHONY: build test lint run dev clean
build: web-install web-build go-build ## Full build: UI embedded into the binary
test: go-test web-test ## Run all tests
lint: go-lint web-lint ## Run all linters
run: build ## Build everything, then run the control plane
	@set -a; [ -f .env ] && . ./.env; set +a; ./$(BIN)
dev: ## Run the Go control plane (serves a placeholder until the UI is built)
	@set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/controlplane
clean: ## Remove build artifacts
	rm -rf $(BIN)

# --- Local Postgres (requires Docker) ----------------------------------------
.PHONY: db-up db-down proxy-up proxy-down
db-up: ## Start local Postgres (compose.dev.yml)
	docker compose -f compose.dev.yml up -d
db-down: ## Stop local Postgres
	docker compose -f compose.dev.yml down

# --- Platform images ---------------------------------------------------------
.PHONY: egress-proxy-image
egress-proxy-image: ## Build the per-Session egress-proxy sidecar image (ADR-0011)
	docker build -t $(EGRESS_PROXY_IMAGE) -f cmd/egress-proxy/Dockerfile .

# --- Live-Session proxy (Traefik) --------------------------------------------
proxy-up: ## Start Traefik in front of Sessions (run the control plane first)
	docker compose -f deploy/traefik/compose.yml up -d
proxy-down: ## Stop Traefik
	docker compose -f deploy/traefik/compose.yml down
