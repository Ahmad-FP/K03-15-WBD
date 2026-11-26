package viewer

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Ahmad-FP/K03-15-WBD/internal/authorization"
	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct{ DB *pgxpool.Pool }

type databaseItem struct {
	ID          int64               `json:"id"`
	Name        string              `json:"name"`
	HostLabel   string              `json:"host_label"`
	AccessLevel authorization.Level `json:"access_level"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	httpx.WriteList[databaseItem](w, r, h.DB, "gagal mengambil daftar database",
		`SELECT d.id, d.name, d.host_label,
			CASE WHEN bool_or(a.access_level = 'view_edit') THEN 'view_edit' ELSE 'view_only' END
		FROM managed_databases d
		JOIN group_database_access a ON a.database_id = d.id
		JOIN group_members gm ON gm.group_id = a.group_id
		WHERE gm.user_id = $1
		GROUP BY d.id
		ORDER BY d.name, d.id`, session.Current(r).UserID)
}

func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	name, err := db.Name(r.Context(), h.DB, id)
	if err != nil {
		status, msg := db.ConnectStatus(err)
		httpx.Error(w, status, msg)
		return
	}
	tx, done, ok := h.readOnly(w, r, id)
	if !ok {
		return
	}
	defer done()
	schemas, err := listSchemas(r.Context(), tx)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "gagal membaca schema database target")
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		ID      int64        `json:"id"`
		Name    string       `json:"name"`
		CanEdit bool         `json:"can_edit"`
		Schemas []schemaInfo `json:"schemas"`
	}{id, name, authorization.EffectiveFromContext(r) == authorization.ViewEdit, schemas})
}

func (h *Handler) Table(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	tx, done, ok := h.readOnly(w, r, id)
	if !ok {
		return
	}
	defer done()
	detail, err := loadTable(r.Context(), tx, r.PathValue("schema"), r.PathValue("table"), parsePage(r.URL.Query().Get("page")))
	if errors.Is(err, errNoTable) {
		httpx.Error(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "gagal membaca data table")
		return
	}
	httpx.JSON(w, http.StatusOK, detail)
}

func (h *Handler) readOnly(w http.ResponseWriter, r *http.Request, id int64) (pgx.Tx, func(), bool) {
	ctx := r.Context()
	conn, err := db.Connect(ctx, h.DB, id)
	if err != nil {
		status, msg := db.ConnectStatus(err)
		httpx.Error(w, status, msg)
		return nil, nil, false
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err == nil {
		_, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '10s'")
	}
	if err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
		status, msg := db.ConnectStatus(db.ErrUnreachable)
		httpx.Error(w, status, msg)
		return nil, nil, false
	}
	return tx, func() {
		bg := context.WithoutCancel(ctx)
		_ = tx.Rollback(bg)
		_ = conn.Close(bg)
	}, true
}
