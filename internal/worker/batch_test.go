package worker

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SQL batch sesuai kontrak: $1 = last_cursor, $2 = batch_size, balikin cursor terurut.
// val = 100 / divisor supaya baris dengan divisor 0 bisa dipakai memicu error di tengah.
const batchSQL = `WITH batch AS (
    SELECT id FROM items
    WHERE ($1::bigint IS NULL OR id > $1::bigint)
    ORDER BY id LIMIT $2
), upd AS (
    UPDATE items SET hits = hits + 1, val = 100 / items.divisor
    FROM batch WHERE items.id = batch.id
    RETURNING batch.id
)
SELECT id::text AS last_cursor FROM upd ORDER BY id`

// tabel items berisi n baris di database target
func seedItems(t *testing.T, tp *pgxpool.Pool, n int) {
	t.Helper()
	if _, err := tp.Exec(context.Background(), `CREATE TABLE items (id bigint PRIMARY KEY, hits int NOT NULL DEFAULT 0, val int, divisor int NOT NULL DEFAULT 1);
		INSERT INTO items (id) SELECT generate_series(1, `+strconv.Itoa(n)+`)`); err != nil {
		t.Fatal(err)
	}
}

func enqueueBatch(t *testing.T, pool *pgxpool.Pool, dbID int64, op string, size int) (jobID, execID int64) {
	t.Helper()
	jobID, execID = enqueueSQL(t, pool, dbID, 1, op, batchSQL)
	if _, err := pool.Exec(context.Background(), `UPDATE migrations SET batch_size = $2, batch_column = 'id'
		WHERE id = (SELECT migration_id FROM migration_executions WHERE id = $1)`, execID, size); err != nil {
		t.Fatal(err)
	}
	return jobID, execID
}

func lastCursor(t *testing.T, pool *pgxpool.Pool, execID int64) string {
	t.Helper()
	var c string
	pool.QueryRow(context.Background(), `SELECT last_cursor FROM migration_executions WHERE id = $1`, execID).Scan(&c)
	return c
}

// jumlah baris items per nilai hits, untuk memastikan tidak ada baris yang diproses dua kali
func hits(t *testing.T, tp *pgxpool.Pool) map[int]int {
	t.Helper()
	rows, err := tp.Query(context.Background(), `SELECT hits, count(*) FROM items GROUP BY hits`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var h, n int
		rows.Scan(&h, &n)
		out[h] = n
	}
	return out
}

func TestBatchCompletes(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	seedItems(t, tp, 25)
	jobID, execID := enqueueBatch(t, pool, dbID, "up", 10)
	execute(t, pool)

	r := execState(t, pool, execID)
	if r.status != "success" || r.batches != 3 || r.rows != 25 {
		t.Fatalf("execution = %+v, want success 3 batch 25 row", r)
	}
	if j, _ := statuses(t, pool, jobID); j != "done" {
		t.Fatalf("job = %s", j)
	}
	if c := lastCursor(t, pool, execID); c != "25" {
		t.Fatalf("last_cursor = %q, want 25", c)
	}
	if h := hits(t, tp); h[1] != 25 {
		t.Fatalf("setiap baris harus diproses tepat sekali: %v", h)
	}
	if n := checkpoints(t, tp, execID); n != 3 {
		t.Fatalf("checkpoint = %d, want 3 (tanpa checkpoint kosong)", n)
	}
}

func TestBatchFailureIsPartialThenRetryResumes(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	seedItems(t, tp, 25)
	ctx := context.Background()
	tp.Exec(ctx, `UPDATE items SET divisor = 0 WHERE id = 15`) // batch kedua (11-20) akan gagal

	_, rootID := enqueueBatch(t, pool, dbID, "up", 10)
	execute(t, pool)
	r := execState(t, pool, rootID)
	if r.status != "partial" || r.batches != 1 || r.rows != 10 || r.error == "" {
		t.Fatalf("execution = %+v, want partial 1 batch 10 row", r)
	}
	// batch 1 tetap committed, batch 2 di-rollback seluruhnya
	if h := hits(t, tp); h[1] != 10 || h[0] != 15 {
		t.Fatalf("hits setelah partial = %v", h)
	}

	// perbaiki data lalu retry: lanjut dari checkpoint root tanpa mengulang batch 1
	tp.Exec(ctx, `UPDATE items SET divisor = 1 WHERE id = 15`)
	pool.Exec(ctx, `UPDATE migration_jobs SET status = 'failed' WHERE execution_id = $1`, rootID)
	_, retryID := enqueueBatch(t, pool, dbID, "retry", 10)
	pool.Exec(ctx, `UPDATE migration_executions SET parent_execution_id = $2 WHERE id = $1`, retryID, rootID)
	execute(t, pool)

	r = execState(t, pool, retryID)
	if r.status != "success" || r.batches != 3 || r.rows != 25 {
		t.Fatalf("retry = %+v, want success dengan total 3 batch 25 row", r)
	}
	if h := hits(t, tp); h[1] != 25 {
		t.Fatalf("batch yang sudah commit tidak boleh diulang: %v", h)
	}
	if n := checkpoints(t, tp, rootID); n != 3 {
		t.Fatalf("checkpoint root = %d, want 3", n)
	}
}

func TestBatchFirstBatchFailureIsFailed(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	seedItems(t, tp, 5)
	tp.Exec(context.Background(), `UPDATE items SET divisor = 0 WHERE id = 2`)
	_, execID := enqueueBatch(t, pool, dbID, "up", 10)
	execute(t, pool)
	if r := execState(t, pool, execID); r.status != "failed" || r.batches != 0 {
		t.Fatalf("gagal di batch pertama harus failed (bukan partial): %+v", r)
	}
}
