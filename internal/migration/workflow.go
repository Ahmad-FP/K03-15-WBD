package migration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/queue"
	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrExecNotFound    = &Error{http.StatusNotFound, "execution tidak ditemukan"}
	ErrInFlight        = &Error{http.StatusConflict, "migration ini sudah memiliki eksekusi yang sedang berjalan atau menunggu persetujuan"}
	ErrMissingLeader   = &Error{http.StatusConflict, "semua group dengan akses ke database harus memiliki leader sebelum mengajukan migration"}
	ErrInvalidOrder    = &Error{http.StatusConflict, "urutan migration tidak valid"}
	ErrIrreversible    = &Error{http.StatusConflict, "migration ini bersifat irreversible dan tidak dapat di-rollback"}
	ErrInvalidState    = &Error{http.StatusConflict, "status execution tidak mengizinkan aksi ini"}
	ErrInvalidDecision = &Error{http.StatusBadRequest, "decision harus approved atau rejected"}
	ErrNotLeader       = &Error{http.StatusForbidden, "hanya group leader yang dapat menyetujui atau menolak migration"}
)

type Execution struct {
	ID                  int64      `json:"id"`
	RequestID           *int64     `json:"request_id"`
	MigrationID         int64      `json:"migration_id"`
	MigrationName       string     `json:"migration_name"`
	Version             int        `json:"version"`
	Operation           string     `json:"operation"`
	Attempt             int        `json:"attempt"`
	Status              string     `json:"status"`
	RevertedExecutionID *int64     `json:"reverted_execution_id"`
	ParentExecutionID   *int64     `json:"parent_execution_id"`
	BatchesCommitted    int        `json:"batches_committed"`
	RowsProcessed       int64      `json:"rows_processed"`
	Error               string     `json:"error"`
	RequestedByUsername string     `json:"requested_by_username"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at"`
}

type Approval struct {
	GroupID           int64      `json:"group_id"`
	GroupName         string     `json:"group_name"`
	LeaderID          *int64     `json:"leader_id"`
	LeaderUsername    *string    `json:"leader_username"`
	Status            string     `json:"status"`
	DecidedByUsername *string    `json:"decided_by_username"`
	DecidedAt         *time.Time `json:"decided_at"`
}

type ExecutionDetail struct {
	Execution
	SQLSnapshot       string     `json:"sql_snapshot"`
	LastCursor        string     `json:"last_cursor"`
	LatestExecutionID int64      `json:"latest_execution_id"`
	JobID             *int64     `json:"job_id"`
	JobStatus         *string    `json:"job_status"`
	Approvals         []Approval `json:"approvals" db:"-"`
}

type headState struct {
	Version     int
	ActiveID    int64
	MigrationID int64
	RootID      int64
}

const activeRowsSQL = `SELECT e.id, e.version, e.migration_id, COALESCE(e.parent_execution_id, e.request_id, e.id) AS root_id
	FROM migration_executions e
	WHERE e.database_id = $1 AND e.operation IN ('up', 'forward', 'retry') AND e.status = 'success'
		AND NOT EXISTS (SELECT 1 FROM migration_executions r
			WHERE r.reverted_execution_id = e.id AND r.operation IN ('rollback', 'compensate') AND r.status = 'success')`

const unresolvedRowsSQL = `SELECT p.id, p.migration_id
	FROM migration_executions p
	WHERE p.database_id = $1 AND p.status IN ('partial', 'partially_reverted')
		AND NOT EXISTS (SELECT 1 FROM migration_executions r
			WHERE r.id > p.id
				AND COALESCE(r.parent_execution_id, r.request_id, r.id) = COALESCE(p.parent_execution_id, p.request_id, p.id)
				AND r.status IN ('success', 'partial', 'partially_reverted'))`

const selectExecutionCols = `e.id, e.request_id, e.migration_id, m.name, e.version, e.operation, e.attempt,
	e.status, e.reverted_execution_id, e.parent_execution_id, e.batches_committed, e.rows_processed,
	e.error, COALESCE(u.username, ''), e.created_at, e.started_at, e.finished_at`

