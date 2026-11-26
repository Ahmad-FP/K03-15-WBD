package users

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Ahmad-FP/K03-15-WBD/internal/authentication"
	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB       *pgxpool.Pool
	Sessions *session.Store
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	httpx.WriteList[User](w, r, h.DB, "gagal mengambil daftar user",
		`SELECT id, username, is_admin FROM users ORDER BY username`)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if !httpx.Decode(w, r, &p) {
		return
	}
	p.Username = strings.TrimSpace(p.Username)
	if p.Username == "" || p.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "username dan password wajib diisi")
		return
	}
	hash, ok := hashPassword(w, p.Password)
	if !ok {
		return
	}
	var u User
	err := h.DB.QueryRow(r.Context(),
		`INSERT INTO users (username, password_hash, is_admin) VALUES ($1, $2, $3) RETURNING id, username, is_admin`,
		p.Username, hash, p.IsAdmin).
		Scan(&u.ID, &u.Username, &u.IsAdmin)
	if httpx.IsPGCode(err, "23505") {
		httpx.Error(w, http.StatusConflict, "username sudah digunakan")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal membuat user")
		return
	}
	httpx.JSON(w, http.StatusCreated, u)
}

func (h *Handler) SetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	var p struct {
		Password string `json:"password"`
	}
	if !httpx.Decode(w, r, &p) {
		return
	}
	if p.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "password wajib diisi")
		return
	}
	hash, ok := hashPassword(w, p.Password)
	if !ok {
		return
	}
	if !httpx.ExecFound(w, r, h.DB, "user tidak ditemukan", "gagal mengubah password",
		`UPDATE users SET password_hash = $2 WHERE id = $1`, id, hash) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) SetAdmin(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	var p struct {
		IsAdmin *bool `json:"is_admin"`
	}
	if !httpx.Decode(w, r, &p) {
		return
	}
	if p.IsAdmin == nil {
		httpx.Error(w, http.StatusBadRequest, "is_admin wajib diisi")
		return
	}
	if !httpx.ExecFound(w, r, h.DB, "user tidak ditemukan", "gagal mengubah status admin",
		`UPDATE users SET is_admin = $2 WHERE id = $1`, id, *p.IsAdmin) {
		return
	}
	if *p.IsAdmin {
		h.Sessions.RotateUser(r.Context(), id)
	} else {
		h.Sessions.RevokeUser(r.Context(), id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	if !httpx.ExecFound(w, r, h.DB, "user tidak ditemukan", "gagal menghapus user",
		`DELETE FROM users WHERE id = $1`, id) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func hashPassword(w http.ResponseWriter, password string) (string, bool) {
	hash, err := authentication.Hash(password)
	if errors.Is(err, authentication.ErrPasswordTooLong) {
		httpx.Error(w, http.StatusBadRequest, "password maksimal 72 byte")
		return "", false
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal memproses password")
		return "", false
	}
	return hash, true
}
