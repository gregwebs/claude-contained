# The Go launcher. Output deliberately lands in bin/ under a different name than
# the tracked `claude-contained` / `claude-docked` at the repo root: those are the
# differential oracle for the rest of the rewrite and must not be overwritten.
#
# The `-docked` symlink is not cosmetic -- argv[0]'s basename is how the binary
# selects its container runtime, so the same build serves both.

GO ?= go
BIN_DIR := bin
BIN := $(BIN_DIR)/claude-go
BIN_DOCKED := $(BIN_DIR)/claude-go-docked

.PHONY: build test vet fmt difftest clean

build:
	$(GO) build -o $(BIN) ./cmd/claude-go
	ln -sf claude-go $(BIN_DOCKED)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# Cross-compare the Go launcher against both bash launchers over every corpus
# entry whose flags this ticket implements: tools, mounts, ports, DNS, sandbox
# flags, SSH, and the error paths. Cases 20-29, 31 and 33-41 are excluded
# because they exercise flags that still refuse with exit 3 (tickets 03-08).
# Widen this list as each later ticket lands rather than editing the corpus.
DIFF_CASES := --case '0*' --case '1*' --case '30-*' --case '32-*'

difftest: build
	tests/differential/harness.sh \
	  --compare claude-contained:$(BIN) \
	  --compare claude-docked:$(BIN_DOCKED) \
	  $(DIFF_CASES)

clean:
	rm -rf $(BIN_DIR)
