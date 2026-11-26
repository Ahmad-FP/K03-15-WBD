package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// tracer provider sementara yang menyimpan span di memori
func memoryTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func TestMiddlewareSpanAndLog(t *testing.T) {
	spans := memoryTracer(t)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(NewLogger(&buf))
	defer slog.SetDefault(prev)

	var traceParent string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/databases/{id}/executions/{eid}/enqueue", func(w http.ResponseWriter, r *http.Request) {
		SetRequestActor(r.Context(), "ani")
		traceParent = TraceParent(r.Context())
		w.WriteHeader(http.StatusAccepted)
	})
	req := httptest.NewRequest("POST", "/api/databases/3/executions/9/enqueue", nil)
	Middleware(mux).ServeHTTP(httptest.NewRecorder(), req)

	if traceParent == "" {
		t.Fatal("TraceParent kosong di dalam handler, trace API → queue tidak akan tersambung")
	}
	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Name() != "POST /api/databases/{id}/executions/{eid}/enqueue" {
		t.Fatalf("span server = %v", ended)
	}
	if !strings.Contains(traceParent, ended[0].SpanContext().TraceID().String()) {
		t.Fatal("traceparent bukan milik span request")
	}
	var rec map[string]any
	json.Unmarshal(buf.Bytes(), &rec)
	if rec["msg"] != "http.request" || rec["status"] != float64(202) || rec["path"] != "/api/databases/3/executions/9/enqueue" || rec["actor"] != "ani" {
		t.Fatalf("log request = %v", rec)
	}
}

func TestRecordErrorRedactsSpan(t *testing.T) {
	spans := memoryTracer(t)
	_, span := Tracer().Start(context.Background(), "op")
	RecordError(span, errors.New("dial postgres://u:topsecret@db:5432/x gagal"))
	span.End()
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("span count = %d", len(ended))
	}
	if strings.Contains(ended[0].Status().Description, "topsecret") {
		t.Fatalf("span status bocor rahasia: %s", ended[0].Status().Description)
	}
	for _, ev := range ended[0].Events() {
		for _, attr := range ev.Attributes {
			if strings.Contains(attr.Value.AsString(), "topsecret") {
				t.Fatalf("span event bocor rahasia: %s=%s", attr.Key, attr.Value.AsString())
			}
		}
	}
}

func TestMiddlewareContinuesIncomingTrace(t *testing.T) {
	spans := memoryTracer(t)
	incoming := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.Header.Set("traceparent", incoming)
	Middleware(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), req)

	ended := spans.Ended()
	if len(ended) != 1 || ended[0].SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("span tidak melanjutkan trace dari header: %v", ended)
	}
}
