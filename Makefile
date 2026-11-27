GOLANGCI_LINT_VERSION := v2.15.0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINARY := amber
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: dev build test lint bench

dev:
	go run . serve --log-level=debug

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

test:
	go test -race ./...
	go test -race -tags bench ./apis

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w on the files above"; exit 1; }
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --build-tags bench

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
