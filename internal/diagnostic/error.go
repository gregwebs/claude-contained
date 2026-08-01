package diagnostic

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"syscall"
)

// ErrorValue is a reviewed error classification. It never calls Error() on
// the wrapped value, so an unknown error contributes only its concrete type
// and a closed class.
type ErrorValue struct{ err error }

// SafeError constructs the only error carrier allowed in diagnostic records.
func SafeError(err error) ErrorValue { return ErrorValue{err: err} }

// ErrorAttr is the only production constructor for the reserved error field.
// Keeping the key here prevents call sites from attaching a raw error.
func ErrorAttr(err error) Attr {
	return Value("error", SafeError(err))
}

func (v ErrorValue) LogValue() slog.Value {
	if v.err == nil {
		return slog.GroupValue(slog.String("class", "none"))
	}
	attrs := []slog.Attr{
		slog.String("type", reflect.TypeOf(v.err).String()),
		slog.String("class", errorClass(v.err)),
	}

	var pathErr *os.PathError
	if errors.As(v.err, &pathErr) {
		attrs = append(attrs,
			slog.String("path_operation", pathErr.Op),
			slog.String("path", pathErr.Path),
		)
	}
	var errno syscall.Errno
	if errors.As(v.err, &errno) {
		attrs = append(attrs, slog.String("errno", strconv.FormatUint(uint64(errno), 10)))
	}
	var exitErr *exec.ExitError
	if errors.As(v.err, &exitErr) {
		attrs = append(attrs, slog.Int("exit_status", exitErr.ExitCode()))
	}
	if errors.Is(v.err, context.Canceled) {
		attrs = append(attrs, slog.String("context_outcome", "canceled"))
	} else if errors.Is(v.err, context.DeadlineExceeded) {
		attrs = append(attrs, slog.String("context_outcome", "deadline-exceeded"))
	}
	return slog.GroupValue(attrs...)
}

func errorClass(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "path"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "process-exit"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return "system"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context"
	}
	return "error"
}
