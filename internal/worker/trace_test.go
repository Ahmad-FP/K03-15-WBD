package worker

import (
	"context"
	"testing"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/queue"
	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// span API → traceparent disimpan queue.Enqueue → span worker.job harus berada di trace yang sama
func TestTraceContinuesFromAPIToWorker(t *testing.T) {
	pool := platform(t)
	spans := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	defer otel.SetTracerProvider(prev)

	ctx := context.Background()
	_, execID := enqueue(t, pool, addDatabase(t, pool, "a", []byte{0}), 1)
	// enqueue di atas langsung bikin job; buang dulu supaya yang dipakai job dari queue.Enqueue
	pool.Exec(ctx, `DELETE FROM migration_jobs WHERE execution_id = $1`, execID)
	pool.Exec(ctx, `UPDATE migration_executions SET status = 'approved' WHERE id = $1`, execID)

	apiCtx, apiSpan := telemetry.Tracer().Start(ctx, "POST /api/databases/{id}/executions/{eid}/enqueue")
	tx, _ := pool.Begin(apiCtx)
	var dbID int64
	tx.QueryRow(apiCtx, `SELECT database_id FROM migration_executions WHERE id = $1`, execID).Scan(&dbID)
	if _, err := queue.Enqueue(apiCtx, tx, dbID, execID, telemetry.TraceParent(apiCtx)); err != nil {
		t.Fatal(err)
	}
	tx.Commit(apiCtx)
	apiSpan.End()

	runCtx, cancel := context.WithCancel(ctx)
	w := &Worker{Pool: pool, Poll: 50 * time.Millisecond, Handle: func(context.Context, *Job) { cancel() }}
	w.Run(runCtx)

	var workerSpan sdktrace.ReadOnlySpan
	for _, s := range spans.Ended() {
		if s.Name() == "worker.job" {
			workerSpan = s
		}
	}
	if workerSpan == nil {
		t.Fatal("span worker.job tidak tercatat")
	}
	if workerSpan.SpanContext().TraceID() != apiSpan.SpanContext().TraceID() {
		t.Fatal("span worker tidak berada di trace yang sama dengan request API")
	}
	if workerSpan.Parent().SpanID() != apiSpan.SpanContext().SpanID() {
		t.Fatal("span worker bukan anak dari span API")
	}
}
