package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStreamFiltersDiagnosticsWithoutFilteringRelocatedOutput(t *testing.T) {
	var destination bytes.Buffer
	stream, err := Open(Options{
		Resolution: Resolution{Level: LevelError, Source: SourceFlag},
		LogOnly:    true,
	}, &destination)
	if err != nil {
		t.Fatal(err)
	}
	ctx := stream.Context(context.Background())
	stdout, stderr := stream.Writers(io.Discard, io.Discard)

	For(ctx, ComponentCLI).Warn("filtered warning")
	For(ctx, ComponentCLI).Error("visible error")
	_, _ = io.WriteString(stdout, "ordinary output\n")
	_, _ = io.WriteString(stderr, "warning output\n")
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	got := destination.String()
	if strings.Contains(got, "filtered warning") {
		t.Errorf("warn diagnostic was not filtered: %q", got)
	}
	for _, anchor := range []string{
		"level=ERROR msg=\"visible error\" kind=diagnostic component=cli",
		"level=INFO msg=\"ordinary output\" kind=output stream=stdout",
		"level=WARN msg=\"warning output\" kind=output stream=stderr",
	} {
		if !strings.Contains(got, anchor) {
			t.Errorf("stream missing %q: %q", anchor, got)
		}
	}
	if strings.Contains(got, "time=") || strings.Contains(got, "source=") {
		t.Errorf("stream contains volatile metadata: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		switch {
		case strings.Contains(line, "kind=output"):
			if strings.Contains(line, "component=") {
				t.Errorf("output record carries a component: %q", line)
			}
		case strings.Contains(line, "kind=diagnostic"):
			if strings.Contains(line, " stream=") {
				t.Errorf("diagnostic record carries output stream metadata: %q", line)
			}
		}
		if strings.Contains(line, " phase=") || strings.Contains(line, " operation=") {
			t.Errorf("record carries forbidden metadata: %q", line)
		}
	}
}

func TestRelocationPreservesLinesBlankLinesAndTrailingFragments(t *testing.T) {
	var destination bytes.Buffer
	stream, err := Open(Options{
		Resolution: Resolution{Level: LevelInfo, Source: SourceLogOnly},
		LogOnly:    true,
	}, &destination)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := stream.Writers(io.Discard, io.Discard)
	_, _ = stdout.Write([]byte("first\n\npart"))
	_, _ = stdout.Write([]byte("ial \xf0\x9f"))
	_, _ = stdout.Write([]byte("\x98\x80\nlast"))
	_, _ = stderr.Write([]byte("tail"))
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	got := destination.String()
	for _, payload := range []string{"msg=first", `msg=""`, `msg="partial 😀"`, "msg=last", "msg=tail"} {
		if !strings.Contains(got, payload) {
			t.Errorf("missing relocated payload %q: %q", payload, got)
		}
	}
	if strings.Count(got, "msg=last") != 1 || strings.Count(got, "msg=tail") != 1 {
		t.Errorf("flush/close duplicated a fragment: %q", got)
	}
	if strings.Index(got, "msg=last") > strings.Index(got, "msg=tail") {
		t.Errorf("trailing flush was not stdout then stderr: %q", got)
	}
}

func TestRelocatedWritesAreSerialized(t *testing.T) {
	var destination bytes.Buffer
	stream, err := Open(Options{Resolution: Resolution{Level: LevelInfo}, LogOnly: true}, &destination)
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := stream.Writers(io.Discard, io.Discard)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.WriteString(stdout, "whole-line\n")
		}()
	}
	wg.Wait()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(destination.String(), "msg=whole-line"); got != 20 {
		t.Errorf("records = %d, want 20: %q", got, destination.String())
	}
}

func TestOpenSecuresBeforeTruncatingAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostic.log")
	if err := os.WriteFile(path, []byte("old contents"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	stream, err := Open(Options{Resolution: Resolution{Level: LevelOff}, FilePath: path}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Errorf("content = %q, want truncated", content)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStreamRetainsTheFirstWriteErrorAndRejectsWritesAfterClose(t *testing.T) {
	want := errors.New("destination failed")
	stream, err := Open(Options{Resolution: Resolution{Level: LevelDebug}, LogOnly: true}, failingWriter{want})
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := stream.Writers(io.Discard, io.Discard)
	if _, err := io.WriteString(stdout, "line\n"); !errors.Is(err, want) {
		t.Errorf("Write error = %v, want %v", err, want)
	}
	if err := stream.Flush(); !errors.Is(err, want) {
		t.Errorf("Flush error = %v, want %v", err, want)
	}
	if err := stream.Close(); !errors.Is(err, want) {
		t.Errorf("Close error = %v, want %v", err, want)
	}
	if err := stream.Close(); !errors.Is(err, want) {
		t.Errorf("second Close error = %v, want %v", err, want)
	}
	if _, err := io.WriteString(stdout, "late\n"); !errors.Is(err, ErrClosed) {
		t.Errorf("late Write error = %v, want ErrClosed", err)
	}
	if err := stream.Flush(); !errors.Is(err, want) {
		t.Errorf("Flush after late write = %v, want first sticky error %v", err, want)
	}
}

func TestLateWriteBecomesStickyAfterCleanClose(t *testing.T) {
	stream, err := Open(Options{Resolution: Resolution{Level: LevelInfo}, LogOnly: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := stream.Writers(io.Discard, io.Discard)
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdout, "late\n"); !errors.Is(err, ErrClosed) {
		t.Errorf("late Write error = %v, want ErrClosed", err)
	}
	if err := stream.Flush(); !errors.Is(err, ErrClosed) {
		t.Errorf("Flush error = %v, want sticky ErrClosed", err)
	}
}

func TestFilterPreservesAttrsAndGroups(t *testing.T) {
	var out bytes.Buffer
	base := slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := (&filterHandler{next: base, level: LevelInfo}).WithAttrs([]slog.Attr{slog.String("bound", "yes")}).WithGroup("nested")
	logger := slog.New(h)
	logger.Debug("hidden")
	logger.Info("visible", "field", "value")
	got := out.String()
	if strings.Contains(got, "hidden") || !strings.Contains(got, "bound=yes") || !strings.Contains(got, "nested.field=value") {
		t.Errorf("handler composition lost filtering/attrs/groups: %q", got)
	}
}

type orderedDestination struct {
	calls  *[]string
	failAt string
}

func (d *orderedDestination) Write(p []byte) (int, error) { return len(p), nil }
func (d *orderedDestination) Chmod(os.FileMode) error {
	*d.calls = append(*d.calls, "chmod")
	if d.failAt == "chmod" {
		return errors.New("chmod failed")
	}
	return nil
}
func (d *orderedDestination) Truncate(int64) error {
	*d.calls = append(*d.calls, "truncate")
	if d.failAt == "truncate" {
		return errors.New("truncate failed")
	}
	return nil
}
func (d *orderedDestination) Seek(int64, int) (int64, error) {
	*d.calls = append(*d.calls, "seek")
	if d.failAt == "seek" {
		return 0, errors.New("seek failed")
	}
	return 0, nil
}
func (d *orderedDestination) Close() error {
	*d.calls = append(*d.calls, "close")
	return nil
}

func TestOpenFailureOrderingClosesWithoutUnsafeFallback(t *testing.T) {
	previous := openDestination
	t.Cleanup(func() { openDestination = previous })

	tests := []struct {
		failAt string
		want   string
	}{
		{"chmod", "open,chmod,close"},
		{"truncate", "open,chmod,truncate,close"},
		{"seek", "open,chmod,truncate,seek,close"},
	}
	for _, tt := range tests {
		t.Run(tt.failAt, func(t *testing.T) {
			var calls []string
			openDestination = func(string) (destination, error) {
				calls = append(calls, "open")
				return &orderedDestination{calls: &calls, failAt: tt.failAt}, nil
			}
			stream, err := Open(Options{FilePath: "diagnostic.log"}, io.Discard)
			if err == nil || stream != nil {
				t.Fatalf("Open = %v, %v; want setup failure", stream, err)
			}
			if got := strings.Join(calls, ","); got != tt.want {
				t.Errorf("calls = %q, want %q", got, tt.want)
			}
		})
	}
}
