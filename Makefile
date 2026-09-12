# TenantFlow developer Makefile — mirrors the CI gates.
# Windows users: run inside WSL (sh + make available).

GO      ?= go
COMPOSE ?= docker compose

# Files that must stay gofmt-clean. internal/auth/middleware.go is a
# deliberate, reviewed exception — never reformat it (see CONTRIBUTING.md).
GO_FILES := $(shell find . -name '*.go' -not -path './internal/auth/middleware.go' -not -path './.git/*' -not -path './web/node_modules/*')

.PHONY: fmt-check fmt vet build test integration race dev-up dev-down dev-logs dev-setup clean check

DEFAULT_GOAL := check

## check: run the full CI gate chain locally
check: fmt-check vet build test

## fmt-check: verify formatting (CI gate)

## fmt-check: verify formatting (CI gate)
fmt-check:
	@out=$$(gofmt -l $(GO_FILES)); \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi; \
	echo "gofmt clean"

## fmt: apply gofmt to all non-excluded files
fmt:
	gofmt -w $(GO_FILES)

## vet: static analysis
vet:
	$(GO) vet ./...

## build: compile everything
build:
	$(GO) build ./...

## test: unit tests (no external services needed)
test:
	$(GO) test ./...

## race: race detector on the concurrency-sensitive packages
race:
	$(GO) test -race ./internal/chaos/ ./internal/instance/ ./internal/metrics/ ./internal/worker/

## integration: full integration suite (needs dev-up running; Keycloak skips if down)
integration:
	$(GO) test -tags integration ./internal/...

## dev-up: start the local stack (Postgres, Temporal, Keycloak, Grafana)
dev-up:
	$(COMPOSE) up -d

## dev-down: stop the local stack
dev-down:
	$(COMPOSE) down

## dev-logs: tail the stack logs
dev-logs:
	$(COMPOSE) logs -f

## dev-setup: bootstrap Keycloak realm, role, and client (idempotent)
dev-setup:
	sh deploy/keycloak/setup.sh

## clean: drop binaries and coverage output
clean:
	rm -rf bin/ dist/ coverage.out

.DEFAULT_GOAL := check