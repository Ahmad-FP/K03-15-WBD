package access

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/Ahmad-FP/K03-15-WBD/internal/authorization"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct{ DB *pgxpool.Pool }
type Grant struct {
	GroupID     int64               `json:"group_id"`
	GroupName   string              `json:"group_name"`
	AccessLevel authorization.Level `json:"access_level"`
}
type payload struct {
	AccessLevel authorization.Level `json:"access_level"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	dbID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT g.id,g.name,a.access_level FROM group_database_access a JOIN platform_groups g ON g.id=a.group_id WHERE a.database_id=$1 ORDER BY g.name`, dbID)
	if err != nil {
		http.Error(w, "gagal mengambil akses database", 500)
		return
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.GroupID, &g.GroupName, &g.AccessLevel); err != nil {
			http.Error(w, "gagal membaca akses database", 500)
			return
		}
		out = append(out, g)
	}
	if rows.Err() != nil {
		http.Error(w, "gagal membaca akses database", 500)
		return
	}
	writeJSON(w, 200, out)
}

func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	dbID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	groupID, ok := parseID(w, r, "groupId")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var p payload
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		http.Error(w, "JSON tidak valid", http.StatusBadRequest)
		return
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "body harus berisi tepat satu objek JSON", http.StatusBadRequest)
		return
	}
	if err := authorization.ValidateLevel(p.AccessLevel); err != nil {
		http.Error(w, "access_level harus view_only atau view_edit", 400)
		return
	}
	_, err := h.DB.Exec(r.Context(), `INSERT INTO group_database_access(group_id,database_id,access_level) VALUES($1,$2,$3) ON CONFLICT(group_id,database_id) DO UPDATE SET access_level=EXCLUDED.access_level,updated_at=now()`, groupID, dbID, p.AccessLevel)
	if err != nil {
		http.Error(w, "akses tidak dapat ditetapkan; group atau database mungkin tidak ditemukan", 409)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	dbID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	groupID, ok := parseID(w, r, "groupId")
	if !ok {
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM group_database_access WHERE group_id=$1 AND database_id=$2`, groupID, dbID)
	if err != nil {
		http.Error(w, "gagal mencabut akses", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "akses tidak ditemukan", 404)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseID(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	v := r.PathValue(key)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		http.Error(w, "id tidak valid", 400)
		return 0, false
	}
	return n, true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
