package queue

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrNotApproved = errors.New("execution belum berstatus approved")

func Enqueue(ctx context.Context, tx pgx.Tx, databaseID, executionID int64, traceParent string) (int64, error) {
	tag, err := tx.Exec(ctx,
		`UPDATE migration_executions SET status = 'pending' WHERE database_id = $1 AND id = $2 AND status = 'approved'`,
		databaseID, executionID,
	)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrNotApproved
	}
	var jobID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO migration_jobs (execution_id, traceparent) VALUES ($1, $2) RETURNING id`,
		executionID, traceParent,
	).Scan(&jobID)
	return jobID, err
}
