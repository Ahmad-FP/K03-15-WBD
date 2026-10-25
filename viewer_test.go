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

// daftarin database target lewat API admin, isinya schema baru di database test
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

// bikin schema kosong buat jadi database target, balikin pool + DSN-nya
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

// kasih user akses ke database lewat group baru
func grant(t *testing.T, pool *pgxpool.Pool, userID, dbID int64, level string) {
	t.Helper()
	ctx := context.Background()
	var gid int64
	if err := pool.QueryRow(ctx, "INSERT INTO platform_groups (name) VALUES ($1) RETURNING id", "g_"+rand.Text()).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	pool.Exec(ctx, "INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)", gid, userID)
	pool.Exec(ctx, "INSERT INTO group_database_access (group_id, database_id, access_level) VALUES ($1, $2, $3)", gid, dbID, level)
}

func dbPath(id int64) string {
	return "/databases/" + strconv.FormatInt(id, 10)
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

	rec := do(h, "GET", "/databases", login(t, h, "budi", "rahasia"), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), dbPath(id)) {
		t.Fatalf("budi: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "View + Edit") {
		t.Error("akses efektif harus ambil yang paling tinggi")
	}
	if strings.Contains(do(h, "GET", "/databases", login(t, h, "siti", "rahasia"), nil).Body.String(), dbPath(id)) {
		t.Error("siti lihat database yang bukan aksesnya")
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

	body := do(h, "GET", dbPath(id), sid, nil).Body.String()
	for _, want := range []string{"kota", "warga", "warga_dewasa", "(view)"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview tidak memuat %q", want)
		}
	}

	var schema string
	target.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema)
	rec := do(h, "GET", dbPath(id)+"/tables/"+schema+"/warga", sid, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("table: status %d %s", rec.Code, rec.Body)
	}
	body = rec.Body.String()
	for _, want := range []string{"umur", "integer", "warga_pkey", "PRIMARY KEY", "FOREIGN KEY", "CHECK", "idx_warga_nama"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail table tidak memuat %q", want)
		}
	}

	if rec := do(h, "GET", dbPath(id)+"/tables/"+schema+"/gaada", sid, nil); rec.Code != http.StatusNotFound {
		t.Errorf("table tidak ada: status %d", rec.Code)
	}
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
			if rec := do(h, "GET", path, sid, nil); rec.Code != http.StatusForbidden {
				t.Errorf("%s tanpa akses: status %d", path, rec.Code)
			}
		}
	}
	if rec := do(h, "GET", dbPath(id), "", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("tanpa session: status %d", rec.Code)
	}
}

func TestViewerConnectionFailure(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	id := addTarget(t, h, admin, "postgres://user:rahasiaDB@127.0.0.1:1/mati")
	budi := addUser(t, pool, "budi", "rahasia")
	grant(t, pool, budi, id, "view_only")

	rec := do(h, "GET", dbPath(id), login(t, h, "budi", "rahasia"), nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
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

	body := do(h, "GET", path, sid, nil).Body.String()
	if !strings.Contains(body, "<i>NULL</i>") || !strings.Contains(body, "barang-50") || strings.Contains(body, "barang-51<") {
		t.Error("halaman 1 harus berisi baris 1-50")
	}
	if !strings.Contains(body, `{&#34;stok&#34;: 1}`) || !strings.Contains(body, "?page=2") {
		t.Error("jsonb atau link halaman berikutnya tidak muncul")
	}

	body = do(h, "GET", path+"?page=2", sid, nil).Body.String()
	if !strings.Contains(body, "barang-60") || strings.Contains(body, "barang-50<") || strings.Contains(body, "?page=3") {
		t.Error("halaman 2 harus berisi baris 51-60 tanpa link berikutnya")
	}
}
