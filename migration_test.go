package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/Ahmad-FP/K03-15-WBD/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type wfEnv struct {
	h            http.Handler
	pool         *pgxpool.Pool
	target       *pgxpool.Pool
	dbID         int64
	admin        string
	editor       string
	editorLeader string
	viewer       string
	viewerLeader string
	other        string
	editGroupID  int64
	viewGroupID  int64
}

func setupWorkflow(t *testing.T, wrapTelemetry bool) wfEnv {
	t.Helper()
	h, pool := testServer(t)
	if wrapTelemetry {
		h = telemetry.Middleware(h)
	}
	admin := login(t, h, "admin", "admin")
	target, dsn := targetSchema(t)
	dbID := addTarget(t, h, admin, dsn)

	editorID := addUser(t, pool, "editor", "rahasia")
	editLeadID := addUser(t, pool, "lead_edit", "rahasia")
	viewerID := addUser(t, pool, "viewer", "rahasia")
	viewLeadID := addUser(t, pool, "lead_view", "rahasia")
	addUser(t, pool, "orang", "rahasia")

	editGID := jsonID(t, mustStatus(t, doJSON(h, "POST", "/api/groups", admin, `{"name":"tim_edit","leader_id":`+itoa(editLeadID)+`}`), http.StatusCreated))
	mustStatus(t, doJSON(h, "POST", "/api/groups/"+itoa(editGID)+"/members", admin, `{"user_id":`+itoa(editorID)+`}`), http.StatusCreated)
	mustStatus(t, doJSON(h, "PUT", "/api/db/"+itoa(dbID)+"/access/"+itoa(editGID), admin, `{"access_level":"view_edit"}`), http.StatusNoContent)

	viewGID := jsonID(t, mustStatus(t, doJSON(h, "POST", "/api/groups", admin, `{"name":"tim_view","leader_id":`+itoa(viewLeadID)+`}`), http.StatusCreated))
	mustStatus(t, doJSON(h, "POST", "/api/groups/"+itoa(viewGID)+"/members", admin, `{"user_id":`+itoa(viewerID)+`}`), http.StatusCreated)
	mustStatus(t, doJSON(h, "PUT", "/api/db/"+itoa(dbID)+"/access/"+itoa(viewGID), admin, `{"access_level":"view_only"}`), http.StatusNoContent)

	return wfEnv{
		h: h, pool: pool, target: target, dbID: dbID, admin: admin,
		editor:       login(t, h, "editor", "rahasia"),
		editorLeader: login(t, h, "lead_edit", "rahasia"),
		viewer:       login(t, h, "viewer", "rahasia"),
		viewerLeader: login(t, h, "lead_view", "rahasia"),
		other:        login(t, h, "orang", "rahasia"),
		editGroupID:  editGID,
		viewGroupID:  viewGID,
	}
}

func createMig(t *testing.T, e wfEnv, body string) int64 {
	t.Helper()
	return jsonID(t, mustStatus(t, doJSON(e.h, "POST", dbPath(e.dbID)+"/migrations", e.editor, body), http.StatusCreated))
}

func execID(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	v := decode[struct {
		ExecutionID int64 `json:"execution_id"`
	}](t, mustStatus(t, rec, http.StatusCreated))
	if v.ExecutionID == 0 {
		t.Fatalf("missing execution_id: %s", rec.Body)
	}
	return v.ExecutionID
}

func approveAll(t *testing.T, e wfEnv, eid int64) {
	t.Helper()
	p := dbPath(e.dbID) + "/executions/" + itoa(eid) + "/approve"
	mustStatus(t, doJSON(e.h, "POST", p, e.viewerLeader, `{"decision":"approved"}`), http.StatusNoContent)
	mustStatus(t, doJSON(e.h, "POST", p, e.editorLeader, `{"decision":"approved"}`), http.StatusNoContent)
}

func runOneJob(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ran := false
	w := &worker.Worker{
		Pool: pool, Concurrency: 1, Poll: 20 * time.Millisecond,
		Handle: func(ctx context.Context, job *worker.Job) {
			(&worker.Executor{Pool: pool}).Handle(ctx, job)
			ran = true
			cancel()
		},
	}
	w.Run(ctx)
	if !ran {
		t.Fatal("worker tidak menemukan job")
	}
}

