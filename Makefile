# Spine Makefile — Build, Test & Install Automation

VERSION := 2.3.0
BINARY_NAME := spine
INSTALL_PATH := /usr/local/bin

.PHONY: all build install test clean help

all: build

## build: Compiles the spine CLI binary to ./bin/spine
build:
	@echo "==> Building $(BINARY_NAME) v$(VERSION)..."
	@mkdir -p bin
	@go build -ldflags="-s -w" -o bin/$(BINARY_NAME) ./cmd/spine/
	@echo "==> Binary built at ./bin/$(BINARY_NAME)"

## install: Compiles and installs the spine CLI binary to /usr/local/bin
install: build
	@echo "==> Installing $(BINARY_NAME) to $(INSTALL_PATH)..."
	@sudo cp bin/$(BINARY_NAME) $(INSTALL_PATH)/$(BINARY_NAME) || cp bin/$(BINARY_NAME) ~/.local/bin/$(BINARY_NAME)
	@echo "==> Successfully installed Spine v$(VERSION)!"

## test: Runs all unit & integration test suites
test:
	@echo "==> Running test suite..."
	@go test -v ./...

## clean: Removes build artifacts
clean:
	@echo "==> Cleaning build output..."
	@rm -rf bin/

## help: Displays available Makefile targets
help:
	@echo "Spine v$(VERSION) Makefile Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -e 's/## //' | column -t -s ':'
