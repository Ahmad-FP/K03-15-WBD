package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func addTarget(t *testing.T, h http.Handler, admin, dsn string) int64 {
	t.Helper()
	t.Setenv("DATABASE_CREDENTIALS_KEY", strings.Repeat("ab", 32))
	body, _ := json.Marshal(map[string]string{"name": "target_" + rand.Text()[:6], "host_label": "test", "connection_url": dsn})
	rec := doJSON(h, "POST", "/api/db", admin, string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register target: status %d %s", rec.Code, rec.Body)
	}
	var d struct{ ID int64 }
	json.NewDecoder(rec.Body).Decode(&d)
	return d.ID
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

func dbPath(id int64) string {
	return "/api/databases/" + strconv.FormatInt(id, 10)
}

func TestDatabaseList(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	_, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	budi := addUser(t, pool, "budi", "rahasia")
	addUser(t, pool, "siti", "rahasia")
	grant(t, pool, budi, id, "view_only")
	grant(t, pool, budi, id, "view_edit")

	rec := doJSON(h, "GET", "/api/databases", login(t, h, "budi", "rahasia"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("budi: status %d", rec.Code)
	}
	var list []struct {
		ID          int64  `json:"id"`
		AccessLevel string `json:"access_level"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].ID != id || list[0].AccessLevel != "view_edit" {
		t.Fatalf("budi list = %s", rec.Body)
	}

	sitiRec := doJSON(h, "GET", "/api/databases", login(t, h, "siti", "rahasia"), "")
	if sitiRec.Code != http.StatusOK || strings.TrimSpace(sitiRec.Body.String()) != "[]" {
		t.Errorf("siti seharusnya dapat []: %d %s", sitiRec.Code, sitiRec.Body)
	}
}

func TestViewerSchema(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	target, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	budi := addUser(t, pool, "budi", "rahasia")
	grant(t, pool, budi, id, "view_only")
	_, err := target.Exec(context.Background(), `
		CREATE TABLE kota (id serial PRIMARY KEY, nama text NOT NULL UNIQUE);
		CREATE TABLE warga (
			id serial PRIMARY KEY,
			nama text NOT NULL,
			umur int CHECK (umur >= 0),
			kota_id int REFERENCES kota(id)
		);
		CREATE INDEX idx_warga_nama ON warga(nama);
		CREATE VIEW warga_dewasa AS SELECT * FROM warga WHERE umur >= 17;`)
	if err != nil {
		t.Fatal(err)
	}
	sid := login(t, h, "budi", "rahasia")

	ov := doJSON(h, "GET", dbPath(id), sid, "")
	if ov.Code != http.StatusOK {
		t.Fatalf("overview: status %d %s", ov.Code, ov.Body)
	}
	body := ov.Body.String()
	for _, want := range []string{`"can_edit":false`, `"kota"`, `"warga"`, `"warga_dewasa"`, `"view"`} {
		if !strings.Contains(body, want) {
			t.Errorf("overview tidak memuat %q: %s", want, body)
		}
	}

	var schema string
	target.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema)
	rec := doJSON(h, "GET", dbPath(id)+"/tables/"+schema+"/warga", sid, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("table: status %d %s", rec.Code, rec.Body)
	}
	body = rec.Body.String()
	for _, want := range []string{`"umur"`, `"integer"`, `"warga_pkey"`, `"PRIMARY KEY"`, `"FOREIGN KEY"`, `"CHECK"`, `"idx_warga_nama"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail table tidak memuat %q", want)
		}
	}

	assertJSONError(t, doJSON(h, "GET", dbPath(id)+"/tables/"+schema+"/gaada", sid, ""), http.StatusNotFound)
}

func TestViewerAccessDenied(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	_, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	addUser(t, pool, "siti", "rahasia")
	siti := login(t, h, "siti", "rahasia")

	for _, sid := range []string{siti, admin} {
		for _, path := range []string{dbPath(id), dbPath(id) + "/tables/public/x"} {
			assertJSONError(t, doJSON(h, "GET", path, sid, ""), http.StatusForbidden)
		}
	}
	assertJSONError(t, doJSON(h, "GET", dbPath(id), "", ""), http.StatusUnauthorized)
}

func TestViewerConnectionFailure(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	id := addTarget(t, h, admin, "postgres://user:rahasiaDB@127.0.0.1:1/mati")
	budi := addUser(t, pool, "budi", "rahasia")
	grant(t, pool, budi, id, "view_only")

	rec := doJSON(h, "GET", dbPath(id), login(t, h, "budi", "rahasia"), "")
	assertJSONError(t, rec, http.StatusBadGateway)
	if strings.Contains(rec.Body.String(), "rahasiaDB") {
		t.Error("password database target bocor di response")
	}
}

func TestViewerData(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	target, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	budi := addUser(t, pool, "budi", "rahasia")
	grant(t, pool, budi, id, "view_only")
	_, err := target.Exec(context.Background(), `
		CREATE TABLE barang (id int PRIMARY KEY, nama text, info jsonb);
		INSERT INTO barang SELECT g, 'barang-' || g, '{"stok": 1}' FROM generate_series(1, 60) g;
		UPDATE barang SET nama = NULL WHERE id = 1;`)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	target.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema)
	sid := login(t, h, "budi", "rahasia")
	path := dbPath(id) + "/tables/" + schema + "/barang"

	var resp struct {
		Page    int     `json:"page"`
		HasNext bool    `json:"has_next"`
		Data    [][]any `json:"data"`
	}
	rec := doJSON(h, "GET", path, sid, "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
		t.Fatalf("page 1: status %d %s", rec.Code, rec.Body)
	}
	if resp.Page != 1 || len(resp.Data) != 50 || !resp.HasNext || resp.Data[0][1] != nil || resp.Data[49][1] != "barang-50" {
		t.Errorf("page 1 tidak sesuai: len=%d hasNext=%v row0=%v row49=%v", len(resp.Data), resp.HasNext, resp.Data[0], resp.Data[49])
	}

	resp.Data = nil
	rec = doJSON(h, "GET", path+"?page=2", sid, "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
		t.Fatalf("page 2: status %d", rec.Code)
	}
	if resp.Page != 2 || len(resp.Data) != 10 || resp.HasNext || resp.Data[9][1] != "barang-60" {
		t.Errorf("page 2 tidak sesuai: len=%d hasNext=%v", len(resp.Data), resp.HasNext)
	}
}
