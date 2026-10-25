package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ambil field id dari response JSON
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

	rec := doJSON(h, "POST", "/api/groups", admin, `{"name":"backend","description":"tim backend"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d", rec.Code)
	}
	gid := jsonID(t, rec)
	base := "/api/groups/" + itoa(gid)

	failures := []struct {
		name, method, path, body string
		status                   int
	}{
		{"nama duplikat", "POST", "/api/groups", `{"name":"backend"}`, http.StatusConflict},
		{"nama kosong", "POST", "/api/groups", `{"name":"  "}`, http.StatusBadRequest},
		{"field asing", "POST", "/api/groups", `{"name":"x","role":"admin"}`, http.StatusBadRequest},
		{"JSON rusak", "POST", "/api/groups", `{"name":`, http.StatusBadRequest},
		{"update group tidak ada", "PUT", "/api/groups/999999", `{"name":"x"}`, http.StatusNotFound},
		{"id bukan angka", "DELETE", "/api/groups/abc", ``, http.StatusBadRequest},
		{"anggota user tidak ada", "POST", base + "/members", `{"user_id":999999}`, http.StatusConflict},
		{"anggota user_id kosong", "POST", base + "/members", `{}`, http.StatusBadRequest},
	}
	for _, f := range failures {
		if rec := doJSON(h, f.method, f.path, admin, f.body); rec.Code != f.status {
			t.Errorf("%s: status %d, want %d", f.name, rec.Code, f.status)
		}
	}

	if rec := doJSON(h, "PUT", base, admin, `{"name":"backend-2","description":"baru"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "backend-2") {
		t.Errorf("update: status %d", rec.Code)
	}
	if rec := doJSON(h, "GET", "/api/groups", admin, ""); !strings.Contains(rec.Body.String(), "backend-2") {
		t.Error("group tidak muncul di list")
	}

	member := `{"user_id":` + itoa(budi) + `}`
	if rec := doJSON(h, "POST", base+"/members", admin, member); rec.Code != http.StatusCreated {
		t.Fatalf("add member: status %d", rec.Code)
	}
	if rec := doJSON(h, "POST", base+"/members", admin, member); rec.Code != http.StatusConflict {
		t.Errorf("member dobel: status %d", rec.Code)
	}
	if rec := doJSON(h, "GET", base+"/members", admin, ""); !strings.Contains(rec.Body.String(), "budi") {
		t.Error("budi tidak muncul di anggota")
	}
	path := base + "/members/" + itoa(budi)
	if rec := doJSON(h, "DELETE", path, admin, ""); rec.Code != http.StatusNoContent {
		t.Errorf("remove member: status %d", rec.Code)
	}
	if rec := doJSON(h, "DELETE", path, admin, ""); rec.Code != http.StatusNotFound {
		t.Errorf("remove member dua kali: status %d", rec.Code)
	}

	if rec := doJSON(h, "DELETE", base, admin, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d", rec.Code)
	}
	if rec := doJSON(h, "DELETE", base, admin, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete dua kali: status %d", rec.Code)
	}
}

func TestDatabaseAPI(t *testing.T) {
	h, _ := testServer(t)
	admin := login(t, h, "admin", "admin")
	_, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	base := "/api/db/" + itoa(id)

	for _, body := range []string{
		`{"name":"x","connection_url":"mysql://u:p@host/db"}`,
		`{"name":"x","connection_url":"postgres://host/db"}`,
		`{"name":"","connection_url":"postgres://u:p@host/db"}`,
	} {
		if rec := doJSON(h, "POST", "/api/db", admin, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
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
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "rahasiaDB") {
		t.Errorf("tes koneksi mati: status %d", rec.Code)
	}
	if rec := doJSON(h, "PUT", "/api/db/999999", admin, body); rec.Code != http.StatusNotFound {
		t.Errorf("update database tidak ada: status %d", rec.Code)
	}

	if rec := doJSON(h, "DELETE", base, admin, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d", rec.Code)
	}
	if rec := doJSON(h, "DELETE", base, admin, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete dua kali: status %d", rec.Code)
	}
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
		if rec := doJSON(h, "PUT", path, admin, body); rec.Code != http.StatusBadRequest {
			t.Errorf("level %s: status %d", body, rec.Code)
		}
	}
	if rec := doJSON(h, "PUT", "/api/db/"+itoa(id)+"/access/999999", admin, `{"access_level":"view_only"}`); rec.Code != http.StatusConflict {
		t.Errorf("group tidak ada: status %d", rec.Code)
	}

	if rec := doJSON(h, "PUT", path, admin, `{"access_level":"view_only"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("grant: status %d", rec.Code)
	}
	if rec := doJSON(h, "GET", "/api/db/"+itoa(id)+"/access", admin, ""); !strings.Contains(rec.Body.String(), `"access_level":"view_only"`) {
		t.Error("grant tidak muncul di list")
	}
	rec := do(h, "GET", dbPath(id), sid, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("view_only buka database: status %d", rec.Code)
	}
	if c := cookie(rec); c != nil {
		sid = c.Value
	}
	if rec := do(h, "GET", dbPath(id)+"/migrations/new", sid, nil); rec.Code != http.StatusForbidden {
		t.Errorf("view_only buka editor migration: status %d", rec.Code)
	}

	doJSON(h, "PUT", path, admin, `{"access_level":"view_edit"}`)
	rec = do(h, "GET", dbPath(id)+"/migrations/new", sid, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("setelah naik ke view_edit: status %d", rec.Code)
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
	if rec := do(h, "GET", dbPath(id), sid, nil); rec.Code != http.StatusForbidden {
		t.Errorf("akses masih ada setelah dicabut: status %d", rec.Code)
	}
	if rec := doJSON(h, "DELETE", path, admin, ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoke dua kali: status %d", rec.Code)
	}
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
		{"GET", "/admin/users"}, {"GET", "/admin/groups"}, {"GET", "/admin/databases"},
		{"GET", "/api/groups"}, {"POST", "/api/groups"}, {"PUT", "/api/groups/1"}, {"DELETE", "/api/groups/1"},
		{"GET", "/api/groups/1/members"}, {"POST", "/api/groups/1/members"}, {"DELETE", "/api/groups/1/members/1"},
		{"GET", "/api/db"}, {"POST", "/api/db"}, {"PUT", "/api/db/1"}, {"DELETE", "/api/db/1"}, {"POST", "/api/db/1/test"},
		{"GET", "/api/db/1/access"}, {"PUT", "/api/db/1/access/1"}, {"DELETE", "/api/db/1/access/1"},
	}
	for _, r := range routes {
		if rec := doJSON(h, r[0], r[1], budi, `{"name":"x"}`); rec.Code != http.StatusForbidden {
			t.Errorf("non-admin %s %s: status %d", r[0], r[1], rec.Code)
		}
		if rec := doJSON(h, r[0], r[1], "", `{"name":"x"}`); rec.Code != http.StatusSeeOther {
			t.Errorf("tanpa session %s %s: status %d", r[0], r[1], rec.Code)
		}
	}
	var n int
	pool.QueryRow(t.Context(), "SELECT count(*) FROM platform_groups").Scan(&n)
	if n != 0 {
		t.Error("group terbuat oleh non-admin")
	}
}

func TestAdminPages(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	_, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	budi := addUser(t, pool, "budi", "rahasia")
	grant(t, pool, budi, id, "view_edit")

	groups := do(h, "GET", "/admin/groups", admin, nil)
	if groups.Code != http.StatusOK || !strings.Contains(groups.Body.String(), "budi") || !strings.Contains(groups.Body.String(), `data-api="POST /api/groups"`) {
		t.Errorf("halaman group: status %d", groups.Code)
	}
	dbs := do(h, "GET", "/admin/databases", admin, nil)
	body := dbs.Body.String()
	if dbs.Code != http.StatusOK || !strings.Contains(body, "View + Edit") || !strings.Contains(body, "/api/db/"+itoa(id)+"/test") {
		t.Errorf("halaman database: status %d", dbs.Code)
	}
	if strings.Contains(body, "search_path") {
		t.Error("connection url tampil di halaman admin")
	}
	if !strings.Contains(do(h, "GET", "/", admin, nil).Body.String(), "/admin/databases") {
		t.Error("beranda admin tidak punya link ke halaman database")
	}
}
