.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint
BIN_DIR := bin
BINARY := $(BIN_DIR)/txmill

.PHONY: help build test lint tidy run ci tools clean

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the txmill binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BINARY) ./cmd/txmill

test: ## Run unit tests
	$(GO) test -race -count=1 ./...

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
