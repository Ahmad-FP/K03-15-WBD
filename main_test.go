package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/authentication"
	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testServer(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ToLower(rand.Text())
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
	return newServer(pool), pool
}

func doJSON(h http.Handler, method, path, sid, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if sid != "" {
		req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func mustStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) *httptest.ResponseRecorder {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid json %q: %v", rec.Body.String(), err)
	}
	return v
}

func cookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == session.CookieName {
			return c
		}
	}
	return nil
}

func login(t *testing.T, h http.Handler, username, password string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"username": username, "password": password})
	rec := mustStatus(t, doJSON(h, "POST", "/api/auth/login", "", string(b)), http.StatusOK)
	c := cookie(rec)
	if c == nil {
		t.Fatalf("login %s: missing session cookie", username)
	}
	return c.Value
}

func loggedIn(h http.Handler, sid string) bool {
	return doJSON(h, "GET", "/api/auth/me", sid, "").Code == http.StatusOK
}

func addUser(t *testing.T, pool *pgxpool.Pool, username, password string) int64 {
	t.Helper()
	hash, err := authentication.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id", username, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func jsonID(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	v := decode[struct{ ID int64 }](t, rec)
	if v.ID == 0 {
		t.Fatalf("response tanpa id: %s", rec.Body)
	}
	return v.ID
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func dbPath(id int64) string { return "/api/databases/" + itoa(id) }

func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Errorf("status %d, want %d (body: %s)", rec.Code, wantStatus, rec.Body)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == "" {
		t.Errorf("expected JSON error body, got %q", rec.Body.String())
	}
}

func addTarget(t *testing.T, h http.Handler, admin, dsn string) int64 {
	t.Helper()
	t.Setenv("DATABASE_CREDENTIALS_KEY", strings.Repeat("ab", 32))
	body, _ := json.Marshal(map[string]string{"name": "target_" + rand.Text()[:6], "host_label": "test", "connection_url": dsn})
	return jsonID(t, mustStatus(t, doJSON(h, "POST", "/api/db", admin, string(body)), http.StatusCreated))
}

func targetSchema(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	schema := "tgt_" + strings.ToLower(rand.Text())
	base := os.Getenv("TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "search_path=" + schema
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	})
	return pool, dsn
}

func grant(t *testing.T, pool *pgxpool.Pool, userID, dbID int64, level string) int64 {
	t.Helper()
	ctx := context.Background()
	var gid int64
	if err := pool.QueryRow(ctx, "INSERT INTO platform_groups (name) VALUES ($1) RETURNING id", "g_"+rand.Text()).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	pool.Exec(ctx, "INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)", gid, userID)
	pool.Exec(ctx, "INSERT INTO group_database_access (group_id, database_id, access_level) VALUES ($1, $2, $3)", gid, dbID, level)
	return gid
}

