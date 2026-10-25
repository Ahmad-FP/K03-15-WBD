package migration

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errTxEnded = errors.New("SQL migration menutup transaction sendiri, perubahan mungkin tidak bisa di-rollback")

// error dari SQL migration-nya sendiri (bukan error sistem), statusnya udah dicatat failed
type ExecError struct{ Err error }

func (e *ExecError) Error() string { return e.Err.Error() }
func (e *ExecError) Unwrap() error { return e.Err }

// jalanin migration ke database target lalu catat hasilnya di riwayat
func Run(ctx context.Context, pool *pgxpool.Pool, target *pgx.Conn, dbID, id, userID int64) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// kunci baris migration biar ga dijalanin dua kali barengan
	var sql string
	var status Status
	err = tx.QueryRow(ctx, `SELECT sql, status FROM migrations WHERE database_id = $1 AND id = $2 FOR UPDATE`, dbID, id).Scan(&sql, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == Success {
		return ErrLocked
	}

	runErr := apply(ctx, target, sql)
	status, msg := Success, ""
	if runErr != nil {
		status, msg = Failed, runErr.Error()
	}
	_, err = tx.Exec(ctx, `UPDATE migrations SET status = $3, error = $4, executed_by = $5, executed_at = now(), updated_at = now()
		WHERE database_id = $1 AND id = $2`, dbID, id, status, msg, userID)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if runErr != nil {
		return &ExecError{runErr}
	}
	return nil
}

// eksekusi SQL dalam satu transaction, commit kalau semua berhasil, rollback kalau ada yang gagal
func apply(ctx context.Context, conn *pgx.Conn, sql string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '60s'"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return err
	}
	// jaga-jaga kalau ada COMMIT yang lolos validasi
	if conn.PgConn().TxStatus() != 'T' {
		return errTxEnded
	}
	return tx.Commit(ctx)
}
