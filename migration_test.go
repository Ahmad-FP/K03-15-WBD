package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type wfEnv struct {
	h            http.Handler
	pool         *pgxpool.Pool
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

func setupWorkflow(t *testing.T) wfEnv {
	t.Helper()
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	_, dsn := targetSchema(t)
	dbID := addTarget(t, h, admin, dsn)

	editorID := addUser(t, pool, "editor", "rahasia")
	editLeadID := addUser(t, pool, "lead_edit", "rahasia")
	viewerID := addUser(t, pool, "viewer", "rahasia")
	viewLeadID := addUser(t, pool, "lead_view", "rahasia")
	addUser(t, pool, "orang", "rahasia")

	editGID := jsonID(t, doJSON(h, "POST", "/api/groups", admin, `{"name":"tim_edit","leader_id":`+itoa(editLeadID)+`}`))
	doJSON(h, "POST", "/api/groups/"+itoa(editGID)+"/members", admin, `{"user_id":`+itoa(editorID)+`}`)
	doJSON(h, "PUT", "/api/db/"+itoa(dbID)+"/access/"+itoa(editGID), admin, `{"access_level":"view_edit"}`)

	viewGID := jsonID(t, doJSON(h, "POST", "/api/groups", admin, `{"name":"tim_view","leader_id":`+itoa(viewLeadID)+`}`))
	doJSON(h, "POST", "/api/groups/"+itoa(viewGID)+"/members", admin, `{"user_id":`+itoa(viewerID)+`}`)
	doJSON(h, "PUT", "/api/db/"+itoa(dbID)+"/access/"+itoa(viewGID), admin, `{"access_level":"view_only"}`)

	return wfEnv{
		h:            h,
		pool:         pool,
		dbID:         dbID,
		admin:        admin,
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
	rec := doJSON(e.h, "POST", dbPath(e.dbID)+"/migrations", e.editor, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create migration: status %d %s", rec.Code, rec.Body)
	}
	return jsonID(t, rec)
}

func execIDFrom(t *testing.T, rec *http.Response, body []byte) int64 {
	t.Helper()
	var out struct {
		ExecutionID int64 `json:"execution_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ExecutionID == 0 {
		t.Fatalf("missing execution_id in %s", string(body))
	}
	return out.ExecutionID
}

func TestMigrationCRUDAndImmutability(t *testing.T) {
	e := setupWorkflow(t)
	base := dbPath(e.dbID) + "/migrations"

	// validasi M2: reversible tanpa sql_down, batch tanpa kolom, batch multi-statement, COMMIT
	for _, bad := range []string{
		`{"name":"a","sql_up":"CREATE TABLE t (id int);"}`,
		`{"name":"a","sql_up":"SELECT 1","sql_down":"SELECT 1","batch_size":10}`,
		`{"name":"a","sql_up":"SELECT 1; SELECT 2;","sql_down":"SELECT 1","batch_column":"id","batch_size":10}`,
		`{"name":"a","sql_up":"CREATE TABLE t (id int);","sql_down":"DROP TABLE t; COMMIT;"}`,
	} {
		assertJSONError(t, doJSON(e.h, "POST", base, e.editor, bad), http.StatusBadRequest)
	}

	m1 := createMig(t, e, `{"name":"tambah produk","sql_up":"CREATE TABLE produk (id serial);","sql_down":"DROP TABLE produk;"}`)
	// M2: boleh buat v2 walaupun v1 masih draft (ErrPending dihapus)
	m2 := createMig(t, e, `{"name":"hapus lama","sql_up":"DELETE FROM produk;","irreversible_reason":"hapus permanen"}`)

	// update & delete draft v2
	rec := doJSON(e.h, "PUT", base+"/"+itoa(m2), e.editor, `{"name":"hapus lama v2","sql_up":"DELETE FROM produk;","irreversible_reason":"hapus permanen"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"hapus lama v2"`) {
		t.Fatalf("update draft: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(e.h, "DELETE", base+"/"+itoa(m2), e.editor, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete draft: %d", rec.Code)
	}
	assertJSONError(t, doJSON(e.h, "GET", base+"/"+itoa(m2), e.editor, ""), http.StatusNotFound)

	// view_only boleh baca, dilarang ubah/hapus/submit; orang tanpa akses ditolak
	if rec := doJSON(e.h, "GET", base, e.viewer, ""); rec.Code != http.StatusOK {
		t.Errorf("viewer list: %d", rec.Code)
	}
	for _, req := range [][2]string{
		{"POST", base}, {"PUT", base + "/" + itoa(m1)}, {"DELETE", base + "/" + itoa(m1)},
		{"POST", base + "/" + itoa(m1) + "/submit"}, {"POST", base + "/" + itoa(m1) + "/rollback"},
	} {
		assertJSONError(t, doJSON(e.h, req[0], req[1], e.viewer, `{"name":"x","sql_up":"SELECT 1","sql_down":"SELECT 1"}`), http.StatusForbidden)
	}
	assertJSONError(t, doJSON(e.h, "GET", base, e.other, ""), http.StatusForbidden)

	// setelah diajukan (punya baris di migration_executions), m1 menjadi immutable
	sub := doJSON(e.h, "POST", base+"/"+itoa(m1)+"/submit", e.editor, "")
	if sub.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", sub.Code, sub.Body)
	}
	det := doJSON(e.h, "GET", base+"/"+itoa(m1), e.viewer, "")
	if det.Code != http.StatusOK || !strings.Contains(det.Body.String(), `"immutable":true`) {
		t.Errorf("m1 harus immutable: %s", det.Body)
	}
	assertJSONError(t, doJSON(e.h, "PUT", base+"/"+itoa(m1), e.editor, `{"name":"ubah","sql_up":"SELECT 1","sql_down":"SELECT 1"}`), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "DELETE", base+"/"+itoa(m1), e.editor, ""), http.StatusConflict)
}

func TestWorkflowApprovalAndEnqueue(t *testing.T) {
	e := setupWorkflow(t)
	base := dbPath(e.dbID)

	m1 := createMig(t, e, `{"name":"init","sql_up":"CREATE TABLE a (id int);","sql_down":"DROP TABLE a;"}`)

	// jika salah satu group (termasuk view_only) tidak punya leader → submit ditolak 409
	e.pool.Exec(context.Background(), `UPDATE platform_groups SET leader_id = NULL WHERE id = $1`, e.viewGroupID)
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/submit", e.editor, ""), http.StatusConflict)

	// kembalikan leader group view_only
	var viewLeadID int64
	e.pool.QueryRow(context.Background(), `SELECT id FROM users WHERE username = 'lead_view'`).Scan(&viewLeadID)
	e.pool.Exec(context.Background(), `UPDATE platform_groups SET leader_id = $2 WHERE id = $1`, e.viewGroupID, viewLeadID)

	// submit sukses
	rec := doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/submit", e.editor, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}
	eid := execIDFrom(t, rec.Result(), rec.Body.Bytes())
	execPath := base + "/executions/" + itoa(eid)

	// submit dobel in-flight ditolak 409
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/submit", e.editor, ""), http.StatusConflict)
	// enqueue sebelum approved ditolak 409
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/enqueue", e.editor, ""), http.StatusConflict)
	// non-leader ditolak 403, decision salah ditolak 400
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/approve", e.editor, `{"decision":"approved"}`), http.StatusForbidden)
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/approve", e.viewer, `{"decision":"approved"}`), http.StatusForbidden)
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/approve", e.viewerLeader, `{"decision":"maybe"}`), http.StatusBadRequest)

	// cross-DB execution ID ditolak 404
	_, dsn2 := targetSchema(t)
	db2 := addTarget(t, e.h, e.admin, dsn2)
	doJSON(e.h, "PUT", "/api/db/"+itoa(db2)+"/access/"+itoa(e.editGroupID), e.admin, `{"access_level":"view_edit"}`)
	assertJSONError(t, doJSON(e.h, "GET", dbPath(db2)+"/executions/"+itoa(eid), e.editorLeader, ""), http.StatusNotFound)
	assertJSONError(t, doJSON(e.h, "POST", dbPath(db2)+"/executions/"+itoa(eid)+"/approve", e.editorLeader, `{"decision":"approved"}`), http.StatusNotFound)
	assertJSONError(t, doJSON(e.h, "POST", dbPath(db2)+"/executions/"+itoa(eid)+"/cancel", e.editorLeader, ""), http.StatusNotFound)

	// leader view_only approve pertama → masih pending_review, approve ulang 409
	if rec := doJSON(e.h, "POST", execPath+"/approve", e.viewerLeader, `{"decision":"approved"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("viewerLeader approve: %d %s", rec.Code, rec.Body)
	}
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/approve", e.viewerLeader, `{"decision":"approved"}`), http.StatusConflict)
	if det := doJSON(e.h, "GET", execPath, e.viewer, ""); !strings.Contains(det.Body.String(), `"status":"pending_review"`) {
		t.Errorf("harus masih pending_review: %s", det.Body)
	}

	// leader view_edit approve kedua → jadi approved!
	if rec := doJSON(e.h, "POST", execPath+"/approve", e.editorLeader, `{"decision":"approved"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("editorLeader approve: %d %s", rec.Code, rec.Body)
	}
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/approve", e.editorLeader, `{"decision":"rejected"}`), http.StatusConflict)

	// view_only tidak boleh enqueue
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/enqueue", e.viewerLeader, ""), http.StatusForbidden)

	// enqueue sukses → 202 + migration_jobs terbuat
	enq := doJSON(e.h, "POST", execPath+"/enqueue", e.editor, "")
	if enq.Code != http.StatusAccepted || !strings.Contains(enq.Body.String(), `"status":"pending"`) || !strings.Contains(enq.Body.String(), `"job_id":`) {
		t.Fatalf("enqueue: %d %s", enq.Code, enq.Body)
	}
	// enqueue ulang atau cancel setelah pending → 409
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/enqueue", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", execPath+"/cancel", e.editor, ""), http.StatusConflict)

	// detail execution memuat latest_execution_id, job_id, job_status, sql_snapshot, approvals
	var detail struct {
		Status            string  `json:"status"`
		SQLSnapshot       string  `json:"sql_snapshot"`
		LatestExecutionID int64   `json:"latest_execution_id"`
		JobID             *int64  `json:"job_id"`
		JobStatus         *string `json:"job_status"`
		Approvals         []struct {
			GroupName         string  `json:"group_name"`
			Status            string  `json:"status"`
			DecidedByUsername *string `json:"decided_by_username"`
		} `json:"approvals"`
	}
	detRec := doJSON(e.h, "GET", execPath, e.viewer, "")
	if detRec.Code != http.StatusOK || json.Unmarshal(detRec.Body.Bytes(), &detail) != nil {
		t.Fatalf("detail: %d %s", detRec.Code, detRec.Body)
	}
	if detail.Status != "pending" || detail.LatestExecutionID != eid || detail.JobID == nil || detail.JobStatus == nil || *detail.JobStatus != "pending" || len(detail.Approvals) != 2 || detail.SQLSnapshot == "" {
		t.Errorf("detail tidak lengkap: %+v", detail)
	}
}

func TestWorkflowRejectAndCancel(t *testing.T) {
	e := setupWorkflow(t)
	base := dbPath(e.dbID)
	m1 := createMig(t, e, `{"name":"init","sql_up":"SELECT 1","sql_down":"SELECT 1"}`)

	// satu leader reject → execution langsung rejected, leader lain dapat 409
	sub := doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/submit", e.editor, "")
	eid := execIDFrom(t, sub.Result(), sub.Body.Bytes())
	if rec := doJSON(e.h, "POST", base+"/executions/"+itoa(eid)+"/approve", e.viewerLeader, `{"decision":"rejected"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(eid)+"/approve", e.editorLeader, `{"decision":"approved"}`), http.StatusConflict)

	// submit lagi lalu cancel saat approved → 204
	sub2 := doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/submit", e.editor, "")
	eid2 := execIDFrom(t, sub2.Result(), sub2.Body.Bytes())
	doJSON(e.h, "POST", base+"/executions/"+itoa(eid2)+"/approve", e.viewerLeader, `{"decision":"approved"}`)
	doJSON(e.h, "POST", base+"/executions/"+itoa(eid2)+"/approve", e.editorLeader, `{"decision":"approved"}`)
	if rec := doJSON(e.h, "POST", base+"/executions/"+itoa(eid2)+"/cancel", e.editor, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel approved: %d %s", rec.Code, rec.Body)
	}
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(eid2)+"/enqueue", e.editor, ""), http.StatusConflict)
}

func TestRollbackForwardAndHideRollback(t *testing.T) {
	e := setupWorkflow(t)
	ctx := context.Background()
	base := dbPath(e.dbID)

	m1 := createMig(t, e, `{"name":"v1","sql_up":"SELECT 1","sql_down":"SELECT -1"}`)
	m2 := createMig(t, e, `{"name":"v2","sql_up":"SELECT 2","sql_down":"SELECT -2"}`)
	m3 := createMig(t, e, `{"name":"v3","sql_up":"SELECT 3","sql_down":"SELECT -3"}`)

	// tidak boleh submit v2 jika v1 masih draft tanpa execution
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/submit", e.editor, ""), http.StatusConflict)

	// simulasikan v1 [E1], v2 [E2], v3 [E3] berhasil
	var e1, e2, e3 int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1,$2,1,'up','SELECT 1','success') RETURNING id`, m1, e.dbID).Scan(&e1)
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1,$2,2,'up','SELECT 2','success') RETURNING id`, m2, e.dbID).Scan(&e2)
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1,$2,3,'up','SELECT 3','success') RETURNING id`, m3, e.dbID).Scan(&e3)

	// hanya v3 (active_head) yang bisa di-rollback
	if det := doJSON(e.h, "GET", base+"/migrations/"+itoa(m2), e.viewer, ""); !strings.Contains(det.Body.String(), `"can_rollback":false`) {
		t.Errorf("v2 seharusnya tidak bisa rollback: %s", det.Body)
	}
	if det := doJSON(e.h, "GET", base+"/migrations/"+itoa(m3), e.viewer, ""); !strings.Contains(det.Body.String(), `"can_rollback":true`) {
		t.Errorf("v3 seharusnya bisa rollback: %s", det.Body)
	}
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/rollback", e.editor, ""), http.StatusConflict)

	// ajukan rollback v3 lalu simulasikan sukses
	rb3Rec := doJSON(e.h, "POST", base+"/migrations/"+itoa(m3)+"/rollback", e.editor, "")
	if rb3Rec.Code != http.StatusCreated {
		t.Fatalf("rollback v3: %d %s", rb3Rec.Code, rb3Rec.Body)
	}
	rb3 := execIDFrom(t, rb3Rec.Result(), rb3Rec.Body.Bytes())
	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'success' WHERE id = $1`, rb3)

	// simulasikan juga rollback v2 sukses → sekarang active_head = v1, v2 & v3 sama-sama rolled-back
	rb2Rec := doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/rollback", e.editor, "")
	if rb2Rec.Code != http.StatusCreated {
		t.Fatalf("rollback v2: %d %s", rb2Rec.Code, rb2Rec.Body)
	}
	rb2 := execIDFrom(t, rb2Rec.Result(), rb2Rec.Body.Bytes())
	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'success' WHERE id = $1`, rb2)

	// forward v3 sebelum v2 ditolak 409
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m3)+"/submit", e.editor, ""), http.StatusConflict)
	if det := doJSON(e.h, "GET", base+"/migrations/"+itoa(m3), e.viewer, ""); !strings.Contains(det.Body.String(), `"can_forward":false`) {
		t.Errorf("v3 seharusnya can_forward=false: %s", det.Body)
	}

	// forward v2 → sukses diajukan; setelah v2 in-flight, forward v3 boleh diajukan, tapi enqueue v3 sebelum v2 di-enqueue ditolak 409
	fw2Rec := doJSON(e.h, "POST", base+"/migrations/"+itoa(m2)+"/submit", e.editor, "")
	if fw2Rec.Code != http.StatusCreated {
		t.Fatalf("forward v2: %d %s", fw2Rec.Code, fw2Rec.Body)
	}
	fw2 := execIDFrom(t, fw2Rec.Result(), fw2Rec.Body.Bytes())
	fw3Rec := doJSON(e.h, "POST", base+"/migrations/"+itoa(m3)+"/submit", e.editor, "")
	if fw3Rec.Code != http.StatusCreated {
		t.Fatalf("forward v3 dengan v2 in-flight: %d %s", fw3Rec.Code, fw3Rec.Body)
	}
	fw3 := execIDFrom(t, fw3Rec.Result(), fw3Rec.Body.Bytes())
	for _, eid := range []int64{fw2, fw3} {
		doJSON(e.h, "POST", base+"/executions/"+itoa(eid)+"/approve", e.viewerLeader, `{"decision":"approved"}`)
		doJSON(e.h, "POST", base+"/executions/"+itoa(eid)+"/approve", e.editorLeader, `{"decision":"approved"}`)
	}
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(fw3)+"/enqueue", e.editor, ""), http.StatusConflict)

	// tambah attempt gagal untuk memastikan kegagalan tetap terlihat saat hide_rollback=true
	var failID int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status, error) VALUES ($1,$2,2,'rollback','SELECT -2','failed','boom') RETURNING id`, m2, e.dbID).Scan(&failID)

	// hide_rollback=false menampilkan semua, hide_rollback=true menyembunyikan {e2,rb2} dan {e3,rb3} tapi tetap menampilkan e1, fw2, fw3, failID
	allRec := doJSON(e.h, "GET", base+"/executions?hide_rollback=false", e.viewer, "")
	hideRec := doJSON(e.h, "GET", base+"/executions?hide_rollback=true", e.viewer, "")
	var allList, hideList []struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(allRec.Body.Bytes(), &allList)
	json.Unmarshal(hideRec.Body.Bytes(), &hideList)
	if len(allList) != len(hideList)+4 {
		t.Fatalf("all=%d hide=%d (harus selisih 4: 2 pasang rollback)", len(allList), len(hideList))
	}
	hidden := map[int64]bool{e2: true, rb2: true, e3: true, rb3: true}
	seenFail := false
	for _, item := range hideList {
		if hidden[item.ID] {
			t.Errorf("id %d seharusnya disembunyikan oleh hide_rollback=true", item.ID)
		}
		if item.ID == failID {
			seenFail = true
		}
	}
	if !seenFail {
		t.Error("attempt gagal ikut tersembunyi di hide_rollback=true")
	}

	// cek auto-retry join latest_execution_id & job_status (worker menandai attempt 1 failed dulu sebelum insert attempt 2)
	if _, err := e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'failed' WHERE id = $1`, fw2); err != nil {
		t.Fatal(err)
	}
	var attempt2 int64
	if err := e.pool.QueryRow(ctx, `INSERT INTO migration_executions (request_id, migration_id, database_id, version, operation, attempt, sql_snapshot, status) VALUES ($1,$2,$3,2,'forward',2,'SELECT 2','running') RETURNING id`, fw2, m2, e.dbID).Scan(&attempt2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO migration_jobs (execution_id, status) VALUES ($1, 'running')`, attempt2); err != nil {
		t.Fatal(err)
	}
	var det struct {
		LatestExecutionID int64   `json:"latest_execution_id"`
		JobStatus         *string `json:"job_status"`
	}
	json.Unmarshal(doJSON(e.h, "GET", base+"/executions/"+itoa(fw2), e.viewer, "").Body.Bytes(), &det)
	if det.LatestExecutionID != attempt2 || det.JobStatus == nil || *det.JobStatus != "running" {
		t.Errorf("latest_execution_id/job_status = %+v, want %d/running", det, attempt2)
	}
}

