# domieface-server
#
# Run `make` or `make help` for the list of targets.
#
# Recipes are POSIX sh, and work from PowerShell, cmd and Git Bash alike --
# Git for Windows puts sh.exe on PATH and GNU Make hands recipes to it.
#
# One Windows quirk shapes the style below. For a recipe with no quoted
# argument, GNU Make skips the shell and calls CreateProcess directly, so
# anything that is not a real .exe on PATH -- awk, rm, shell builtins -- is not
# found. Quoting an argument is enough to route the line through sh instead.
# That is why arguments are quoted throughout, including ones that look like
# they do not need it.
#
# SHELL is deliberately left unset: make already picks /bin/sh on Unix and finds
# Git's sh.exe on PATH on Windows. Pinning it to /bin/sh breaks the latter.

.DEFAULT_GOAL := help

BINARY_NAME := domieface-server
BIN_DIR     := bin

ifeq ($(OS),Windows_NT)
	BINARY := $(BIN_DIR)/$(BINARY_NAME).exe
else
	BINARY := $(BIN_DIR)/$(BINARY_NAME)
endif

# Local settings, gitignored. Anything set here wins over the defaults below,
# and docker compose reads the same file for ${POSTGRES_PORT}, so one place
# configures both. Start from .env.example. The leading - means "carry on if it
# does not exist".
-include .env

# Every variable, including whatever .env defines, reaches `make run` and the
# server process.
export

# Port the API listens on. Change it if 8080 is already taken -- a local Apache
# or another dev server is the usual culprit.
PORT ?= 8080

# Host port Postgres is published on. Change it if 5432 is already taken --
# either POSTGRES_PORT=55433 in .env, or `make dev POSTGRES_PORT=55433`.
POSTGRES_PORT ?= 5432

# Derived from POSTGRES_PORT, so the port is the only thing to change. Set
# DATABASE_URL in .env to point somewhere else entirely.
DATABASE_URL ?= postgres://domieface:domieface@localhost:$(POSTGRES_PORT)/domieface?sslmode=disable

# Stamped into the binary and reported by /healthz. Override on release:
#   make build VERSION=1.4.0
VERSION ?= dev
LDFLAGS  := -X domieface/com/internal/buildinfo.version=$(VERSION)

COMPOSE      := docker compose
DEPS         := postgres minio minio-init
COVER_PROFILE := coverage.out

## General

# firstword, not MAKEFILE_LIST: the list also contains .env once it is included,
# and the help text lives only in this file.
.PHONY: help
help: ## Show this help
	@echo "domieface-server"
	@echo ""
	@awk 'BEGIN { FS = ":.*## " } \
		/^## / { printf "\n\033[1m%s\033[0m\n", substr($$0, 4); next } \
		/^[a-zA-Z0-9_-]+:.*## / { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 }' "$(firstword $(MAKEFILE_LIST))"
	@echo ""

## Development

.PHONY: dev
dev: deps-up run ## Start Postgres and MinIO, then run the server on this machine

.PHONY: run
run: ## Run the server against DATABASE_URL (migrations apply at startup)
	go run "."

.PHONY: deps-up
deps-up: ## Start Postgres and MinIO in the background
	$(COMPOSE) up -d $(DEPS)

.PHONY: deps-down
deps-down: ## Stop Postgres and MinIO, keeping their data
	$(COMPOSE) stop $(DEPS)

.PHONY: deps-reset
deps-reset: ## Destroy Postgres and MinIO including all data, then start fresh
	$(COMPOSE) down -v
	$(COMPOSE) up -d $(DEPS)

## Quality

.PHONY: check
check: fmt-check vet build test ## Everything CI runs, in the same order

.PHONY: test
test: ## Run the tests (no database or object storage needed)
	go test "./..."

.PHONY: test-race
test-race: ## Run the tests under the race detector (needs a C toolchain)
	CGO_ENABLED=1 go test -race "./..."

.PHONY: cover
cover: ## Run the tests and open the HTML coverage report
	go test -coverprofile="$(COVER_PROFILE)" "./..."
	@go tool cover -func="$(COVER_PROFILE)" | tail -1
	go tool cover -html="$(COVER_PROFILE)"

.PHONY: fmt
fmt: ## Format every Go file in place
	gofmt -w "."

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-clean:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet
	go vet "./..."

.PHONY: tidy
tidy: ## Add missing and remove unused module dependencies
	go mod tidy

## Documentation

.PHONY: docs
docs: ## Check the spec matches the routes, and say where the docs are served
	go test "./internal/api/" -run "OpenAPI|Docs|Spec" -count=1
	@echo ""
	@echo "  Swagger UI   http://localhost:$(PORT)/docs"
	@echo "  OpenAPI spec http://localhost:$(PORT)/openapi.yaml"
	@echo ""
	@echo "  Run 'make dev' to serve them."

## Build

.PHONY: build
build: ## Compile the server to bin/ (go build creates the directory)
	go build -trimpath -ldflags "$(LDFLAGS)" -o "$(BINARY)" "."

.PHONY: docker-build
docker-build: ## Build the production container image
	docker build --build-arg "VERSION=$(VERSION)" -t "$(BINARY_NAME):$(VERSION)" -t "$(BINARY_NAME):latest" "."

.PHONY: clean
clean: ## Remove build output and the coverage profile
	rm -rf "$(BIN_DIR)" "$(COVER_PROFILE)"
	go clean -testcache

## Database

.PHONY: psql
psql: ## Open a psql shell on the compose database
	$(COMPOSE) exec postgres psql -U domieface -d domieface

.PHONY: db-reset
db-reset: ## Empty every table, keeping the schema
	$(COMPOSE) exec -T postgres psql -U domieface -d domieface \
		-c "TRUNCATE posts, uploads, refresh_tokens, users CASCADE;"

.PHONY: db-migrations
db-migrations: ## List migrations that have been applied
	$(COMPOSE) exec -T postgres psql -U domieface -d domieface \
		-c "SELECT version, applied_at FROM schema_migrations ORDER BY version;"

## Docker (full stack)

.PHONY: up
up: ## Start the whole stack, API included, in the background
	$(COMPOSE) up -d --build

.PHONY: down
down: ## Stop the whole stack, keeping data
	$(COMPOSE) down

.PHONY: down-volumes
down-volumes: ## Stop the whole stack and delete all data
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Follow the API container logs
	$(COMPOSE) logs -f api

.PHONY: ps
ps: ## Show the status of every service
	$(COMPOSE) ps
