package auth

import (
	"embed"
	"html/template"
	"net/http"

	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.html
var files embed.FS

var tmpl = template.Must(template.ParseFS(files, "*.html"))

type Handler struct {
	DB       *pgxpool.Pool
	Sessions *session.Store
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	tmpl.ExecuteTemplate(w, "login.html", nil)
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var id int64
	var hash string
	h.DB.QueryRow(r.Context(), "SELECT id, password_hash FROM users WHERE username = $1", r.FormValue("username")).Scan(&id, &hash)
	if !Check(hash, r.FormValue("password")) {
		w.WriteHeader(http.StatusUnauthorized)
		tmpl.ExecuteTemplate(w, "login.html", "Username atau password salah")
		return
	}
	h.Sessions.Start(w, r, id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	h.Sessions.End(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	tmpl.ExecuteTemplate(w, "home.html", session.Current(r))
}
