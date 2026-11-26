package worker

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SQL down batch sesuai kontrak: $1 = prev_cursor (eksklusif, NULL di batch 1), $2 = last_cursor (inklusif)
const batchDownSQL = `UPDATE items SET hits = hits - 1, val = 100 / items.divisor
WHERE ($1::bigint IS NULL OR id > $1::bigint) AND id <= $2::bigint`

// ajukan rollback/compensate yang membatalkan root, dengan relasi seperti yang diisi API
func enqueueReverse(t *testing.T, pool *pgxpool.Pool, dbID int64, op string, root, reverted int64) int64 {
	t.Helper()
	_, id := enqueueSQL(t, pool, dbID, 1, op, batchDownSQL)
	var rev any
	if reverted != 0 {
		rev = reverted
	}
	if _, err := pool.Exec(context.Background(), `UPDATE migration_executions SET parent_execution_id = $2, reverted_execution_id = $3 WHERE id = $1`,
		id, root, rev); err != nil {
		t.Fatal(err)
	}
	return id
}

// up batch 25 baris (3 batch) sampai sukses, balikin id execution-nya (= root checkpoint)
func batchUp(t *testing.T, pool, tp *pgxpool.Pool, dbID int64) int64 {
	t.Helper()
	seedItems(t, tp, 25)
	_, upID := enqueueBatch(t, pool, dbID, "up", 10)
	execute(t, pool)
	if r := execState(t, pool, upID); r.status != "success" || r.batches != 3 {
		t.Fatalf("persiapan up batch gagal: %+v", r)
	}
	return upID
}

func head(t *testing.T, pool *pgxpool.Pool, dbID int64) int {
	t.Helper()
	h, err := activeHead(context.Background(), pool, dbID)
	if err != nil {
		t.Fatal(err)
	}
	return h.version
}

func TestBatchRollback(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	upID := batchUp(t, pool, tp, dbID)

	rbID := enqueueReverse(t, pool, dbID, "rollback", upID, upID)
	execute(t, pool)

	if r := execState(t, pool, rbID); r.status != "success" || r.batches != 3 || r.rows != 25 {
		t.Fatalf("rollback = %+v, want success 3 batch 25 row", r)
	}
	if h := hits(t, tp); h[0] != 25 {
		t.Fatalf("semua baris harus kembali seperti semula: %v", h)
	}
	if n := checkpoints(t, tp, upID); n != 0 {
		t.Fatalf("checkpoint tersisa %d, harus terhapus per batch", n)
	}
	if v := head(t, pool, dbID); v != 0 {
		t.Fatalf("versi aktif setelah rollback = v%d, want v0", v)
	}
}

func TestBatchRollbackPartiallyRevertedThenCompensate(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	upID := batchUp(t, pool, tp, dbID)
	ctx := context.Background()

	// batch 2 (11-20) gagal dibatalkan; urutan terbalik berarti batch 3 sudah dibatalkan duluan
	tp.Exec(ctx, `UPDATE items SET divisor = 0 WHERE id = 15`)
	rbID := enqueueReverse(t, pool, dbID, "rollback", upID, upID)
	execute(t, pool)
	if r := execState(t, pool, rbID); r.status != "partially_reverted" || r.batches != 1 || r.rows != 5 {
		t.Fatalf("rollback = %+v, want partially_reverted setelah 1 batch", r)
	}
	if n := checkpoints(t, tp, upID); n != 2 {
		t.Fatalf("sisa checkpoint = %d, want 2 (batch 1 dan 2)", n)
	}
	if h := hits(t, tp); h[0] != 5 || h[1] != 20 {
		t.Fatalf("hanya batch 3 (21-25) yang boleh sudah dibatalkan: %v", h)
	}

	// perbaiki data lalu compensate: lanjutkan dari sisa checkpoint
	tp.Exec(ctx, `UPDATE items SET divisor = 1 WHERE id = 15`)
	compID := enqueueReverse(t, pool, dbID, "compensate", upID, upID)
	execute(t, pool)
	if r := execState(t, pool, compID); r.status != "success" || r.batches != 2 {
		t.Fatalf("compensate = %+v, want success 2 batch", r)
	}
	if h := hits(t, tp); h[0] != 25 {
		t.Fatalf("semua baris harus sudah dibatalkan: %v", h)
	}
	if n := checkpoints(t, tp, upID); n != 0 || head(t, pool, dbID) != 0 {
		t.Fatal("checkpoint harus habis dan v1 tidak aktif lagi")
	}
}

