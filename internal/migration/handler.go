package migration

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/authorization"
	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.html
var files embed.FS

var tmpl = template.Must(template.ParseFS(files, "*.html"))

type Handler struct{ DB *pgxpool.Pool }

type page struct {
	DatabaseID int64
	Database   string
	CanEdit    bool
	Alert      string
}

// halaman riwayat migration satu database
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	list, err := List(r.Context(), h.DB, p.DatabaseID)
	if err != nil {
		http.Error(w, "gagal mengambil riwayat migration", http.StatusInternalServerError)
		return
	}
	tmpl.ExecuteTemplate(w, "list.html", struct {
		page
		Migrations []Migration
	}{p, list})
}

// form kosong buat nulis migration baru
func (h *Handler) New(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	tmpl.ExecuteTemplate(w, "new.html", struct {
		page
		Name, SQL string
	}{p, "", ""})
}

// simpen migration baru sebagai draft
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	name, sql := r.FormValue("name"), r.FormValue("sql")
	id, err := Create(r.Context(), h.DB, p.DatabaseID, session.Current(r).UserID, name, sql)
	if err != nil {
		p.Alert = message(err)
		w.WriteHeader(statusOf(err))
		tmpl.ExecuteTemplate(w, "new.html", struct {
			page
			Name, SQL string
		}{p, name, sql})
		return
	}
	http.Redirect(w, r, migrationPath(p.DatabaseID, id), http.StatusSeeOther)
}

// detail migration, kalau boleh edit sekalian ada form-nya
func (h *Handler) Show(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	m, ok := h.find(w, r, p)
	if !ok {
		return
	}
	h.renderDetail(w, p, m)
}

// simpen perubahan SQL migration yang belum sukses
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	m, ok := h.find(w, r, p)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	name, sql := r.FormValue("name"), r.FormValue("sql")
	if err := Update(r.Context(), h.DB, p.DatabaseID, m.ID, name, sql); err != nil {
		p.Alert = message(err)
		m.Name, m.SQL = name, sql
		w.WriteHeader(statusOf(err))
		h.renderDetail(w, p, m)
		return
	}
	http.Redirect(w, r, migrationPath(p.DatabaseID, m.ID), http.StatusSeeOther)
}

// buang migration yang belum sukses
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	m, ok := h.find(w, r, p)
	if !ok {
		return
	}
	if err := Delete(r.Context(), h.DB, p.DatabaseID, m.ID); err != nil {
		http.Error(w, message(err), statusOf(err))
		return
	}
	http.Redirect(w, r, migrationsPath(p.DatabaseID), http.StatusSeeOther)
}

// eksekusi migration ke database target, hasilnya keliatan di halaman detail
func (h *Handler) Run(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	m, ok := h.find(w, r, p)
	if !ok {
		return
	}
	if m.Status == Success {
		h.runFailed(w, p, m, http.StatusConflict, ErrLocked.Error())
		return
	}
	// ga ikut batal kalau browser ditutup di tengah jalan, biar riwayat tetap konsisten
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	conn, err := db.Connect(ctx, h.DB, p.DatabaseID)
	if err != nil {
		status, msg := db.ConnectStatus(err)
		h.runFailed(w, p, m, status, msg)
		return
	}
	defer conn.Close(ctx)
	err = Run(ctx, h.DB, conn, p.DatabaseID, m.ID, session.Current(r).UserID)
	var execErr *ExecError
	if err != nil && !errors.As(err, &execErr) {
		h.runFailed(w, p, m, statusOf(err), message(err))
		return
	}
	http.Redirect(w, r, migrationPath(p.DatabaseID, m.ID), http.StatusSeeOther)
}

// migration batal dijalanin, balik ke halaman detail sambil nampilin alasannya
func (h *Handler) runFailed(w http.ResponseWriter, p page, m Migration, status int, msg string) {
	p.Alert = msg
	w.WriteHeader(status)
	h.renderDetail(w, p, m)
}

func (h *Handler) renderDetail(w http.ResponseWriter, p page, m Migration) {
	tmpl.ExecuteTemplate(w, "detail.html", struct {
		page
		Migration
		Editable bool
	}{p, m, p.CanEdit && m.Status != Success})
}

// data header halaman: nama database dan level akses user
func (h *Handler) page(w http.ResponseWriter, r *http.Request) (page, bool) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	name, err := db.Name(r.Context(), h.DB, id)
	if err != nil {
		db.ConnectError(w, err)
		return page{}, false
	}
	return page{DatabaseID: id, Database: name, CanEdit: authorization.EffectiveFromContext(r) == authorization.ViewEdit}, true
}

// ambil migration dari {mid} di path
func (h *Handler) find(w http.ResponseWriter, r *http.Request, p page) (Migration, bool) {
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil || mid <= 0 {
		http.Error(w, "id migration tidak valid", http.StatusBadRequest)
		return Migration{}, false
	}
	m, err := Get(r.Context(), h.DB, p.DatabaseID, mid)
	if err != nil {
		http.Error(w, message(err), statusOf(err))
		return Migration{}, false
	}
	return m, true
}

// status HTTP yang cocok buat tiap error migration
func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrName), errors.Is(err, ErrEmptySQL), errors.Is(err, ErrTxControl):
		return http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrPending), errors.Is(err, ErrLocked):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// pesan error yang aman ditampilin, error internal ga ikut dibocorin
func message(err error) string {
	if statusOf(err) == http.StatusInternalServerError {
		return "terjadi kesalahan di server, coba lagi"
	}
	return err.Error()
}

func migrationsPath(dbID int64) string {
	return "/databases/" + strconv.FormatInt(dbID, 10) + "/migrations"
}

func migrationPath(dbID, id int64) string {
	return migrationsPath(dbID) + "/" + strconv.FormatInt(id, 10)
}
