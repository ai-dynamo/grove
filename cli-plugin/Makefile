# Makefile for kubectl-grove CLI plugin

BINARY_NAME := kubectl-grove
VERSION ?= dev
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

.PHONY: all build test clean install lint fmt

all: build

## Build the CLI binary
build:
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) .

## Run all tests
test:
	go test -v ./...

## Run tests with coverage
test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html

## Install the binary to GOPATH/bin
install: build
	cp bin/$(BINARY_NAME) $(GOPATH)/bin/

## Run go mod tidy
tidy:
	go mod tidy

## Format code
fmt:
	go fmt ./...

## Run linter
lint:
	golangci-lint run ./...

## Build for multiple platforms
build-all:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 .
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 .

## Show help
help:
	@echo "Available targets:"
	@echo "  build       - Build the CLI binary"
	@echo "  test        - Run all tests"
	@echo "  test-cover  - Run tests with coverage report"
	@echo "  clean       - Remove build artifacts"
	@echo "  install     - Install binary to GOPATH/bin"
	@echo "  tidy        - Run go mod tidy"
	@echo "  fmt         - Format code"
	@echo "  lint        - Run linter"
	@echo "  build-all   - Build for multiple platforms"
