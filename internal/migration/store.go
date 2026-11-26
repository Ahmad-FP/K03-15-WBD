package migration

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = &Error{http.StatusNotFound, "migration tidak ditemukan"}
	ErrLocked   = &Error{http.StatusConflict, "migration yang sudah pernah diajukan tidak bisa diubah atau dihapus"}
)

type Migration struct {
	ID                 int64     `json:"id"`
	Version            int       `json:"version"`
	Name               string    `json:"name"`
	SQLUp              string    `json:"sql_up"`
	SQLDown            string    `json:"sql_down"`
	IrreversibleReason string    `json:"irreversible_reason"`
	BatchColumn        string    `json:"batch_column"`
	BatchSize          int       `json:"batch_size"`
	Immutable          bool      `json:"immutable"`
	CreatedByUsername  string    `json:"created_by_username"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type MigrationDetail struct {
	Migration
	CanRollback bool `json:"can_rollback"`
	CanForward  bool `json:"can_forward"`
}

const selectMigration = `SELECT m.id, m.version, m.name, m.sql, m.sql_down, m.irreversible_reason,
		m.batch_column, m.batch_size,
		EXISTS (SELECT 1 FROM migration_executions e WHERE e.migration_id = m.id),
		COALESCE(c.username, ''), m.created_at, m.updated_at
	FROM migrations m
	LEFT JOIN users c ON c.id = m.created_by`

func List(ctx context.Context, pool *pgxpool.Pool, dbID int64) ([]Migration, error) {
	rows, err := pool.Query(ctx, selectMigration+` WHERE m.database_id = $1 ORDER BY m.version DESC`, dbID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Migration])
	if list == nil {
		list = []Migration{}
	}
	return list, err
}

func Get(ctx context.Context, q queryer, dbID, id int64) (Migration, error) {
	rows, err := q.Query(ctx, selectMigration+` WHERE m.database_id = $1 AND m.id = $2`, dbID, id)
	if err != nil {
		return Migration{}, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Migration])
	if errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	return m, err
}

func GetDetail(ctx context.Context, pool *pgxpool.Pool, dbID, id int64) (MigrationDetail, error) {
	m, err := Get(ctx, pool, dbID, id)
	if err != nil {
		return MigrationDetail{}, err
	}
	canRollback, canForward, err := migrationActions(ctx, pool, dbID, m)
	if err != nil {
		return MigrationDetail{}, err
	}
	return MigrationDetail{Migration: m, CanRollback: canRollback, CanForward: canForward}, nil
}

func Create(ctx context.Context, pool *pgxpool.Pool, dbID, userID int64, in Input) (Migration, error) {
	in, err := cleanInput(in)
	if err != nil {
		return Migration{}, err
	}
	tx, err := beginDatabaseTx(ctx, pool, dbID)
	if err != nil {
		return Migration{}, err
	}
	defer tx.Rollback(ctx)

	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO migrations
			(database_id, version, name, sql, sql_down, irreversible_reason, batch_column, batch_size, created_by)
		VALUES ($1, (SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE database_id = $1), $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		dbID, in.Name, in.SQLUp, in.SQLDown, in.IrreversibleReason, in.BatchColumn, in.BatchSize, userID).Scan(&id)
	if err != nil {
		return Migration{}, err
	}
	m, err := Get(ctx, tx, dbID, id)
	if err != nil {
		return Migration{}, err
	}
	return m, tx.Commit(ctx)
}

func Update(ctx context.Context, pool *pgxpool.Pool, dbID, id int64, in Input) (Migration, error) {
	in, err := cleanInput(in)
	if err != nil {
		return Migration{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Migration{}, err
	}
	defer tx.Rollback(ctx)

	var locked int64
	err = tx.QueryRow(ctx, `SELECT id FROM migrations WHERE database_id = $1 AND id = $2 FOR UPDATE`, dbID, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	if err != nil {
		return Migration{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE migrations
		SET name = $3, sql = $4, sql_down = $5, irreversible_reason = $6, batch_column = $7, batch_size = $8, updated_at = now()
		WHERE database_id = $1 AND id = $2
			AND NOT EXISTS (SELECT 1 FROM migration_executions WHERE migration_id = $2)`,
		dbID, id, in.Name, in.SQLUp, in.SQLDown, in.IrreversibleReason, in.BatchColumn, in.BatchSize)
	if err != nil {
		return Migration{}, err
	}
	if tag.RowsAffected() == 0 {
		return Migration{}, ErrLocked
	}
	m, err := Get(ctx, tx, dbID, id)
	if err != nil {
		return Migration{}, err
	}
	return m, tx.Commit(ctx)
}

func Delete(ctx context.Context, pool *pgxpool.Pool, dbID, id int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM migrations WHERE database_id = $1 AND id = $2`, dbID, id)
	if httpx.IsPGCode(err, "23503") {
		return ErrLocked
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
