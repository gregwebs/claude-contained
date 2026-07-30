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
SHELL_SOURCES := $(shell git ls-files -- '*.sh') claude-contained claude-docked
BIN_DIR := bin
BIN := $(BIN_DIR)/claude-contained
BIN_DOCKED := $(BIN_DIR)/claude-contained-docked

.PHONY: build test vet fmt difftest clean quality fmt-check lint-go lint-shell check-tools \
	check-shellcheck-version check-golangci-lint-version

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

# Cross-compare the Go launcher against both bash launchers over every corpus
# entry whose flags are implemented: tools, mounts, ports, DNS, sandbox flags,
# SSH, the tool-process environment, --share-skills, the host mutations
# (account-state relocation, the node_modules overlay, placeholder cleanup),
# worktree auto-locking (including the interactive offer and the mutex),
# the Zellij session store (launch gate, generated and explicit session names,
# attach, the force flag), and the error paths.
# The whole 30-39 range is ported and included, alongside 40 (plain attach),
# 41 and 52-56 (worktree locking). Newly admitted by ticket 08: 33-37 (the
# Zellij store) and 31 (Zellij session-name validation), which passed before
# this ticket -- its refusal happens during parsing -- but sat outside the
# bounded ranges.
# Ranges are deliberately bounded rather than open-ended, so a corpus entry
# added by a later ticket is not silently pulled in before its code exists.
# Widen this list as each later ticket lands rather than editing the corpus.
# Newly admitted by ticket 10: 57-59, the rebuild paths (tools, full, and an
# unknown mode rejected before any build runs).
DIFF_CASES := --case '0*' --case '1*' --case '2*' --case '3[0-9]-*' --case '40-*' \
              --case '41-*' --case '4[2-9]-*' --case '5[0-9]-*'

difftest: build
	tests/differential/harness.sh \
	  --compare claude-contained:$(BIN) \
	  --compare claude-docked:$(BIN_DOCKED) \
	  $(DIFF_CASES)

clean:
	rm -rf $(BIN_DIR)