func TestAuthSessionAndSPA(t *testing.T) {
	h, pool := testServer(t)
	ctx := context.Background()

	if _, err := os.Stat("frontend/dist/index.html"); err == nil {
		for _, p := range []string{"/", "/migrations/42"} {
			rec := mustStatus(t, doJSON(h, "GET", p, "", ""), http.StatusOK)
			if !strings.Contains(rec.Body.String(), `<div id="root">`) {
				t.Errorf("SPA %s tidak memuat root", p)
			}
		}
	}
	assertJSONError(t, doJSON(h, "GET", "/api/tidak-ada", "", ""), http.StatusNotFound)

	for _, sid := range []string{"", "tebakan"} {
		assertJSONError(t, doJSON(h, "GET", "/api/auth/me", sid, ""), http.StatusUnauthorized)
	}
	assertJSONError(t, doJSON(h, "POST", "/api/users", "", `{"username":"budi","password":"rahasia"}`), http.StatusUnauthorized)

	rec := mustStatus(t, doJSON(h, "POST", "/api/auth/login", "", `{"username":"admin","password":"admin"}`), http.StatusOK)
	c := cookie(rec)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie tidak valid: %+v", c)
	}
	me := mustStatus(t, doJSON(h, "GET", "/api/auth/me", c.Value, ""), http.StatusOK)
	if !strings.Contains(me.Body.String(), `"username":"admin"`) || !strings.Contains(me.Body.String(), `"is_admin":true`) || strings.Contains(me.Body.String(), c.Value) {
		t.Fatalf("me body salah: %s", me.Body)
	}
	for _, creds := range [][2]string{{"admin", "pw-keliru"}, {"tidakada", "admin"}} {
		b, _ := json.Marshal(map[string]string{"username": creds[0], "password": creds[1]})
		bad := doJSON(h, "POST", "/api/auth/login", "", string(b))
		assertJSONError(t, bad, http.StatusUnauthorized)
		if cookie(bad) != nil || strings.Contains(bad.Body.String(), creds[1]) {
			t.Errorf("login gagal %v bocor cookie/password", creds)
		}
	}

	sid := c.Value
	for _, path := range []string{"/api/auth/login", "/api/auth/logout"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"username":"admin","password":"admin"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
		r := httptest.NewRecorder()
		h.ServeHTTP(r, req)
		if r.Code != http.StatusForbidden || cookie(r) != nil {
			t.Errorf("cross-site %s: status %d", path, r.Code)
		}
	}
	if !loggedIn(h, sid) {
		t.Fatal("cross-site logout menutup session")
	}

	var created time.Time
	pool.QueryRow(ctx, "SELECT created_at FROM sessions WHERE id = $1", sid).Scan(&created)
	pool.Exec(ctx, "UPDATE sessions SET rotated_at = $1 WHERE id = $2", time.Now().Add(-session.RotateEvery-time.Minute), sid)
	rot := mustStatus(t, doJSON(h, "GET", "/api/auth/me", sid, ""), http.StatusOK)
	if cookie(rot) == nil || cookie(rot).Value == sid {
		t.Fatalf("session tidak berotasi: %v", cookie(rot))
	}
	current := cookie(rot).Value
	if cookie(mustStatus(t, doJSON(h, "GET", "/api/auth/me", current, ""), http.StatusOK)) != nil {
		t.Error("session baru langsung berotasi lagi")
	}
	if grace := mustStatus(t, doJSON(h, "GET", "/api/auth/me", sid, ""), http.StatusOK); cookie(grace) == nil || cookie(grace).Value != current {
		t.Errorf("grace period gagal: %v", cookie(grace))
	}
	var after time.Time
	pool.QueryRow(ctx, "SELECT created_at FROM sessions WHERE id = $1", current).Scan(&after)
	if !after.Equal(created) {
		t.Error("rotasi mengubah created_at")
	}
	pool.Exec(ctx, "UPDATE sessions SET prev_until = $1 WHERE id = $2", time.Now().Add(-time.Second), current)
	if loggedIn(h, sid) || !loggedIn(h, current) {
		t.Error("grace period expiry salah")
	}

	pool.Exec(ctx, "UPDATE sessions SET rotated_at = 'epoch' WHERE id = $1", current)
	out := mustStatus(t, doJSON(h, "POST", "/api/auth/logout", current, ""), http.StatusNoContent)
	if cookies := out.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge >= 0 || loggedIn(h, current) {
		t.Fatalf("logout tidak bersih: %+v", cookies)
	}

	now := time.Now()
	sIdle := login(t, h, "admin", "admin")
	pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $1 WHERE id = $2", now.Add(-session.IdleTimeout-time.Minute), sIdle)
	sAbs := login(t, h, "admin", "admin")
	pool.Exec(ctx, "UPDATE sessions SET created_at = $1, rotated_at = $2 WHERE id = $3", now.Add(-session.MaxAge-time.Minute), now, sAbs)
	if loggedIn(h, sIdle) || loggedIn(h, sAbs) {
		t.Error("session kadaluarsa masih diterima")
	}
}

