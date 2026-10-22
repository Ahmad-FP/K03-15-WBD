package main

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/auth"
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
	pool, _ := pgxpool.NewWithConfig(ctx, cfg)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema+";"+db.Schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	})
	return newServer(pool), pool
}

func do(h http.Handler, method, path, sid string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	rec := do(h, "POST", "/login", "", url.Values{"username": {username}, "password": {password}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login %s: status %d", username, rec.Code)
	}
	return cookie(rec).Value
}

func loggedIn(h http.Handler, sid string) bool {
	return do(h, "GET", "/", sid, nil).Code == http.StatusOK
}

func addUser(t *testing.T, pool *pgxpool.Pool, username, password string) int64 {
	var id int64
	err := pool.QueryRow(context.Background(), "INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id", username, auth.Hash(password)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLogin(t *testing.T) {
	h, _ := testServer(t)

	rec := do(h, "POST", "/login", "", url.Values{"username": {"admin"}, "password": {"admin"}})
	c := cookie(rec)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || c == nil {
		t.Fatalf("login: status %d, location %q, cookie %v", rec.Code, rec.Header().Get("Location"), c)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Error("session cookie not HttpOnly/SameSite")
	}

	home := do(h, "GET", "/", c.Value, nil)
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "admin") {
		t.Fatalf("home: status %d", home.Code)
	}
	if strings.Contains(home.Body.String(), c.Value) {
		t.Error("session id in response body")
	}

	for _, creds := range [][2]string{{"admin", "pw-keliru"}, {"tidakada", "admin"}} {
		rec := do(h, "POST", "/login", "", url.Values{"username": {creds[0]}, "password": {creds[1]}})
		if rec.Code != http.StatusUnauthorized || cookie(rec) != nil {
			t.Errorf("login %v: status %d, cookie %v", creds, rec.Code, cookie(rec))
		}
		if strings.Contains(rec.Body.String(), creds[1]) {
			t.Errorf("login %v: password in response", creds)
		}
	}
}

func TestProtectedNeedsSession(t *testing.T) {
	h, pool := testServer(t)

	for _, sid := range []string{"", "tebakan"} {
		rec := do(h, "GET", "/", sid, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("sid %q: status %d", sid, rec.Code)
		}
	}

	do(h, "POST", "/admin/users", "", url.Values{"username": {"budi"}, "password": {"rahasia"}})
	var n int
	pool.QueryRow(context.Background(), "SELECT count(*) FROM users WHERE username = 'budi'").Scan(&n)
	if n != 0 {
		t.Error("user created without a session")
	}
}

func TestLogout(t *testing.T) {
	h, _ := testServer(t)
	sid := login(t, h, "admin", "admin")

	rec := do(h, "POST", "/logout", sid, nil)
	if rec.Code != http.StatusSeeOther || cookie(rec).MaxAge >= 0 {
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

	rec := do(h, "GET", "/", old, nil)
	if rec.Code != http.StatusOK || cookie(rec) == nil || cookie(rec).Value == old {
		t.Fatalf("no rotation: status %d, cookie %v", rec.Code, cookie(rec))
	}
	current := cookie(rec).Value

	rec = do(h, "GET", "/", current, nil)
	if rec.Code != http.StatusOK || cookie(rec) != nil {
		t.Errorf("new id: status %d, cookie %v", rec.Code, cookie(rec))
	}

	rec = do(h, "GET", "/", old, nil)
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

	for _, path := range []string{"/login", "/logout"} {
		req := httptest.NewRequest("POST", path, strings.NewReader("username=admin&password=admin"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

	rec := do(h, "POST", "/admin/users", admin, url.Values{"username": {"budi"}, "password": {"rahasia"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: status %d", rec.Code)
	}
	if !strings.Contains(do(h, "GET", "/admin/users", admin, nil).Body.String(), "budi") {
		t.Error("new user not listed")
	}
	var id int64
	var hash string
	pool.QueryRow(ctx, "SELECT id, password_hash FROM users WHERE username = 'budi'").Scan(&id, &hash)
	if hash == "rahasia" || !auth.Check(hash, "rahasia") {
		t.Error("password not stored as a hash")
	}
	budi := login(t, h, "budi", "rahasia")

	rec = do(h, "POST", "/admin/users", admin, url.Values{"username": {"budi"}, "password": {"lain"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("duplicate username: status %d", rec.Code)
	}

	path := "/admin/users/" + strconv.FormatInt(id, 10)
	do(h, "POST", path+"/password", admin, url.Values{"password": {"baru"}})
	if do(h, "POST", "/login", "", url.Values{"username": {"budi"}, "password": {"rahasia"}}).Code != http.StatusUnauthorized {
		t.Error("old password still works")
	}
	login(t, h, "budi", "baru")

	do(h, "POST", path+"/delete", admin, nil)
	if loggedIn(h, budi) {
		t.Error("deleted user still has a session")
	}
	if do(h, "POST", "/login", "", url.Values{"username": {"budi"}, "password": {"baru"}}).Code != http.StatusUnauthorized {
		t.Error("deleted user can log in")
	}
}

func TestPrivilegeChange(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	id := addUser(t, pool, "budi", "rahasia")
	budi := login(t, h, "budi", "rahasia")
	path := "/admin/users/" + strconv.FormatInt(id, 10) + "/admin"

	do(h, "POST", path, admin, url.Values{"admin": {"true"}})
	rec := do(h, "GET", "/", budi, nil)
	if rec.Code != http.StatusOK || cookie(rec) == nil || cookie(rec).Value == budi {
		t.Fatalf("promotion did not rotate: status %d, cookie %v", rec.Code, cookie(rec))
	}
	if !strings.Contains(rec.Body.String(), "/admin/users") {
		t.Error("promoted user not shown as admin")
	}
	budi = cookie(rec).Value

	do(h, "POST", path, admin, url.Values{"admin": {"false"}})
	if loggedIn(h, budi) {
		t.Error("demoted user still has a session")
	}
}
