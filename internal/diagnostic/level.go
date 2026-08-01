// Package diagnostic owns the contributor-facing diagnostic stream.
package diagnostic

import (
	"fmt"
	"log/slog"
)

// Level is the closed set accepted by --log-level and
// CLAUDE_CONTAINED_LOG_LEVEL.
type Level uint8

const (
	LevelOff Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "off"
	}
}

func (l Level) slogLevel() slog.Level {
	switch l {
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func parseLevel(value string) (Level, error) {
	switch value {
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	case "off":
		return LevelOff, nil
	default:
		return LevelOff, fmt.Errorf("log level must be debug, info, warn, error, or off: %s", value)
	}
}

// LevelSource records which precedence arm selected the effective level.
type LevelSource uint8

const (
	SourceDefault LevelSource = iota
	SourceLogOnly
	SourceEnvironment
	SourceFlag
)

func (s LevelSource) String() string {
	switch s {
	case SourceLogOnly:
		return "log-only"
	case SourceEnvironment:
		return "environment"
	case SourceFlag:
		return "flag"
	default:
		return "default"
	}
}

// Resolution is the effective threshold and where it came from.
type Resolution struct {
	Level  Level
	Source LevelSource
}

// ResolveLevel applies flag, environment, --log-only implication, default.
func ResolveLevel(flag string, flagSet bool, env string, logOnly bool) (Resolution, error) {
	value, source := "off", SourceDefault
	switch {
	case flagSet:
		value, source = flag, SourceFlag
	case env != "":
		value, source = env, SourceEnvironment
	case logOnly:
		value, source = "info", SourceLogOnly
	}
	level, err := parseLevel(value)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{Level: level, Source: source}, nil
}
