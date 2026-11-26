package telemetry

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

const scope = "github.com/Ahmad-FP/K03-15-WBD"

var propagator = propagation.TraceContext{}

// pasang tracer global yang nulis span ke w (console exporter), panggil shutdown sebelum keluar
func Init(service string, w io.Writer) (shutdown func(context.Context) error, err error) {
	exp, err := stdouttrace.New(stdouttrace.WithWriter(w))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName(service))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagator)
	return tp.Shutdown, nil
}

// tracer bersama untuk semua span aplikasi
func Tracer() trace.Tracer {
	return otel.Tracer(scope)
}

// span aktif di ctx dalam format W3C traceparent, disimpan ke queue supaya worker nyambung ke trace API
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// kebalikan TraceParent: lanjutkan trace dari traceparent yang dibawa job
func WithTraceParent(ctx context.Context, traceParent string) context.Context {
	if traceParent == "" {
		return ctx
	}
	return propagator.Extract(ctx, propagation.MapCarrier{"traceparent": traceParent})
}

func traceIDs(ctx context.Context) (traceID, spanID string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}

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
