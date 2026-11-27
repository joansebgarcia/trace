GOLANGCI_LINT_VERSION := v2.15.0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINARY := trace
LDFLAGS := -s -w -X main.version=$(VERSION)
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: dev build test lint bench go-dev go-build go-test go-lint web-dev web-build web-test web-lint

# Each main target covers the Go server and the JS workspace. The go-*
# targets need no Node, so the server builds and tests without it; the web-*
# targets need Node and pnpm (see engines and packageManager in package.json).

dev:
	$(MAKE) -j2 go-dev web-dev

# build embeds the web UI: the Vite build first, then go build -tags ui.
build: web-build
	CGO_ENABLED=0 go build -tags ui -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

test: go-test web-test

lint: go-lint web-lint

go-dev:
	go run . serve --log-level=debug

# go-build builds the binary without the web UI; it serves a placeholder page.
go-build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

go-test:
	go test -race ./...
	go test -race -tags bench ./bench

go-lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w on the files above"; exit 1; }
	$(GOLANGCI_LINT) run
	$(GOLANGCI_LINT) run --build-tags bench

# web-dev serves the UI with hot reload on the Vite port, proxying /api/ to
# the server of go-dev.
web-dev: node_modules/.modules.yaml
	pnpm --filter ./web dev

web-build: node_modules/.modules.yaml
	pnpm build

# web-test also runs the Go tests against the embedded Vite build.
web-test: web-build
	pnpm test
	go test -race -tags ui ./apis ./cmd ./web

web-lint: web-build
	pnpm lint
	pnpm typecheck
	$(GOLANGCI_LINT) run --build-tags ui

node_modules/.modules.yaml: package.json pnpm-lock.yaml pnpm-workspace.yaml web/package.json
	pnpm install --frozen-lockfile
	touch $@

# bench verifies ADR 0001 against a real server built with the bench tag.
# BENCH_SERVER wraps the binary, such as to limit its CPUs and memory;
# BENCH_FLAGS passes flags to the load generator (go run ./bench -h). The
# recipe reads it from the environment, so the shell never parses its value
# as commands.
BENCH_SERVER ?=
BENCH_FLAGS ?=
export BENCH_FLAGS

bench:
	CGO_ENABLED=0 go build -tags bench -trimpath -o bin/$(BINARY)-bench .
	go run ./bench -server "$(strip $(BENCH_SERVER) bin/$(BINARY)-bench)" $$BENCH_FLAGS
