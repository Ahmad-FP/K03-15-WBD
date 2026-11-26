package telemetry

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// catat status code yang ditulis handler
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// server span untuk setiap request HTTP, jadi TraceParent(ctx) di handler terisi dan trace API → queue tersambung
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := Tracer().Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("url.path", r.URL.Path)))
		defer span.End()

		started := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		r = r.WithContext(ctx)
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}

		// r.Pattern diisi ServeMux setelah routing, contoh "POST /api/databases/{id}/executions/{eid}/enqueue"
		if r.Pattern != "" {
			span.SetName(r.Pattern)
			span.SetAttributes(attribute.String("http.route", r.Pattern))
		}
		span.SetAttributes(attribute.Int("http.response.status_code", sw.status))
		level := slog.LevelInfo
		if sw.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(sw.status))
			level = slog.LevelError
		}
		slog.LogAttrs(ctx, level, "http.request",
			slog.String("event", "http.request"), slog.String("method", r.Method), slog.String("path", r.URL.Path),
			slog.String("route", r.Pattern), slog.Int("status", sw.status),
			slog.Float64("duration_ms", float64(time.Since(started).Microseconds())/1000))
	})
}
