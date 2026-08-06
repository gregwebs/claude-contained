# The Go launcher. Output lands in bin/, built under its primary name.
#
# The `-docked` symlink is a compat/test fixture, not a shipped name: argv[0]'s
# basename containing "dock" still selects the Docker runtime (a compat
# affordance for users who had `claude-docked` on PATH), and it is how the
# shell test suites drive the Docker runtime as a target path.

GO ?= go
GOFMT ?= gofmt
SHELLCHECK ?= shellcheck
GOLANGCI_LINT ?= golangci-lint

SHELLCHECK_REQUIRED_VERSION := 0.11.0
GOLANGCI_LINT_REQUIRED_VERSION := 2.12.2

GO_SOURCES := $(shell git ls-files -- '*.go')
SHELL_SOURCES := $(shell git ls-files -- '*.sh')
BIN_DIR := bin
BIN := $(BIN_DIR)/claude-contained
BIN_DOCKED := $(BIN_DIR)/claude-contained-docked

PREFIX ?= $(HOME)/.local

.PHONY: build test vet fmt clean quality fmt-check lint-go lint-shell check-tools \
	check-shellcheck-version check-golangci-lint-version install

quality: check-tools fmt-check vet test lint-go lint-shell

check-tools: check-shellcheck-version check-golangci-lint-version

check-shellcheck-version:
	@command -v $(SHELLCHECK) >/dev/null 2>&1 || { \
		echo "ShellCheck $(SHELLCHECK_REQUIRED_VERSION) is required; $(SHELLCHECK) was not found." >&2; \
		echo "See CONTRIBUTING.md: Development Setup." >&2; \
		exit 1; \
	}
	@found_version="$$($(SHELLCHECK) --version 2>/dev/null | sed -n 's/^version: //p')"; \
	if [ "$$found_version" != "$(SHELLCHECK_REQUIRED_VERSION)" ]; then \
		echo "ShellCheck $(SHELLCHECK_REQUIRED_VERSION) is required; found $${found_version:-unknown}." >&2; \
		echo "See CONTRIBUTING.md: Development Setup." >&2; \
		exit 1; \
	fi

check-golangci-lint-version:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint $(GOLANGCI_LINT_REQUIRED_VERSION) is required; $(GOLANGCI_LINT) was not found." >&2; \
		echo "Install with: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_REQUIRED_VERSION)" >&2; \
		echo "See CONTRIBUTING.md: Development Setup." >&2; \
		exit 1; \
	}
	@found_version="$$($(GOLANGCI_LINT) version 2>/dev/null | sed -n 's/^golangci-lint has version v\{0,1\}\([^ ]*\).*/\1/p')"; \
	if [ "$$found_version" != "$(GOLANGCI_LINT_REQUIRED_VERSION)" ]; then \
		echo "golangci-lint $(GOLANGCI_LINT_REQUIRED_VERSION) is required; found $${found_version:-unknown}." >&2; \
		echo "Install with: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_REQUIRED_VERSION)" >&2; \
		echo "See CONTRIBUTING.md: Development Setup." >&2; \
		exit 1; \
	fi

fmt-check:
	@unformatted="$$($(GOFMT) -l $(GO_SOURCES))"; \
	if [ -n "$$unformatted" ]; then \
		echo "$$unformatted"; \
		exit 1; \
	fi

lint-go: check-golangci-lint-version
	$(GOLANGCI_LINT) run

lint-shell: check-shellcheck-version
	$(SHELLCHECK) --severity=warning $(SHELL_SOURCES)

build:
	$(GO) build -o $(BIN) ./cmd/claude-contained
	ln -sf claude-contained $(BIN_DOCKED)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(BIN_DIR)

# A symlink, not a copy: --rebuild finds the Dockerfile by resolving this
# executable and walking to the enclosing git root (internal/host/buildcontext.go).
# A copy outside the checkout has no enclosing checkout and forces
# --build-context / CLAUDE_CONTAINED_BUILD_CONTEXT on every rebuild.
install: build
	mkdir -p $(PREFIX)/bin
	ln -sf $(abspath $(BIN)) $(PREFIX)/bin/claude-contained
