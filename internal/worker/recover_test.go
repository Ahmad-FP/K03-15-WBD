package worker

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tiru worker yang mati di tengah job: koneksi pemegang lock diputus tanpa menulis status akhir
func crash(t *testing.T, pool *pgxpool.Pool) *Job {
	t.Helper()
	ctx := context.Background()
	job, err := Claim(ctx, pool)
	if err != nil || job == nil {
		t.Fatalf("claim gagal: %v", err)
	}
	conn := job.conn.Hijack()
	job.conn = nil
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestRecoverOrphanedJob(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	jobID, execID := enqueueSQL(t, pool, dbID, 1, "up", "CREATE TABLE items (id int)")
	crash(t, pool)
	if j, e := statuses(t, pool, jobID); j != "running" || e != "running" {
		t.Fatalf("persiapan: job harus tertinggal running, dapat %s/%s", j, e)
	}

	n, err := Recover(context.Background(), pool)
	if err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v; want 1 job dipulihkan", n, err)
	}
	if j, e := statuses(t, pool, jobID); j != "pending" || e != "pending" {
		t.Fatalf("job yatim harus kembali ke antrean, dapat %s/%s", j, e)
	}
	execute(t, pool)
	if r := execState(t, pool, execID); r.status != "success" || !tableExists(t, tp, "items") {
		t.Fatalf("job yang dipulihkan harus bisa diselesaikan: %+v", r)
	}
}

func TestRecoverSkipsLiveWorker(t *testing.T) {
	pool := platform(t)
	ctx := context.Background()
	jobID, _ := enqueue(t, pool, addDatabase(t, pool, "a", []byte{0}), 1)
	live, _ := Claim(ctx, pool) // worker hidup masih memegang lock
	defer live.Release(ctx)

	if n, err := Recover(ctx, pool); err != nil || n != 0 {
		t.Fatalf("job milik worker yang hidup tidak boleh diambil alih: %d, %v", n, err)
	}
	if j, _ := statuses(t, pool, jobID); j != "running" {
		t.Fatalf("job = %s, want tetap running", j)
	}
}

func TestRecoverAfterCommitDoesNotRunTwice(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	ctx := context.Background()
	// worker mati SETELAH SQL ter-commit di target tapi SEBELUM status ditulis ke platform:
	// SQL ini akan gagal kalau dijalankan lagi, jadi lulus berarti checkpoint mencegah eksekusi ganda
	_, execID := enqueueSQL(t, pool, dbID, 1, "up", "CREATE TABLE items (id int)")
	tp.Exec(ctx, "CREATE TABLE items (id int)")
	tp.Exec(ctx, checkpointTable)
	tp.Exec(ctx, `INSERT INTO _wbd_checkpoints (execution_id, batch_no, rows_affected) VALUES ($1, 1, 0)`, execID)
	crash(t, pool)

	Recover(ctx, pool)
	execute(t, pool)
	if r := execState(t, pool, execID); r.status != "success" || r.error != "" {
		t.Fatalf("hasil yang sudah ter-commit harus dicatat success tanpa dijalankan ulang: %+v", r)
	}
}

func TestRunRecoversOnStart(t *testing.T) {
	pool := platform(t)
	tp, dbID := target(t, pool)
	_, execID := enqueueSQL(t, pool, dbID, 1, "up", "CREATE TABLE items (id int)")
	crash(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w := &Worker{Pool: pool, Poll: 50 * time.Millisecond}
	w.Handle = func(ctx context.Context, job *Job) {
		(&Executor{Pool: pool}).Handle(ctx, job)
		cancel()
	}
	w.Run(ctx)
	if r := execState(t, pool, execID); r.status != "success" || !tableExists(t, tp, "items") {
		t.Fatalf("worker baru harus memulihkan dan menyelesaikan job yatim saat start: %+v", r)
	}
}
