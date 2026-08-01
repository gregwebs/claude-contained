package diagnostic

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func TestComponentsAreClosedAndEveryRecordCarriesMetadata(t *testing.T) {
	want := []string{"cli", "host", "env", "plan", "runtime", "worktree", "zellij", "attach", "rebuild"}
	components := Components()
	if len(components) != len(want) {
		t.Fatalf("Components length = %d, want %d", len(components), len(want))
	}

	var out bytes.Buffer
	base := slog.NewTextHandler(&out, &slog.HandlerOptions{
		Level: LevelDebug.slogLevel(),
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	ctx := WithLogger(context.Background(), slog.New(base))
	for i, component := range components {
		if component.String() != want[i] {
			t.Errorf("component %d = %q, want %q", i, component, want[i])
		}
		For(ctx, component).Info("anchor")
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(want) {
		t.Fatalf("records = %d, want %d: %q", len(lines), len(want), out.String())
	}
	for i, line := range lines {
		if !strings.Contains(line, "kind=diagnostic") || !strings.Contains(line, "component="+want[i]) {
			t.Errorf("record %d missing closed metadata: %q", i, line)
		}
	}
}

func TestComponentLoggerAcceptsOnlyPrivateDiagnosticAttributes(t *testing.T) {
	loggerType := reflect.TypeOf(ComponentLogger{})
	wantAttrs := reflect.TypeOf([]Attr{})
	for _, methodName := range []string{"Debug", "Info", "Warn", "Error"} {
		method, ok := loggerType.MethodByName(methodName)
		if !ok {
			t.Fatalf("ComponentLogger.%s is missing", methodName)
		}
		if !method.Type.IsVariadic() || method.Type.In(method.Type.NumIn()-1) != wantAttrs {
			t.Errorf("ComponentLogger.%s does not require ...diagnostic.Attr: %v", methodName, method.Type)
		}
	}

	attrType := reflect.TypeOf(Attr{})
	for i := 0; i < attrType.NumField(); i++ {
		if attrType.Field(i).IsExported() {
			t.Errorf("Attr field %q is exported and permits unreviewed construction", attrType.Field(i).Name)
		}
	}
}

func TestLoggerWithoutContextDiscardsAndIgnoresGlobalDefault(t *testing.T) {
	var hostile bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&hostile, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	loggerFromContext(context.Background()).Error("must disappear")
	For(context.Background(), ComponentCLI).Error("must also disappear")
	if hostile.Len() != 0 {
		t.Fatalf("missing context wrote through slog.Default: %q", hostile.String())
	}
}

func TestInvalidComponentCannotProduceARecord(t *testing.T) {
	var out bytes.Buffer
	ctx := WithLogger(context.Background(), slog.New(slog.NewTextHandler(&out, nil)))
	For(ctx, Component(255)).Error("invalid")
	if out.Len() != 0 {
		t.Fatalf("invalid component produced a record: %q", out.String())
	}
}
