package worker

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// database platform kosong di schema baru, skip kalau TEST_DATABASE_URL tidak diset
func platform(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diset")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "wrk_" + strings.ToLower(rand.Text())
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema+";"+db.Schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	})
	return pool
}

func addDatabase(t *testing.T, pool *pgxpool.Pool, name string, dsn []byte) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO managed_databases (name, encrypted_dsn) VALUES ($1, $2) RETURNING id`, name, dsn).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// migration + execution berstatus pending + job di antrean, balikin id job dan execution
func enqueue(t *testing.T, pool *pgxpool.Pool, dbID int64, version int) (jobID, execID int64) {
	t.Helper()
	return enqueueSQL(t, pool, dbID, version, "up", "SELECT 1")
}

func enqueueSQL(t *testing.T, pool *pgxpool.Pool, dbID int64, version int, op, sql string) (jobID, execID int64) {
	t.Helper()
	ctx := context.Background()
	var mid int64
	if err := pool.QueryRow(ctx, `INSERT INTO migrations (database_id, version, name, sql) VALUES ($1, $2, $3, $4)
		ON CONFLICT (database_id, version) DO UPDATE SET name = migrations.name RETURNING id`,
		dbID, version, "m"+rand.Text(), sql).Scan(&mid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status)
		VALUES ($1, $2, $3, $4, $5, 'pending') RETURNING id`, mid, dbID, version, op, sql).Scan(&execID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO migration_jobs (execution_id) VALUES ($1) RETURNING id`, execID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	return jobID, execID
}

func statuses(t *testing.T, pool *pgxpool.Pool, jobID int64) (job, exec string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT j.status, e.status FROM migration_jobs j
		JOIN migration_executions e ON e.id = j.execution_id WHERE j.id = $1`, jobID).Scan(&job, &exec); err != nil {
		t.Fatal(err)
	}
	return job, exec
}

func TestClaimLocksDatabase(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	dbA := addDatabase(t, pool, "a", []byte{0})
	dbB := addDatabase(t, pool, "b", []byte{0})
	jobA1, _ := enqueue(t, pool, dbA, 1)
	jobA2, _ := enqueue(t, pool, dbA, 2)
	jobB, _ := enqueue(t, pool, dbB, 1)

	first, err := Claim(ctx, pool)
	if err != nil || first == nil || first.ID != jobA1 {
		t.Fatalf("claim pertama = %+v, %v; want job %d", first, err, jobA1)
	}
	if j, e := statuses(t, pool, jobA1); j != "running" || e != "running" {
		t.Fatalf("status setelah claim = %s/%s", j, e)
	}

	// database A terkunci, jadi job A2 dilewati dan job database B yang diambil
	second, err := Claim(ctx, pool)
	if err != nil || second == nil || second.ID != jobB {
		t.Fatalf("claim kedua = %+v, %v; want job database lain %d", second, err, jobB)
	}
	if third, _ := Claim(ctx, pool); third != nil {
		t.Fatalf("claim ketiga harus kosong karena semua database sibuk, dapat job %d", third.ID)
	}

	first.Release(ctx)
	second.Release(ctx)
	next, err := Claim(ctx, pool)
	if err != nil || next == nil || next.ID != jobA2 {
		t.Fatalf("setelah lock dilepas, claim = %+v, %v; want job %d", next, err, jobA2)
	}
	next.Release(ctx)
}

func TestClaimRespectsRunAfter(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	jobID, _ := enqueue(t, pool, addDatabase(t, pool, "a", []byte{0}), 1)
	pool.Exec(ctx, `UPDATE migration_jobs SET run_after = now() + interval '1 hour' WHERE id = $1`, jobID)
	if job, err := Claim(ctx, pool); job != nil || err != nil {
		t.Fatalf("job dengan run_after di masa depan tidak boleh diambil: %+v, %v", job, err)
	}
}

func TestRequeue(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	jobID, _ := enqueue(t, pool, addDatabase(t, pool, "a", []byte{0}), 1)
	job, _ := Claim(ctx, pool)
	if err := job.Requeue(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var started *time.Time
	pool.QueryRow(ctx, `SELECT started_at FROM migration_executions e JOIN migration_jobs j ON j.execution_id = e.id WHERE j.id = $1`, jobID).Scan(&started)
	if j, e := statuses(t, pool, jobID); j != "pending" || e != "pending" || started != nil {
		t.Fatalf("requeue harus balik ke pending/pending tanpa started_at, dapat %s/%s %v", j, e, started)
	}
	again, err := Claim(ctx, pool)
	if err != nil || again == nil {
		t.Fatalf("job yang di-requeue harus bisa diambil lagi (lock sudah lepas): %v", err)
	}
	again.Release(ctx)
}

func TestRunProcessesQueue(t *testing.T) {
	pool := platform(t)
	jobA, _ := enqueue(t, pool, addDatabase(t, pool, "a", []byte{0}), 1)
	jobB, _ := enqueue(t, pool, addDatabase(t, pool, "b", []byte{0}), 1)

	ctx, cancel := context.WithCancel(context.Background())
	var done atomic.Int32
	w := &Worker{Pool: pool, Poll: 50 * time.Millisecond, Handle: func(ctx context.Context, job *Job) {
		tx, err := job.Begin(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		tx.Exec(ctx, `UPDATE migration_jobs SET status = 'done' WHERE id = $1`, job.ID)
		tx.Exec(ctx, `UPDATE migration_executions SET status = 'success', finished_at = now() WHERE id = $1`, job.ExecutionID)
		tx.Commit(ctx)
		if done.Add(1) == 2 {
			cancel()
		}
	}}
	finished := make(chan struct{})
	go func() { w.Run(ctx); close(finished) }()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("worker tidak menyelesaikan antrean dalam 10 detik")
	}
	for _, id := range []int64{jobA, jobB} {
		if j, e := statuses(t, pool, id); j != "done" || e != "success" {
			t.Fatalf("job %d = %s/%s, want done/success", id, j, e)
		}
	}
}
