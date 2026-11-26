package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestViewer(t *testing.T) {
	h, pool := testServer(t)
	admin := login(t, h, "admin", "admin")
	target, dsn := targetSchema(t)
	id := addTarget(t, h, admin, dsn)
	budi := addUser(t, pool, "budi", "rahasia")
	addUser(t, pool, "siti", "rahasia")
	grant(t, pool, budi, id, "view_only")
	grant(t, pool, budi, id, "view_edit")

	budiSID := login(t, h, "budi", "rahasia")
	sitiSID := login(t, h, "siti", "rahasia")

	list := decode[[]struct {
		ID          int64  `json:"id"`
		AccessLevel string `json:"access_level"`
	}](t, mustStatus(t, doJSON(h, "GET", "/api/databases", budiSID, ""), http.StatusOK))
	if len(list) != 1 || list[0].ID != id || list[0].AccessLevel != "view_edit" {
		t.Fatalf("daftar db budi salah: %+v", list)
	}
	if strings.TrimSpace(mustStatus(t, doJSON(h, "GET", "/api/databases", sitiSID, ""), http.StatusOK).Body.String()) != "[]" {
		t.Error("siti seharusnya dapat []")
	}

	for _, sid := range []string{sitiSID, admin} {
		for _, p := range []string{dbPath(id), dbPath(id) + "/tables/public/x"} {
			assertJSONError(t, doJSON(h, "GET", p, sid, ""), http.StatusForbidden)
		}
	}
	assertJSONError(t, doJSON(h, "GET", dbPath(id), "", ""), http.StatusUnauthorized)

	_, err := target.Exec(context.Background(), `
		CREATE TABLE kota (id serial PRIMARY KEY, nama text NOT NULL UNIQUE);
		CREATE TABLE warga (id serial PRIMARY KEY, nama text NOT NULL, umur int CHECK (umur >= 0), kota_id int REFERENCES kota(id));
		CREATE INDEX idx_warga_nama ON warga(nama);
		CREATE VIEW warga_dewasa AS SELECT * FROM warga WHERE umur >= 17;
		CREATE TABLE barang (id int PRIMARY KEY, nama text, info jsonb);
		INSERT INTO barang SELECT g, 'barang-' || g, '{"stok":1}' FROM generate_series(1, 60) g;
		UPDATE barang SET nama = NULL WHERE id = 1;`)
	if err != nil {
		t.Fatal(err)
	}

	ov := mustStatus(t, doJSON(h, "GET", dbPath(id), budiSID, ""), http.StatusOK).Body.String()
	for _, want := range []string{`"can_edit":true`, `"kota"`, `"warga"`, `"warga_dewasa"`, `"view"`} {
		if !strings.Contains(ov, want) {
			t.Errorf("overview tidak memuat %q", want)
		}
	}

	var schema string
	target.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema)
	tbl := mustStatus(t, doJSON(h, "GET", dbPath(id)+"/tables/"+schema+"/warga", budiSID, ""), http.StatusOK).Body.String()
	for _, want := range []string{`"umur"`, `"integer"`, `"warga_pkey"`, `"PRIMARY KEY"`, `"FOREIGN KEY"`, `"CHECK"`, `"idx_warga_nama"`} {
		if !strings.Contains(tbl, want) {
			t.Errorf("detail table tidak memuat %q", want)
		}
	}
	assertJSONError(t, doJSON(h, "GET", dbPath(id)+"/tables/"+schema+"/gaada", budiSID, ""), http.StatusNotFound)
	assertJSONError(t, doJSON(h, "GET", dbPath(id)+"/tables/pg_catalog/pg_roles", budiSID, ""), http.StatusNotFound)

	type pageResp struct {
		Page    int     `json:"page"`
		HasNext bool    `json:"has_next"`
		Data    [][]any `json:"data"`
	}
	p1 := decode[pageResp](t, mustStatus(t, doJSON(h, "GET", dbPath(id)+"/tables/"+schema+"/barang", budiSID, ""), http.StatusOK))
	if p1.Page != 1 || len(p1.Data) != 50 || !p1.HasNext || p1.Data[0][1] != nil || p1.Data[49][1] != "barang-50" {
		t.Errorf("page 1 salah: len=%d hasNext=%v", len(p1.Data), p1.HasNext)
	}
	p2 := decode[pageResp](t, mustStatus(t, doJSON(h, "GET", dbPath(id)+"/tables/"+schema+"/barang?page=2", budiSID, ""), http.StatusOK))
	if p2.Page != 2 || len(p2.Data) != 10 || p2.HasNext || p2.Data[9][1] != "barang-60" {
		t.Errorf("page 2 salah: len=%d hasNext=%v", len(p2.Data), p2.HasNext)
	}

	badID := addTarget(t, h, admin, "postgres://user:rahasiaDB@127.0.0.1:1/mati")
	grant(t, pool, budi, badID, "view_only")
	fail := doJSON(h, "GET", dbPath(badID), login(t, h, "budi", "rahasia"), "")
	assertJSONError(t, fail, http.StatusBadGateway)
	if strings.Contains(fail.Body.String(), "rahasiaDB") {
		t.Error("password bocor di error viewer")
	}
}
