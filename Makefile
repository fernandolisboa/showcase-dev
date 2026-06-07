# Showcase control plane — developer tasks. See README.md and docs/adr/0009.
.DEFAULT_GOAL := help
SHELL := /bin/bash
BIN := bin/controlplane

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- Go (control plane) ------------------------------------------------------
.PHONY: go-build go-test go-lint tidy
go-build: ## Build the control-plane binary (embeds current internal/web/dist)
	go build -o $(BIN) ./cmd/controlplane
go-test: ## Run Go tests
	go test -race ./...
go-lint: ## Vet Go (plus golangci-lint if installed)
	go vet ./...
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; ran go vet only"
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
.PHONY: db-up db-down
db-up: ## Start local Postgres (compose.dev.yml)
	docker compose -f compose.dev.yml up -d
db-down: ## Stop local Postgres
	docker compose -f compose.dev.yml down