func TestCompensatePartialUp(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	seedItems(t, tp, 25)
	ctx := context.Background()
	tp.Exec(ctx, `UPDATE items SET divisor = 0 WHERE id = 15`)
	_, upID := enqueueBatch(t, pool, dbID, "up", 10)
	execute(t, pool)
	if r := execState(t, pool, upID); r.status != "partial" {
		t.Fatalf("persiapan: up harus partial, dapat %+v", r)
	}
	pool.Exec(ctx, `UPDATE migration_jobs SET status = 'failed' WHERE execution_id = $1`, upID)

	// compensate partial up: batalkan batch yang sudah commit (reverted_execution_id NULL)
	compID := enqueueReverse(t, pool, dbID, "compensate", upID, 0)
	execute(t, pool)
	if r := execState(t, pool, compID); r.status != "success" || r.batches != 1 {
		t.Fatalf("compensate = %+v", r)
	}
	if h := hits(t, tp); h[0] != 25 || checkpoints(t, tp, upID) != 0 {
		t.Fatalf("batch yang sudah commit harus dibatalkan: %v", h)
	}
	var unresolved int
	pool.QueryRow(ctx, `SELECT count(*) FROM (`+unresolvedRows+`) u`, dbID).Scan(&unresolved)
	if unresolved != 0 {
		t.Fatal("partial harus dianggap selesai setelah compensate sukses, database tidak boleh terblokir")
	}
}

func TestCompensateNormalModeRejected(t *testing.T) {
	pool := platform(t)
	_, dbID := target(t, pool)
	m := addMigration(t, pool, dbID, 1)
	root := addExec(t, pool, dbID, row{migration: m, version: 1, op: "up", status: "partial"})
	_, compID := enqueueSQL(t, pool, dbID, 1, "compensate", "SELECT 1")
	pool.Exec(context.Background(), `UPDATE migration_executions SET parent_execution_id = $2 WHERE id = $1`, compID, root)
	execute(t, pool)
	if r := execState(t, pool, compID); r.status != "failed" {
		t.Fatalf("compensate di mode normal harus ditolak: %+v", r)
	}
}

func TestReverseBatchAutoRetryPreservesPartiallyReverted(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	upID := batchUp(t, pool, tp, dbID)
	ctx := context.Background()

	// Simulasi Attempt 1 rollback sudah revert 1 batch (batch 3) lalu gagal transient dan membuat Attempt 2
	tp.Exec(ctx, `UPDATE items SET hits = hits - 1 WHERE id > 20`)
	tp.Exec(ctx, `DELETE FROM _wbd_checkpoints WHERE execution_id = $1 AND batch_no = 3`, upID)
	tp.Exec(ctx, `UPDATE items SET divisor = 0 WHERE id = 15`) // buat batch 2 gagal non-transient

	rb1 := enqueueReverse(t, pool, dbID, "rollback", upID, upID)
	pool.Exec(ctx, `UPDATE migration_executions SET status = 'failed', batches_committed = 1, rows_processed = 5, last_cursor = '20' WHERE id = $1`, rb1)
	var rb2 int64
	if err := pool.QueryRow(ctx, `INSERT INTO migration_executions
		(request_id, migration_id, database_id, version, operation, attempt, sql_snapshot, status,
		 reverted_execution_id, parent_execution_id, batches_committed, rows_processed, last_cursor)
		SELECT id, migration_id, database_id, version, operation, 2, sql_snapshot, 'pending',
		 reverted_execution_id, parent_execution_id, 1, 5, '20'
		FROM migration_executions WHERE id = $1 RETURNING id`, rb1).Scan(&rb2); err != nil {
		t.Fatal(err)
	}
	pool.Exec(ctx, `UPDATE migration_jobs SET execution_id = $2, status = 'pending', run_after = now() WHERE execution_id = $1`, rb1, rb2)

	execute(t, pool)
	r := execState(t, pool, rb2)
	if r.status != "partially_reverted" || r.batches != 1 || r.rows != 5 {
		t.Fatalf("attempt 2 reverse batch harus tetap partially_reverted dengan progres 1/5, dapat %+v", r)
	}
	var cursor string
	pool.QueryRow(ctx, `SELECT last_cursor FROM migration_executions WHERE id = $1`, rb2).Scan(&cursor)
	if cursor != "20" {
		t.Fatalf("last_cursor = %q, want 20", cursor)
	}
}