func ListExecutions(ctx context.Context, pool *pgxpool.Pool, dbID int64, hideRollback bool) ([]Execution, error) {
	q := `SELECT ` + selectExecutionCols + `
		FROM migration_executions e
		JOIN migrations m ON m.id = e.migration_id
		LEFT JOIN users u ON u.id = e.requested_by
		WHERE e.database_id = $1`
	if hideRollback {
		q += ` AND NOT EXISTS (SELECT 1 FROM migration_executions r
			WHERE r.database_id = $1 AND r.operation IN ('rollback', 'compensate')
				AND r.status = 'success' AND r.reverted_execution_id IS NOT NULL
				AND (e.id = r.id OR e.id = r.reverted_execution_id))`
	}
	return httpx.Collect[Execution](ctx, pool, q+` ORDER BY e.id DESC`, dbID)
}

func GetExecution(ctx context.Context, pool *pgxpool.Pool, dbID, eid int64) (ExecutionDetail, error) {
	rows, err := pool.Query(ctx, `SELECT `+selectExecutionCols+`,
			e.sql_snapshot, e.last_cursor, latest.id, j.id, j.status
		FROM migration_executions e
		JOIN migrations m ON m.id = e.migration_id
		LEFT JOIN users u ON u.id = e.requested_by
		CROSS JOIN LATERAL (
			SELECT MAX(x.id) AS id FROM migration_executions x
			WHERE x.database_id = e.database_id AND COALESCE(x.request_id, x.id) = COALESCE(e.request_id, e.id)
		) latest
		LEFT JOIN migration_jobs j ON j.execution_id = latest.id
		WHERE e.database_id = $1 AND e.id = $2`, dbID, eid)
	if err != nil {
		return ExecutionDetail{}, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[ExecutionDetail])
	if errors.Is(err, pgx.ErrNoRows) {
		return ExecutionDetail{}, ErrExecNotFound
	}
	if err != nil {
		return ExecutionDetail{}, err
	}
	reqID := d.ID
	if d.RequestID != nil {
		reqID = *d.RequestID
	}
	d.Approvals, err = httpx.Collect[Approval](ctx, pool, `SELECT ap.group_id, g.name, g.leader_id, l.username,
			ap.status, du.username, ap.decided_at
		FROM migration_approvals ap
		JOIN platform_groups g ON g.id = ap.group_id
		LEFT JOIN users l ON l.id = g.leader_id
		LEFT JOIN users du ON du.id = ap.decided_by
		WHERE ap.execution_id = $1
		ORDER BY g.name`, reqID)
	return d, err
}

func Submit(ctx context.Context, pool *pgxpool.Pool, dbID, mid, userID int64) (int64, error) {
	return withDatabaseTx(ctx, pool, dbID, func(tx pgx.Tx) (int64, error) {
		m, err := lockMigration(ctx, tx, dbID, mid)
		if err != nil {
			return 0, err
		}
		if err := checkGroupLeaders(ctx, tx, dbID); err != nil {
			return 0, err
		}
		if unresolved, err := hasUnresolved(ctx, tx, dbID); err != nil || unresolved {
			if err != nil {
				return 0, err
			}
			return 0, ErrInvalidState
		}
		head, err := activeHead(ctx, tx, dbID)
		if err != nil {
			return 0, err
		}
		if m.Version <= head.Version {
			return 0, ErrInvalidOrder
		}
		everSuccess, err := everSucceeded(ctx, tx, mid)
		if err != nil {
			return 0, err
		}
		op := "up"
		if !everSuccess {
			hasDraft, err := exists(ctx, tx, `SELECT EXISTS (SELECT 1 FROM migrations w
				WHERE w.database_id = $1 AND w.version > $2 AND w.version < $3
					AND NOT EXISTS (SELECT 1 FROM migration_executions x WHERE x.migration_id = w.id))`,
				dbID, head.Version, m.Version)
			if err != nil {
				return 0, err
			}
			if hasDraft {
				return 0, ErrInvalidOrder
			}
		} else {
			op = "forward"
			blocked, err := hasBlockedRolledBackBetween(ctx, tx, dbID, head.Version, m.Version)
			if err != nil {
				return 0, err
			}
			if blocked {
				return 0, ErrInvalidOrder
			}
		}
		return createExecution(ctx, tx, dbID, m.ID, m.Version, op, m.SQLUp, nil, nil, userID)
	})
}

