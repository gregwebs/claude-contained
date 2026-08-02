package diagnostic

import "log/slog"

// Component is a compile-time-safe diagnostic origin.
type Component uint8

const (
	ComponentCLI Component = iota
	ComponentHost
	ComponentEnv
	ComponentPlan
	ComponentRuntime
	ComponentWorktree
	ComponentZellij
	ComponentAttach
	ComponentRebuild
	// ComponentLayer is appended, never inserted: the numeric values are a
	// closed set several tests index positionally, and renumbering an existing
	// component would silently re-attribute records rather than fail.
	ComponentLayer
	componentCount
)

var components = [...]Component{
	ComponentCLI,
	ComponentHost,
	ComponentEnv,
	ComponentPlan,
	ComponentRuntime,
	ComponentWorktree,
	ComponentZellij,
	ComponentAttach,
	ComponentRebuild,
	ComponentLayer,
}

// Components returns the closed set in help/documentation order.
func Components() []Component {
	return append([]Component(nil), components[:]...)
}

func (c Component) valid() bool { return c < componentCount }

func (c Component) String() string {
	switch c {
	case ComponentCLI:
		return "cli"
	case ComponentHost:
		return "host"
	case ComponentEnv:
		return "env"
	case ComponentPlan:
		return "plan"
	case ComponentRuntime:
		return "runtime"
	case ComponentWorktree:
		return "worktree"
	case ComponentZellij:
		return "zellij"
	case ComponentAttach:
		return "attach"
	case ComponentRebuild:
		return "rebuild"
	case ComponentLayer:
		return "layer"
	default:
		return "invalid"
	}
}

// LogValue prevents a component from being formatted through an open-ended
// interface or arbitrary string at call sites.
func (c Component) LogValue() slog.Value { return slog.StringValue(c.String()) }