func TestUsersAndPrivileges(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")

	rec := mustStatus(t, doJSON(h, "POST", "/api/users", admin, `{"username":"budi","password":"rahasia","is_admin":false}`), http.StatusCreated)
	if strings.Contains(rec.Body.String(), "rahasia") {
		t.Error("password bocor di response")
	}
	id := jsonID(t, rec)
	if list := mustStatus(t, doJSON(h, "GET", "/api/users", admin, ""), http.StatusOK); !strings.Contains(list.Body.String(), `"username":"budi"`) {
		t.Errorf("user baru tidak tampil: %s", list.Body)
	}
	var hash string
	var isAdmin bool
	pool.QueryRow(t.Context(), "SELECT password_hash, is_admin FROM users WHERE id = $1", id).Scan(&hash, &isAdmin)
	if hash == "rahasia" || !authentication.Check(hash, "rahasia") || isAdmin {
		t.Error("hash/role user tidak sesuai")
	}

	longPw := strings.Repeat("x", 73)
	assertJSONError(t, doJSON(h, "POST", "/api/users", admin, `{"username":"budi","password":"x"}`), http.StatusConflict)
	assertJSONError(t, doJSON(h, "POST", "/api/users", admin, `{"username":"","password":"x"}`), http.StatusBadRequest)
	assertJSONError(t, doJSON(h, "POST", "/api/users", admin, `{"username":"cici","password":""}`), http.StatusBadRequest)
	assertJSONError(t, doJSON(h, "POST", "/api/users", admin, `{"username":"long","password":"`+longPw+`"}`), http.StatusBadRequest)

	path := "/api/users/" + itoa(id)
	mustStatus(t, doJSON(h, "PUT", path+"/password", admin, `{"password":"baru"}`), http.StatusNoContent)
	assertJSONError(t, doJSON(h, "PUT", path+"/password", admin, `{"password":""}`), http.StatusBadRequest)
	assertJSONError(t, doJSON(h, "PUT", path+"/password", admin, `{"password":"`+longPw+`"}`), http.StatusBadRequest)
	assertJSONError(t, doJSON(h, "PUT", "/api/users/999999/password", admin, `{"password":"x"}`), http.StatusNotFound)
	assertJSONError(t, doJSON(h, "POST", "/api/auth/login", "", `{"username":"budi","password":"rahasia"}`), http.StatusUnauthorized)

	budi := login(t, h, "budi", "baru")
	assertJSONError(t, doJSON(h, "PUT", path+"/admin", admin, `{}`), http.StatusBadRequest)
	mustStatus(t, doJSON(h, "PUT", path+"/admin", admin, `{"is_admin":true}`), http.StatusNoContent)
	prom := mustStatus(t, doJSON(h, "GET", "/api/auth/me", budi, ""), http.StatusOK)
	if cookie(prom) == nil || cookie(prom).Value == budi || !strings.Contains(prom.Body.String(), `"is_admin":true`) {
		t.Fatalf("promosi admin tidak merotasi: %s", prom.Body)
	}
	budi = cookie(prom).Value
	mustStatus(t, doJSON(h, "PUT", path+"/admin", admin, `{"is_admin":false}`), http.StatusNoContent)
	if loggedIn(h, budi) {
		t.Error("demote admin tidak mencabut session")
	}

	budi = login(t, h, "budi", "baru")
	mustStatus(t, doJSON(h, "DELETE", path, admin, ""), http.StatusNoContent)
	assertJSONError(t, doJSON(h, "DELETE", path, admin, ""), http.StatusNotFound)
	if loggedIn(h, budi) || doJSON(h, "POST", "/api/auth/login", "", `{"username":"budi","password":"baru"}`).Code != http.StatusUnauthorized {
		t.Error("user terhapus masih punya akses")
	}
}

