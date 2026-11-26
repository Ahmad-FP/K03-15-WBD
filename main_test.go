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
	rec := doJSON(h, "POST", "/api/auth/login", "", string(b))
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: status %d %s", username, rec.Code, rec.Body)
	}
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
	var id int64
	err := pool.QueryRow(context.Background(), "INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id",
		username, authentication.Hash(password)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

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

func TestLogin(t *testing.T) {
	h, _ := testServer(t)

	rec := doJSON(h, "POST", "/api/auth/login", "", `{"username":"admin","password":"admin"}`)
	c := cookie(rec)
	if rec.Code != http.StatusOK || c == nil {
		t.Fatalf("login: status %d, cookie %v, body %s", rec.Code, c, rec.Body)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Error("session cookie not HttpOnly/SameSite")
	}

	me := doJSON(h, "GET", "/api/auth/me", c.Value, "")
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"username":"admin"`) || !strings.Contains(me.Body.String(), `"is_admin":true`) {
		t.Fatalf("me: status %d body %s", me.Code, me.Body)
	}
	if strings.Contains(me.Body.String(), c.Value) {
		t.Error("session id in response body")
	}

	for _, creds := range [][2]string{{"admin", "pw-keliru"}, {"tidakada", "admin"}} {
		b, _ := json.Marshal(map[string]string{"username": creds[0], "password": creds[1]})
		rec := doJSON(h, "POST", "/api/auth/login", "", string(b))
		assertJSONError(t, rec, http.StatusUnauthorized)
		if cookie(rec) != nil {
			t.Errorf("login %v issued cookie", creds)
		}
		if strings.Contains(rec.Body.String(), creds[1]) {
			t.Errorf("login %v: password in response", creds)
		}
	}
}

func TestProtectedNeedsSession(t *testing.T) {
	h, pool := testServer(t)

	for _, sid := range []string{"", "tebakan"} {
		assertJSONError(t, doJSON(h, "GET", "/api/auth/me", sid, ""), http.StatusUnauthorized)
	}

	assertJSONError(t, doJSON(h, "POST", "/api/users", "", `{"username":"budi","password":"rahasia","is_admin":false}`), http.StatusUnauthorized)
	var n int
	pool.QueryRow(context.Background(), "SELECT count(*) FROM users WHERE username = 'budi'").Scan(&n)
	if n != 0 {
		t.Error("user created without a session")
	}
}

func TestLogout(t *testing.T) {
	h, _ := testServer(t)
	sid := login(t, h, "admin", "admin")

	rec := doJSON(h, "POST", "/api/auth/logout", sid, "")
	if rec.Code != http.StatusNoContent || cookie(rec).MaxAge >= 0 {
		t.Fatalf("logout: status %d, cookie %v", rec.Code, cookie(rec))
	}
	if loggedIn(h, sid) {
		t.Error("session still valid after logout")
	}
}

func TestExpiration(t *testing.T) {
	h, pool := testServer(t)
	ctx := context.Background()
	now := time.Now()

	sid := login(t, h, "admin", "admin")
	pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $1", now.Add(-session.IdleTimeout-time.Minute))
	if loggedIn(h, sid) {
		t.Error("idle session still valid")
	}

	sid = login(t, h, "admin", "admin")
	pool.Exec(ctx, "UPDATE sessions SET created_at = $1, rotated_at = $2 WHERE id = $3", now.Add(-session.MaxAge-time.Minute), now, sid)
	if loggedIn(h, sid) {
		t.Error("session past absolute expiration still valid")
	}
}

func TestRotation(t *testing.T) {
	h, pool := testServer(t)
	ctx := context.Background()
	old := login(t, h, "admin", "admin")
	var created time.Time
	pool.QueryRow(ctx, "SELECT created_at FROM sessions").Scan(&created)
	pool.Exec(ctx, "UPDATE sessions SET rotated_at = $1", time.Now().Add(-session.RotateEvery-time.Minute))

	rec := doJSON(h, "GET", "/api/auth/me", old, "")
	if rec.Code != http.StatusOK || cookie(rec) == nil || cookie(rec).Value == old {
		t.Fatalf("no rotation: status %d, cookie %v", rec.Code, cookie(rec))
	}
	current := cookie(rec).Value

	rec = doJSON(h, "GET", "/api/auth/me", current, "")
	if rec.Code != http.StatusOK || cookie(rec) != nil {
		t.Errorf("new id: status %d, cookie %v", rec.Code, cookie(rec))
	}

	rec = doJSON(h, "GET", "/api/auth/me", old, "")
	if rec.Code != http.StatusOK || cookie(rec) == nil || cookie(rec).Value != current {
		t.Errorf("old id in grace: status %d, cookie %v", rec.Code, cookie(rec))
	}

	var after time.Time
	pool.QueryRow(ctx, "SELECT created_at FROM sessions").Scan(&after)
	if !after.Equal(created) {
		t.Error("rotation moved absolute expiration")
	}

	pool.Exec(ctx, "UPDATE sessions SET prev_until = $1", time.Now().Add(-time.Second))
	if loggedIn(h, old) {
		t.Error("old id valid after grace period")
	}
	if !loggedIn(h, current) {
		t.Error("new id invalid after grace period")
	}
}

func TestCSRF(t *testing.T) {
	h, _ := testServer(t)
	sid := login(t, h, "admin", "admin")

	for _, path := range []string{"/api/auth/login", "/api/auth/logout"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"username":"admin","password":"admin"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || cookie(rec) != nil {
			t.Errorf("cross-site %s: status %d", path, rec.Code)
		}
	}
	if !loggedIn(h, sid) {
		t.Error("cross-site logout ended the session")
	}
}

func TestUserManagement(t *testing.T) {
	h, pool := testServer(t)
	ctx := context.Background()
	admin := login(t, h, "admin", "admin")

	rec := doJSON(h, "POST", "/api/users", admin, `{"username":"budi","password":"rahasia","is_admin":false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "rahasia") {
		t.Error("password leaked in create response")
	}
	id := jsonID(t, rec)

	list := doJSON(h, "GET", "/api/users", admin, "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"username":"budi"`) {
		t.Errorf("new user not listed: %d %s", list.Code, list.Body)
	}
	var hash string
	var isAdmin bool
	pool.QueryRow(ctx, "SELECT password_hash, is_admin FROM users WHERE id = $1", id).Scan(&hash, &isAdmin)
	if hash == "rahasia" || !authentication.Check(hash, "rahasia") {
		t.Error("password not stored as a hash")
	}
	if isAdmin {
		t.Error("is_admin=false should not create an admin user")
	}
	budi := login(t, h, "budi", "rahasia")

	assertJSONError(t, doJSON(h, "POST", "/api/users", admin, `{"username":"budi","password":"lain","is_admin":false}`), http.StatusConflict)
	assertJSONError(t, doJSON(h, "POST", "/api/users", admin, `{"username":"","password":"x"}`), http.StatusBadRequest)
	assertJSONError(t, doJSON(h, "POST", "/api/users", admin, `{"username":"cici","password":""}`), http.StatusBadRequest)

	path := "/api/users/" + strconv.FormatInt(id, 10)
	if rec := doJSON(h, "PUT", path+"/password", admin, `{"password":"baru"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("set password: status %d", rec.Code)
	}
	assertJSONError(t, doJSON(h, "PUT", path+"/password", admin, `{"password":""}`), http.StatusBadRequest)
	assertJSONError(t, doJSON(h, "PUT", "/api/users/999999/password", admin, `{"password":"x"}`), http.StatusNotFound)
	if doJSON(h, "POST", "/api/auth/login", "", `{"username":"budi","password":"rahasia"}`).Code != http.StatusUnauthorized {
		t.Error("old password still works")
	}
	login(t, h, "budi", "baru")

	if rec := doJSON(h, "DELETE", path, admin, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", rec.Code)
	}
	assertJSONError(t, doJSON(h, "DELETE", path, admin, ""), http.StatusNotFound)
	if loggedIn(h, budi) {
		t.Error("deleted user still has a session")
	}
	if doJSON(h, "POST", "/api/auth/login", "", `{"username":"budi","password":"baru"}`).Code != http.StatusUnauthorized {
		t.Error("deleted user can log in")
	}
}

func TestPrivilegeChange(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	id := addUser(t, pool, "budi", "rahasia")
	budi := login(t, h, "budi", "rahasia")
	path := "/api/users/" + strconv.FormatInt(id, 10) + "/admin"

	assertJSONError(t, doJSON(h, "PUT", path, admin, `{}`), http.StatusBadRequest)
	if rec := doJSON(h, "PUT", path, admin, `{"is_admin":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("promote: status %d", rec.Code)
	}
	rec := doJSON(h, "GET", "/api/auth/me", budi, "")
	if rec.Code != http.StatusOK || cookie(rec) == nil || cookie(rec).Value == budi {
		t.Fatalf("promotion did not rotate: status %d, cookie %v", rec.Code, cookie(rec))
	}
	if !strings.Contains(rec.Body.String(), `"is_admin":true`) {
		t.Error("promoted user not shown as admin")
	}
	budi = cookie(rec).Value

	if rec := doJSON(h, "PUT", path, admin, `{"is_admin":false}`); rec.Code != http.StatusNoContent {
		t.Fatalf("demote: status %d", rec.Code)
	}
	if loggedIn(h, budi) {
		t.Error("demoted user still has a session")
	}
}

func TestAccessGrantRotates(t *testing.T) {
	h, pool := testServer(t)
	ctx := context.Background()
	admin := login(t, h, "admin", "admin")
	id := addUser(t, pool, "budi", "rahasia")
	budi := login(t, h, "budi", "rahasia")
	pool.Exec(ctx, "INSERT INTO platform_groups (id, name) VALUES (1, 'dev')")
	pool.Exec(ctx, "INSERT INTO managed_databases (id, name, encrypted_dsn) VALUES (1, 'target', '')")

	steps := []struct {
		name, method, path, body string
		status                   int
	}{
		{"joining a group", "POST", "/api/groups/1/members", `{"user_id":` + strconv.FormatInt(id, 10) + `}`, http.StatusCreated},
		{"set leader", "PUT", "/api/groups/1", `{"name":"dev","description":"","leader_id":` + strconv.FormatInt(id, 10) + `}`, http.StatusOK},
		{"access grant", "PUT", "/api/db/1/access/1", `{"access_level":"view_edit"}`, http.StatusNoContent},
	}
	for _, s := range steps {
		if rec := doJSON(h, s.method, s.path, admin, s.body); rec.Code != s.status {
			t.Fatalf("%s: status %d %s", s.name, rec.Code, rec.Body)
		}
		rec := doJSON(h, "GET", "/api/auth/me", budi, "")
		if rec.Code != http.StatusOK || cookie(rec) == nil || cookie(rec).Value == budi {
			t.Fatalf("%s did not rotate: status %d, cookie %v", s.name, rec.Code, cookie(rec))
		}
		budi = cookie(rec).Value
	}
}
