package authentication

import (
	"errors"
	"net/http"

	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB       *pgxpool.Pool
	Sessions *session.Store
}

type userResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpx.Decode(w, r, &p) {
		return
	}
	var u userResponse
	var hash string
	err := h.DB.QueryRow(r.Context(),
		`SELECT id, username, password_hash, is_admin FROM users WHERE username = $1`, p.Username).
		Scan(&u.ID, &u.Username, &hash, &u.IsAdmin)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusInternalServerError, "gagal memeriksa akun")
		return
	}
	if err != nil || !Check(hash, p.Password) {
		httpx.Error(w, http.StatusUnauthorized, "username atau password salah")
		return
	}
	if err := h.Sessions.Start(w, r, u.ID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal membuat session")
		return
	}
	httpx.JSON(w, http.StatusOK, u)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	h.Sessions.End(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	s := session.Current(r)
	httpx.JSON(w, http.StatusOK, userResponse{ID: s.UserID, Username: s.Username, IsAdmin: s.Admin})
}
