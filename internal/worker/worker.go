package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// pengerjaan satu job yang sudah di-claim. Handler wajib menulis status akhir lewat job.Begin;
// lock dilepas otomatis setelah Handler selesai.
type Handler func(ctx context.Context, job *Job)

type Worker struct {
	Pool        *pgxpool.Pool
	Handle      Handler
	Concurrency int           // maksimal job paralel (beda database), default 4
	Poll        time.Duration // jeda cek antrean saat kosong, default 1 detik
	RecoverEach time.Duration // jeda pengecekan job running yatim (worker crash), default 30 detik
}

// jalankan loop claim sampai ctx dibatalkan, lalu tunggu job yang sedang jalan selesai
func (w *Worker) Run(ctx context.Context) {
	n, poll := w.Concurrency, w.Poll
	if n <= 0 {
		n = 4
	}
	if poll <= 0 {
		poll = time.Second
	}
	recoverEach := w.RecoverEach
	if recoverEach <= 0 {
		recoverEach = 30 * time.Second
	}
	slots := make(chan struct{}, n)
	var wg sync.WaitGroup
	defer wg.Wait()

	var lastRecover time.Time
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		// saat start dan berkala: kembalikan job yang ditinggal worker lain yang mati
		if time.Since(lastRecover) >= recoverEach {
			lastRecover = time.Now()
			if _, err := Recover(ctx, w.Pool); err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "worker.recover_failed", "error", err)
			}
		}
		job, err := Claim(ctx, w.Pool)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "worker.claim_failed", "error", err)
		}
		if job == nil {
			<-slots
			select {
			case <-time.After(poll):
				continue
			case <-ctx.Done():
				return
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			w.process(ctx, job)
		}()
	}
}

func (w *Worker) process(ctx context.Context, job *Job) {
	// job yang sudah mulai tetap diselesaikan walaupun worker diminta berhenti
	ctx = telemetry.WithTraceParent(context.WithoutCancel(ctx), job.TraceParent)
	ctx = telemetry.WithAttrs(ctx,
		slog.Int64("job_id", job.ID), slog.Int64("execution_id", job.ExecutionID), slog.Int64("database_id", job.DatabaseID))
	ctx, span := telemetry.Tracer().Start(ctx, "worker.job", trace.WithAttributes(
		attribute.Int64("job.id", job.ID), attribute.Int64("execution.id", job.ExecutionID), attribute.Int64("database.id", job.DatabaseID)))
	defer span.End()
	defer job.Release(ctx)
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "worker.panic", "panic", r)
			if err := job.Requeue(ctx, 5); err != nil {
				slog.ErrorContext(ctx, "worker.requeue_failed", "error", err)
			}
		}
	}()
	slog.InfoContext(ctx, "worker.job_claimed")
	w.Handle(ctx, job)
}
