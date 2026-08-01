package runtime

import (
	"log/slog"
	"strings"
)

// SelectionSource is the closed vocabulary for runtime selection precedence.
type SelectionSource uint8

const (
	SelectionFromFlag SelectionSource = iota
	SelectionFromEnvironment
	SelectionFromArgv0
	SelectionFromPlatform
)

func (s SelectionSource) String() string {
	switch s {
	case SelectionFromEnvironment:
		return "environment"
	case SelectionFromArgv0:
		return "argv0"
	case SelectionFromPlatform:
		return "platform"
	default:
		return "flag"
	}
}

// SelectionDiagnostic reports a normalized decision without retaining a raw,
// potentially hostile flag or environment value.
type SelectionDiagnostic struct {
	Runtime string
	Source  SelectionSource
	Valid   bool
}

func (d SelectionDiagnostic) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("source", d.Source.String()),
		slog.Bool("valid", d.Valid),
	}
	if d.Valid {
		attrs = append(attrs, slog.String("runtime", d.Runtime))
	}
	return slog.GroupValue(attrs...)
}

// DiagnosticSelection applies the same precedence as Select while exposing
// only normalized runtime names and a validity bit.
func DiagnosticSelection(s Selection) SelectionDiagnostic {
	if s.Flag != "" {
		name, valid := diagnosticRuntimeName(s.Flag, s.Platform)
		return SelectionDiagnostic{Runtime: name, Source: SelectionFromFlag, Valid: valid}
	}
	if s.Env != "" {
		name, valid := diagnosticRuntimeName(s.Env, s.Platform)
		return SelectionDiagnostic{Runtime: name, Source: SelectionFromEnvironment, Valid: valid}
	}
	if strings.Contains(strings.ToLower(baseName(s.Argv0)), "dock") {
		return SelectionDiagnostic{Runtime: NameDocker, Source: SelectionFromArgv0, Valid: true}
	}
	name := NameDocker
	if s.Platform == Darwin {
		name = NameApple
	}
	return SelectionDiagnostic{Runtime: name, Source: SelectionFromPlatform, Valid: true}
}

func diagnosticRuntimeName(value string, platform Platform) (string, bool) {
	switch strings.ToLower(value) {
	case NameDocker:
		return NameDocker, true
	case NameApple:
		return NameApple, platform == Darwin
	default:
		return "", false
	}
}

// DiagnosticArgv is the only runtime argv representation safe for diagnostic
// records. Every operand following -e is replaced while non-environment
// arguments remain visible for debugging.
type DiagnosticArgv []string

func (a DiagnosticArgv) LogValue() slog.Value {
	return slog.AnyValue(redactDiagnosticArgv([]string(a)))
}

func redactDiagnosticArgv(argv []string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		out = append(out, argv[i])
		if argv[i] != "-e" {
			continue
		}
		if i+1 >= len(argv) {
			out = append(out, "<redacted>")
			continue
		}
		if argv[i+1] == "-e" {
			// Do not consume the next flag: it must redact its own operand too.
			out = append(out, "<redacted>")
			continue
		}
		i++
		key, _, found := strings.Cut(argv[i], "=")
		if !found || !diagnosticEnvKey(key) {
			out = append(out, "<redacted>")
			continue
		}
		out = append(out, key+"=<redacted>")
	}
	return out
}

func diagnosticEnvKey(key string) bool {
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
