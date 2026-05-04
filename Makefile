.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint
BIN_DIR := bin
BINARY := $(BIN_DIR)/txmill

.PHONY: help build test lint tidy run ci tools clean migrate-up migrate-down migrate-status alert-test dev-up dev-down dev-logs

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the txmill binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BINARY) ./cmd/txmill
	$(GO) build -o $(BIN_DIR)/migrate ./cmd/migrate

test: ## Run unit tests
	$(GO) test -race -count=1 -p 1 ./...

lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

tidy: ## Tidy go modules
	$(GO) mod tidy

run: build ## Build and run
	$(BINARY)

ci: lint test build ## Lint + test + build (what CI runs)

tools: ## Install dev tools (golangci-lint)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

migrate-up: ## Apply all pending migrations (TXMILL_DB_URL=...)
	$(GO) run ./cmd/migrate up

migrate-down: ## Roll back the most recent migration
	$(GO) run ./cmd/migrate down

migrate-status: ## Show migration status
	$(GO) run ./cmd/migrate status

alert-test: ## Send a test alert via every configured transport
	$(GO) run ./cmd/alert-test

dev-up: ## Start local dev dependencies (Postgres) via docker compose
	docker compose up -d --wait

dev-down: ## Stop local dev dependencies (preserves data volume)
	docker compose down

dev-logs: ## Tail local dev dependency logs
	docker compose logs -f