func Rollback(ctx context.Context, pool *pgxpool.Pool, dbID, mid, userID int64) (int64, error) {
	return withDatabaseTx(ctx, pool, dbID, func(tx pgx.Tx) (int64, error) {
		m, err := lockMigration(ctx, tx, dbID, mid)
		if err != nil {
			return 0, err
		}
		if err := checkGroupLeaders(ctx, tx, dbID); err != nil {
			return 0, err
		}
		head, err := activeHead(ctx, tx, dbID)
		if err != nil {
			return 0, err
		}
		if head.ActiveID == 0 || m.ID != head.MigrationID || m.Version != head.Version {
			return 0, ErrInvalidOrder
		}
		if !isReversible(m) {
			return 0, ErrIrreversible
		}
		if unresolved, err := hasUnresolved(ctx, tx, dbID); err != nil || unresolved {
			if err != nil {
				return 0, err
			}
			return 0, ErrInvalidState
		}
		return createExecution(ctx, tx, dbID, m.ID, m.Version, "rollback", m.SQLDown, &head.ActiveID, &head.RootID, userID)
	})
}

func submitRecovery(ctx context.Context, pool *pgxpool.Pool, dbID, eid, userID int64, op string) (int64, error) {
	return withDatabaseTx(ctx, pool, dbID, func(tx pgx.Tx) (int64, error) {
		var mid, rootID int64
		var version int
		var status string
		var targetReverted *int64
		err := tx.QueryRow(ctx, `SELECT migration_id, version, status, reverted_execution_id, COALESCE(parent_execution_id, request_id, id)
			FROM migration_executions WHERE database_id = $1 AND id = $2`, dbID, eid).
			Scan(&mid, &version, &status, &targetReverted, &rootID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrExecNotFound
		}
		if err != nil {
			return 0, err
		}
		if err := checkGroupLeaders(ctx, tx, dbID); err != nil {
			return 0, err
		}
		if op == "retry" && status != "partial" {
			return 0, ErrInvalidState
		}
		unresolved, err := exists(ctx, tx, `SELECT EXISTS (SELECT 1 FROM (`+unresolvedRowsSQL+`) u WHERE u.id = $2)`, dbID, eid)
		if err != nil {
			return 0, err
		}
		if !unresolved {
			return 0, ErrInvalidState
		}
		m, err := lockMigration(ctx, tx, dbID, mid)
		if err != nil {
			return 0, err
		}
		sqlSnapshot := m.SQLUp
		var revertedID *int64
		if op == "compensate" {
			if !isReversible(m) {
				return 0, ErrIrreversible
			}
			sqlSnapshot = m.SQLDown
			revertedID = targetReverted
		}
		return createExecution(ctx, tx, dbID, mid, version, op, sqlSnapshot, revertedID, &rootID, userID)
	})
}

