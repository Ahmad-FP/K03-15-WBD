package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// tabel bukti commit di database target, ditulis di transaksi yang sama dengan SQL migration
const checkpointTable = `CREATE TABLE IF NOT EXISTS _wbd_checkpoints (
    execution_id bigint NOT NULL,
    batch_no integer NOT NULL,
    prev_cursor text NOT NULL DEFAULT '',
    last_cursor text NOT NULL DEFAULT '',
    rows_affected bigint NOT NULL DEFAULT 0,
    committed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (execution_id, batch_no)
)`

var errTxEnded = errors.New("SQL migration menutup transaction sendiri, perubahan mungkin tidak bisa di-rollback")

// data execution yang dibutuhkan untuk menjalankan satu attempt
type execution struct {
	ID          int64
	ReqID       int64 // COALESCE(request_id, id): id pengajuan pertama, stabil lintas retry
	RootID      int64 // COALESCE(parent_execution_id, request_id, id): pemilik checkpoint batch
	MigrationID int64
	Version     int
	Operation   string
	Attempt     int
	SQL         string // sql_snapshot: salinan SQL yang benar-benar dijalankan
	Actor       string
	BatchSize   int
	MaxAttempts int
}

// hasil akhir satu attempt, ditulis ke migration_executions dan migration_jobs
type outcome struct {
	status  string // success / failed / partial / partially_reverted
	err     error
	batches int
	rows    int64
}

// Handler worker: jalankan SQL migration di database target lalu catat hasilnya
type Executor struct {
	Pool *pgxpool.Pool
}

func (x *Executor) Handle(ctx context.Context, job *Job) {
	e, err := loadExecution(ctx, x.Pool, job.ExecutionID)
	if err != nil {
		slog.ErrorContext(ctx, "worker.load_failed", "error", err)
		if err := job.Requeue(ctx, 5); err != nil {
			slog.ErrorContext(ctx, "worker.requeue_failed", "error", err)
		}
		return
	}
	ctx = telemetry.WithActor(ctx, e.Actor)
	ctx = telemetry.WithAttrs(ctx, slog.Int64("migration_id", e.MigrationID), slog.Int("version", e.Version),
		slog.String("operation", e.Operation), slog.Int("attempt", e.Attempt))
	ctx, span := telemetry.Tracer().Start(ctx, "migration.execute")
	defer span.End()

	started := time.Now()
	logAttempt(ctx, "migration.attempt.start", job, e, "running", nil)
	out := x.run(ctx, job, e)
	if err := finish(ctx, job, e, out); err != nil {
		slog.ErrorContext(ctx, "worker.finish_failed", "error", err)
		return
	}
	if out.err != nil {
		span.RecordError(out.err)
		span.SetStatus(codes.Error, telemetry.Redact(out.err.Error()))
	}
	span.SetAttributes(attribute.String("migration.status", out.status), attribute.Int64("migration.rows", out.rows))
	logAttempt(ctx, "migration.attempt.end", job, e, out.status, out.err, slog.Float64("duration_ms", float64(time.Since(started).Microseconds())/1000),
		slog.Int("batches_committed", out.batches), slog.Int64("rows_processed", out.rows))
}

func (x *Executor) run(ctx context.Context, job *Job, e execution) outcome {
	target, err := db.Connect(ctx, x.Pool, job.DatabaseID)
	if err != nil {
		return outcome{status: "failed", err: err}
	}
	defer target.Close(context.WithoutCancel(ctx))
	if e.BatchSize == 0 {
		return runNormal(ctx, target, e)
	}
	switch e.Operation {
	case "up", "forward", "retry":
		return x.runBatch(ctx, job, target, e)
	}
	return outcome{status: "failed", err: errors.New("operasi " + e.Operation + " mode batch belum didukung worker")}
}