func TestAdminGroupsDBsAndAccess(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	budi := addUser(t, pool, "budi", "rahasia")
	siti := addUser(t, pool, "siti", "rahasia")

	gRec := mustStatus(t, doJSON(h, "POST", "/api/groups", admin, `{"name":"backend","description":"tim","leader_id":`+itoa(budi)+`}`), http.StatusCreated)
	if !strings.Contains(gRec.Body.String(), `"leader_username":"budi"`) {
		t.Errorf("leader_username hilang: %s", gRec.Body)
	}
	gid := jsonID(t, gRec)
	gBase := "/api/groups/" + itoa(gid)
	if !strings.Contains(doJSON(h, "GET", gBase+"/members", admin, "").Body.String(), `"username":"budi"`) {
		t.Error("leader tidak otomatis jadi member")
	}

	for _, f := range []struct {
		m, p, b string
		want    int
	}{
		{"POST", "/api/groups", `{"name":"backend"}`, http.StatusConflict},
		{"POST", "/api/groups", `{"name":"  "}`, http.StatusBadRequest},
		{"POST", "/api/groups", `{"name":"x","leader_id":0}`, http.StatusBadRequest},
		{"POST", "/api/groups", `{"name":"x","leader_id":999999}`, http.StatusConflict},
		{"POST", "/api/groups", `{"name":`, http.StatusBadRequest},
		{"PUT", "/api/groups/999999", `{"name":"x"}`, http.StatusNotFound},
		{"DELETE", "/api/groups/abc", ``, http.StatusBadRequest},
		{"POST", gBase + "/members", `{"user_id":999999}`, http.StatusConflict},
		{"POST", gBase + "/members", `{}`, http.StatusBadRequest},
	} {
		assertJSONError(t, doJSON(h, f.m, f.p, admin, f.b), f.want)
	}

	if rec := mustStatus(t, doJSON(h, "PUT", gBase, admin, `{"name":"backend-1","description":"tetap"}`), http.StatusOK); !strings.Contains(rec.Body.String(), `"leader_username":"budi"`) {
		t.Errorf("update tanpa leader_id menghapus leader: %s", rec.Body)
	}

	budiSID := login(t, h, "budi", "rahasia")
	sitiSID := login(t, h, "siti", "rahasia")
	if rec := mustStatus(t, doJSON(h, "PUT", gBase, admin, `{"name":"backend-2","description":"baru","leader_id":`+itoa(siti)+`}`), http.StatusOK); !strings.Contains(rec.Body.String(), `"leader_username":"siti"`) {
		t.Errorf("ganti leader gagal: %s", rec.Body)
	}
	if loggedIn(h, budiSID) {
		t.Error("sesi leader lama tidak dicabut saat diganti")
	}
	rot := mustStatus(t, doJSON(h, "GET", "/api/auth/me", sitiSID, ""), http.StatusOK)
	if cookie(rot) == nil || cookie(rot).Value == sitiSID {
		t.Error("leader baru tidak merotasi session")
	}
	sitiSID = cookie(rot).Value
	assertJSONError(t, doJSON(h, "POST", gBase+"/members", admin, `{"user_id":`+itoa(budi)+`}`), http.StatusConflict)

	mustStatus(t, doJSON(h, "DELETE", gBase+"/members/"+itoa(siti), admin, ""), http.StatusNoContent)
	if loggedIn(h, sitiSID) || !strings.Contains(doJSON(h, "GET", "/api/groups", admin, "").Body.String(), `"leader_id":null`) {
		t.Error("keluarkan leader tidak cabut sesi / clear leader_id")
	}
	assertJSONError(t, doJSON(h, "DELETE", gBase+"/members/"+itoa(siti), admin, ""), http.StatusNotFound)

	_, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	dBase := "/api/db/" + itoa(id)
	for _, body := range []string{
		`{"name":"x","connection_url":"mysql://u:p@host/db"}`,
		`{"name":"x","connection_url":"postgres://host/db"}`,
		`{"name":"","connection_url":"postgres://u:p@host/db"}`,
	} {
		assertJSONError(t, doJSON(h, "POST", "/api/db", admin, body), http.StatusBadRequest)
	}
	if list := mustStatus(t, doJSON(h, "GET", "/api/db", admin, ""), http.StatusOK); strings.Contains(list.Body.String(), "postgres:") {
		t.Error("credential bocor di list db")
	}
	mustStatus(t, doJSON(h, "POST", dBase+"/test", admin, ""), http.StatusOK)

	aPath := dBase + "/access/" + itoa(gid)
	budiSID = login(t, h, "budi", "rahasia")
	for _, body := range []string{`{"access_level":"admin"}`, `{"access_level":""}`, `{}`} {
		assertJSONError(t, doJSON(h, "PUT", aPath, admin, body), http.StatusBadRequest)
	}
	assertJSONError(t, doJSON(h, "PUT", dBase+"/access/999999", admin, `{"access_level":"view_only"}`), http.StatusConflict)
	mustStatus(t, doJSON(h, "PUT", aPath, admin, `{"access_level":"view_only"}`), http.StatusNoContent)
	if !strings.Contains(doJSON(h, "GET", dBase+"/access", admin, "").Body.String(), `"access_level":"view_only"`) {
		t.Error("grant tidak tampil di list")
	}
	rec := mustStatus(t, doJSON(h, "GET", dbPath(id), budiSID, ""), http.StatusOK)
	if cookie(rec) == nil || cookie(rec).Value == budiSID {
		t.Error("grant akses tidak merotasi session")
	}
	budiSID = cookie(rec).Value
	assertJSONError(t, doJSON(h, "POST", dbPath(id)+"/migrations", budiSID, `{"name":"x","sql_up":"SELECT 1","sql_down":"SELECT 1"}`), http.StatusForbidden)

	mustStatus(t, doJSON(h, "PUT", aPath, admin, `{"access_level":"view_edit"}`), http.StatusNoContent)
	rec = mustStatus(t, doJSON(h, "POST", dbPath(id)+"/migrations", budiSID, `{"name":"x","sql_up":"SELECT 1","sql_down":"SELECT 1"}`), http.StatusCreated)
	if c := cookie(rec); c != nil {
		budiSID = c.Value
	}
	mustStatus(t, doJSON(h, "DELETE", aPath, admin, ""), http.StatusNoContent)
	if loggedIn(h, budiSID) {
		t.Error("cabut akses tidak mencabut session")
	}
	budiSID = login(t, h, "budi", "rahasia")
	assertJSONError(t, doJSON(h, "GET", dbPath(id), budiSID, ""), http.StatusForbidden)
	assertJSONError(t, doJSON(h, "DELETE", aPath, admin, ""), http.StatusNotFound)

	for _, r := range [][2]string{
		{"GET", "/api/users"}, {"POST", "/api/users"}, {"PUT", "/api/users/1/password"}, {"PUT", "/api/users/1/admin"}, {"DELETE", "/api/users/1"},
		{"GET", "/api/groups"}, {"POST", "/api/groups"}, {"PUT", "/api/groups/1"}, {"DELETE", "/api/groups/1"},
		{"GET", "/api/groups/1/members"}, {"POST", "/api/groups/1/members"}, {"DELETE", "/api/groups/1/members/1"},
		{"GET", "/api/db"}, {"POST", "/api/db"}, {"PUT", "/api/db/1"}, {"DELETE", "/api/db/1"}, {"POST", "/api/db/1/test"},
		{"GET", "/api/db/1/access"}, {"PUT", "/api/db/1/access/1"}, {"DELETE", "/api/db/1/access/1"},
	} {
		assertJSONError(t, doJSON(h, r[0], r[1], budiSID, `{"name":"x"}`), http.StatusForbidden)
		assertJSONError(t, doJSON(h, r[0], r[1], "", `{"name":"x"}`), http.StatusUnauthorized)
	}

	badBody := `{"name":"mati","host_label":"x","connection_url":"postgres://u:rahasiaDB@127.0.0.1:1/x"}`
	mustStatus(t, doJSON(h, "PUT", dBase, admin, badBody), http.StatusOK)
	fail := doJSON(h, "POST", dBase+"/test", admin, "")
	assertJSONError(t, fail, http.StatusBadGateway)
	if strings.Contains(fail.Body.String(), "rahasiaDB") {
		t.Error("password bocor di error test koneksi")
	}
	assertJSONError(t, doJSON(h, "PUT", "/api/db/999999", admin, badBody), http.StatusNotFound)

	id2 := addTarget(t, h, admin, dsn)
	var mid int64
	pool.QueryRow(t.Context(), `INSERT INTO migrations (database_id, version, name, sql, sql_down) VALUES ($1, 1, 'm1', 'SELECT 1', 'SELECT 1') RETURNING id`, id2).Scan(&mid)
	pool.Exec(t.Context(), `INSERT INTO migration_executions (migration_id, database_id, version, operation, sql_snapshot, status) VALUES ($1, $2, 1, 'up', 'SELECT 1', 'success')`, mid, id2)
	assertJSONError(t, doJSON(h, "DELETE", "/api/db/"+itoa(id2), admin, ""), http.StatusConflict)

	mustStatus(t, doJSON(h, "DELETE", dBase, admin, ""), http.StatusNoContent)
	assertJSONError(t, doJSON(h, "DELETE", dBase, admin, ""), http.StatusNotFound)

	mustStatus(t, doJSON(h, "DELETE", gBase, admin, ""), http.StatusNoContent)
	assertJSONError(t, doJSON(h, "DELETE", gBase, admin, ""), http.StatusNotFound)
	if loggedIn(h, budiSID) || !loggedIn(h, admin) {
		t.Error("hapus group gagal mencabut sesi anggota atau ikut mencabut admin")
	}
}
