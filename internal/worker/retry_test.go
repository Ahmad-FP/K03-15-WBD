package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{db.ErrUnreachable, true},
		{fmt.Errorf("connect: %w", db.ErrUnreachable), true},
		{&pgconn.PgError{Code: "40P01"}, true},  // deadlock
		{&pgconn.PgError{Code: "40001"}, true},  // serialization failure
		{&pgconn.PgError{Code: "08006"}, true},  // connection failure
		{&pgconn.PgError{Code: "42P01"}, false}, // tabel tidak ada: salah SQL, bukan gangguan
		{&pgconn.PgError{Code: "23505"}, false}, // unique violation
		{errors.New("ada versi draft sebelum v2"), false},
	}
	for _, c := range cases {
		if got := transient(c.err); got != c.want {
			t.Errorf("transient(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}

type attemptRow struct {
	id, reqID int64
	attempt   int
	status    string
}

// semua attempt untuk satu pengajuan, urut dari yang pertama
func attempts(t *testing.T, pool *pgxpool.Pool, reqID int64) []attemptRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id, COALESCE(request_id, id), attempt, status FROM migration_executions
		WHERE COALESCE(request_id, id) = $1 ORDER BY id`, reqID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []attemptRow
	for rows.Next() {
		var a attemptRow
		rows.Scan(&a.id, &a.reqID, &a.attempt, &a.status)
		out = append(out, a)
	}
	return out
}

func TestAutoRetryAppendOnly(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	t.Setenv("DATABASE_CREDENTIALS_KEY", strings.Repeat("ab", 32))
	enc, _ := db.EncryptDSN("postgres://x:y@127.0.0.1:1/nope?connect_timeout=1")
	jobID, firstID := enqueueSQL(t, pool, addDatabase(t, pool, "mati", enc), 1, "up", "SELECT 1")

	execute(t, pool)
	got := attempts(t, pool, firstID)
	if len(got) != 2 || got[0].status != "failed" || got[1].status != "pending" || got[1].attempt != 2 || got[1].reqID != firstID {
		t.Fatalf("setelah gagal sementara pertama: %+v", got)
	}
	var execID int64
	var status string
	var wait float64
	pool.QueryRow(ctx, `SELECT execution_id, status, EXTRACT(EPOCH FROM run_after - now()) FROM migration_jobs WHERE id = $1`, jobID).Scan(&execID, &status, &wait)
	if execID != got[1].id || status != "pending" || wait < 1 {
		t.Fatalf("job harus menunjuk attempt baru dengan jeda backoff: exec=%d status=%s wait=%.1fs", execID, status, wait)
	}
	if job, _ := Claim(ctx, pool); job != nil {
		job.Release(ctx)
		t.Fatal("job belum boleh diambil sebelum jeda backoff lewat")
	}

	// percepat jeda, jalankan attempt 2 dan 3 (max_attempts default 3)
	for range 2 {
		pool.Exec(ctx, `UPDATE migration_jobs SET run_after = now() WHERE id = $1`, jobID)
		execute(t, pool)
	}
	got = attempts(t, pool, firstID)
	if len(got) != 3 {
		t.Fatalf("harus berhenti di max_attempts (3), dapat %d attempt: %+v", len(got), got)
	}
	for i, a := range got {
		if a.status != "failed" || a.attempt != i+1 {
			t.Fatalf("attempt %d = %+v, semua harus tercatat failed tanpa ditimpa", i+1, a)
		}
	}
	if j, _ := statuses(t, pool, jobID); j != "failed" {
		t.Fatalf("job setelah attempt habis = %s, want failed", j)
	}
}

func TestNoRetryForSQLError(t *testing.T) {
	pool := platform(t)
	_, dbID := target(t, pool)
	jobID, execID := enqueueSQL(t, pool, dbID, 1, "up", "SELECT * FROM tidak_ada")
	execute(t, pool)
	if got := attempts(t, pool, execID); len(got) != 1 || got[0].status != "failed" {
		t.Fatalf("salah SQL tidak boleh di-retry otomatis: %+v", got)
	}
	if j, _ := statuses(t, pool, jobID); j != "failed" {
		t.Fatalf("job = %s, want failed", j)
	}
}

func TestRetryKeepsApproval(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	t.Setenv("DATABASE_CREDENTIALS_KEY", strings.Repeat("ab", 32))
	enc, _ := db.EncryptDSN("postgres://x:y@127.0.0.1:1/nope?connect_timeout=1")
	dbID := addDatabase(t, pool, "mati", enc)
	jobID, firstID := enqueueSQL(t, pool, dbID, 1, "up", "SELECT 1")
	var g int64
	pool.QueryRow(ctx, `INSERT INTO platform_groups (name) VALUES ('g') RETURNING id`).Scan(&g)
	pool.Exec(ctx, `INSERT INTO group_database_access (group_id, database_id, access_level) VALUES ($1, $2, 'view_edit')`, g, dbID)
	pool.Exec(ctx, `INSERT INTO migration_approvals (execution_id, group_id, status) VALUES ($1, $2, 'approved')`, firstID, g)

	execute(t, pool)
	pool.Exec(ctx, `UPDATE migration_jobs SET run_after = now() WHERE id = $1`, jobID)
	execute(t, pool)

	// attempt 2 tetap memakai approval milik req_id (attempt pertama), jadi gagalnya karena koneksi, bukan approval
	got := attempts(t, pool, firstID)
	var errMsg string
	pool.QueryRow(ctx, `SELECT error FROM migration_executions WHERE id = $1`, got[1].id).Scan(&errMsg)
	if len(got) != 3 || strings.Contains(errMsg, "menyetujui") {
		t.Fatalf("attempt retry ditolak approval: %+v, error=%q", got, errMsg)
	}
}
