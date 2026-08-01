package diagnostic

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestSafeErrorClassifiesWithoutFormattingArbitraryErrors(t *testing.T) {
	const sentinel = "DO-NOT-LOG"
	err := fmt.Errorf("outer %s: %w", sentinel, &os.PathError{
		Op:   "open",
		Path: "/reviewed/path",
		Err:  syscall.EACCES,
	})
	var out bytes.Buffer
	slog.New(slog.NewTextHandler(&out, nil)).Error("failed", "error", SafeError(err))
	got := out.String()
	if strings.Contains(got, sentinel) {
		t.Fatalf("SafeError formatted arbitrary error text: %q", got)
	}
	for _, anchor := range []string{"error.class=path", "error.path_operation=open", "error.path=/reviewed/path", "error.errno="} {
		if !strings.Contains(got, anchor) {
			t.Errorf("SafeError missing %q: %q", anchor, got)
		}
	}
}
func TestSafeErrorUnknownIncludesOnlyTypeAndClass(t *testing.T) {
	const sentinel = "UNKNOWN-ERROR-SECRET"
	var out bytes.Buffer
	slog.New(slog.NewTextHandler(&out, nil)).Error("failed", "error", SafeError(errors.New(sentinel)))
	got := out.String()
	if strings.Contains(got, sentinel) || !strings.Contains(got, "error.class=error") || !strings.Contains(got, "error.type=") {
		t.Errorf("unknown error classification = %q", got)
	}
}
