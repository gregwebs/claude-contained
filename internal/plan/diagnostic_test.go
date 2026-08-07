package plan

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"claude-contained/internal/cli"
	"claude-contained/internal/diagnostic"
	"claude-contained/internal/runtime"
)

func TestDiagnosticSummaryOmitsEnvironmentAndCommandValues(t *testing.T) {
	const sentinel = "DO-NOT-LOG"
	program := Program{
		Steps: []Step{Print{Text: sentinel}},
		Run: &runtime.RunSpec{
			Args: []runtime.Arg{
				runtime.NameArg{Value: "aic-safe"},
				runtime.WorkdirArg{Value: "/project"},
				runtime.MountArg{Src: "/project", Dst: "/project"},
				runtime.EnvArg{Key: "TOKEN", Value: sentinel},
			},
			Command: []string{"tool", sentinel},
		},
	}
	var out bytes.Buffer
	ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	diagnostic.For(ctx, diagnostic.ComponentPlan).Info("execution plan built",
		diagnostic.Value("summary", Summarize(program, cli.Config{Command: []string{"claude"}})))
	RecordAppliedStep(ctx, 0, program.Steps[0])
	got := out.String()
	if strings.Contains(got, sentinel) {
		t.Fatalf("plan diagnostics leaked a protected payload: %q", got)
	}
	if strings.Contains(got, " stream=") {
		t.Fatalf("plan diagnostic used output-only stream metadata: %q", got)
	}
	for _, anchor := range []string{"summary.command_source=explicit", "summary.command_len=1", "summary.project_dir=/project", "summary.environment_count=1", "step_kind=print"} {
		if !strings.Contains(got, anchor) {
			t.Errorf("plan diagnostics missing %q: %q", anchor, got)
		}
	}
}
