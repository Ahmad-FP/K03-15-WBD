package worker

import (
	"context"
	"log/slog"

	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
)

// Pulihkan job yang tertinggal berstatus running karena worker-nya mati.
//
// Advisory lock dipegang di level session: kalau proses worker mati, koneksinya putus dan
// Postgres melepas lock-nya otomatis. Jadi job running yang lock database-nya bisa kita ambil
// pasti tidak sedang dikerjakan siapa pun. Job itu dikembalikan ke antrean; saat dijalankan ulang,
// executor mengecek _wbd_checkpoints dulu sehingga SQL yang sudah ter-commit tidak dijalankan dua kali
// (mode normal langsung success, mode batch lanjut dari batch terakhir).
func Recover(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "worker.recover")
	defer span.End()

	rows, err := pool.Query(ctx, `SELECT j.id, j.execution_id, e.database_id FROM migration_jobs j
		JOIN migration_executions e ON e.id = j.execution_id WHERE j.status = 'running' ORDER BY j.id`)
	if err != nil {
		return 0, err
	}
	var stale []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.ExecutionID, &j.DatabaseID); err != nil {
			rows.Close()
			return 0, err
		}
		stale = append(stale, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	recovered := 0
	for _, j := range stale {
		ok, err := recoverOne(ctx, pool, j)
		if err != nil {
			slog.ErrorContext(ctx, "worker.recover_failed", "job_id", j.ID, "error", err)
			continue
		}
		if ok {
			recovered++
		}
	}
	span.SetAttributes(attribute.Int("recover.count", recovered))
	return recovered, nil
}

func recoverOne(ctx context.Context, pool *pgxpool.Pool, j Job) (bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()

	locked, err := tryLock(ctx, conn, j.DatabaseID)
	if err != nil || !locked {
		// lock masih dipegang worker yang hidup: job itu memang sedang dikerjakan
		return false, err
	}
	defer unlock(context.WithoutCancel(ctx), conn, j.DatabaseID)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// cek ulang di bawah lock: bisa saja job sudah selesai sebelum kita sempat mengunci
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM migration_jobs WHERE id = $1 FOR UPDATE`, j.ID).Scan(&status); err != nil {
		return false, err
	}
	if status != "running" {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE migration_jobs SET status = 'pending', run_after = now(), updated_at = now() WHERE id = $1`, j.ID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE migration_executions SET status = 'pending', started_at = NULL WHERE id = $1`, j.ExecutionID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	slog.WarnContext(ctx, "worker.crash_recovered", "job_id", j.ID, "execution_id", j.ExecutionID, "database_id", j.DatabaseID)
	return true, nil
}
