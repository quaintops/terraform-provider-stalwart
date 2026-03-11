SHELL := /bin/bash

.PHONY: help
.ONESHELL:
.SILENT:

VERSION      = $(shell grep -oP 'var version = "\K[^"]+' main.go)
BINARY       = terraform-provider-stalwart
REGISTRY     = registry.terraform.io/bilbilak/stalwart
OS_ARCH      = $(shell go env GOOS)_$(shell go env GOARCH)
INSTALL_DIR  = $(HOME)/.terraform.d/plugins/$(REGISTRY)/$(VERSION)/$(OS_ARCH)
TFPLUGINDOCS = $(shell go env GOPATH)/bin/tfplugindocs

help: ## Show available commands
	echo -e "\nUsage:\n"
	grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
	echo

build: ## Build the provider binary
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY)_v$(VERSION) .

install: build ## Install the provider into the local Terraform plugin cache
	mkdir -p $(INSTALL_DIR)
	cp $(BINARY)_v$(VERSION) $(INSTALL_DIR)/$(BINARY)_v$(VERSION)

docs: ## Regenerate docs/ from templates/ and the provider schema
	$(TFPLUGINDOCS) generate --provider-name stalwart

fmt: ## Format Go source files
	gofmt -s -w ./internal/

vet: ## Run go vet
	go vet ./...

clean: ## Remove build artifacts
	rm -f $(BINARY)_v*

%:
	:
