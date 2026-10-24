package db

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var Schema string

type Handler struct{ DB *pgxpool.Pool }
type Database struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	HostLabel string    `json:"host_label"`
	CreatedAt time.Time `json:"created_at"`
}
type createPayload struct {
	Name          string `json:"name"`
	HostLabel     string `json:"host_label"`
	ConnectionURL string `json:"connection_url"`
}
type updatePayload struct {
	Name          string `json:"name"`
	HostLabel     string `json:"host_label"`
	ConnectionURL string `json:"connection_url"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `SELECT id,name,host_label,created_at FROM managed_databases ORDER BY name`)
	if err != nil {
		http.Error(w, "gagal mengambil database", 500)
		return
	}
	defer rows.Close()
	out := []Database{}
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.ID, &d.Name, &d.HostLabel, &d.CreatedAt); err != nil {
			http.Error(w, "gagal membaca database", 500)
			return
		}
		out = append(out, d)
	}
	if rows.Err() != nil {
		http.Error(w, "gagal membaca database", 500)
		return
	}
	writeJSON(w, 200, out)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var p createPayload
	if !decode(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	p.HostLabel = strings.TrimSpace(p.HostLabel)
	if err := validate(p.Name, p.ConnectionURL); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	encrypted, err := encryptDSN(p.ConnectionURL)
	if err != nil {
		http.Error(w, "enkripsi credential belum dikonfigurasi dengan benar", 500)
		return
	}
	var d Database
	err = h.DB.QueryRow(r.Context(), `INSERT INTO managed_databases(name,host_label,encrypted_dsn) VALUES($1,$2,$3) RETURNING id,name,host_label,created_at`, p.Name, p.HostLabel, encrypted).Scan(&d.ID, &d.Name, &d.HostLabel, &d.CreatedAt)
	if err != nil {
		http.Error(w, "database tidak dapat didaftarkan; nama mungkin sudah digunakan", 409)
		return
	}
	writeJSON(w, 201, d)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p updatePayload
	if !decode(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	p.HostLabel = strings.TrimSpace(p.HostLabel)
	if err := validate(p.Name, p.ConnectionURL); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	encrypted, err := encryptDSN(p.ConnectionURL)
	if err != nil {
		http.Error(w, "enkripsi credential belum dikonfigurasi dengan benar", 500)
		return
	}
	var d Database
	err = h.DB.QueryRow(r.Context(), `UPDATE managed_databases SET name=$2,host_label=$3,encrypted_dsn=$4,updated_at=now() WHERE id=$1 RETURNING id,name,host_label,created_at`, id, p.Name, p.HostLabel, encrypted).Scan(&d.ID, &d.Name, &d.HostLabel, &d.CreatedAt)
	if err != nil {
		http.Error(w, "database tidak ditemukan atau nama sudah digunakan", 404)
		return
	}
	writeJSON(w, 200, d)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM managed_databases WHERE id=$1`, id)
	if err != nil {
		http.Error(w, "database tidak dapat dihapus; periksa relasi akses", 409)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "database tidak ditemukan", 404)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var encrypted []byte
	err := h.DB.QueryRow(r.Context(), `SELECT encrypted_dsn FROM managed_databases WHERE id=$1`, id).Scan(&encrypted)
	if err != nil {
		http.Error(w, "database tidak ditemukan", 404)
		return
	}
	dsn, err := decryptDSN(encrypted)
	if err != nil {
		http.Error(w, "credential tidak dapat dibaca; periksa konfigurasi enkripsi", 500)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		http.Error(w, "format koneksi PostgreSQL tidak valid", 400)
		return
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		http.Error(w, "koneksi PostgreSQL gagal", 502)
		return
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		http.Error(w, "koneksi PostgreSQL gagal atau akses ditolak oleh server target", 502)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "message": "koneksi berhasil"})
}

func validate(name, dsn string) error {
	if name == "" || len(name) > 100 {
		return errText("name wajib diisi (maksimal 100 karakter)")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return errText("connection_url harus berupa URL PostgreSQL yang valid")
	}
	if u.User == nil {
		return errText("connection_url harus menyertakan user database")
	}
	return nil
}

type errText string

func (e errText) Error() string { return string(e) }
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		http.Error(w, "JSON tidak valid", http.StatusBadRequest)
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "body harus berisi tepat satu objek JSON", http.StatusBadRequest)
		return false
	}
	return true
}
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "id tidak valid", 400)
		return 0, false
	}
	return id, true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
