# Convenience targets; scripts/ci.sh is the source of truth for checks.
GO ?= $(HOME)/.local/go-root/go/bin/go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test run check
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X github.com/axellageraldinc/xellter/internal/app.Version=$(VERSION)" -o bin/xellter ./cmd/xellter
test:
	./scripts/ci.sh
run: build
	./bin/xellter serve -config xellter.yaml
check: build
	./bin/xellter check -config xellter.yaml
