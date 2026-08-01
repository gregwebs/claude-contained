package plan

import (
	"context"
	"log/slog"

	"claude-contained/internal/cli"
	"claude-contained/internal/diagnostic"
	"claude-contained/internal/runtime"
)

// DiagnosticSummary is an allowlisted view of Program. It intentionally has
// no command or environment-value fields.
type DiagnosticSummary struct {
	Route            string
	Tool             string
	Shell            bool
	ProjectDir       string
	ContainerName    string
	StepCount        int
	MountCount       int
	EnvironmentCount int
}

func (s DiagnosticSummary) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("route", s.Route),
		slog.String("tool", s.Tool),
		slog.Bool("shell", s.Shell),
		slog.String("project_dir", s.ProjectDir),
		slog.String("container_name", s.ContainerName),
		slog.Int("step_count", s.StepCount),
		slog.Int("mount_count", s.MountCount),
		slog.Int("environment_count", s.EnvironmentCount),
	)
}

// Summarize extracts only reviewed fields from the plan and parsed config.
func Summarize(program Program, cfg cli.Config) DiagnosticSummary {
	tool := cfg.Tool
	switch tool {
	case "claude", "codex", "copilot", "gemini", "vibe":
	default:
		tool = "invalid"
	}
	summary := DiagnosticSummary{
		Route:     "incomplete",
		Tool:      tool,
		Shell:     cfg.ShellMode,
		StepCount: len(program.Steps),
	}
	if program.Pending != nil {
		summary.Route = "prompt"
	}
	if program.Run != nil {
		summary.Route = "run"
		for _, arg := range program.Run.Args {
			switch value := arg.(type) {
			case runtime.NameArg:
				summary.ContainerName = value.Value
			case runtime.WorkdirArg:
				summary.ProjectDir = value.Value
			case runtime.MountArg:
				summary.MountCount++
			case runtime.EnvArg:
				summary.EnvironmentCount++
			}
		}
	}
	return summary
}

// RecordAppliedStep records only safe facts after the side effect succeeds.
func RecordAppliedStep(ctx context.Context, index int, step Step) {
	logger := diagnostic.For(ctx, diagnostic.ComponentPlan)
	attrs := []diagnostic.Attr{
		diagnostic.Int("index", index),
		diagnostic.String("step_kind", DiagnosticStepKind(step)),
	}
	switch value := step.(type) {
	case MkdirAll:
		attrs = append(attrs, diagnostic.String("path", value.Path))
	case CopyFile:
		attrs = append(attrs, diagnostic.String("source_path", value.Src), diagnostic.String("destination_path", value.Dst))
	case MoveFile:
		attrs = append(attrs, diagnostic.String("source_path", value.Src), diagnostic.String("destination_path", value.Dst))
	case Symlink:
		attrs = append(attrs, diagnostic.String("target_path", value.Target), diagnostic.String("link_path", value.Link))
	case RemoveFile:
		attrs = append(attrs, diagnostic.String("path", value.Path))
	case Print:
		attrs = append(attrs, diagnostic.String("print_destination", outputStream(value.Stderr)))
	case WorktreeAutoLock:
		attrs = append(attrs, diagnostic.String("repo", value.Repo), diagnostic.Int("worktree_count", len(value.Worktrees)))
	}
	logger.Debug("execution plan step applied", attrs...)
}

// DiagnosticStepKind is the closed, payload-free classification used by both
// success and failure records.
func DiagnosticStepKind(step Step) string {
	switch step.(type) {
	case MkdirAll:
		return "mkdir"
	case CopyFile:
		return "copy"
	case MoveFile:
		return "move"
	case Symlink:
		return "symlink"
	case RemoveFile:
		return "remove"
	case Print:
		return "print"
	case WorktreeAutoLock:
		return "worktree-auto-lock"
	default:
		return "invalid"
	}
}

func outputStream(stderr bool) string {
	if stderr {
		return "stderr"
	}
	return "stdout"
}
