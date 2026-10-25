package migration

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Status string

const (
	Draft   Status = "draft"
	Success Status = "success"
	Failed  Status = "failed"
)

var (
	ErrNotFound = errors.New("migration tidak ditemukan")
	ErrPending  = errors.New("masih ada migration yang belum sukses di database ini, jalankan atau hapus dulu")
	ErrLocked   = errors.New("migration yang sudah sukses tidak bisa diubah lagi")
)

type Migration struct {
	ID         int64
	Version    int
	Name       string
	SQL        string
	Status     Status
	Error      string
	CreatedBy  string
	ExecutedBy string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ExecutedAt *time.Time
}

const selectMigration = `SELECT m.id, m.version, m.name, m.sql, m.status, m.error,
		COALESCE(c.username, ''), COALESCE(e.username, ''), m.created_at, m.updated_at, m.executed_at
	FROM migrations m
	LEFT JOIN users c ON c.id = m.created_by
	LEFT JOIN users e ON e.id = m.executed_by`

// riwayat migration satu database, versi terbaru di atas
func List(ctx context.Context, pool *pgxpool.Pool, dbID int64) ([]Migration, error) {
	rows, err := pool.Query(ctx, selectMigration+` WHERE m.database_id = $1 ORDER BY m.version DESC`, dbID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Migration])
}

// detail satu migration, harus punya database yang sama
func Get(ctx context.Context, pool *pgxpool.Pool, dbID, id int64) (Migration, error) {
	rows, err := pool.Query(ctx, selectMigration+` WHERE m.database_id = $1 AND m.id = $2`, dbID, id)
	if err != nil {
		return Migration{}, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Migration])
	if errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	return m, err
}

// bikin draft baru dengan versi = versi terakhir + 1
func Create(ctx context.Context, pool *pgxpool.Pool, dbID, userID int64, name, sql string) (int64, error) {
	if err := Validate(name, sql); err != nil {
		return 0, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// kunci baris database biar dua orang ga dapet nomor versi yang sama
	var locked int64
	err = tx.QueryRow(ctx, `SELECT id FROM managed_databases WHERE id = $1 FOR UPDATE`, dbID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM migrations WHERE database_id = $1 AND status <> 'success')`, dbID).Scan(&pending); err != nil {
		return 0, err
	}
	if pending {
		return 0, ErrPending
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO migrations (database_id, version, name, sql, created_by)
		VALUES ($1, (SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE database_id = $1), $2, $3, $4)
		RETURNING id`, dbID, strings.TrimSpace(name), sql, userID).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// ubah nama/SQL migration yang belum sukses, statusnya balik jadi draft
func Update(ctx context.Context, pool *pgxpool.Pool, dbID, id int64, name, sql string) error {
	if err := Validate(name, sql); err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `UPDATE migrations SET name = $3, sql = $4, status = 'draft', error = '', updated_at = now()
		WHERE database_id = $1 AND id = $2 AND status <> 'success'`, dbID, id, strings.TrimSpace(name), sql)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return lockedOrMissing(ctx, pool, dbID, id)
	}
	return nil
}

// hapus migration yang belum pernah sukses
func Delete(ctx context.Context, pool *pgxpool.Pool, dbID, id int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM migrations WHERE database_id = $1 AND id = $2 AND status <> 'success'`, dbID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return lockedOrMissing(ctx, pool, dbID, id)
	}
	return nil
}

// bedain migration yang emang ga ada sama yang udah sukses
func lockedOrMissing(ctx context.Context, pool *pgxpool.Pool, dbID, id int64) error {
	if _, err := Get(ctx, pool, dbID, id); err != nil {
		return err
	}
	return ErrLocked
}
