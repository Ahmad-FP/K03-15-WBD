package worker

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// posisi terakhir yang sudah ter-commit untuk satu root execution
type progress struct {
	batches int
	rows    int64
	cursor  *string // nil = belum ada batch, mulai dari awal
}

// mode batch (up/forward/retry): tiap batch transaksi sendiri + checkpoint di transaksi yang sama.
// sql_snapshot menerima $1 = last_cursor (NULL di batch pertama) dan $2 = batch_size,
// lalu mengembalikan kolom cursor yang terurut dari tiap baris yang diproses.
func (x *Executor) runBatch(ctx context.Context, job *Job, target *pgx.Conn, e execution) outcome {
	if _, err := target.Exec(ctx, checkpointTable); err != nil {
		return outcome{status: "failed", err: err}
	}
	// retry melanjutkan checkpoint root yang sama tanpa mengulang batch yang sudah commit
	p, err := loadProgress(ctx, target, e.RootID)
	if err != nil {
		return outcome{status: "failed", err: err}
	}
	for {
		n, last, err := runOneBatch(ctx, target, e, p)
		if err != nil {
			status := "failed"
			if p.batches > 0 {
				// batch sebelumnya tetap committed, tampilkan sebagai partial supaya bisa di-retry/compensate
				status = "partial"
			}
			return outcome{status: status, err: err, batches: p.batches, rows: p.rows}
		}
		if n == 0 {
			return outcome{status: "success", batches: p.batches, rows: p.rows}
		}
		p.batches++
		p.rows += n
		p.cursor = &last
		if err := job.saveProgress(ctx, e.ID, p); err != nil {
			slog.WarnContext(ctx, "migration.progress_save_failed", "error", err)
		}
	}
}

// satu batch: jalankan SQL dengan cursor terakhir, tulis checkpoint, commit. n = 0 berarti data sudah habis.
func runOneBatch(ctx context.Context, target *pgx.Conn, e execution, p progress) (n int64, last string, err error) {
	batchNo := p.batches + 1
	ctx = telemetry.WithAttrs(ctx, slog.Int("batch", batchNo))
	ctx, span := telemetry.Tracer().Start(ctx, "migration.batch", trace.WithAttributes(
		attribute.Int("batch.no", batchNo), attribute.Int("batch.size", e.BatchSize)))
	defer func() {
		span.SetAttributes(attribute.Int64("batch.rows", n))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, telemetry.Redact(err.Error()))
		}
		span.End()
	}()

	tx, err := target.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	rows, err := tx.Query(ctx, e.SQL, p.cursor, e.BatchSize)
	if err != nil {
		return 0, "", err
	}
	for rows.Next() {
		var c *string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return 0, "", err
		}
		if c == nil {
			rows.Close()
			return 0, "", errors.New("SQL batch mengembalikan cursor NULL")
		}
		n++
		last = *c
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	if n == 0 {
		// tidak ada baris tersisa: jangan tulis checkpoint kosong
		return 0, "", nil
	}
	prev := ""
	if p.cursor != nil {
		prev = *p.cursor
	}
	if _, err := tx.Exec(ctx, `INSERT INTO _wbd_checkpoints (execution_id, batch_no, prev_cursor, last_cursor, rows_affected)
		VALUES ($1, $2, $3, $4, $5)`, e.RootID, batchNo, prev, last, n); err != nil {
		return 0, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", err
	}
	slog.InfoContext(ctx, "migration.batch_committed", "batch", batchNo, "rows", n, "last_cursor", last)
	return n, last, nil
}

func loadProgress(ctx context.Context, target *pgx.Conn, rootID int64) (progress, error) {
	var p progress
	var cursor string
	err := target.QueryRow(ctx, `SELECT count(*), COALESCE(sum(rows_affected), 0),
			COALESCE((SELECT last_cursor FROM _wbd_checkpoints WHERE execution_id = $1 ORDER BY batch_no DESC LIMIT 1), '')
		FROM _wbd_checkpoints WHERE execution_id = $1`, rootID).Scan(&p.batches, &p.rows, &cursor)
	if p.batches > 0 {
		p.cursor = &cursor
	}
	return p, err
}

// progres batch yang sudah committed, supaya SPA bisa memantau tanpa menunggu job selesai
func (j *Job) saveProgress(ctx context.Context, execID int64, p progress) error {
	_, err := j.conn.Exec(ctx, `UPDATE migration_executions SET batches_committed = $2, rows_processed = $3, last_cursor = $4 WHERE id = $1`,
		execID, p.batches, p.rows, *p.cursor)
	return err
}
