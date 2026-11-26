package worker

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// keputusan revalidasi setelah lock didapat, sebelum SQL dijalankan
type verdict int

const (
	proceed verdict = iota
	requeue         // tunda sebentar, kondisi bisa berubah sendiri (antrean di depan / database masih terblokir)
	reject          // gagal permanen, harus diajukan ulang
)

type decision struct {
	verdict verdict
	reason  string
}

func ok() decision                            { return decision{verdict: proceed} }
func wait(format string, a ...any) decision   { return decision{requeue, fmt.Sprintf(format, a...)} }
func refuse(format string, a ...any) decision { return decision{reject, fmt.Sprintf(format, a...)} }

// root_id = pemilik checkpoint batch; dipakai berulang di query di bawah
const rootOf = `COALESCE(%[1]s.parent_execution_id, %[1]s.request_id, %[1]s.id)`

// eksekusi up/forward/retry yang sukses dan belum dibatalkan rollback/compensate sukses
const activeRows = `SELECT e.id, e.version, e.migration_id FROM migration_executions e
	WHERE e.database_id = $1 AND e.operation IN ('up', 'forward', 'retry') AND e.status = 'success'
	AND NOT EXISTS (SELECT 1 FROM migration_executions r WHERE r.reverted_execution_id = e.id
		AND r.operation IN ('rollback', 'compensate') AND r.status = 'success')`

// partial / partially_reverted yang belum diselesaikan eksekusi lebih baru pada root yang sama
var unresolvedRows = fmt.Sprintf(`SELECT p.id, %s AS root FROM migration_executions p
	WHERE p.database_id = $1 AND p.status IN ('partial', 'partially_reverted')
	AND NOT EXISTS (SELECT 1 FROM migration_executions r WHERE r.id > p.id AND %s = %s
		AND r.status IN ('success', 'partial', 'partially_reverted'))`,
	fmt.Sprintf(rootOf, "p"), fmt.Sprintf(rootOf, "r"), fmt.Sprintf(rootOf, "p"))

// migration yang pernah sukses lalu dibatalkan, dan sekarang tidak aktif
const rolledBack = `EXISTS (SELECT 1 FROM migration_executions e WHERE e.migration_id = m.id
		AND e.operation IN ('up', 'forward', 'retry') AND e.status = 'success')
	AND NOT EXISTS (SELECT 1 FROM active a WHERE a.migration_id = m.id)`

type headState struct {
	version  int
	activeID int64 // eksekusi aktif di versi head (0 kalau belum ada)
}

// Lock → Revalidation → Execute: cek ulang semua syarat di bawah advisory lock (kontrak poin 3a–3c)
func revalidate(ctx context.Context, pool *pgxpool.Pool, job *Job, e execution) (decision, error) {
	if d, err := checkApproval(ctx, pool, job, e); err != nil || d.verdict != proceed {
		return d, err
	}
	if d, err := checkBlocking(ctx, pool, job, e); err != nil || d.verdict != proceed {
		return d, err
	}
	return checkPreconditions(ctx, pool, job, e)
}

// (a) semua group yang SAAT INI punya akses ke database harus sudah approve pengajuan req_id
func checkApproval(ctx context.Context, pool *pgxpool.Pool, job *Job, e execution) (decision, error) {
	var missing int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM group_database_access a WHERE a.database_id = $1
		AND NOT EXISTS (SELECT 1 FROM migration_approvals ap WHERE ap.execution_id = $2
			AND ap.group_id = a.group_id AND ap.status = 'approved')`, job.DatabaseID, e.ReqID).Scan(&missing)
	if err != nil {
		return decision{}, err
	}
	if missing > 0 {
		return refuse("%d group dengan akses ke database belum menyetujui pengajuan ini, ajukan ulang", missing), nil
	}
	return ok(), nil
}

// (b) database terblokir oleh partial yang belum selesai atau eksekusi lain yang masih running
func checkBlocking(ctx context.Context, pool *pgxpool.Pool, job *Job, e execution) (decision, error) {
	var blockers int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+unresolvedRows+`) u
		WHERE NOT ($2 IN ('retry', 'compensate') AND u.root = $3)`, job.DatabaseID, e.Operation, e.RootID).Scan(&blockers)
	if err != nil {
		return decision{}, err
	}
	if blockers > 0 {
		return wait("database masih punya eksekusi partial yang belum di-retry/compensate"), nil
	}
	var running int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM migration_executions WHERE database_id = $1 AND status = 'running' AND id <> $2`,
		job.DatabaseID, e.ID).Scan(&running); err != nil {
		return decision{}, err
	}
	if running > 0 {
		return wait("masih ada eksekusi lain yang running di database ini"), nil
	}
	return ok(), nil
}

