package users

import (
	"embed"
	"html/template"
	"net/http"
	"strconv"

	"github.com/Ahmad-FP/K03-15-WBD/internal/auth"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed users.html
var files embed.FS

var tmpl = template.Must(template.ParseFS(files, "users.html"))

type Handler struct {
	DB       *pgxpool.Pool
	Sessions *session.Store
}

type user struct {
	ID       int64
	Username string
	Admin    bool
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rows, _ := h.DB.Query(r.Context(), "SELECT id, username, is_admin FROM users ORDER BY username")
	list, _ := pgx.CollectRows(rows, pgx.RowToStructByPos[user])
	tmpl.Execute(w, list)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	_, err := h.DB.Exec(r.Context(), "INSERT INTO users (username, password_hash, is_admin) VALUES ($1, $2, $3)",
		r.FormValue("username"), auth.Hash(r.FormValue("password")), r.FormValue("admin") != "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *Handler) SetPassword(w http.ResponseWriter, r *http.Request) {
	h.DB.Exec(r.Context(), "UPDATE users SET password_hash = $2 WHERE id = $1", userID(r), auth.Hash(r.FormValue("password")))
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *Handler) SetAdmin(w http.ResponseWriter, r *http.Request) {
	admin := r.FormValue("admin") == "true"
	h.DB.Exec(r.Context(), "UPDATE users SET is_admin = $2 WHERE id = $1", userID(r), admin)
	if admin {
		h.Sessions.RotateUser(r.Context(), userID(r))
	} else {
		h.Sessions.RevokeUser(r.Context(), userID(r))
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	h.DB.Exec(r.Context(), "DELETE FROM users WHERE id = $1", userID(r))
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func userID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}
