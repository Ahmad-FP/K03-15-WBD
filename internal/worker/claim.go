package worker

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// namespace advisory lock milik platform, key kedua = database_id
const lockSpace = 3110

// berapa kandidat job yang dicoba dalam satu kali claim sebelum menyerah
const maxCandidates = 20

// job yang sedang dipegang worker: koneksi khusus ini memegang advisory lock database-nya sampai Release
type Job struct {
	ID          int64
	ExecutionID int64
	DatabaseID  int64
	TraceParent string

	conn *pgxpool.Conn
}

// ambil satu job pending yang database-nya tidak sedang dipakai worker lain.
// nil, nil berarti tidak ada job yang bisa diambil sekarang.
func Claim(ctx context.Context, pool *pgxpool.Pool) (*Job, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "worker.claim")
	defer span.End()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	job, err := claimOn(ctx, conn)
	if job == nil {
		conn.Release()
		return nil, err
	}
	span.SetAttributes(attribute.Int64("job.id", job.ID), attribute.Int64("execution.id", job.ExecutionID), attribute.Int64("database.id", job.DatabaseID))
	return job, nil
}

func claimOn(ctx context.Context, conn *pgxpool.Conn) (*Job, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	busy := []int64{}
	for range maxCandidates {
		var j Job
		err := tx.QueryRow(ctx, `SELECT j.id, j.execution_id, e.database_id, j.traceparent
			FROM migration_jobs j JOIN migration_executions e ON e.id = j.execution_id
			WHERE j.status = 'pending' AND j.run_after <= now() AND NOT (e.database_id = ANY($1))
			ORDER BY j.id LIMIT 1
			FOR UPDATE OF j SKIP LOCKED`, busy).Scan(&j.ID, &j.ExecutionID, &j.DatabaseID, &j.TraceParent)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		locked, err := tryLock(ctx, tx, j.DatabaseID)
		if err != nil {
			return nil, err
		}
		if !locked {
			// database ini sedang dimigrasi worker lain, coba job untuk database lain
			busy = append(busy, j.DatabaseID)
			continue
		}
		if err := markRunning(ctx, tx, j); err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			// transaksi claim batal, lock session-nya harus dilepas manual (rollback dulu biar koneksi bisa dipakai)
			tx.Rollback(ctx)
			unlock(context.WithoutCancel(ctx), conn, j.DatabaseID)
			return nil, err
		}
		j.conn = conn
		return &j, nil
	}
	return nil, nil
}

// lock level session: tetap dipegang setelah transaksi claim commit, sampai unlock eksplisit
// bisa dipanggil lewat transaksi maupun koneksi langsung
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func tryLock(ctx context.Context, tx queryRower, databaseID int64) (bool, error) {
	_, span := telemetry.Tracer().Start(ctx, "worker.lock", trace.WithAttributes(attribute.Int64("database.id", databaseID)))
	defer span.End()
	var ok bool
	err := tx.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, $2::int)`, lockSpace, databaseID).Scan(&ok)
	span.SetAttributes(attribute.Bool("lock.acquired", ok))
	return ok, err
}

func markRunning(ctx context.Context, tx pgx.Tx, j Job) error {
	if _, err := tx.Exec(ctx, `UPDATE migration_jobs SET status = 'running', updated_at = now() WHERE id = $1`, j.ID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE migration_executions SET status = 'running', started_at = now() WHERE id = $1`, j.ExecutionID)
	return err
}

func unlock(ctx context.Context, conn *pgxpool.Conn, databaseID int64) {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1, $2::int)`, lockSpace, databaseID); err != nil {
		slog.ErrorContext(ctx, "worker.unlock_failed", "database_id", databaseID, "error", err)
	}
}

// transaksi di koneksi pemegang lock, dipakai untuk menulis status akhir sebelum Release
func (j *Job) Begin(ctx context.Context) (pgx.Tx, error) {
	return j.conn.Begin(ctx)
}

// lepas advisory lock lalu kembalikan koneksi ke pool. Panggil setelah status akhir job di-commit.
func (j *Job) Release(ctx context.Context) {
	if j.conn == nil {
		return
	}
	unlock(context.WithoutCancel(ctx), j.conn, j.DatabaseID)
	j.conn.Release()
	j.conn = nil
}

// kembalikan job ke antrean (job & execution sama-sama pending) lalu lepas lock
func (j *Job) Requeue(ctx context.Context, delaySeconds int) error {
	defer j.Release(ctx)
	tx, err := j.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE migration_jobs SET status = 'pending', run_after = now() + make_interval(secs => $2), updated_at = now() WHERE id = $1`, j.ID, delaySeconds); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE migration_executions SET status = 'pending', started_at = NULL WHERE id = $1`, j.ExecutionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
