package viewer

import (
	"embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Ahmad-FP/K03-15-WBD/internal/authorization"
	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.html
var files embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"pathEscape": url.PathEscape,
	"inc":        func(n int) int { return n + 1 },
	"dec":        func(n int) int { return n - 1 },
}).ParseFS(files, "*.html"))

type Handler struct{ DB *pgxpool.Pool }

type database struct {
	ID        int64
	Name      string
	HostLabel string
	Level     authorization.Level
}

type page struct {
	ID      int64
	Name    string
	CanEdit bool
}

// daftar database yang bisa dibuka user lewat group-nya
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `SELECT d.id, d.name, d.host_label, a.access_level
		FROM managed_databases d
		JOIN group_database_access a ON a.database_id = d.id
		JOIN group_members gm ON gm.group_id = a.group_id
		WHERE gm.user_id = $1 ORDER BY d.name, d.id`, session.Current(r).UserID)
	if err != nil {
		http.Error(w, "gagal mengambil daftar database", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var list []database
	levels := map[int64][]authorization.Level{}
	for rows.Next() {
		var d database
		if err := rows.Scan(&d.ID, &d.Name, &d.HostLabel, &d.Level); err != nil {
			http.Error(w, "gagal membaca daftar database", http.StatusInternalServerError)
			return
		}
		if _, seen := levels[d.ID]; !seen {
			list = append(list, d)
		}
		levels[d.ID] = append(levels[d.ID], d.Level)
	}
	if rows.Err() != nil {
		http.Error(w, "gagal membaca daftar database", http.StatusInternalServerError)
		return
	}
	for i := range list {
		list[i].Level = authorization.Effective(levels[list[i].ID])
	}
	tmpl.ExecuteTemplate(w, "databases.html", list)
}

// halaman utama satu database: daftar schema dan table
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	tx, done, ok := h.readOnly(w, r, p.ID)
	if !ok {
		return
	}
	defer done()
	schemas, err := listSchemas(r.Context(), tx)
	if err != nil {
		http.Error(w, "gagal membaca schema database target", http.StatusBadGateway)
		return
	}
	tmpl.ExecuteTemplate(w, "overview.html", struct {
		page
		Schemas []schemaInfo
	}{p, schemas})
}

// detail satu table: kolom, constraint, index, dan isi datanya
func (h *Handler) Table(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	tx, done, ok := h.readOnly(w, r, p.ID)
	if !ok {
		return
	}
	defer done()
	ctx := r.Context()
	schema, name := r.PathValue("schema"), r.PathValue("table")
	oid, kind, err := findTable(ctx, tx, schema, name)
	if errors.Is(err, errNoTable) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "gagal membaca table", http.StatusBadGateway)
		return
	}
	cols, err := listColumns(ctx, tx, oid)
	if err != nil {
		http.Error(w, "gagal membaca kolom", http.StatusBadGateway)
		return
	}
	cons, err := listConstraints(ctx, tx, oid)
	if err != nil {
		http.Error(w, "gagal membaca constraint", http.StatusBadGateway)
		return
	}
	idx, err := listIndexes(ctx, tx, oid)
	if err != nil {
		http.Error(w, "gagal membaca index", http.StatusBadGateway)
		return
	}
	num := parsePage(r.URL.Query().Get("page"))
	data, err := readRows(ctx, tx, schema, name, oid, num)
	if err != nil {
		http.Error(w, "gagal membaca data table", http.StatusBadGateway)
		return
	}
	tmpl.ExecuteTemplate(w, "table.html", struct {
		page
		Schema, Table, Kind string
		Columns             []column
		Constraints         []constraint
		Indexes             []index
		Data                tableData
		Page                int
	}{p, schema, name, kind, cols, cons, idx, data, num})
}

// data umum buat header halaman (nama db, boleh edit atau ngga)
func (h *Handler) page(w http.ResponseWriter, r *http.Request) (page, bool) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	name, err := db.Name(r.Context(), h.DB, id)
	if err != nil {
		db.ConnectError(w, err)
		return page{}, false
	}
	return page{ID: id, Name: name, CanEdit: authorization.EffectiveFromContext(r) == authorization.ViewEdit}, true
}

// buka transaksi read-only ke database target, viewer ga boleh ngubah apa-apa
func (h *Handler) readOnly(w http.ResponseWriter, r *http.Request, id int64) (pgx.Tx, func(), bool) {
	ctx := r.Context()
	conn, err := db.Connect(ctx, h.DB, id)
	if err != nil {
		db.ConnectError(w, err)
		return nil, nil, false
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err == nil {
		_, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '10s'")
	}
	if err != nil {
		conn.Close(ctx)
		http.Error(w, "gagal membuka transaksi di database target", http.StatusBadGateway)
		return nil, nil, false
	}
	return tx, func() {
		tx.Rollback(ctx)
		conn.Close(ctx)
	}, true
}
