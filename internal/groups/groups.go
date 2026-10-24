package groups

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB       *pgxpool.Pool
	Sessions *session.Store
}
type Group struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Member struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}
type payload struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	UserID      int64  `json:"user_id"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `SELECT id, name, description FROM platform_groups ORDER BY name`)
	if err != nil {
		http.Error(w, "gagal mengambil group", 500)
		return
	}
	defer rows.Close()
	result := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description); err != nil {
			http.Error(w, "gagal membaca group", 500)
			return
		}
		result = append(result, g)
	}
	if rows.Err() != nil {
		http.Error(w, "gagal membaca group", 500)
		return
	}
	writeJSON(w, 200, result)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var p payload
	if !decode(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	if p.Name == "" || len(p.Name) > 100 {
		http.Error(w, "nama group wajib diisi (maksimal 100 karakter)", 400)
		return
	}
	var g Group
	err := h.DB.QueryRow(r.Context(), `INSERT INTO platform_groups(name, description) VALUES($1,$2) RETURNING id,name,description`, p.Name, p.Description).Scan(&g.ID, &g.Name, &g.Description)
	if err != nil {
		http.Error(w, "group tidak dapat dibuat; nama mungkin sudah digunakan", 409)
		return
	}
	writeJSON(w, 201, g)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p payload
	if !decode(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 100 {
		http.Error(w, "nama group wajib diisi (maksimal 100 karakter)", 400)
		return
	}
	var g Group
	err := h.DB.QueryRow(r.Context(), `UPDATE platform_groups SET name=$2, description=$3, updated_at=now() WHERE id=$1 RETURNING id,name,description`, id, p.Name, strings.TrimSpace(p.Description)).Scan(&g.ID, &g.Name, &g.Description)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "group tidak ditemukan", 404)
		return
	}
	if err != nil {
		http.Error(w, "group tidak dapat diperbarui", 409)
		return
	}
	writeJSON(w, 200, g)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM platform_groups WHERE id=$1`, id)
	if err != nil {
		http.Error(w, "group tidak dapat dihapus", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "group tidak ditemukan", 404)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT u.id,u.username FROM group_members gm JOIN users u ON u.id=gm.user_id WHERE gm.group_id=$1 ORDER BY u.username`, id)
	if err != nil {
		http.Error(w, "gagal mengambil anggota", 500)
		return
	}
	defer rows.Close()
	result := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.Username); err != nil {
			http.Error(w, "gagal membaca anggota", 500)
			return
		}
		result = append(result, m)
	}
	if rows.Err() != nil {
		http.Error(w, "gagal membaca anggota", 500)
		return
	}
	writeJSON(w, 200, result)
}

func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	gid, ok := pathID(w, r)
	if !ok {
		return
	}
	var p payload
	if !decode(w, r, &p) {
		return
	}
	if p.UserID <= 0 {
		http.Error(w, "user_id tidak valid", 400)
		return
	}
	_, err := h.DB.Exec(r.Context(), `INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, gid, p.UserID)
	if err != nil {
		http.Error(w, "anggota tidak dapat ditambahkan; group/user mungkin tidak ada atau sudah menjadi anggota", 409)
		return
	}
	h.Sessions.RotateUser(r.Context(), p.UserID)
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	gid, ok := pathID(w, r)
	if !ok {
		return
	}
	uid, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil || uid <= 0 {
		http.Error(w, "userId tidak valid", 400)
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, gid, uid)
	if err != nil {
		http.Error(w, "gagal menghapus anggota", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "keanggotaan tidak ditemukan", 404)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "id tidak valid", 400)
		return 0, false
	}
	return id, true
}
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
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
