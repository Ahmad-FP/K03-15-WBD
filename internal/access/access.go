package access

import (
	"net/http"

	"github.com/Ahmad-FP/K03-15-WBD/internal/authorization"
	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB       *pgxpool.Pool
	Sessions *session.Store
}

type Grant struct {
	GroupID     int64               `json:"group_id"`
	GroupName   string              `json:"group_name"`
	AccessLevel authorization.Level `json:"access_level"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	dbID, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	httpx.WriteList[Grant](w, r, h.DB, "gagal mengambil akses database",
		`SELECT g.id, g.name, a.access_level
		FROM group_database_access a JOIN platform_groups g ON g.id = a.group_id
		WHERE a.database_id = $1 ORDER BY g.name`, dbID)
}

func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	dbID, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	groupID, ok := httpx.ID(w, r, "groupId")
	if !ok {
		return
	}
	var p struct {
		AccessLevel authorization.Level `json:"access_level"`
	}
	if !httpx.Decode(w, r, &p) {
		return
	}
	if err := authorization.ValidateLevel(p.AccessLevel); err != nil {
		httpx.Error(w, http.StatusBadRequest, "access_level harus view_only atau view_edit")
		return
	}
	_, err := h.DB.Exec(r.Context(), `INSERT INTO group_database_access (group_id, database_id, access_level)
		VALUES ($1, $2, $3)
		ON CONFLICT (group_id, database_id) DO UPDATE SET access_level = EXCLUDED.access_level, updated_at = now()`,
		groupID, dbID, p.AccessLevel)
	if httpx.IsPGCode(err, "23503") {
		httpx.Error(w, http.StatusConflict, "akses tidak dapat ditetapkan; group atau database tidak ditemukan")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal menetapkan akses database")
		return
	}
	h.Sessions.RotateGroup(r.Context(), groupID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	dbID, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	groupID, ok := httpx.ID(w, r, "groupId")
	if !ok {
		return
	}
	if !httpx.ExecFound(w, r, h.DB, "akses tidak ditemukan", "gagal mencabut akses",
		`DELETE FROM group_database_access WHERE group_id = $1 AND database_id = $2`, groupID, dbID) {
		return
	}
	h.Sessions.RevokeGroup(r.Context(), groupID)
	w.WriteHeader(http.StatusNoContent)
}
