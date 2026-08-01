package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// ErrClosed is returned by relocated writers after their stream is closed.
var ErrClosed = errors.New("diagnostic stream is closed")

// Options configures one diagnostic stream.
type Options struct {
	Resolution Resolution
	FilePath   string
	LogOnly    bool
}

// SetupError identifies a destination setup failure without permitting a
// fallback to stderr.
type SetupError struct {
	Path string
	Err  error
}

func (e *SetupError) Error() string {
	return fmt.Sprintf("cannot open diagnostic file %s: %v", e.Path, e.Err)
}

func (e *SetupError) Unwrap() error { return e.Err }

type destination interface {
	io.Writer
	Chmod(os.FileMode) error
	Truncate(int64) error
	Seek(int64, int) (int64, error)
	Close() error
}

var openDestination = func(path string) (destination, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
}

// stickyWriter serializes complete slog records and retains the first
// destination failure, because logger methods cannot return handler errors.
type stickyWriter struct {
	mu     sync.Mutex
	dst    io.Writer
	first  error
	closed bool
}

func (w *stickyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		if w.first == nil {
			w.first = ErrClosed
		}
		return 0, ErrClosed
	}
	n, err := w.dst.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil && w.first == nil {
		w.first = err
	}
	return n, err
}

func (w *stickyWriter) remember(err error) {
	if err == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.first == nil {
		w.first = err
	}
}

func (w *stickyWriter) err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.first
}

func (w *stickyWriter) close(owned io.Closer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.first
	}
	if owned != nil {
		if err := owned.Close(); err != nil && w.first == nil {
			w.first = err
		}
	}
	w.closed = true
	return w.first
}

// Stream owns formatting, line-aware relocation, and optional file lifecycle.
type Stream struct {
	mu      sync.Mutex
	base    slog.Handler
	writer  *stickyWriter
	logger  *slog.Logger
	owned   io.Closer
	logOnly bool
	closed  bool
	stdout  *routedWriter
	stderr  *routedWriter
}

// Open creates one source-free, timestamp-free TextHandler. A named file is
// narrowed to 0600 on its descriptor before existing contents are truncated.
func Open(options Options, defaultDestination io.Writer) (*Stream, error) {
	if defaultDestination == nil {
		defaultDestination = io.Discard
	}

	dst := defaultDestination
	var owned io.Closer
	if options.FilePath != "" {
		file, err := openDestination(options.FilePath)
		if err != nil {
			return nil, &SetupError{Path: options.FilePath, Err: err}
		}
		fail := func(err error) (*Stream, error) {
			_ = file.Close()
			return nil, &SetupError{Path: options.FilePath, Err: err}
		}
		if err := file.Chmod(0o600); err != nil {
			return fail(err)
		}
		if err := file.Truncate(0); err != nil {
			return fail(err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fail(err)
		}
		dst, owned = file, file
	}

	sticky := &stickyWriter{dst: dst}
	base := slog.NewTextHandler(sticky, &slog.HandlerOptions{
		AddSource: false,
		Level:     slog.LevelDebug,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	})
	stream := &Stream{
		base:    base,
		writer:  sticky,
		owned:   owned,
		logOnly: options.LogOnly,
	}
	stream.logger = slog.New(&filterHandler{next: base, level: options.Resolution.Level})
	return stream, nil
}

// Context installs both the logger and the stream lifecycle handle.
func (s *Stream) Context(parent context.Context) context.Context {
	return WithStream(WithLogger(parent, s.logger), s)
}

// Writers keeps the user-facing channel unchanged unless --log-only was set.
func (s *Stream) Writers(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	if !s.logOnly {
		return stdout, stderr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdout == nil {
		s.stdout = &routedWriter{stream: s, name: "stdout", level: slog.LevelInfo}
		s.stderr = &routedWriter{stream: s, name: "stderr", level: slog.LevelWarn}
	}
	return s.stdout, s.stderr
}

type routedWriter struct {
	stream  *Stream
	name    string
	level   slog.Level
	pending []byte // guarded by stream.mu
}

func (w *routedWriter) Write(p []byte) (int, error) {
	s := w.stream
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.writer.remember(ErrClosed)
		return 0, ErrClosed
	}

	w.pending = append(w.pending, p...)
	var first error
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		line := string(w.pending[:newline])
		w.pending = w.pending[newline+1:]
		if err := s.emitOutputLocked(w.level, w.name, line); err != nil && first == nil {
			first = err
		}
	}
	return len(p), first
}

func (s *Stream) emitOutputLocked(level slog.Level, stream, message string) error {
	ctx := context.Background()
	if !s.base.Enabled(ctx, level) {
		return nil
	}
	record := slog.NewRecord(time.Time{}, level, message, 0)
	record.AddAttrs(slog.String("kind", "output"), slog.String("stream", stream))
	err := s.base.Handle(ctx, record)
	s.writer.remember(err)
	return err
}

func (s *Stream) flushRouteLocked(writer *routedWriter) error {
	if writer == nil || len(writer.pending) == 0 {
		return nil
	}
	message := string(writer.pending)
	writer.pending = nil // consume before emission so a failure cannot duplicate it
	return s.emitOutputLocked(writer.level, writer.name, message)
}

// Flush emits pending stdout then pending stderr and returns the first sticky
// stream failure.
func (s *Stream) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		_ = s.flushRouteLocked(s.stdout)
		_ = s.flushRouteLocked(s.stderr)
	}
	return s.writer.err()
}

// Close is idempotent. It closes only a file opened by Open.
func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.writer.err()
	}
	_ = s.flushRouteLocked(s.stdout)
	_ = s.flushRouteLocked(s.stderr)
	err := s.writer.close(s.owned)
	s.closed = true
	return err
}
