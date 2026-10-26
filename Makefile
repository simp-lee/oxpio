.DEFAULT_GOAL := check

.PHONY: tools fmt fmt-check live-preview-wasm lint test audit check

GO ?= go
GO_ENV_GOBIN := $(strip $(shell $(GO) env GOBIN 2>/dev/null))
GO_ENV_GOPATH := $(strip $(shell $(GO) env GOPATH 2>/dev/null))
GO_BIN_DIR := $(if $(GO_ENV_GOBIN),$(GO_ENV_GOBIN),$(if $(GO_ENV_GOPATH),$(GO_ENV_GOPATH)/bin))

ifneq ($(GO_BIN_DIR),)
export PATH := $(GO_BIN_DIR):$(PATH)
endif

PKGS ?= ./...
GOLANGCI_LINT ?= golangci-lint
GOFUMPT ?= gofumpt
GOIMPORTS ?= goimports
GOLANGCI_LINT_VERSION := v2.11.4
GOFUMPT_VERSION := v0.9.2
GOIMPORTS_VERSION := v0.44.0

tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	$(GO) install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

fmt:
	@command -v $(GOIMPORTS) >/dev/null 2>&1 || { echo "$(GOIMPORTS) not found; run: make tools"; exit 1; }
	@command -v $(GOFUMPT) >/dev/null 2>&1 || { echo "$(GOFUMPT) not found; run: make tools"; exit 1; }
	@files="$$(find . -type f -name '*.go' -not -path './vendor/*')"; \
	if [ -n "$$files" ]; then $(GOIMPORTS) -w $$files && $(GOFUMPT) -w $$files; fi

live-preview-wasm:
	cp "$$($(GO) env GOROOT)/lib/wasm/wasm_exec.js" internal/edit/web/wasm_exec.js
	GOOS=js GOARCH=wasm $(GO) build -trimpath -ldflags='-s -w' -o internal/edit/web/live-preview.wasm ./cmd/livepreview-wasm

fmt-check:
	@command -v $(GOIMPORTS) >/dev/null 2>&1 || { echo "$(GOIMPORTS) not found; run: make tools"; exit 1; }
	@command -v $(GOFUMPT) >/dev/null 2>&1 || { echo "$(GOFUMPT) not found; run: make tools"; exit 1; }
	@files="$$(find . -type f -name '*.go' -not -path './vendor/*')"; \
	if [ -z "$$files" ]; then exit 0; fi; \
	imports_out="$$( $(GOIMPORTS) -l $$files )" || exit 1; \
	format_out="$$( $(GOFUMPT) -l $$files )" || exit 1; \
	out="$$(printf '%s\n%s\n' "$$imports_out" "$$format_out" | awk 'NF && !seen[$$0]++')"; \
	if [ -n "$$out" ]; then printf '%s\n' "$$out"; exit 1; fi

lint:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { echo "$(GOLANGCI_LINT) not found; run: make tools"; exit 1; }
	$(GOLANGCI_LINT) run $(PKGS)

test: live-preview-wasm
	$(GO) test $(PKGS)

audit:
	sh scripts/verify-product-boundary.sh
	sh scripts/probe-output-atomicity.sh --verify

check: fmt-check lint test audit