func TestRetryAndCompensate(t *testing.T) {
	e := setupWorkflow(t)
	ctx := context.Background()
	base := dbPath(e.dbID)

	m1 := createMig(t, e, `{"name":"batch","sql_up":"UPDATE t SET x=1 WHERE id>$1 LIMIT $2","sql_down":"UPDATE t SET x=0 WHERE id>$1 AND id<=$2","batch_column":"id","batch_size":100}`)
	mIrr := createMig(t, e, `{"name":"irr","sql_up":"DELETE FROM t WHERE id>$1","irreversible_reason":"hapus","batch_column":"id","batch_size":100}`)

	// simulasikan batch m1 partial
	var p1 int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status, batches_committed) VALUES ($1,$2,1,'up','UPDATE t','partial',3) RETURNING id`, m1, e.dbID).Scan(&p1)

	// tidak boleh submit ulang saat masih unresolved partial
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m1)+"/submit", e.editor, ""), http.StatusConflict)

	// retry sukses + in-flight duplikat 409
	r1Rec := doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/retry", e.editor, "")
	if r1Rec.Code != http.StatusCreated {
		t.Fatalf("retry: %d %s", r1Rec.Code, r1Rec.Body)
	}
	r1 := execIDFrom(t, r1Rec.Result(), r1Rec.Body.Bytes())
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/retry", e.editor, ""), http.StatusConflict)

	// cancel r1, lalu ajukan compensate atas p1
	doJSON(e.h, "POST", base+"/executions/"+itoa(r1)+"/cancel", e.editor, "")
	c1Rec := doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/compensate", e.editor, "")
	if c1Rec.Code != http.StatusCreated {
		t.Fatalf("compensate: %d %s", c1Rec.Code, c1Rec.Body)
	}
	c1 := execIDFrom(t, c1Rec.Result(), c1Rec.Body.Bytes())

	// simulasikan c1 menjadi partially_reverted (menggantikan p1 sebagai unresolved terbaru)
	e.pool.Exec(ctx, `UPDATE migration_executions SET status = 'partially_reverted' WHERE id = $1`, c1)

	// p1 sudah tidak unresolved → retry/compensate ke p1 ditolak 409; retry ke partially_reverted juga 409
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/retry", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(p1)+"/compensate", e.editor, ""), http.StatusConflict)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(c1)+"/retry", e.editor, ""), http.StatusConflict)

	// compensate lanjutan atas c1 (partially_reverted) sukses dan mempertahankan parent_execution_id = p1
	c2Rec := doJSON(e.h, "POST", base+"/executions/"+itoa(c1)+"/compensate", e.editor, "")
	if c2Rec.Code != http.StatusCreated {
		t.Fatalf("compensate lanjutan: %d %s", c2Rec.Code, c2Rec.Body)
	}
	c2 := execIDFrom(t, c2Rec.Result(), c2Rec.Body.Bytes())
	var parentID, revID *int64
	e.pool.QueryRow(ctx, `SELECT parent_execution_id, reverted_execution_id FROM migration_executions WHERE id = $1`, c2).Scan(&parentID, &revID)
	if parentID == nil || *parentID != p1 || revID != nil {
		t.Errorf("compensate c2 parent=%v rev=%v, want parent=%d rev=nil", parentID, revID, p1)
	}

	// compensate pada rollback partially_reverted harus mempertahankan reverted_execution_id = active.id
	m3 := createMig(t, e, `{"name":"batch-rb","sql_up":"UPDATE t SET y=1 WHERE id>$1 LIMIT $2","sql_down":"UPDATE t SET y=0 WHERE id>$1 AND id<=$2","batch_column":"id","batch_size":100}`)
	var actID, rbPartial int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1,$2,3,'up','UPDATE t','success') RETURNING id`, m3, e.dbID).Scan(&actID)
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status, reverted_execution_id, parent_execution_id) VALUES ($1,$2,3,'rollback','UPDATE t','partially_reverted',$3,$3) RETURNING id`, m3, e.dbID, actID).Scan(&rbPartial)
	// rollback baru ditolak selama partially_reverted belum selesai
	assertJSONError(t, doJSON(e.h, "POST", base+"/migrations/"+itoa(m3)+"/rollback", e.editor, ""), http.StatusConflict)
	cRbRec := doJSON(e.h, "POST", base+"/executions/"+itoa(rbPartial)+"/compensate", e.editor, "")
	if cRbRec.Code != http.StatusCreated {
		t.Fatalf("compensate rollback partially_reverted: %d %s", cRbRec.Code, cRbRec.Body)
	}
	cRb := execIDFrom(t, cRbRec.Result(), cRbRec.Body.Bytes())
	e.pool.QueryRow(ctx, `SELECT parent_execution_id, reverted_execution_id FROM migration_executions WHERE id = $1`, cRb).Scan(&parentID, &revID)
	if parentID == nil || *parentID != actID || revID == nil || *revID != actID {
		t.Errorf("compensate rollback parent=%v rev=%v, want %d/%d", parentID, revID, actID, actID)
	}

	// compensate pada migration irreversible ditolak 409
	var pIrr int64
	e.pool.QueryRow(ctx, `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status, batches_committed) VALUES ($1,$2,2,'up','DELETE FROM t','partial',1) RETURNING id`, mIrr, e.dbID).Scan(&pIrr)
	assertJSONError(t, doJSON(e.h, "POST", base+"/executions/"+itoa(pIrr)+"/compensate", e.editor, ""), http.StatusConflict)
}
