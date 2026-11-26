package db

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/jackc/pgx/v5"
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

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `SELECT id, name, host_label, created_at FROM managed_databases ORDER BY name`)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal mengambil database")
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Database])
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal membaca database")
		return
	}
	if out == nil {
		out = []Database{}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	name, hostLabel, encrypted, ok := decodeDB(w, r)
	if !ok {
		return
	}
	var d Database
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO managed_databases (name, host_label, encrypted_dsn) VALUES ($1, $2, $3) RETURNING id, name, host_label, created_at`,
		name, hostLabel, encrypted).Scan(&d.ID, &d.Name, &d.HostLabel, &d.CreatedAt)
	if httpx.IsPGCode(err, "23505") {
		httpx.Error(w, http.StatusConflict, "database tidak dapat didaftarkan; nama sudah digunakan")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal mendaftarkan database")
		return
	}
	httpx.JSON(w, http.StatusCreated, d)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	name, hostLabel, encrypted, ok := decodeDB(w, r)
	if !ok {
		return
	}
	var d Database
	err := h.DB.QueryRow(r.Context(),
		`UPDATE managed_databases SET name = $2, host_label = $3, encrypted_dsn = $4, updated_at = now() WHERE id = $1 RETURNING id, name, host_label, created_at`,
		id, name, hostLabel, encrypted).Scan(&d.ID, &d.Name, &d.HostLabel, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "database tidak ditemukan")
		return
	}
	if httpx.IsPGCode(err, "23505") {
		httpx.Error(w, http.StatusConflict, "nama database sudah digunakan")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal memperbarui database")
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM managed_databases WHERE id = $1`, id)
	if httpx.IsPGCode(err, "23503") {
		httpx.Error(w, http.StatusConflict, "database tidak dapat dihapus karena masih memiliki riwayat migration atau relasi akses")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal menghapus database")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "database tidak ditemukan")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	conn, err := Connect(ctx, h.DB, id)
	if err != nil {
		status, msg := ConnectStatus(err)
		httpx.Error(w, status, msg)
		return
	}
	defer conn.Close(ctx)
	if err := conn.Ping(ctx); err != nil {
		httpx.Error(w, http.StatusBadGateway, "koneksi PostgreSQL gagal atau akses ditolak oleh server target")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "koneksi berhasil"})
}

func decodeDB(w http.ResponseWriter, r *http.Request) (name, hostLabel string, encrypted []byte, ok bool) {
	var p struct {
		Name          string `json:"name"`
		HostLabel     string `json:"host_label"`
		ConnectionURL string `json:"connection_url"`
	}
	if !httpx.Decode(w, r, &p) {
		return "", "", nil, false
	}
	name = strings.TrimSpace(p.Name)
	hostLabel = strings.TrimSpace(p.HostLabel)
	if err := validate(name, p.ConnectionURL); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return "", "", nil, false
	}
	encrypted, err := encryptDSN(p.ConnectionURL)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enkripsi credential belum dikonfigurasi dengan benar")
		return "", "", nil, false
	}
	return name, hostLabel, encrypted, true
}

func validate(name, dsn string) error {
	if name == "" {
		return errors.New("name wajib diisi")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return errors.New("connection_url harus berupa URL PostgreSQL yang valid")
	}
	if u.User == nil {
		return errors.New("connection_url harus menyertakan user database")
	}
	return nil
}
