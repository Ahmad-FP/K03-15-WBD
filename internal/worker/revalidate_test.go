package worker

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func addMigration(t *testing.T, pool *pgxpool.Pool, dbID int64, version int) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `INSERT INTO migrations (database_id, version, name, sql) VALUES ($1, $2, $3, 'SELECT 1') RETURNING id`,
		dbID, version, "m"+rand.Text()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// satu baris migration_executions dengan relasi opsional (0 = NULL)
type row struct {
	migration int64
	version   int
	op        string
	status    string
	parent    int64
	reverted  int64
}

func addExec(t *testing.T, pool *pgxpool.Pool, dbID int64, r row) int64 {
	t.Helper()
	nz := func(v int64) any {
		if v == 0 {
			return nil
		}
		return v
	}
	var id int64
	if err := pool.QueryRow(context.Background(), `INSERT INTO migration_executions
		(migration_id, database_id, version, operation, sql_snapshot, status, parent_execution_id, reverted_execution_id)
		VALUES ($1, $2, $3, $4, 'SELECT 1', $5, $6, $7) RETURNING id`,
		r.migration, dbID, r.version, r.op, r.status, nz(r.parent), nz(r.reverted)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// jalankan revalidasi untuk execution id seperti yang dilakukan worker
func verdictFor(t *testing.T, pool *pgxpool.Pool, dbID, execID int64) decision {
	t.Helper()
	ctx := context.Background()
	e, err := loadExecutionNoJob(ctx, pool, execID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := revalidate(ctx, pool, &Job{ExecutionID: execID, DatabaseID: dbID}, e)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// loadExecution butuh baris migration_jobs; test revalidasi tidak selalu punya job
func loadExecutionNoJob(ctx context.Context, pool *pgxpool.Pool, id int64) (execution, error) {
	var e execution
	err := pool.QueryRow(ctx, `SELECT e.id, COALESCE(e.request_id, e.id), COALESCE(e.parent_execution_id, e.request_id, e.id),
			e.migration_id, e.version, e.operation FROM migration_executions e WHERE e.id = $1`, id).
		Scan(&e.ID, &e.ReqID, &e.RootID, &e.MigrationID, &e.Version, &e.Operation)
	return e, err
}

func expect(t *testing.T, name string, got decision, want verdict) {
	t.Helper()
	if got.verdict != want {
		t.Errorf("%s: verdict = %d (%s), want %d", name, got.verdict, got.reason, want)
	}
}

func TestRevalidateApproval(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	dbID := addDatabase(t, pool, "a", []byte{0})
	m1 := addMigration(t, pool, dbID, 1)
	exec := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "up", status: "running"})

	var g1, g2 int64
	pool.QueryRow(ctx, `INSERT INTO platform_groups (name) VALUES ('g1') RETURNING id`).Scan(&g1)
	pool.QueryRow(ctx, `INSERT INTO platform_groups (name) VALUES ('g2') RETURNING id`).Scan(&g2)
	pool.Exec(ctx, `INSERT INTO group_database_access (group_id, database_id, access_level) VALUES ($1, $3, 'view_edit'), ($2, $3, 'view_only')`, g1, g2, dbID)
	pool.Exec(ctx, `INSERT INTO migration_approvals (execution_id, group_id, status) VALUES ($1, $2, 'approved')`, exec, g1)

	expect(t, "group view_only belum approve", verdictFor(t, pool, dbID, exec), reject)
	pool.Exec(ctx, `INSERT INTO migration_approvals (execution_id, group_id, status) VALUES ($1, $2, 'approved')`, exec, g2)
	expect(t, "semua group approve", verdictFor(t, pool, dbID, exec), proceed)
}

func TestRevalidateBlocking(t *testing.T) {
	pool := platform(t)
	dbID := addDatabase(t, pool, "a", []byte{0})
	m1, m2 := addMigration(t, pool, dbID, 1), addMigration(t, pool, dbID, 2)
	partial := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "up", status: "partial"})
	up2 := addExec(t, pool, dbID, row{migration: m2, version: 2, op: "up", status: "running"})
	expect(t, "partial belum selesai memblokir database", verdictFor(t, pool, dbID, up2), requeue)

	retry := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "retry", status: "running", parent: partial})
	expect(t, "retry untuk root partial itu sendiri boleh jalan", verdictFor(t, pool, dbID, retry), requeue) // up2 masih running
	pool.Exec(context.Background(), `UPDATE migration_executions SET status = 'pending' WHERE id = $1`, up2)
	expect(t, "retry untuk root partial itu sendiri boleh jalan", verdictFor(t, pool, dbID, retry), proceed)
}

