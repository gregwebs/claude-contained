package diagnostic

import (
	"context"
	"log/slog"
	"time"
)

type loggerKey struct{}
type streamKey struct{}

var discardLogger = slog.New(slog.DiscardHandler)

// WithLogger installs a logger without modifying process-global slog state.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		logger = discardLogger
	}
	return context.WithValue(ctx, loggerKey{}, logger)
}

// loggerFromContext retrieves the contextual logger or a discard logger. It is
// deliberately private: production packages can emit records only through the
// component-bound, typed interface returned by For.
func loggerFromContext(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && logger != nil {
			return logger
		}
	}
	return discardLogger
}

// Attr is the only attribute shape accepted by a ComponentLogger. Its slog
// representation is intentionally private so callers cannot attach arbitrary
// configs, plans, argv slices, or errors through slog's open-ended any path.
type Attr struct{ attr slog.Attr }

func String(key, value string) Attr    { return Attr{attr: slog.String(key, value)} }
func Bool(key string, value bool) Attr { return Attr{attr: slog.Bool(key, value)} }
func Int(key string, value int) Attr   { return Attr{attr: slog.Int(key, value)} }
func Duration(key string, value time.Duration) Attr {
	return Attr{attr: slog.Duration(key, value)}
}

// Value accepts only a type that owns a reviewed slog representation.
func Value(key string, value slog.LogValuer) Attr {
	return Attr{attr: slog.Attr{Key: key, Value: slog.AnyValue(value)}}
}

// ComponentLogger exposes only closed levels and diagnostic-owned attributes.
type ComponentLogger struct {
	ctx    context.Context
	logger *slog.Logger
}

func (l ComponentLogger) log(level slog.Level, message string, attrs []Attr) {
	slogAttrs := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		slogAttrs = append(slogAttrs, attr.attr)
	}
	l.logger.LogAttrs(l.ctx, level, message, slogAttrs...)
}

func (l ComponentLogger) Debug(message string, attrs ...Attr) {
	l.log(slog.LevelDebug, message, attrs)
}
func (l ComponentLogger) Info(message string, attrs ...Attr) {
	l.log(slog.LevelInfo, message, attrs)
}
func (l ComponentLogger) Warn(message string, attrs ...Attr) {
	l.log(slog.LevelWarn, message, attrs)
}
func (l ComponentLogger) Error(message string, attrs ...Attr) {
	l.log(slog.LevelError, message, attrs)
}

// For binds the mandatory metadata for one diagnostic component.
func For(ctx context.Context, component Component) ComponentLogger {
	if ctx == nil {
		ctx = context.Background()
	}
	if !component.valid() {
		return ComponentLogger{ctx: ctx, logger: discardLogger}
	}
	return ComponentLogger{ctx: ctx, logger: loggerFromContext(ctx).With(
		slog.String("kind", "diagnostic"),
		slog.String("component", component.String()),
	)}
}

// WithStream installs stream lifecycle state for Flush.
func WithStream(ctx context.Context, stream *Stream) context.Context {
	return context.WithValue(ctx, streamKey{}, stream)
}

// Flush emits pending relocated fragments. With no stream it is a no-op.
func Flush(ctx context.Context) error {
	if ctx != nil {
		if stream, ok := ctx.Value(streamKey{}).(*Stream); ok && stream != nil {
			return stream.Flush()
		}
	}
	return nil
}