// (c) syarat per operasi, ketat: tidak boleh melompati versi
func checkPreconditions(ctx context.Context, pool *pgxpool.Pool, job *Job, e execution) (decision, error) {
	head, err := activeHead(ctx, pool, job.DatabaseID)
	if err != nil {
		return decision{}, err
	}
	switch e.Operation {
	case "up", "forward":
		return checkForwardMove(ctx, pool, job, e, head)
	case "rollback":
		var reverted *int64
		if err := pool.QueryRow(ctx, `SELECT reverted_execution_id FROM migration_executions WHERE id = $1`, e.ID).Scan(&reverted); err != nil {
			return decision{}, err
		}
		if e.Version != head.version {
			return refuse("rollback hanya boleh untuk versi aktif terakhir (v%d), bukan v%d", head.version, e.Version), nil
		}
		if reverted == nil || *reverted != head.activeID {
			return refuse("eksekusi yang mau dibatalkan bukan eksekusi aktif v%d", head.version), nil
		}
		return ok(), nil
	case "retry", "compensate":
		statuses := "'partial'"
		if e.Operation == "compensate" {
			statuses = "'partial', 'partially_reverted'"
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+unresolvedRows+`) u JOIN migration_executions p ON p.id = u.id
			WHERE u.root = $2 AND p.status IN (`+statuses+`)`, job.DatabaseID, e.RootID).Scan(&n); err != nil {
			return decision{}, err
		}
		if n == 0 {
			return refuse("tidak ada eksekusi %s yang belum selesai untuk root #%d", statuses, e.RootID), nil
		}
		return ok(), nil
	}
	return refuse("operasi %q tidak dikenal", e.Operation), nil
}

// up/forward ke versi v di atas head
func checkForwardMove(ctx context.Context, pool *pgxpool.Pool, job *Job, e execution, head headState) (decision, error) {
	if e.Version <= head.version {
		return refuse("v%d tidak di atas versi aktif v%d", e.Version, head.version), nil
	}
	// versi di antara head dan v yang sudah antre di depan: tunggu giliran
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM migration_executions w JOIN migration_jobs j ON j.execution_id = w.id
		WHERE w.database_id = $1 AND w.version > $2 AND w.version < $3 AND j.status IN ('pending', 'running')`,
		job.DatabaseID, head.version, e.Version).Scan(&queued); err != nil {
		return decision{}, err
	}
	if queued > 0 {
		return wait("versi sebelum v%d masih antre", e.Version), nil
	}
	// versi di antara yang belum di-enqueue: v tidak boleh mendahului
	var unqueued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM migration_executions w
		WHERE w.database_id = $1 AND w.version > $2 AND w.version < $3 AND w.status IN ('pending_review', 'approved')`,
		job.DatabaseID, head.version, e.Version).Scan(&unqueued); err != nil {
		return decision{}, err
	}
	if unqueued > 0 {
		return refuse("ada versi sebelum v%d yang masih menunggu review/enqueue", e.Version), nil
	}

	if e.Operation == "up" {
		var everSuccess, drafts int
		err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM migration_executions WHERE migration_id = $1 AND operation IN ('up', 'forward', 'retry') AND status = 'success'),
			(SELECT count(*) FROM migrations m WHERE m.database_id = $2 AND m.version > $3 AND m.version < $4
				AND NOT EXISTS (SELECT 1 FROM migration_executions x WHERE x.migration_id = m.id))`,
			e.MigrationID, job.DatabaseID, head.version, e.Version).Scan(&everSuccess, &drafts)
		if err != nil {
			return decision{}, err
		}
		if everSuccess > 0 {
			return refuse("v%d sudah pernah sukses, gunakan forward", e.Version), nil
		}
		if drafts > 0 {
			return refuse("ada versi draft sebelum v%d yang belum pernah diajukan", e.Version), nil
		}
		return ok(), nil
	}

	// forward: v sedang rolled-back dan tidak ada versi rolled-back di antara head dan v
	var selfRolledBack bool
	var between int
	err := pool.QueryRow(ctx, `WITH active AS (`+activeRows+`) SELECT
		(SELECT `+rolledBack+` FROM migrations m WHERE m.id = $2),
		(SELECT count(*) FROM migrations m WHERE m.database_id = $1 AND m.version > $3 AND m.version < $4 AND `+rolledBack+`)`,
		job.DatabaseID, e.MigrationID, head.version, e.Version).Scan(&selfRolledBack, &between)
	if err != nil {
		return decision{}, err
	}
	if !selfRolledBack {
		return refuse("forward hanya untuk migration yang sedang di-rollback"), nil
	}
	if between > 0 {
		return refuse("forward versi sebelum v%d dulu", e.Version), nil
	}
	return ok(), nil
}

func activeHead(ctx context.Context, pool *pgxpool.Pool, databaseID int64) (headState, error) {
	var h headState
	err := pool.QueryRow(ctx, `WITH active AS (`+activeRows+`)
		SELECT COALESCE(max(version), 0), COALESCE((SELECT id FROM active ORDER BY version DESC, id DESC LIMIT 1), 0) FROM active`,
		databaseID).Scan(&h.version, &h.activeID)
	return h, err
}
