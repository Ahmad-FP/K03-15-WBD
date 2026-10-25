package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type migrationEnv struct {
	h      http.Handler
	id     int64
	editor string
	viewer string
	other  string
}

// server + satu database target, editor (view_edit), viewer (view_only), dan user tanpa akses
func setupMigration(t *testing.T) (migrationEnv, func(sql string) bool) {
	t.Helper()
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	target, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	grant(t, pool, addUser(t, pool, "editor", "rahasia"), id, "view_edit")
	grant(t, pool, addUser(t, pool, "viewer", "rahasia"), id, "view_only")
	addUser(t, pool, "orang", "rahasia")
	env := migrationEnv{h, id, login(t, h, "editor", "rahasia"), login(t, h, "viewer", "rahasia"), login(t, h, "orang", "rahasia")}
	exists := func(rel string) bool {
		var ok bool
		target.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", rel).Scan(&ok)
		return ok
	}
	return env, exists
}

// bikin draft lewat form dan balikin path detailnya
func createMigration(t *testing.T, e migrationEnv, name, sql string) string {
	t.Helper()
	rec := do(e.h, "POST", dbPath(e.id)+"/migrations", e.editor, url.Values{"name": {name}, "sql": {sql}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create %s: status %d %s", name, rec.Code, rec.Body)
	}
	return rec.Header().Get("Location")
}

func TestMigrationDraft(t *testing.T) {
	e, _ := setupMigration(t)
	path := createMigration(t, e, "tambah produk", "CREATE TABLE produk (id serial PRIMARY KEY);")

	body := do(e.h, "GET", path, e.editor, nil).Body.String()
	if !strings.Contains(body, "v1 tambah produk") || !strings.Contains(body, "draft") || !strings.Contains(body, "editor") {
		t.Errorf("detail draft tidak lengkap")
	}

	rec := do(e.h, "POST", dbPath(e.id)+"/migrations", e.editor, url.Values{"name": {"lain"}, "sql": {"SELECT 1"}})
	if rec.Code != http.StatusConflict {
		t.Errorf("draft kedua saat masih ada pending: status %d", rec.Code)
	}

	rec = do(e.h, "POST", path, e.editor, url.Values{"name": {"tambah produk"}, "sql": {"CREATE TABLE produk (id bigserial PRIMARY KEY);"}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(do(e.h, "GET", path, e.editor, nil).Body.String(), "bigserial") {
		t.Errorf("update draft: status %d", rec.Code)
	}

	rec = do(e.h, "POST", path, e.editor, url.Values{"name": {"x"}, "sql": {"CREATE TABLE a (id int); COMMIT;"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("SQL dengan COMMIT: status %d", rec.Code)
	}

	if rec := do(e.h, "POST", path+"/delete", e.editor, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: status %d", rec.Code)
	}
	if rec := do(e.h, "GET", path, e.editor, nil); rec.Code != http.StatusNotFound {
		t.Errorf("migration masih ada setelah dihapus: status %d", rec.Code)
	}
	createMigration(t, e, "pengganti", "SELECT 1")
}

func TestMigrationViewOnly(t *testing.T) {
	e, _ := setupMigration(t)
	path := createMigration(t, e, "tambah produk", "CREATE TABLE produk (id int);")
	base := dbPath(e.id) + "/migrations"

	for _, p := range []string{base, path} {
		if rec := do(e.h, "GET", p, e.viewer, nil); rec.Code != http.StatusOK {
			t.Errorf("viewer GET %s: status %d", p, rec.Code)
		}
	}
	if strings.Contains(do(e.h, "GET", path, e.viewer, nil).Body.String(), "<textarea") {
		t.Error("viewer dapat form edit")
	}

	form := url.Values{"name": {"x"}, "sql": {"DROP TABLE produk"}}
	denied := []struct{ method, path string }{
		{"GET", base + "/new"},
		{"POST", base},
		{"POST", path},
		{"POST", path + "/delete"},
	}
	for _, d := range denied {
		if rec := do(e.h, d.method, d.path, e.viewer, form); rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s: status %d", d.method, d.path, rec.Code)
		}
	}
	for _, p := range []string{base, path} {
		if rec := do(e.h, "GET", p, e.other, nil); rec.Code != http.StatusForbidden {
			t.Errorf("tanpa akses GET %s: status %d", p, rec.Code)
		}
	}
	if !strings.Contains(do(e.h, "GET", path, e.editor, nil).Body.String(), "CREATE TABLE produk (id int);") {
		t.Error("SQL berubah padahal request ditolak")
	}
}
