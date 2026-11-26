package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func jsonID(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	var v struct{ ID int64 }
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil || v.ID == 0 {
		t.Fatalf("response tanpa id: %v", err)
	}
	return v.ID
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestGroupAPI(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	budi := addUser(t, pool, "budi", "rahasia")
	siti := addUser(t, pool, "siti", "rahasia")

	// create dengan leader_id otomatis memasukkan leader ke group_members
	rec := doJSON(h, "POST", "/api/groups", admin, `{"name":"backend","description":"tim backend","leader_id":`+itoa(budi)+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"leader_username":"budi"`) {
		t.Errorf("leader_username hilang: %s", rec.Body)
	}
	gid := jsonID(t, rec)
	base := "/api/groups/" + itoa(gid)

	if rec := doJSON(h, "GET", base+"/members", admin, ""); !strings.Contains(rec.Body.String(), `"username":"budi"`) {
		t.Errorf("leader tidak otomatis jadi member: %s", rec.Body)
	}

	failures := []struct {
		name, method, path, body string
		status                   int
	}{
		{"nama duplikat", "POST", "/api/groups", `{"name":"backend"}`, http.StatusConflict},
		{"nama kosong", "POST", "/api/groups", `{"name":"  "}`, http.StatusBadRequest},
		{"leader_id nol", "POST", "/api/groups", `{"name":"x","leader_id":0}`, http.StatusBadRequest},
		{"leader tidak ada", "POST", "/api/groups", `{"name":"x","leader_id":999999}`, http.StatusConflict},
		{"JSON rusak", "POST", "/api/groups", `{"name":`, http.StatusBadRequest},
		{"update group tidak ada", "PUT", "/api/groups/999999", `{"name":"x"}`, http.StatusNotFound},
		{"id bukan angka", "DELETE", "/api/groups/abc", ``, http.StatusBadRequest},
		{"anggota user tidak ada", "POST", base + "/members", `{"user_id":999999}`, http.StatusConflict},
		{"anggota user_id kosong", "POST", base + "/members", `{}`, http.StatusBadRequest},
	}
	for _, f := range failures {
		assertJSONError(t, doJSON(h, f.method, f.path, admin, f.body), f.status)
	}

	// update tanpa leader_id mempertahankan leader yang ada
	if rec := doJSON(h, "PUT", base, admin, `{"name":"backend-1","description":"tetap"}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"leader_username":"budi"`) {
		t.Errorf("update tanpa leader_id seharusnya mempertahankan leader: %d %s", rec.Code, rec.Body)
	}

	// update ganti leader ke siti → otomatis masuk group_members
	if rec := doJSON(h, "PUT", base, admin, `{"name":"backend-2","description":"baru","leader_id":`+itoa(siti)+`}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"name":"backend-2"`) || !strings.Contains(rec.Body.String(), `"leader_username":"siti"`) {
		t.Errorf("update: status %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(h, "GET", "/api/groups", admin, ""); !strings.Contains(rec.Body.String(), `"leader_username":"siti"`) {
		t.Errorf("group tidak muncul di list dengan leader: %s", rec.Body)
	}
	if rec := doJSON(h, "GET", base+"/members", admin, ""); !strings.Contains(rec.Body.String(), `"username":"siti"`) {
		t.Errorf("leader baru tidak otomatis jadi member: %s", rec.Body)
	}

	// hapus siti (leader saat ini) → leader_id di group harus jadi NULL
	if rec := doJSON(h, "DELETE", base+"/members/"+itoa(siti), admin, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove leader member: status %d", rec.Code)
	}
	if rec := doJSON(h, "GET", "/api/groups", admin, ""); !strings.Contains(rec.Body.String(), `"leader_id":null`) {
		t.Errorf("leader_id tidak di-clear saat leader dikeluarkan dari group: %s", rec.Body)
	}
	assertJSONError(t, doJSON(h, "DELETE", base+"/members/"+itoa(siti), admin, ""), http.StatusNotFound)

	// member dobel
	if rec := doJSON(h, "POST", base+"/members", admin, `{"user_id":`+itoa(budi)+`}`); rec.Code != http.StatusConflict {
		t.Errorf("member dobel: status %d", rec.Code)
	}

	if rec := doJSON(h, "DELETE", base, admin, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d", rec.Code)
	}
	assertJSONError(t, doJSON(h, "DELETE", base, admin, ""), http.StatusNotFound)
}

func TestDatabaseAPI(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	_, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	base := "/api/db/" + itoa(id)

	for _, body := range []string{
		`{"name":"x","connection_url":"mysql://u:p@host/db"}`,
		`{"name":"x","connection_url":"postgres://host/db"}`,
		`{"name":"","connection_url":"postgres://u:p@host/db"}`,
	} {
		assertJSONError(t, doJSON(h, "POST", "/api/db", admin, body), http.StatusBadRequest)
	}

	rec := doJSON(h, "GET", "/api/db", admin, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":`+itoa(id)) {
		t.Fatalf("list: status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "postgres:") || strings.Contains(rec.Body.String(), "connection_url") {
		t.Error("credential koneksi bocor di list")
	}

	if rec := doJSON(h, "POST", base+"/test", admin, ""); rec.Code != http.StatusOK {
		t.Errorf("tes koneksi valid: status %d %s", rec.Code, rec.Body)
	}
	body := `{"name":"mati","host_label":"x","connection_url":"postgres://u:rahasiaDB@127.0.0.1:1/x"}`
	if rec := doJSON(h, "PUT", base, admin, body); rec.Code != http.StatusOK {
		t.Fatalf("update: status %d", rec.Code)
	}
	rec = doJSON(h, "POST", base+"/test", admin, "")
	assertJSONError(t, rec, http.StatusBadGateway)
	if strings.Contains(rec.Body.String(), "rahasiaDB") {
		t.Error("password bocor di error test koneksi")
	}
	assertJSONError(t, doJSON(h, "PUT", "/api/db/999999", admin, body), http.StatusNotFound)

	// database dengan riwayat migration_executions tidak boleh dihapus (409)
	id2 := addTarget(t, h, admin, dsn)
	var mid int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO migrations (database_id, version, name, sql, sql_down) VALUES ($1, 1, 'm1', 'SELECT 1', 'SELECT 1') RETURNING id`, id2).Scan(&mid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1, $2, 1, 'up', 'SELECT 1', 'success')`, mid, id2); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(h, "DELETE", "/api/db/"+itoa(id2), admin, "")
	assertJSONError(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "migration") {
		t.Errorf("pesan 409 tidak menyebut riwayat migration: %s", rec.Body)
	}

	if rec := doJSON(h, "DELETE", base, admin, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d", rec.Code)
	}
	assertJSONError(t, doJSON(h, "DELETE", base, admin, ""), http.StatusNotFound)
}

func TestAccessAPI(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	_, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	budi := addUser(t, pool, "budi", "rahasia")
	gid := jsonID(t, doJSON(h, "POST", "/api/groups", admin, `{"name":"dev"}`))
	doJSON(h, "POST", "/api/groups/"+itoa(gid)+"/members", admin, `{"user_id":`+itoa(budi)+`}`)
	path := "/api/db/" + itoa(id) + "/access/" + itoa(gid)
	sid := login(t, h, "budi", "rahasia")

	for _, body := range []string{`{"access_level":"admin"}`, `{"access_level":""}`, `{}`} {
		assertJSONError(t, doJSON(h, "PUT", path, admin, body), http.StatusBadRequest)
	}
	assertJSONError(t, doJSON(h, "PUT", "/api/db/"+itoa(id)+"/access/999999", admin, `{"access_level":"view_only"}`), http.StatusConflict)

	if rec := doJSON(h, "PUT", path, admin, `{"access_level":"view_only"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("grant: status %d", rec.Code)
	}
	if rec := doJSON(h, "GET", "/api/db/"+itoa(id)+"/access", admin, ""); !strings.Contains(rec.Body.String(), `"access_level":"view_only"`) {
		t.Error("grant tidak muncul di list")
	}
	rec := doJSON(h, "GET", dbPath(id), sid, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("view_only buka database: status %d %s", rec.Code, rec.Body)
	}
	if c := cookie(rec); c != nil {
		sid = c.Value
	}
	assertJSONError(t, doJSON(h, "POST", dbPath(id)+"/migrations", sid, `{"name":"x","sql_up":"SELECT 1","sql_down":"SELECT 1"}`), http.StatusForbidden)

	doJSON(h, "PUT", path, admin, `{"access_level":"view_edit"}`)
	rec = doJSON(h, "POST", dbPath(id)+"/migrations", sid, `{"name":"x","sql_up":"SELECT 1","sql_down":"SELECT 1"}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("setelah naik ke view_edit: status %d %s", rec.Code, rec.Body)
	}
	if c := cookie(rec); c != nil {
		sid = c.Value
	}

	if rec := doJSON(h, "DELETE", path, admin, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: status %d", rec.Code)
	}
	if loggedIn(h, sid) {
		t.Error("session masih aktif setelah akses dicabut")
	}
	sid = login(t, h, "budi", "rahasia")
	assertJSONError(t, doJSON(h, "GET", dbPath(id), sid, ""), http.StatusForbidden)
	assertJSONError(t, doJSON(h, "DELETE", path, admin, ""), http.StatusNotFound)
}

func TestRevokeEndsSession(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	budi := addUser(t, pool, "budi", "rahasia")
	siti := addUser(t, pool, "siti", "rahasia")
	gid := jsonID(t, doJSON(h, "POST", "/api/groups", admin, `{"name":"dev"}`))
	base := "/api/groups/" + itoa(gid)
	for _, uid := range []int64{budi, siti} {
		doJSON(h, "POST", base+"/members", admin, `{"user_id":`+itoa(uid)+`}`)
	}
	budiSid := login(t, h, "budi", "rahasia")
	sitiSid := login(t, h, "siti", "rahasia")

	doJSON(h, "DELETE", base+"/members/"+itoa(budi), admin, "")
	if loggedIn(h, budiSid) {
		t.Error("session budi masih aktif setelah dikeluarkan dari group")
	}
	if !loggedIn(h, sitiSid) {
		t.Error("session siti ikut hilang padahal masih anggota")
	}

	doJSON(h, "DELETE", base, admin, "")
	if loggedIn(h, sitiSid) {
		t.Error("session siti masih aktif setelah group dihapus")
	}
	if !loggedIn(h, admin) {
		t.Error("session admin ikut hilang")
	}
}

func TestAdminRoutesNeedAdmin(t *testing.T) {
	h, pool := testServer(t)
	addUser(t, pool, "budi", "rahasia")
	budi := login(t, h, "budi", "rahasia")

	routes := [][2]string{
		{"GET", "/api/users"}, {"POST", "/api/users"},
		{"PUT", "/api/users/1/password"}, {"PUT", "/api/users/1/admin"}, {"DELETE", "/api/users/1"},
		{"GET", "/api/groups"}, {"POST", "/api/groups"}, {"PUT", "/api/groups/1"}, {"DELETE", "/api/groups/1"},
		{"GET", "/api/groups/1/members"}, {"POST", "/api/groups/1/members"}, {"DELETE", "/api/groups/1/members/1"},
		{"GET", "/api/db"}, {"POST", "/api/db"}, {"PUT", "/api/db/1"}, {"DELETE", "/api/db/1"}, {"POST", "/api/db/1/test"},
		{"GET", "/api/db/1/access"}, {"PUT", "/api/db/1/access/1"}, {"DELETE", "/api/db/1/access/1"},
	}
	for _, r := range routes {
		assertJSONError(t, doJSON(h, r[0], r[1], budi, `{"name":"x"}`), http.StatusForbidden)
		assertJSONError(t, doJSON(h, r[0], r[1], "", `{"name":"x"}`), http.StatusUnauthorized)
	}
	assertJSONError(t, doJSON(h, "GET", "/api/tidak-ada", budi, ""), http.StatusNotFound)

	var n int
	pool.QueryRow(t.Context(), "SELECT count(*) FROM platform_groups").Scan(&n)
	if n != 0 {
		t.Error("group terbuat oleh non-admin")
	}
}