func TestMigrationCRUDAndWorkflow(t *testing.T) {
	e := setupWorkflow(t, false)
	base := dbPath(e.dbID)
	mBase := base + "/migrations"

	for _, bad := range []string{
		`{"name":"a","sql_up":"CREATE TABLE t (id int);"}`,
		`{"name":"a","sql_up":"SELECT 1","sql_down":"SELECT 1","batch_size":10}`,
		`{"name":"a","sql_up":"SELECT 1; SELECT 2;","sql_down":"SELECT 1","batch_column":"id","batch_size":10}`,
		`{"name":"a","sql_up":"CREATE TABLE t (id int);","sql_down":"DROP TABLE t; COMMIT;"}`,
	} {
		assertJSONError(t, doJSON(e.h, "POST", mBase, e.editor, bad), http.StatusBadRequest)
	}

	m1 := createMig(t, e, `{"name":"tambah produk","sql_up":"CREATE TABLE produk (id serial);","sql_down":"DROP TABLE produk;"}`)
	irrev := decode[struct {
		ID      int64  `json:"id"`
		SQLDown string `json:"sql_down"`
	}](t, mustStatus(t, doJSON(e.h, "POST", mBase, e.editor, `{"name":"hapus lama","sql_up":"DELETE FROM produk;","sql_down":"SELECT 1","irreversible_reason":"hapus permanen"}`), http.StatusCreated))
	if irrev.SQLDown != "" {
		t.Fatalf("irreversible seharusnya mengosongkan sql_down, dapat %q", irrev.SQLDown)
	}
	m2 := irrev.ID

	if upd := mustStatus(t, doJSON(e.h, "PUT", mBase+"/"+itoa(m2), e.editor, `{"name":"hapus v2","sql_up":"DELETE FROM produk;","irreversible_reason":"hapus permanen"}`), http.StatusOK); !strings.Contains(upd.Body.String(), `"name":"hapus v2"`) {
		t.Fatalf("update draft: %s", upd.Body)
	}
	mustStatus(t, doJSON(e.h, "DELETE", mBase+"/"+itoa(m2), e.editor, ""), http.StatusNoContent)
	assertJSONError(t, doJSON(e.h, "GET", mBase+"/"+itoa(m2), e.editor, ""), http.StatusNotFound)

	mustStatus(t, doJSON(e.h, "GET", mBase, e.viewer, ""), http.StatusOK)
	for _, req := range [][2]string{
		{"POST", mBase}, {"PUT", mBase + "/" + itoa(m1)}, {"DELETE", mBase + "/" + itoa(m1)},
		{"POST", mBase + "/" + itoa(m1) + "/submit"}, {"POST", mBase + "/" + itoa(m1) + "/rollback"},
	} {
		assertJSONError(t, doJSON(e.h, req[0], req[1], e.viewer, `{"name":"x","sql_up":"SELECT 1","sql_down":"SELECT 1"}`), http.StatusForbidden)
	}
	assertJSONError(t, doJSON(e.h, "GET", mBase, e.other, ""), http.StatusForbidden)

	e.pool.Exec(t.Context(), `UPDATE platform_groups SET leader_id = NULL WHERE id = $1`, e.viewGroupID)
	assertJSONError(t, doJSON(e.h, "POST", mBase+"/"+itoa(m1)+"/submit", e.editor, ""), http.StatusConflict)
	e.pool.Exec(t.Context(), `UPDATE platform_groups SET leader_id = (SELECT id FROM users WHERE username = 'lead_view') WHERE id = $1`, e.viewGroupID)

	eid := execID(t, doJSON(e.h, "POST", mBase+"/"+itoa(m1)+"/submit", e.editor, ""))
	if !strings.Contains(mustStatus(t, doJSON(e.h, "GET", mBase+"/"+itoa(m1), e.viewer, ""), http.StatusOK).Body.String(), `"immutable":true`) {
		t.Error("m1 harus immutable")
	}
	assertJSONError(t, doJSON(e.h, "PUT", mBase+"/"+itoa(m1), e.editor, `{"name":"ubah","sql_up":"SELECT 1","sql_down":"SELECT 1"}`), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "DELETE", mBase+"/"+itoa(m1), e.editor, ""), http.StatusConflict)

	ePath := base + "/executions/" + itoa(eid)
	assertJSONError(t, doJSON(e.h, "POST", mBase+"/"+itoa(m1)+"/submit", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/enqueue", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/approve", e.editor, `{"decision":"approved"}`), http.StatusForbidden)
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/approve", e.viewer, `{"decision":"approved"}`), http.StatusForbidden)
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/approve", e.viewerLeader, `{"decision":"maybe"}`), http.StatusBadRequest)

	_, dsn2 := targetSchema(t)
	db2 := addTarget(t, e.h, e.admin, dsn2)
	mustStatus(t, doJSON(e.h, "PUT", "/api/db/"+itoa(db2)+"/access/"+itoa(e.editGroupID), e.admin, `{"access_level":"view_edit"}`), http.StatusNoContent)
	assertJSONError(t, doJSON(e.h, "GET", dbPath(db2)+"/executions/"+itoa(eid), e.editorLeader, ""), http.StatusNotFound)
	assertJSONError(t, doJSON(e.h, "POST", dbPath(db2)+"/executions/"+itoa(eid)+"/approve", e.editorLeader, `{"decision":"approved"}`), http.StatusNotFound)
	assertJSONError(t, doJSON(e.h, "POST", dbPath(db2)+"/executions/"+itoa(eid)+"/cancel", e.editorLeader, ""), http.StatusNotFound)

	mustStatus(t, doJSON(e.h, "POST", ePath+"/approve", e.viewerLeader, `{"decision":"approved"}`), http.StatusNoContent)
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/approve", e.viewerLeader, `{"decision":"approved"}`), http.StatusConflict)
	if !strings.Contains(doJSON(e.h, "GET", ePath, e.viewer, "").Body.String(), `"status":"pending_review"`) {
		t.Error("harus masih pending_review")
	}
	mustStatus(t, doJSON(e.h, "POST", ePath+"/approve", e.editorLeader, `{"decision":"approved"}`), http.StatusNoContent)
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/approve", e.editorLeader, `{"decision":"rejected"}`), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/enqueue", e.viewerLeader, ""), http.StatusForbidden)

	enq := mustStatus(t, doJSON(e.h, "POST", ePath+"/enqueue", e.editor, ""), http.StatusAccepted)
	if !strings.Contains(enq.Body.String(), `"status":"pending"`) || !strings.Contains(enq.Body.String(), `"job_id":`) {
		t.Fatalf("enqueue body salah: %s", enq.Body)
	}
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/enqueue", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", ePath+"/cancel", e.editor, ""), http.StatusConflict)

	det := decode[struct {
		Status            string  `json:"status"`
		SQLSnapshot       string  `json:"sql_snapshot"`
		LatestExecutionID int64   `json:"latest_execution_id"`
		JobID             *int64  `json:"job_id"`
		JobStatus         *string `json:"job_status"`
		Approvals         []any   `json:"approvals"`
	}](t, mustStatus(t, doJSON(e.h, "GET", ePath, e.viewer, ""), http.StatusOK))
	if det.Status != "pending" || det.LatestExecutionID != eid || det.JobID == nil || det.JobStatus == nil || *det.JobStatus != "pending" || len(det.Approvals) != 2 || det.SQLSnapshot == "" {
		t.Errorf("detail tidak lengkap: %+v", det)
	}

	e.pool.Exec(t.Context(), `UPDATE migration_executions SET status = 'failed' WHERE id = $1`, eid)
	rejID := execID(t, doJSON(e.h, "POST", mBase+"/"+itoa(m1)+"/submit", e.editor, ""))
	mustStatus(t, doJSON(e.h, "POST", base+"/executions/"+itoa(rejID)+"/approve", e.viewerLeader, `{"decision":"rejected"}`), http.StatusNoContent)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(rejID)+"/approve", e.editorLeader, `{"decision":"approved"}`), http.StatusConflict)

	canID := execID(t, doJSON(e.h, "POST", mBase+"/"+itoa(m1)+"/submit", e.editor, ""))
	approveAll(t, e, canID)
	mustStatus(t, doJSON(e.h, "POST", base+"/executions/"+itoa(canID)+"/cancel", e.editor, ""), http.StatusNoContent)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(canID)+"/enqueue", e.editor, ""), http.StatusConflict)
}