func Approve(ctx context.Context, pool *pgxpool.Pool, dbID, eid, userID int64, decision string) error {
	if decision != "approved" && decision != "rejected" {
		return ErrInvalidDecision
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var reqID int64
	var status string
	var isLeader bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(e.request_id, e.id), e.status,
			EXISTS (SELECT 1 FROM migration_approvals ap JOIN platform_groups g ON g.id = ap.group_id
				WHERE ap.execution_id = COALESCE(e.request_id, e.id) AND g.leader_id = $3)
		FROM migration_executions e WHERE e.database_id = $1 AND e.id = $2 FOR UPDATE`, dbID, eid, userID).
		Scan(&reqID, &status, &isLeader)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExecNotFound
	}
	if err != nil {
		return err
	}
	if !isLeader {
		return ErrNotLeader
	}
	if status != "pending_review" {
		return ErrInvalidState
	}

	tag, err := tx.Exec(ctx, `UPDATE migration_approvals ap
		SET status = $3, decided_by = $2, decided_at = now()
		FROM platform_groups g
		WHERE ap.execution_id = $1 AND ap.group_id = g.id AND g.leader_id = $2 AND ap.status = 'pending'`,
		reqID, userID, decision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}

	if decision == "rejected" {
		_, err = tx.Exec(ctx, `UPDATE migration_executions SET status = 'rejected', finished_at = now() WHERE id = $1`, eid)
	} else {
		_, err = tx.Exec(ctx, `UPDATE migration_executions SET status = 'approved'
			WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM migration_approvals WHERE execution_id = $2 AND status <> 'approved')`, eid, reqID)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func Cancel(ctx context.Context, pool *pgxpool.Pool, dbID, eid int64) error {
	var status string
	err := pool.QueryRow(ctx, `WITH old AS (
			SELECT id, status FROM migration_executions WHERE database_id = $1 AND id = $2 FOR UPDATE
		), upd AS (
			UPDATE migration_executions SET status = 'rejected', finished_at = now()
			WHERE id IN (SELECT id FROM old WHERE status IN ('pending_review', 'approved'))
		)
		SELECT status FROM old`, dbID, eid).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExecNotFound
	}
	if err != nil {
		return err
	}
	if status != "pending_review" && status != "approved" {
		return ErrInvalidState
	}
	return nil
}

func Enqueue(ctx context.Context, pool *pgxpool.Pool, dbID, eid int64) (int64, error) {
	return withDatabaseTx(ctx, pool, dbID, func(tx pgx.Tx) (int64, error) {
		var version int
		var op string
		err := tx.QueryRow(ctx, `SELECT version, operation FROM migration_executions
			WHERE database_id = $1 AND id = $2`, dbID, eid).Scan(&version, &op)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrExecNotFound
		}
		if err != nil {
			return 0, err
		}
		if op == "up" || op == "forward" {
			head, err := activeHead(ctx, tx, dbID)
			if err != nil {
				return 0, err
			}
			unqueued, err := exists(ctx, tx, `SELECT EXISTS (SELECT 1 FROM migration_executions
				WHERE database_id = $1 AND version > $2 AND version < $3 AND status IN ('pending_review', 'approved'))`,
				dbID, head.Version, version)
			if err != nil {
				return 0, err
			}
			if unqueued {
				return 0, ErrInvalidOrder
			}
		}
		jobID, err := queue.Enqueue(ctx, tx, dbID, eid, telemetry.TraceParent(ctx))
		if errors.Is(err, queue.ErrNotApproved) {
			return 0, ErrInvalidState
		}
		return jobID, err
	})
}

func migrationActions(ctx context.Context, q httpx.Queryer, dbID int64, m Migration) (canRollback, canForward bool, err error) {
	if inFlight, err := exists(ctx, q, `SELECT EXISTS (SELECT 1 FROM migration_executions
		WHERE migration_id = $1 AND status IN ('pending_review', 'approved', 'pending', 'running'))`, m.ID); err != nil || inFlight {
		return false, false, err
	}
	if unresolved, err := hasUnresolved(ctx, q, dbID); err != nil || unresolved {
		return false, false, err
	}
	head, err := activeHead(ctx, q, dbID)
	if err != nil {
		return false, false, err
	}
	if head.ActiveID > 0 && m.ID == head.MigrationID && m.Version == head.Version && isReversible(m) {
		canRollback = true
	}
	if m.Version > head.Version {
		if ok, err := everSucceeded(ctx, q, m.ID); err != nil {
			return false, false, err
		} else if ok {
			blocked, err := hasBlockedRolledBackBetween(ctx, q, dbID, head.Version, m.Version)
			if err != nil {
				return false, false, err
			}
			canForward = !blocked
		}
	}
	return canRollback, canForward, nil
}

func withDatabaseTx[T any](ctx context.Context, pool *pgxpool.Pool, dbID int64, fn func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var locked int64
	if err := tx.QueryRow(ctx, `SELECT id FROM managed_databases WHERE id = $1 FOR UPDATE`, dbID).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrNotFound
	} else if err != nil {
		return zero, err
	}
	v, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return v, nil
}

func exists(ctx context.Context, q httpx.Queryer, sql string, args ...any) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, sql, args...).Scan(&ok)
	return ok, err
}

func checkGroupLeaders(ctx context.Context, q httpx.Queryer, dbID int64) error {
	var total, missing int
	err := q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE g.leader_id IS NULL)
		FROM group_database_access a JOIN platform_groups g ON g.id = a.group_id
		WHERE a.database_id = $1`, dbID).Scan(&total, &missing)
	if err != nil {
		return err
	}
	if total == 0 || missing > 0 {
		return ErrMissingLeader
	}
	return nil
}

