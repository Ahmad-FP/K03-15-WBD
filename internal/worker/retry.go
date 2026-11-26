package worker

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	backoffBase = 2 * time.Second
	backoffMax  = time.Minute
)

// SQLSTATE yang menandakan gangguan sementara, bukan kesalahan SQL migration
var transientStates = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
	"55P03": true, // lock_not_available
	"53300": true, // too_many_connections
	"57P01": true, // admin_shutdown
	"57P02": true, // crash_shutdown
	"57P03": true, // cannot_connect_now
}

// gagal karena gangguan (koneksi putus, deadlock, server restart) yang layak dicoba lagi otomatis
func transient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, db.ErrUnreachable) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// kelas 08 = connection exception
		return transientStates[pgErr.Code] || strings.HasPrefix(pgErr.Code, "08")
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return pgconn.SafeToRetry(err) || pgconn.Timeout(err)
}

// jeda sebelum attempt ke-n+1: 2s, 4s, 8s, ... maksimal 1 menit
func backoff(attempt int) time.Duration {
	d := backoffBase
	for i := 1; i < attempt && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}

// riwayat append-only: attempt sekarang ditutup failed (tidak ditimpa), attempt baru dibuat
// dengan request_id yang sama, lalu job diarahkan ke attempt baru itu dengan jeda backoff
func scheduleRetry(ctx context.Context, job *Job, e execution, out outcome) (newID int64, delay time.Duration, err error) {
	tx, err := job.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if _, err := tx.Exec(ctx, `UPDATE migration_executions SET status = 'failed', error = $2, batches_committed = $3, rows_processed = $4, finished_at = now()
		WHERE id = $1`, e.ID, telemetry.Redact(out.err.Error()), out.batches, out.rows); err != nil {
		return 0, 0, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO migration_executions (request_id, migration_id, database_id, version, operation, attempt, sql_snapshot,
			status, reverted_execution_id, parent_execution_id, batches_committed, rows_processed, last_cursor, requested_by)
		SELECT COALESCE(request_id, id), migration_id, database_id, version, operation, attempt + 1, sql_snapshot,
			'pending', reverted_execution_id, parent_execution_id, $2, $3, last_cursor, requested_by
		FROM migration_executions WHERE id = $1
		RETURNING id`, e.ID, out.batches, out.rows).Scan(&newID)
	if err != nil {
		return 0, 0, err
	}
	delay = backoff(e.Attempt)
	if _, err := tx.Exec(ctx, `UPDATE migration_jobs SET execution_id = $2, status = 'pending', run_after = now() + make_interval(secs => $3), updated_at = now()
		WHERE id = $1`, job.ID, newID, delay.Seconds()); err != nil {
		return 0, 0, err
	}
	return newID, delay, tx.Commit(ctx)
}