// mode normal: seluruh SQL dalam satu transaksi + checkpoint batch 1 sebagai bukti commit
func runNormal(ctx context.Context, target *pgx.Conn, e execution) outcome {
	if _, err := target.Exec(ctx, checkpointTable); err != nil {
		return outcome{status: "failed", err: err}
	}
	// pesan duplikat / crash setelah commit: SQL sudah pernah ter-commit, jangan jalankan dua kali
	var rows int64
	err := target.QueryRow(ctx, `SELECT rows_affected FROM _wbd_checkpoints WHERE execution_id = $1 AND batch_no = 1`, e.ReqID).Scan(&rows)
	if err == nil {
		slog.InfoContext(ctx, "migration.already_committed", "req_id", e.ReqID)
		return outcome{status: "success", batches: 1, rows: rows}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return outcome{status: "failed", err: err}
	}

	tx, err := target.Begin(ctx)
	if err != nil {
		return outcome{status: "failed", err: err}
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	tag, err := tx.Exec(ctx, e.SQL)
	if err != nil {
		return outcome{status: "failed", err: err}
	}
	if target.PgConn().TxStatus() != 'T' {
		return outcome{status: "failed", err: errTxEnded}
	}
	rows = tag.RowsAffected()
	if _, err := tx.Exec(ctx, `INSERT INTO _wbd_checkpoints (execution_id, batch_no, rows_affected) VALUES ($1, 1, $2)`, e.ReqID, rows); err != nil {
		return outcome{status: "failed", err: err}
	}
	if err := tx.Commit(ctx); err != nil {
		return outcome{status: "failed", err: err}
	}
	return outcome{status: "success", batches: 1, rows: rows}
}

func loadExecution(ctx context.Context, pool *pgxpool.Pool, id int64) (execution, error) {
	var e execution
	err := pool.QueryRow(ctx, `SELECT e.id, COALESCE(e.request_id, e.id), COALESCE(e.parent_execution_id, e.request_id, e.id), e.migration_id, e.version, e.operation, e.attempt,
			e.sql_snapshot, COALESCE(u.username, ''), m.batch_size, j.max_attempts
		FROM migration_executions e
		JOIN migrations m ON m.id = e.migration_id
		JOIN migration_jobs j ON j.execution_id = e.id
		LEFT JOIN users u ON u.id = e.requested_by
		WHERE e.id = $1`, id).Scan(&e.ID, &e.ReqID, &e.RootID, &e.MigrationID, &e.Version, &e.Operation, &e.Attempt,
		&e.SQL, &e.Actor, &e.BatchSize, &e.MaxAttempts)
	return e, err
}

// tulis status akhir execution + job di koneksi pemegang lock, sebelum lock dilepas
func finish(ctx context.Context, job *Job, e execution, out outcome) error {
	tx, err := job.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	msg := ""
	if out.err != nil {
		msg = telemetry.Redact(out.err.Error())
	}
	if _, err := tx.Exec(ctx, `UPDATE migration_executions SET status = $2, error = $3, batches_committed = $4, rows_processed = $5, finished_at = now()
		WHERE id = $1`, e.ID, out.status, msg, out.batches, out.rows); err != nil {
		return err
	}
	jobStatus := "done"
	if out.status != "success" {
		jobStatus = "failed"
	}
	if _, err := tx.Exec(ctx, `UPDATE migration_jobs SET status = $2, updated_at = now() WHERE id = $1`, job.ID, jobStatus); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// log aplikasi untuk setiap attempt (berhasil maupun gagal), termasuk salinan SQL yang dijalankan
func logAttempt(ctx context.Context, event string, job *Job, e execution, status string, err error, extra ...slog.Attr) {
	attrs := []slog.Attr{
		slog.String("event", event), slog.Int64("database_id", job.DatabaseID), slog.Int64("job_id", job.ID),
		slog.Int64("migration_id", e.MigrationID), slog.Int64("execution_id", e.ID), slog.Int64("req_id", e.ReqID),
		slog.Int("version", e.Version), slog.String("operation", e.Operation), slog.Int("attempt", e.Attempt),
		slog.String("actor", e.Actor), slog.String("status", status), slog.String("sql_snapshot", telemetry.Redact(e.SQL)),
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelError
		attrs = append(attrs, slog.String("error", telemetry.Redact(err.Error())))
	}
	slog.LogAttrs(ctx, level, event, append(attrs, extra...)...)
}
