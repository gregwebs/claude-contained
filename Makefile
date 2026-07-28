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
# entry whose flags are implemented: tools, mounts, ports, DNS, sandbox flags,
# SSH, the tool-process environment, --share-skills, the host mutations
# (account-state relocation, the node_modules overlay, placeholder cleanup),
# and the error paths.
# Cases 31 and 33-41 are excluded because they exercise flags that still
# refuse with exit 3 (--zellij, -a/--attach, worktree locking).
# Ranges are deliberately bounded rather than open-ended, so a corpus entry
# added by a later ticket is not silently pulled in before its code exists.
# Widen this list as each later ticket lands rather than editing the corpus.
DIFF_CASES := --case '0*' --case '1*' --case '2*' --case '30-*' --case '32-*' \
              --case '4[2-9]-*' --case '5[0-1]-*'

difftest: build
	tests/differential/harness.sh \
	  --compare claude-contained:$(BIN) \
	  --compare claude-docked:$(BIN_DOCKED) \
	  $(DIFF_CASES)

clean:
	rm -rf $(BIN_DIR)
