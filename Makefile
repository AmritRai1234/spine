# Spine Makefile — Build, Test & Install Automation

# Single source of truth: spine.go (const Version). Never hardcode a version here.
VERSION := $(shell sed -n 's/^const Version = "\(.*\)"/\1/p' spine.go)
BINARY_NAME := spine
INSTALL_PATH := /usr/local/bin

.PHONY: all build install test test-race doc-lint clean help

all: build

## build: Compiles the spine CLI binary to ./bin/spine
build:
	@echo "==> Building $(BINARY_NAME) v$(VERSION)..."
	@mkdir -p bin
	@go build -tags sqlite_fts5 -ldflags="-s -w" -o bin/$(BINARY_NAME) ./cmd/spine/
	@echo "==> Binary built at ./bin/$(BINARY_NAME)"

## install: Compiles and installs the spine CLI binary to /usr/local/bin
install: build
	@echo "==> Installing $(BINARY_NAME) to $(INSTALL_PATH)..."
	@sudo cp bin/$(BINARY_NAME) $(INSTALL_PATH)/$(BINARY_NAME) || cp bin/$(BINARY_NAME) ~/.local/bin/$(BINARY_NAME)
	@echo "==> Successfully installed Spine v$(VERSION)!"

## test: Runs doc-lint plus all unit & integration test suites
test: doc-lint
	@echo "==> Running test suite..."
	@go test -tags sqlite_fts5 -v ./...

## test-race: Runs doc-lint plus all tests under the race detector
test-race: doc-lint
	@echo "==> Running test suite with race detector..."
	@go test -tags sqlite_fts5 -race ./...

## doc-lint: Verifies documented .spine manifest keys match the parser whitelist
doc-lint:
	@./scripts/doc_lint.sh

## clean: Removes build artifacts
clean:
	@echo "==> Cleaning build output..."
	@rm -rf bin/

## help: Displays available Makefile targets
help:
	@echo "Spine v$(VERSION) Makefile Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -e 's/## //' | column -t -s ':'
