package admin

import (
	"context"
	"embed"
	"html/template"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.html
var files embed.FS

var tmpl = template.Must(template.ParseFS(files, "*.html"))

type Handler struct{ DB *pgxpool.Pool }

type option struct {
	ID   int64
	Name string
}

type group struct {
	ID          int64
	Name        string
	Description string
	Members     []option
}

type grant struct {
	GroupID   int64
	GroupName string
	Level     string
}

type database struct {
	ID        int64
	Name      string
	HostLabel string
	Grants    []grant
}

// halaman kelola group dan anggotanya
func (h *Handler) Groups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groups, err := collect(ctx, h.DB, `SELECT id, name, description FROM platform_groups ORDER BY name`,
		func(row pgx.CollectableRow) (group, error) {
			var g group
			return g, row.Scan(&g.ID, &g.Name, &g.Description)
		})
	if err != nil {
		http.Error(w, "gagal mengambil group", http.StatusInternalServerError)
		return
	}
	for i := range groups {
		groups[i].Members, err = options(ctx, h.DB, `SELECT u.id, u.username FROM group_members gm JOIN users u ON u.id = gm.user_id
			WHERE gm.group_id = $1 ORDER BY u.username`, groups[i].ID)
		if err != nil {
			http.Error(w, "gagal mengambil anggota group", http.StatusInternalServerError)
			return
		}
	}
	users, err := options(ctx, h.DB, `SELECT id, username FROM users ORDER BY username`)
	if err != nil {
		http.Error(w, "gagal mengambil user", http.StatusInternalServerError)
		return
	}
	tmpl.ExecuteTemplate(w, "groups.html", struct {
		Groups []group
		Users  []option
	}{groups, users})
}

// halaman kelola koneksi database dan akses group ke tiap database
func (h *Handler) Databases(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dbs, err := collect(ctx, h.DB, `SELECT id, name, host_label FROM managed_databases ORDER BY name`,
		func(row pgx.CollectableRow) (database, error) {
			var d database
			return d, row.Scan(&d.ID, &d.Name, &d.HostLabel)
		})
	if err != nil {
		http.Error(w, "gagal mengambil database", http.StatusInternalServerError)
		return
	}
	for i := range dbs {
		dbs[i].Grants, err = collect(ctx, h.DB, `SELECT g.id, g.name, a.access_level FROM group_database_access a
			JOIN platform_groups g ON g.id = a.group_id WHERE a.database_id = $1 ORDER BY g.name`,
			func(row pgx.CollectableRow) (grant, error) {
				var g grant
				return g, row.Scan(&g.GroupID, &g.GroupName, &g.Level)
			}, dbs[i].ID)
		if err != nil {
			http.Error(w, "gagal mengambil akses database", http.StatusInternalServerError)
			return
		}
	}
	groups, err := options(ctx, h.DB, `SELECT id, name FROM platform_groups ORDER BY name`)
	if err != nil {
		http.Error(w, "gagal mengambil group", http.StatusInternalServerError)
		return
	}
	tmpl.ExecuteTemplate(w, "databases.html", struct {
		Databases []database
		Groups    []option
	}{dbs, groups})
}

// pilihan id + nama buat dropdown
func options(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]option, error) {
	return collect(ctx, pool, sql, pgx.RowToStructByPos[option], args...)
}

// jalanin query terus kumpulin hasilnya jadi slice
func collect[T any](ctx context.Context, pool *pgxpool.Pool, sql string, fn pgx.RowToFunc[T], args ...any) ([]T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, fn)
}
