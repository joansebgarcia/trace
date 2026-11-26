GOLANGCI_LINT_VERSION := v2.15.0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINARY := amber
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: dev build test lint

dev:
	go run . serve --log-level=debug

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w on the files above"; exit 1; }
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run