func TestRevalidateUpOrdering(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	dbID := addDatabase(t, pool, "a", []byte{0})
	m1, m2, m3 := addMigration(t, pool, dbID, 1), addMigration(t, pool, dbID, 2), addMigration(t, pool, dbID, 3)
	addExec(t, pool, dbID, row{migration: m1, version: 1, op: "up", status: "success"})

	up3 := addExec(t, pool, dbID, row{migration: m3, version: 3, op: "up", status: "running"})
	expect(t, "v2 masih draft tanpa eksekusi", verdictFor(t, pool, dbID, up3), reject)

	up2 := addExec(t, pool, dbID, row{migration: m2, version: 2, op: "up", status: "approved"})
	expect(t, "v2 belum di-enqueue", verdictFor(t, pool, dbID, up3), reject)

	pool.Exec(ctx, `UPDATE migration_executions SET status = 'pending' WHERE id = $1`, up2)
	pool.Exec(ctx, `INSERT INTO migration_jobs (execution_id) VALUES ($1)`, up2)
	expect(t, "v2 antre di depan, v3 menunggu", verdictFor(t, pool, dbID, up3), requeue)

	pool.Exec(ctx, `UPDATE migration_executions SET status = 'success' WHERE id = $1`, up2)
	pool.Exec(ctx, `UPDATE migration_jobs SET status = 'done' WHERE execution_id = $1`, up2)
	expect(t, "v2 sudah sukses, giliran v3", verdictFor(t, pool, dbID, up3), proceed)

	pool.Exec(ctx, `UPDATE migration_executions SET status = 'success' WHERE id = $1`, up3)
	again := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "up", status: "running"})
	expect(t, "v1 sudah pernah sukses", verdictFor(t, pool, dbID, again), reject)
}

func TestRevalidateRollbackAndForward(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	dbID := addDatabase(t, pool, "a", []byte{0})
	m1, m2 := addMigration(t, pool, dbID, 1), addMigration(t, pool, dbID, 2)
	up1 := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "up", status: "success"})
	up2 := addExec(t, pool, dbID, row{migration: m2, version: 2, op: "up", status: "success"})

	rb1 := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "rollback", status: "running", reverted: up1})
	expect(t, "rollback v1 saat head v2", verdictFor(t, pool, dbID, rb1), reject)
	pool.Exec(ctx, `UPDATE migration_executions SET status = 'failed' WHERE id = $1`, rb1)

	rb2 := addExec(t, pool, dbID, row{migration: m2, version: 2, op: "rollback", status: "running", reverted: up2, parent: up2})
	expect(t, "rollback head v2", verdictFor(t, pool, dbID, rb2), proceed)
	pool.Exec(ctx, `UPDATE migration_executions SET status = 'success' WHERE id = $1`, rb2)

	fw2 := addExec(t, pool, dbID, row{migration: m2, version: 2, op: "forward", status: "running"})
	expect(t, "forward v2 yang sudah di-rollback", verdictFor(t, pool, dbID, fw2), proceed)

	pool.Exec(ctx, `UPDATE migration_executions SET status = 'failed' WHERE id = $1`, fw2)
	fw1 := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "forward", status: "running"})
	expect(t, "forward v1 yang masih aktif", verdictFor(t, pool, dbID, fw1), reject)

	// compensate dengan reverted_execution_id != active.id harus ditolak
	pool.Exec(ctx, `UPDATE migration_executions SET status = 'failed' WHERE id = $1`, fw1)
	pr := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "rollback", status: "partially_reverted", parent: up1, reverted: up1})
	staleComp := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "compensate", status: "running", parent: up1, reverted: up2})
	expect(t, "compensate dengan reverted_execution_id bukan head aktif", verdictFor(t, pool, dbID, staleComp), reject)
	pool.Exec(ctx, `UPDATE migration_executions SET status = 'failed' WHERE id = $1`, staleComp)
	goodComp := addExec(t, pool, dbID, row{migration: m1, version: 1, op: "compensate", status: "running", parent: up1, reverted: up1})
	expect(t, "compensate dengan reverted_execution_id == head aktif", verdictFor(t, pool, dbID, goodComp), proceed)
	_ = pr
}

func TestRequeueAndRejectThroughExecutor(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	dbID := addDatabase(t, pool, "a", []byte{0})
	addMigration(t, pool, dbID, 1) // v1 draft tanpa eksekusi
	jobID, execID := enqueueSQL(t, pool, dbID, 2, "up", "SELECT 1")
	execute(t, pool)
	if r := execState(t, pool, execID); r.status != "failed" || r.error == "" {
		t.Fatalf("pengajuan yang melompati draft harus ditolak: %+v", r)
	}
	if j, _ := statuses(t, pool, jobID); j != "failed" {
		t.Fatalf("job = %s, want failed", j)
	}

	// partial di database lain memblokir: job dikembalikan ke antrean, bukan dijalankan
	db2 := addDatabase(t, pool, "b", []byte{0})
	m := addMigration(t, pool, db2, 1)
	addExec(t, pool, db2, row{migration: m, version: 1, op: "up", status: "partial"})
	jobID, _ = enqueueSQL(t, pool, db2, 2, "up", "SELECT 1")
	job, _ := Claim(ctx, pool)
	(&Executor{Pool: pool}).Handle(ctx, job)
	if j, e := statuses(t, pool, jobID); j != "pending" || e != "pending" {
		t.Fatalf("job yang terblokir harus di-requeue, dapat %s/%s", j, e)
	}
}
