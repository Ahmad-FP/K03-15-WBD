package telemetry

import (
	"context"
	"log/slog"
)

func TraceParent(_ context.Context) string { return "" }

func traceIDs(_ context.Context) (traceID, spanID string) { return "", "" }

type Handler struct{ slog.Handler }

func (h Handler) Handle(ctx context.Context, r slog.Record) error {
	if tid, sid := traceIDs(ctx); tid != "" {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", tid), slog.String("span_id", sid))
	}
	return h.Handler.Handle(ctx, r)
}

func (h Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return Handler{h.Handler.WithAttrs(attrs)}
}

func (h Handler) WithGroup(name string) slog.Handler {
	return Handler{h.Handler.WithGroup(name)}
}
