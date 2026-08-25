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

.PHONY: web
web: ## Build the web UI into internal/http/embedded/dist (commit the result)
	cd web && npm install && npm run build

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

.PHONY: ios-core
ios-core: ## Test the iOS widget's resolve rules (pure Swift, no simulator)
# On a Mac this must never quietly pass having run nothing — that is the exact
# failure mode CLAUDE.md warns about for DOCKER_HOST. Elsewhere there is no
# Swift toolchain to have, so it says so and moves on.
	@if [ "$$(uname -s)" != "Darwin" ]; then \
		echo "SKIPPED ios-core: not macOS, no Swift toolchain"; \
	else \
		command -v swift >/dev/null 2>&1 || { echo "ios-core: swift not found on macOS"; exit 1; }; \
		cd web/src-tauri/ios/widget/HelvaWidgetCore && swift test; \
	fi

.PHONY: web-unit
web-unit: ## Frontend typecheck + unit checks that need no browser and no server
# `tsc -b` is first because until 2026-08-14 it was not here at all, and nothing
# else in the gate ran it: `npm run lint` is oxlint (which does not typecheck)
# and the suites below are plain node. `tsc -b` lived only inside `npm run
# build`, which only `make web` calls — and `check` does not call `make web`.
# So a type error passed the one gate this project has. It covers web/src AND
# web/extension/src, both projects of the solution-style web/tsconfig.json.
#
# Then two suites, both pure node — no node_modules, no browser, and Node >= 22
# strips the .ts imports itself:
#
#   position.mjs  the TS port must still match internal/service/position.go.
#                 The Go half runs under `test`, so a change to position.go is
#                 already caught; this is the other direction. Without it an
#                 edit to web/src/offline/position.ts drifts silently and
#                 reorders made offline land somewhere different from the same
#                 reorder made online.
#   revive.mjs    the IndexedDB read boundary. A row written by an older app
#                 version must never come back with a required field missing.
#
# Same rule as ios-core: it must never quietly pass having run nothing.
	@if ! command -v node >/dev/null 2>&1; then \
		echo "web-unit: node not found"; exit 1; \
	fi
	@cd web && npx tsc -b && npm run --silent lint && npm run --silent test:unit && echo "web typecheck + lint clean"

.PHONY: version-parity
version-parity: ## Fail if the four files carrying the app version disagree
	@./scripts/version-parity.sh

.PHONY: check
check: vet lint sec vuln openapi-check test ios-core web-unit version-parity build ## Run the full local gate (mirrors CI)
	@echo "all checks passed"
