# punchcard — developer tasks. `make check` mirrors the CI gate.
SHELL := bash
.DEFAULT_GOAL := help

GOBIN := $(shell go env GOPATH)/bin
BINARY := bin/punchcard
PKG := ./...

# Pinned tool versions (keep in sync with .github/workflows/ci.yml).
SQLC_VERSION        := v1.31.1
GOOSE_VERSION       := v3.27.2
GOLANGCI_VERSION    := v2.12.2
GOVULNCHECK_VERSION := v1.6.0
GOSEC_VERSION       := v2.28.0

VERSION ?= dev
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: tools
tools: ## Install pinned dev tools into $GOPATH/bin
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)

.PHONY: build
build: ## Build the binary
	go build $(LDFLAGS) -o $(BINARY) ./cmd/punchcard

.PHONY: run
run: ## Run the server (loads ./.env if present)
	@set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/punchcard serve

.PHONY: test
test: ## Run all tests (needs Docker or TEST_DATABASE_URL)
	go test -race -count=1 $(PKG)

.PHONY: sqlc
sqlc: ## Regenerate sqlc code from db/queries + db/migrations
	$(GOBIN)/sqlc generate

.PHONY: openapi
openapi: ## Regenerate docs/openapi.json from the code
	go test ./internal/http -run TestOpenAPISpec -update

.PHONY: openapi-check
openapi-check: ## Fail if docs/openapi.json is out of sync
	go test ./internal/http -run TestOpenAPISpec -count=1

.PHONY: lint
lint: ## Run golangci-lint
	$(GOBIN)/golangci-lint run $(PKG)

.PHONY: fmt
fmt: ## Auto-format (gofmt + goimports via golangci-lint)
	$(GOBIN)/golangci-lint fmt $(PKG)

.PHONY: vet
vet: ## Run go vet
	go vet $(PKG)

.PHONY: sec
sec: ## Run gosec
	$(GOBIN)/gosec -quiet -exclude-generated $(PKG)

.PHONY: vuln
vuln: ## Run govulncheck
	$(GOBIN)/govulncheck $(PKG)

.PHONY: migrate-up
migrate-up: ## Apply all migrations
	go run ./cmd/punchcard migrate up

.PHONY: migrate-down
migrate-down: ## Roll back one migration
	go run ./cmd/punchcard migrate down

.PHONY: docker-up
docker-up: ## Start the self-host stack (app + postgres + caddy)
	docker compose -f deploy/docker-compose.yml up --build -d

.PHONY: docker-down
docker-down: ## Stop the self-host stack
	docker compose -f deploy/docker-compose.yml down

.PHONY: check
check: vet lint sec vuln openapi-check test build ## Run the full local gate (mirrors CI)
	@echo "all checks passed"
