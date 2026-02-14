.PHONY: all build test clean docker lint fmt help install build-all

# Variables
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)
GOFLAGS := -trimpath

# Targets
all: lint test build

build: ## Build both binaries
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o relp-forwarder ./cmd/relp-forwarder/
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o relp-listener ./cmd/relp-listener/

build-all: ## Build for all platforms
	@for bin in relp-forwarder relp-listener; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $${bin}-linux-amd64 ./cmd/$${bin}/; \
		CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $${bin}-linux-arm64 ./cmd/$${bin}/; \
		CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $${bin}-darwin-amd64 ./cmd/$${bin}/; \
		CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $${bin}-darwin-arm64 ./cmd/$${bin}/; \
	done

test: ## Run tests
	go test -v -race -coverprofile=coverage.out ./...

lint: ## Run linter
	@which golangci-lint > /dev/null || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	golangci-lint run ./...

fmt: ## Format code
	go fmt ./...

clean: ## Clean build artifacts
	rm -f relp-forwarder relp-forwarder-* relp-listener relp-listener-* coverage.out

docker: ## Build Docker image
	docker build -t syslog-ng-relp:$(VERSION) -t syslog-ng-relp:latest .

install: build ## Install to /usr/local/bin
	install -m 755 relp-forwarder /usr/local/bin/
	install -m 755 relp-listener /usr/local/bin/

help: ## Show help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'
