package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schema kosong sebagai database target + baris managed_databases yang menunjuk ke sana
func target(t *testing.T, pool *pgxpool.Pool) (*pgxpool.Pool, int64) {
	t.Helper()
	t.Setenv("DATABASE_CREDENTIALS_KEY", strings.Repeat("ab", 32))
	ctx := context.Background()
	schema := "tgt_" + strings.ToLower(rand.Text())
	base := os.Getenv("TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "search_path=" + schema
	tp, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tp.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tp.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		tp.Close()
	})
	enc, err := db.EncryptDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return tp, addDatabase(t, pool, schema, enc)
}

// claim job lalu jalankan executor seperti yang dilakukan Worker.process
func execute(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	job, err := Claim(ctx, pool)
	if err != nil || job == nil {
		t.Fatalf("tidak ada job yang bisa di-claim: %v", err)
	}
	defer job.Release(ctx)
	(&Executor{Pool: pool}).Handle(ctx, job)
}

type execRow struct {
	status, error string
	batches       int
	rows          int64
	finished      bool
}

func execState(t *testing.T, pool *pgxpool.Pool, id int64) execRow {
	t.Helper()
	var r execRow
	if err := pool.QueryRow(context.Background(), `SELECT status, error, batches_committed, rows_processed, finished_at IS NOT NULL
		FROM migration_executions WHERE id = $1`, id).Scan(&r.status, &r.error, &r.batches, &r.rows, &r.finished); err != nil {
		t.Fatal(err)
	}
	return r
}

func tableExists(t *testing.T, tp *pgxpool.Pool, name string) bool {
	t.Helper()
	var ok bool
	tp.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok)
	return ok
}

func checkpoints(t *testing.T, tp *pgxpool.Pool, reqID int64) int {
	t.Helper()
	var n int
	tp.QueryRow(context.Background(), `SELECT count(*) FROM _wbd_checkpoints WHERE execution_id = $1`, reqID).Scan(&n)
	return n
}

func TestExecuteNormalSuccess(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	jobID, execID := enqueueSQL(t, pool, dbID, 1, "up",
		"CREATE TABLE items (id int PRIMARY KEY); INSERT INTO items VALUES (1), (2), (3)")
	execute(t, pool)

	r := execState(t, pool, execID)
	if r.status != "success" || r.batches != 1 || r.rows != 3 || !r.finished || r.error != "" {
		t.Fatalf("execution = %+v", r)
	}
	if j, _ := statuses(t, pool, jobID); j != "done" {
		t.Fatalf("job = %s, want done", j)
	}
	if !tableExists(t, tp, "items") || checkpoints(t, tp, execID) != 1 {
		t.Fatal("perubahan atau checkpoint tidak ter-commit di database target")
	}
}

func TestExecuteNormalFailureRollsBack(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	jobID, execID := enqueueSQL(t, pool, dbID, 1, "up", "CREATE TABLE half (id int); SELECT * FROM tidak_ada")
	execute(t, pool)

	r := execState(t, pool, execID)
	if r.status != "failed" || !strings.Contains(r.error, "tidak_ada") || !r.finished {
		t.Fatalf("execution = %+v", r)
	}
	if j, _ := statuses(t, pool, jobID); j != "failed" {
		t.Fatalf("job = %s, want failed", j)
	}
	if tableExists(t, tp, "half") || checkpoints(t, tp, execID) != 0 {
		t.Fatal("transaksi gagal harus di-rollback seluruhnya, termasuk checkpoint")
	}
}

func TestExecuteRollbackOperation(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	tp.Exec(context.Background(), "CREATE TABLE items (id int)")
	_, execID := enqueueSQL(t, pool, dbID, 1, "rollback", "DROP TABLE items")
	execute(t, pool)

	if r := execState(t, pool, execID); r.status != "success" {
		t.Fatalf("rollback = %+v", r)
	}
	if tableExists(t, tp, "items") {
		t.Fatal("SQL down tidak dijalankan")
	}
}

func TestExecuteSkipsAlreadyCommitted(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	// SQL ini pasti gagal kalau dijalankan; checkpoint membuktikan attempt sebelumnya sudah commit
	_, execID := enqueueSQL(t, pool, dbID, 1, "up", "SELECT * FROM tidak_ada")
	ctx := context.Background()
	tp.Exec(ctx, checkpointTable)
	tp.Exec(ctx, `INSERT INTO _wbd_checkpoints (execution_id, batch_no, rows_affected) VALUES ($1, 1, 7)`, execID)
	execute(t, pool)

	if r := execState(t, pool, execID); r.status != "success" || r.rows != 7 {
		t.Fatalf("execution yang sudah ter-commit harus success tanpa dijalankan ulang: %+v", r)
	}
}

func TestExecuteUnreachableTarget(t *testing.T) {
	pool := platform(t)
	t.Setenv("DATABASE_CREDENTIALS_KEY", strings.Repeat("ab", 32))
	enc, _ := db.EncryptDSN("postgres://x:y@127.0.0.1:1/nope?connect_timeout=1")
	_, execID := enqueueSQL(t, pool, addDatabase(t, pool, "mati", enc), 1, "up", "SELECT 1")
	execute(t, pool)
	if r := execState(t, pool, execID); r.status != "failed" || r.error == "" {
		t.Fatalf("target mati harus failed: %+v", r)
	}
}

func TestAttemptIsLogged(t *testing.T) {
	pool := platform(t)
	_, dbID := target(t, pool)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(telemetry.NewLogger(&buf))
	defer slog.SetDefault(prev)

	_, execID := enqueueSQL(t, pool, dbID, 1, "up", "SELECT * FROM tidak_ada")
	execute(t, pool)

	out := buf.String()
	for _, want := range []string{`"msg":"migration.attempt.start"`, `"msg":"migration.attempt.end"`, `"status":"failed"`,
		`"sql_snapshot":"SELECT * FROM tidak_ada"`, `"operation":"up"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log attempt tidak memuat %s", want)
		}
	}
	if !strings.Contains(out, `"execution_id":`) || execID == 0 {
		t.Error("log attempt tidak memuat execution_id")
	}
}