func lockMigration(ctx context.Context, q httpx.Queryer, dbID, mid int64) (Migration, error) {
	var m Migration
	err := q.QueryRow(ctx, `SELECT id, version, sql, sql_down, irreversible_reason
		FROM migrations WHERE database_id = $1 AND id = $2 FOR UPDATE`, dbID, mid).
		Scan(&m.ID, &m.Version, &m.SQLUp, &m.SQLDown, &m.IrreversibleReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	return m, err
}

func activeHead(ctx context.Context, q httpx.Queryer, dbID int64) (headState, error) {
	var h headState
	err := q.QueryRow(ctx, `WITH active AS (`+activeRowsSQL+`)
		SELECT version, id, migration_id, root_id FROM active ORDER BY version DESC, id DESC LIMIT 1`, dbID).
		Scan(&h.Version, &h.ActiveID, &h.MigrationID, &h.RootID)
	if errors.Is(err, pgx.ErrNoRows) {
		return headState{}, nil
	}
	return h, err
}

func everSucceeded(ctx context.Context, q httpx.Queryer, mid int64) (bool, error) {
	return exists(ctx, q, `SELECT EXISTS (SELECT 1 FROM migration_executions
		WHERE migration_id = $1 AND operation IN ('up', 'forward', 'retry') AND status = 'success')`, mid)
}

func hasUnresolved(ctx context.Context, q httpx.Queryer, dbID int64) (bool, error) {
	return exists(ctx, q, `SELECT EXISTS (SELECT 1 FROM (`+unresolvedRowsSQL+`) u)`, dbID)
}

func hasBlockedRolledBackBetween(ctx context.Context, q httpx.Queryer, dbID int64, headVersion, targetVersion int) (bool, error) {
	return exists(ctx, q, `WITH active AS (`+activeRowsSQL+`)
		SELECT EXISTS (
			SELECT 1 FROM migrations w
			WHERE w.database_id = $1 AND w.version > $2 AND w.version < $3
				AND EXISTS (SELECT 1 FROM migration_executions e
					WHERE e.migration_id = w.id AND e.operation IN ('up', 'forward', 'retry') AND e.status = 'success')
				AND NOT EXISTS (SELECT 1 FROM active a WHERE a.migration_id = w.id)
				AND NOT EXISTS (SELECT 1 FROM migration_executions f
					WHERE f.migration_id = w.id AND f.operation = 'forward'
						AND f.status IN ('pending_review', 'approved', 'pending', 'running'))
		)`, dbID, headVersion, targetVersion)
}

func createExecution(ctx context.Context, tx pgx.Tx, dbID, mid int64, version int, op, sqlSnapshot string, revertedID, parentID *int64, userID int64) (int64, error) {
	var eid int64
	err := tx.QueryRow(ctx, `WITH e AS (
			INSERT INTO migration_executions
				(migration_id, database_id, version, operation, sql_snapshot, status, reverted_execution_id, parent_execution_id, requested_by)
			VALUES ($1, $2, $3, $4, $5, 'pending_review', $6, $7, $8)
			RETURNING id
		), a AS (
			INSERT INTO migration_approvals (execution_id, group_id)
			SELECT e.id, group_id FROM e, group_database_access WHERE database_id = $2
		)
		SELECT id FROM e`,
		mid, dbID, version, op, sqlSnapshot, revertedID, parentID, userID).Scan(&eid)
	if httpx.IsPGCode(err, "23505") {
		return 0, ErrInFlight
	}
	return eid, err
}

func isReversible(m Migration) bool {
	return m.IrreversibleReason == "" && strings.TrimSpace(m.SQLDown) != ""
}
