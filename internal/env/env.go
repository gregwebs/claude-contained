// Package env holds the environment variables destined for the tool process:
// where each came from, in what order they are emitted, and which keys are
// refused outright.
//
// Everything here is a pure function of its inputs. In particular LoadFile
// takes the file's *bytes*, never a path — the project env file is writable
// from inside the container, so it is parsed literally and never evaluated.
// Evaluating it would hand a contained agent arbitrary code execution on the
// next launch, and a signature that cannot reach the filesystem or a shell
// makes that guarantee visible rather than merely intended.
package env

import (
	"fmt"
	"strings"
)

// Origin records where an assignment came from. It decides two things: which
// keys are refused, and how the summary attributes each name.
type Origin int

const (
	// Flag is -e/--env on the command line: the highest priority source.
	Flag Origin = iota
	// File is the project env file, which a contained agent can write.
	File
	// Builtin is the launcher's own contribution (TZ, GH_TOKEN). Built-ins only
	// fill gaps, and are never named in the summary.
	Builtin
)

// Pair is one environment variable bound for the container.
type Pair struct{ Key, Value string }

// entry is one recorded assignment together with where it came from.
type entry struct {
	key    string
	value  string
	origin Origin
}

// Store is an ordered set of assignments holding at most one entry per key.
//
// Deduplication happens here rather than being left to the container runtime:
// duplicate -e arguments are not documented to resolve last-wins in either
// runtime, so precedence has to be settled before the argv is built.
type Store struct{ entries []entry }

func New() *Store { return &Store{} }

func (s *Store) indexOf(key string) (int, bool) {
	for i, e := range s.entries {
		if e.key == key {
			return i, true
		}
	}
	return -1, false
}

// Set records KEY=VALUE, replacing any earlier entry for the same key. The
// replacement is in place, so a repeated key keeps the position of its first
// appearance — which is observable, because position determines argv order.
func (s *Store) Set(assignment, source string, origin Origin) error {
	if err := validate(assignment, source, origin); err != nil {
		return err
	}
	key, value, _ := strings.Cut(assignment, "=")

	if i, ok := s.indexOf(key); ok {
		s.entries[i].value = value
		s.entries[i].origin = origin
		return nil
	}
	s.entries = append(s.entries, entry{key: key, value: value, origin: origin})
	return nil
}

// Default records KEY=VALUE only when the key is absent, so a higher-priority
// source already present wins.
//
// It deliberately does not validate when the key is present: bash extracts the
// key and returns early, so an assignment that would fail validation is never
// examined once something else has claimed the key.
func (s *Store) Default(assignment, source string, origin Origin) error {
	key, _, _ := strings.Cut(assignment, "=")
	if _, ok := s.indexOf(key); ok {
		return nil
	}
	return s.Set(assignment, source, origin)
}

// Pairs returns the assignments in emission order.
func (s *Store) Pairs() []Pair {
	out := make([]Pair, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, Pair{Key: e.key, Value: e.value})
	}
	return out
}

// Summary reports which variables are being passed in, by name only — values
// routinely hold tokens and this lands in the user's scrollback. It returns ""
// when there is nothing worth saying, which includes the common case of
// built-ins alone: TZ is set on nearly every run and would otherwise print on
// every launch.
func (s *Store) Summary() string {
	var fromFlags, fromFile []string
	for _, e := range s.entries {
		switch e.origin {
		case Flag:
			fromFlags = append(fromFlags, e.key)
		case File:
			fromFile = append(fromFile, e.key)
		}
	}
	if len(fromFlags) == 0 && len(fromFile) == 0 {
		return ""
	}

	msg := "env:"
	if len(fromFlags) > 0 {
		msg += " " + strings.Join(fromFlags, ", ") + " (--env)"
	}
	if len(fromFlags) > 0 && len(fromFile) > 0 {
		msg += ";"
	}
	if len(fromFile) > 0 {
		msg += " " + strings.Join(fromFile, ", ") + " (.claude-contained/env)"
	}
	return msg
}

// reservedAlwaysPrefixes are namespaces owned by the launcher and the
// entrypoint; accepting anything inside them would let a caller impersonate the
// launcher's own signalling.
var reservedAlwaysPrefixes = []string{"HOST_", "SRT_", "CLAUDE_CONTAINED_"}

// reservedAlwaysExact are individually load-bearing. STAY_ROOT makes the
// entrypoint skip the `gosu dev` drop, which would leave the sandbox running as
// root; SSH_AUTH_SOCK is copied straight into the sandbox policy's socket
// allowlist. HOME and JAVA_HOME are overwritten by the entrypoint and PATH is
// prepended to, so accepting any of those three would be a lie.
var reservedAlwaysExact = map[string]bool{
	"STAY_ROOT": true, "SSH_AUTH_SOCK": true, "GIT_PROTECT_DIRS": true,
	"HOME": true, "PATH": true, "JAVA_HOME": true,
}

// reservedInFile are fine to pass by hand but are code injection into the
// sandbox wrapper itself when they come from a file a contained agent can write.
var reservedInFile = map[string]bool{
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "NODE_OPTIONS": true,
}

func keyReservedAlways(key string) bool {
	for _, p := range reservedAlwaysPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return reservedAlwaysExact[key]
}

// validKey implements ^[A-Za-z_][A-Za-z0-9_]*$ without a regexp.
func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validate applies the four checks in bash's order. The first failure wins and
// is the only message emitted, so the order is part of the observable behavior.
func validate(assignment, source string, origin Origin) error {
	key, _, found := strings.Cut(assignment, "=")
	if !found {
		return fmt.Errorf("error: %s: expected KEY=VALUE, got '%s'", source, assignment)
	}
	if !validKey(key) {
		return fmt.Errorf("error: %s: not a valid environment variable name: '%s'", source, key)
	}
	if keyReservedAlways(key) {
		// "claude-contained" here is the product name, identical for both
		// container runtimes -- not the program name.
		return fmt.Errorf("error: %s: %s is reserved by claude-contained and cannot be set", source, key)
	}
	if origin == File && reservedInFile[key] {
		//nolint:staticcheck // The trailing period preserves shell-launcher compatibility.
		return fmt.Errorf("error: %s: %s cannot be set from a project env file\n"+
			"       It is read by the sandbox wrapper itself; pass -e %s=... if you mean it.",
			source, key, key)
	}
	return nil
}
