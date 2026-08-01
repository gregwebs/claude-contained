package diagnostic

import (
	"context"
	"log/slog"
)

// filterHandler applies the threshold only to diagnostic records. Relocated
// output is sent to Stream.base directly and therefore cannot be discarded by
// --log-level.
type filterHandler struct {
	next  slog.Handler
	level Level
}

func (h *filterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.level != LevelOff && level >= h.level.slogLevel() && h.next.Enabled(ctx, level)
}

func (h *filterHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.next.Handle(ctx, record)
}

func (h *filterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &filterHandler{next: h.next.WithAttrs(attrs), level: h.level}
}

func (h *filterHandler) WithGroup(name string) slog.Handler {
	return &filterHandler{next: h.next.WithGroup(name), level: h.level}
}
