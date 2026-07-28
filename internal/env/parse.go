package env

import (
	"fmt"
	"strings"
)

// FileName is the project env file's path relative to the project directory. It
// doubles as the label in rejection messages, which name the file and line
// rather than an absolute path.
const FileName = ".claude-contained/env"

// posixSpace is the [[:space:]] set bash trims from the front of each line.
const posixSpace = " \t\n\v\f\r"

// LoadFile applies the project env file's contents to the store.
//
// The parse is literal throughout: no expansion, no substitution, no shell.
// Line by line it strips one trailing carriage return, strips *leading*
// whitespace only, skips blanks and comments, splits on the first `=`, and
// removes at most one matching pair of surrounding quotes.
//
// Two behaviors are easy to miss and both are load-bearing:
//
//   - Trailing whitespace is deliberately kept, so `FOO=bar   ` has three
//     trailing spaces in its value.
//   - A key already set by a flag causes its line to be skipped *entirely*,
//     before validation. That is what lets `-e LD_PRELOAD=…` coexist with an
//     LD_PRELOAD line in the file: the flag wins and the file line is never
//     examined, so its file-only reservation never fires.
func (s *Store) LoadFile(content []byte) error {
	for i, raw := range splitLines(string(content)) {
		lineno := i + 1

		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimLeft(line, posixSpace)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		source := fmt.Sprintf("%s:%d", FileName, lineno)

		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			// Deliberately routed through the same validator the flag path uses
			// rather than formatting the message here: bash calls
			// validate_env_assignment for exactly this case, and two copies of
			// the wording would drift apart.
			return validate(trimmed, source, File)
		}

		if idx, ok := s.indexOf(key); ok && s.entries[idx].origin == Flag {
			continue
		}

		value = stripOnePair(value)

		if err := s.Set(key+"="+value, source, File); err != nil {
			return err
		}
	}
	return nil
}

// splitLines reproduces bash's `while IFS= read -r line || [[ -n "$line" ]]`:
// every line is yielded, and a final line with no trailing newline still counts,
// while a trailing newline does not produce a phantom empty line.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// stripOnePair removes one matching pair of surrounding quotes, and only one.
// Both delimiters must be the same kind, so `"mismatched'` is left alone, and
// `""x""` yields `"x"` rather than `x`.
func stripOnePair(value string) string {
	if len(value) < 2 {
		return value
	}
	first, last := value[0], value[len(value)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}
