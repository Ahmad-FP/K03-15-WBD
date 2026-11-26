package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceParentWithoutSpan(t *testing.T) {
	if got := TraceParent(context.Background()); got != "" {
		t.Fatalf("TraceParent tanpa span = %q, want kosong", got)
	}
	if tid, _ := traceIDs(context.Background()); tid != "" {
		t.Fatalf("traceIDs tanpa span = %q, want kosong", tid)
	}
}

func TestTraceParentRoundTrip(t *testing.T) {
	tracer := sdktrace.NewTracerProvider().Tracer("test")
	ctx, span := tracer.Start(context.Background(), "api")
	defer span.End()

	tp := TraceParent(ctx)
	if !strings.HasPrefix(tp, "00-"+span.SpanContext().TraceID().String()) {
		t.Fatalf("traceparent %q tidak memuat trace id span", tp)
	}

	// worker melanjutkan trace yang sama dari string traceparent
	remote := trace.SpanContextFromContext(WithTraceParent(context.Background(), tp))
	if remote.TraceID() != span.SpanContext().TraceID() || !remote.IsRemote() {
		t.Fatalf("trace tidak tersambung: %v", remote)
	}
	if got := WithTraceParent(ctx, ""); got != ctx {
		t.Fatal("traceparent kosong harus mengembalikan ctx apa adanya")
	}
}

func TestLoggerAddsTraceIDs(t *testing.T) {
	tracer := sdktrace.NewTracerProvider().Tracer("test")
	ctx, span := tracer.Start(context.Background(), "worker")
	defer span.End()

	var buf bytes.Buffer
	NewLogger(&buf).InfoContext(ctx, "job diambil")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["trace_id"] != span.SpanContext().TraceID().String() || rec["span_id"] != span.SpanContext().SpanID().String() {
		t.Fatalf("trace_id/span_id tidak ada di log: %v", rec)
	}
}

func TestInitWritesSpans(t *testing.T) {
	var buf bytes.Buffer
	shutdown, err := Init("test-service", &buf)
	if err != nil {
		t.Fatal(err)
	}
	_, span := Tracer().Start(context.Background(), "queue.enqueue")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "queue.enqueue") || !strings.Contains(out, "test-service") {
		t.Fatalf("span tidak tertulis ke exporter: %s", out)
	}
}
