package worker

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/queue"
	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
)

// SQL batch untuk benchmark
const benchBatchSQL = `WITH batch AS (
    SELECT id FROM items
    WHERE ($1::bigint IS NULL OR id > $1::bigint)
    ORDER BY id LIMIT $2
), upd AS (
    UPDATE items SET status = 'migrated'
    FROM batch WHERE items.id = batch.id
    RETURNING batch.id
)
SELECT id::text AS last_cursor FROM upd ORDER BY id`

// Benchmark wajib spek: UPDATE semua baris tanpa batch harus > 1 detik, lalu dataset yang sama
// dipakai untuk migration mode batch lewat worker.
//
//	WBD_BENCH_ROWS=2000000 WBD_BENCH_BATCH=50000 TEST_DATABASE_URL=... go test ./internal/worker -run TestLargeDatasetBenchmark -v -timeout 30m
func TestLargeDatasetBenchmark(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("WBD_BENCH_ROWS"))
	if n <= 0 {
		t.Skip("WBD_BENCH_ROWS tidak diset")
	}
	size, _ := strconv.Atoi(os.Getenv("WBD_BENCH_BATCH"))
	if size <= 0 {
		size = 50000
	}
	pool := platform(t)
	tp, dbID := target(t, pool)
	ctx := context.Background()

	start := time.Now()
	if _, err := tp.Exec(ctx, `CREATE TABLE items (id bigint PRIMARY KEY, status text NOT NULL DEFAULT 'new', payload text NOT NULL);
		INSERT INTO items (id, payload) SELECT g, md5(g::text) FROM generate_series(1, `+strconv.Itoa(n)+`) g;
		ANALYZE items`); err != nil {
		t.Fatal(err)
	}
	t.Logf("dataset: %d baris dibuat dalam %v", n, time.Since(start).Round(time.Millisecond))

	// UPDATE satu query tanpa batch, di dalam transaksi yang di-rollback supaya dataset tetap sama
	tx, err := tp.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	tag, err := tx.Exec(ctx, `UPDATE items SET status = 'migrated'`)
	plain := time.Since(start)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tanpa batch: UPDATE %d baris dalam %v", tag.RowsAffected(), plain.Round(time.Millisecond))
	if plain < time.Second {
		t.Errorf("UPDATE tanpa batch hanya %v; naikkan WBD_BENCH_ROWS sampai > 1 detik", plain)
	}

	// dataset yang sama lewat worker mode batch
	_, execID := enqueueSQL(t, pool, dbID, 1, "up", benchBatchSQL)
	pool.Exec(ctx, `UPDATE migrations SET batch_size = $2, batch_column = 'id' WHERE id = (SELECT migration_id FROM migration_executions WHERE id = $1)`, execID, size)
	start = time.Now()
	execute(t, pool)
	batched := time.Since(start)
	r := execState(t, pool, execID)
	var migrated int
	tp.QueryRow(ctx, `SELECT count(*) FROM items WHERE status = 'migrated'`).Scan(&migrated)
	t.Logf("mode batch: %d baris dalam %d batch (ukuran %d) selama %v, status %s", r.rows, r.batches, size, batched.Round(time.Millisecond), r.status)
	if r.status != "success" || migrated != n {
		t.Fatalf("migration batch tidak selesai: %+v, migrated=%d", r, migrated)
	}
}

// Contoh output log JSON dan span OTel dari alur sungguhan API → queue → worker → lock → batch/query.
//
//	WBD_SAMPLE_DIR=docs/samples TEST_DATABASE_URL=... go test ./internal/worker -run TestSampleTelemetry -v
func TestSampleTelemetry(t *testing.T) {
	dir := os.Getenv("WBD_SAMPLE_DIR")
	if dir == "" {
		t.Skip("WBD_SAMPLE_DIR tidak diset")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logs, err := os.Create(filepath.Join(dir, "worker-logs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	spans, err := os.Create(filepath.Join(dir, "worker-spans.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer spans.Close()

	pool := platform(t)
	tp, dbID := target(t, pool)
	seedItems(t, tp, 25)
	ctx := context.Background()

	prev := slog.Default()
	slog.SetDefault(telemetry.NewLogger(logs))
	defer slog.SetDefault(prev)
	shutdown, err := telemetry.Init("worker", spans)
	if err != nil {
		t.Fatal(err)
	}
	var user int64
	pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ('ani', 'x') RETURNING id`).Scan(&user)

	// pengajuan batch yang sudah approved, di-enqueue dari dalam span request API
	_, execID := enqueueBatch(t, pool, dbID, "up", 10)
	pool.Exec(ctx, `DELETE FROM migration_jobs WHERE execution_id = $1`, execID)
	pool.Exec(ctx, `UPDATE migration_executions SET status = 'approved', requested_by = $2 WHERE id = $1`, execID, user)
	apiCtx, apiSpan := telemetry.Tracer().Start(ctx, "POST /api/databases/{id}/executions/{eid}/enqueue")
	tx, _ := pool.Begin(apiCtx)
	if _, err := queue.Enqueue(apiCtx, tx, dbID, execID, telemetry.TraceParent(apiCtx)); err != nil {
		t.Fatal(err)
	}
	tx.Commit(apiCtx)
	apiSpan.End()

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	w := &Worker{Pool: pool, Poll: 50 * time.Millisecond, Handle: func(ctx context.Context, job *Job) {
		(&Executor{Pool: pool}).Handle(ctx, job)
		cancel()
	}}
	w.Run(runCtx)
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if r := execState(t, pool, execID); r.status != "success" {
		t.Fatalf("contoh alur tidak sukses: %+v", r)
	}
}