func TestRollbackForwardAndRecovery(t *testing.T) {
	e := setupWorkflow(t, false)
	ctx := context.Background()
	base := dbPath(e.dbID)

	m1 := createMig(t, e, `{"name":"v1","sql_up":"SELECT 1","sql_down":"SELECT -1"}`)
	m2 := createMig(t, e, `{"name":"v2","sql_up":"SELECT 2","sql_down":"SELECT -2"}`)
	m3 := createMig(t, e, `{"name":"v3","sql_up":"SELECT 3","sql_down":"SELECT -3"}`)
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/submit", e.editor, ""), http.StatusConflict)

	var e1, e2, e3 int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1,$2,1,'up','SELECT 1','success') RETURNING id`, m1, e.dbID).Scan(&e1)
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1,$2,2,'up','SELECT 2','success') RETURNING id`, m2, e.dbID).Scan(&e2)
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1,$2,3,'up','SELECT 3','success') RETURNING id`, m3, e.dbID).Scan(&e3)

	if !strings.Contains(doJSON(e.h, "GET", base+"/migrations/"+itoa(m2), e.viewer, "").Body.String(), `"can_rollback":false`) ||
		!strings.Contains(doJSON(e.h, "GET", base+"/migrations/"+itoa(m3), e.viewer, "").Body.String(), `"can_rollback":true`) {
		t.Error("can_rollback salah")
	}
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/rollback", e.editor, ""), http.StatusConflict)

	rb3 := execID(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m3)+"/rollback", e.editor, ""))
	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'success' WHERE id = $1`, rb3)
	rb2 := execID(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/rollback", e.editor, ""))
	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'success' WHERE id = $1`, rb2)

	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m3)+"/submit", e.editor, ""), http.StatusConflict)
	if !strings.Contains(doJSON(e.h, "GET", base+"/migrations/"+itoa(m3), e.viewer, "").Body.String(), `"can_forward":false`) {
		t.Error("v3 seharusnya can_forward=false")
	}

	fw2 := execID(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/submit", e.editor, ""))
	fw3 := execID(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m3)+"/submit", e.editor, ""))
	approveAll(t, e, fw2)
	approveAll(t, e, fw3)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(fw3)+"/enqueue", e.editor, ""), http.StatusConflict)

	var failID int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status, error) VALUES ($1,$2,2,'rollback','SELECT -2','failed','boom') RETURNING id`, m2, e.dbID).Scan(&failID)

	allList := decode[[]struct{ ID int64 }](t, mustStatus(t, doJSON(e.h, "GET", base+"/executions?hide_rollback=false", e.viewer, ""), http.StatusOK))
	hideList := decode[[]struct{ ID int64 }](t, mustStatus(t, doJSON(e.h, "GET", base+"/executions?hide_rollback=true", e.viewer, ""), http.StatusOK))
	if len(allList) != len(hideList)+4 {
		t.Fatalf("all=%d hide=%d", len(allList), len(hideList))
	}
	hidden := map[int64]bool{e2: true, rb2: true, e3: true, rb3: true}
	seenFail := false
	for _, it := range hideList {
		if hidden[it.ID] {
			t.Errorf("id %d seharusnya disembunyikan", it.ID)
		}
		if it.ID == failID {
			seenFail = true
		}
	}
	if !seenFail {
		t.Error("attempt gagal ikut tersembunyi")
	}

	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'failed' WHERE id = $1`, fw2)
	var attempt2 int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (request_id, migration_id, database_id, version, operation, attempt, sql_snapshot, status) VALUES ($1,$2,$3,2,'forward',2,'SELECT 2','running') RETURNING id`, fw2, m2, e.dbID).Scan(&attempt2)
	e.pool.Exec(ctx, `INSERT INTO migration_jobs (execution_id, status) VALUES ($1, 'running')`, attempt2)
	det := decode[struct {
		LatestExecutionID int64   `json:"latest_execution_id"`
		JobStatus         *string `json:"job_status"`
	}](t, mustStatus(t, doJSON(e.h, "GET", base+"/executions/"+itoa(fw2), e.viewer, ""), http.StatusOK))
	if det.LatestExecutionID != attempt2 || det.JobStatus == nil || *det.JobStatus != "running" {
		t.Errorf("latest/job_status = %+v, want %d/running", det, attempt2)
	}

	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'failed' WHERE id IN ($1, $2)`, attempt2, fw3)
	mBatch := createMig(t, e, `{"name":"batch","sql_up":"UPDATE t SET x=1 WHERE id>$1 LIMIT $2","sql_down":"UPDATE t SET x=0 WHERE id>$1 AND id<=$2","batch_column":"id","batch_size":100}`)
	mIrr := createMig(t, e, `{"name":"irr","sql_up":"DELETE FROM t WHERE id>$1","irreversible_reason":"hapus","batch_column":"id","batch_size":100}`)
	var p1 int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status, batches_committed) VALUES ($1,$2,4,'up','UPDATE t','partial',3) RETURNING id`, mBatch, e.dbID).Scan(&p1)

	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(mBatch)+"/submit", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/rollback", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(mIrr)+"/submit", e.editor, ""), http.StatusConflict)
	if !strings.Contains(doJSON(e.h, "GET", base+"/migrations/"+itoa(m1), e.viewer, "").Body.String(), `"can_rollback":false`) {
		t.Error("m1 tidak boleh can_rollback=true saat DB punya unresolved partial")
	}

	r1 := execID(t, doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/retry", e.editor, ""))
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/retry", e.editor, ""), http.StatusConflict)
	mustStatus(t, doJSON(e.h, "POST", base+"/executions/"+itoa(r1)+"/cancel", e.editor, ""), http.StatusNoContent)

	c1 := execID(t, doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/compensate", e.editor, ""))
	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'partially_reverted' WHERE id = $1`, c1)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/retry", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/compensate", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(c1)+"/retry", e.editor, ""), http.StatusConflict)

	c2 := execID(t, doJSON(e.h, "POST", base+"/executions/"+itoa(c1)+"/compensate", e.editor, ""))
	var parentID, revID *int64
	e.pool.QueryRow(ctx, `SELECT parent_execution_id, reverted_execution_id FROM migration_executions WHERE id = $1`, c2).Scan(&parentID, &revID)
	if parentID == nil || *parentID != p1 || revID != nil {
		t.Errorf("compensate c2 parent=%v rev=%v, want %d/nil", parentID, revID, p1)
	}
	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'success' WHERE id = $1`, c2)

	var actID, rbPartial int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1,$2,4,'up','UPDATE t','success') RETURNING id`, mBatch, e.dbID).Scan(&actID)
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status, reverted_execution_id, parent_execution_id) VALUES ($1,$2,4,'rollback','UPDATE t','partially_reverted',$3,$3) RETURNING id`, mBatch, e.dbID, actID).Scan(&rbPartial)
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(mBatch)+"/rollback", e.editor, ""), http.StatusConflict)
	cRb := execID(t, doJSON(e.h, "POST", base+"/executions/"+itoa(rbPartial)+"/compensate", e.editor, ""))
	e.pool.QueryRow(ctx, `SELECT parent_execution_id, reverted_execution_id FROM migration_executions WHERE id = $1`, cRb).Scan(&parentID, &revID)
	if parentID == nil || *parentID != actID || revID == nil || *revID != actID {
		t.Errorf("compensate rollback parent=%v rev=%v, want %d/%d", parentID, revID, actID, actID)
	}

	var pIrr int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status, batches_committed) VALUES ($1,$2,5,'up','DELETE FROM t','partial',1) RETURNING id`, mIrr, e.dbID).Scan(&pIrr)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(pIrr)+"/compensate", e.editor, ""), http.StatusConflict)
}

func TestMigrationWorkerEndToEnd(t *testing.T) {
	e := setupWorkflow(t, true)
	base := dbPath(e.dbID)

	spans := tracetest.NewSpanRecorder()
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	defer otel.SetTracerProvider(prevTP)

	var logBuf bytes.Buffer
	prevLog := slog.Default()
	slog.SetDefault(telemetry.NewLogger(&logBuf))
	defer slog.SetDefault(prevLog)

	m1 := createMig(t, e, `{
		"name":"create_items",
		"sql_up":"CREATE TABLE items (id bigint PRIMARY KEY, status text NOT NULL, divisor int NOT NULL); INSERT INTO items VALUES (1,'old',1),(2,'old',1),(3,'old',0),(4,'old',1);",
		"sql_down":"DROP TABLE items;"
	}`)
	m2 := createMig(t, e, `{
		"name":"backfill_items",
		"sql_up":"WITH batch AS (SELECT id FROM items WHERE ($1::bigint IS NULL OR id > $1::bigint) ORDER BY id LIMIT $2) UPDATE items SET status = 'new_' || (100 / divisor) FROM batch WHERE items.id = batch.id RETURNING items.id::text",
		"sql_down":"UPDATE items SET status = 'old' WHERE ($1::bigint IS NULL OR id > $1::bigint) AND id <= $2::bigint",
		"batch_column":"id","batch_size":2
	}`)

	run := func(eid int64) {
		t.Helper()
		approveAll(t, e, eid)
		mustStatus(t, doJSON(e.h, "POST", base+"/executions/"+itoa(eid)+"/enqueue", e.editor, ""), http.StatusAccepted)
		runOneJob(t, e.pool)
	}

	e1 := execID(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/submit", e.editor, ""))
	run(e1)

	e2 := execID(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/submit", e.editor, ""))
	run(e2)
	det := decode[struct {
		Status           string  `json:"status"`
		BatchesCommitted int     `json:"batches_committed"`
		RowsProcessed    int64   `json:"rows_processed"`
		LastCursor       string  `json:"last_cursor"`
		JobStatus        *string `json:"job_status"`
	}](t, mustStatus(t, doJSON(e.h, "GET", base+"/executions/"+itoa(e2), e.editor, ""), http.StatusOK))
	if det.Status != "partial" || det.BatchesCommitted != 1 || det.RowsProcessed != 2 || det.LastCursor != "2" || det.JobStatus == nil || *det.JobStatus != "failed" {
		t.Fatalf("partial v2 salah: %+v", det)
	}

	run(execID(t, doJSON(e.h, "POST", base+"/executions/"+itoa(e2)+"/compensate", e.editor, "")))
	rb1 := execID(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/rollback", e.editor, ""))
	run(rb1)

	for _, ex := range decode[[]struct{ ID int64 }](t, mustStatus(t, doJSON(e.h, "GET", base+"/executions?hide_rollback=true", e.editor, ""), http.StatusOK)) {
		if ex.ID == e1 || ex.ID == rb1 {
			t.Fatalf("hide_rollback gagal menyembunyikan #%d/#%d", e1, rb1)
		}
	}

	enqueueTraces := map[string]bool{}
	for _, s := range spans.Ended() {
		if s.Name() == "POST /api/databases/{id}/executions/{eid}/enqueue" {
			enqueueTraces[s.SpanContext().TraceID().String()] = true
		}
	}
	linked := false
	for _, s := range spans.Ended() {
		if s.Name() == "worker.job" && enqueueTraces[s.SpanContext().TraceID().String()] {
			linked = true
		}
	}
	if !linked {
		t.Fatal("trace API enqueue tidak tersambung ke worker.job")
	}

	var sawHTTPActor, sawWorkerAttrs bool
	for _, line := range strings.Split(strings.TrimSpace(logBuf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["msg"] == "http.request" && rec["actor"] == "editor" {
			sawHTTPActor = true
		}
		if rec["msg"] == "worker.job_claimed" && rec["database_id"] == float64(e.dbID) && rec["trace_id"] != nil {
			sawWorkerAttrs = true
		}
	}
	if !sawHTTPActor || !sawWorkerAttrs {
		t.Fatalf("log korelasi kurang: httpActor=%v workerAttrs=%v", sawHTTPActor, sawWorkerAttrs)
	}
}
