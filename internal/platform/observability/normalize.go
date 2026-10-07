package observability

import (
	"context"
	"log/slog"
	"time"
)

// NormalizeHandler wraps an embedder's handler (funcd.WithNormalizedLogFields, ADR-0197): Handle builds a new record
// with the time in UTC truncated to the millisecond and each attribute resolved and passed through durationField (a
// group is rebuilt from its members); WithAttrs does the same to its attributes. Enabled and WithGroup forward
// unchanged.
type NormalizeHandler struct{ inner slog.Handler }

// NewNormalizeHandler wraps inner.
func NewNormalizeHandler(inner slog.Handler) *NormalizeHandler {
	return &NormalizeHandler{inner: inner}
}

// Enabled reports whether inner handles level.
func (h *NormalizeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle passes inner a copy of r with its time and attributes normalized.
func (h *NormalizeHandler) Handle(ctx context.Context, r slog.Record) error {
	t := r.Time
	if !t.IsZero() {
		t = t.UTC().Truncate(time.Millisecond)
	}
	out := slog.NewRecord(t, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(normalizeAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

// WithAttrs wraps inner with attrs normalized.
func (h *NormalizeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	norm := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		norm[i] = normalizeAttr(a)
	}
	return NewNormalizeHandler(h.inner.WithAttrs(norm))
}

// WithGroup wraps inner's group handler.
func (h *NormalizeHandler) WithGroup(name string) slog.Handler {
	return NewNormalizeHandler(h.inner.WithGroup(name))
}

// normalizeAttr resolves a and passes it through durationField, rebuilding a group from its normalized members.
func normalizeAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() != slog.KindGroup {
		return durationField(a)
	}
	members := a.Value.Group()
	norm := make([]slog.Attr, len(members))
	for i, m := range members {
		norm[i] = normalizeAttr(m)
	}
	return slog.Attr{Key: a.Key, Value: slog.GroupValue(norm...)}
}
