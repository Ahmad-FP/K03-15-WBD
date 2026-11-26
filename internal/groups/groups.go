package groups

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB       *pgxpool.Pool
	Sessions *session.Store
}

type Group struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	LeaderID       *int64 `json:"leader_id"`
	LeaderUsername string `json:"leader_username"`
}

type Member struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type optID struct {
	set bool
	val *int64
}

func (o *optID) UnmarshalJSON(b []byte) error {
	o.set = true
	if string(b) == "null" {
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	o.val = &n
	return nil
}

type groupPayload struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	LeaderID    optID  `json:"leader_id"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `SELECT g.id, g.name, g.description, g.leader_id, COALESCE(u.username, '')
		FROM platform_groups g LEFT JOIN users u ON u.id = g.leader_id ORDER BY g.name`)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal mengambil group")
		return
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Group])
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal membaca group")
		return
	}
	if result == nil {
		result = []Group{}
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var p groupPayload
	if !decodeGroup(w, r, &p) {
		return
	}
	var g Group
	err := h.DB.QueryRow(r.Context(), `
		WITH g AS (
			INSERT INTO platform_groups (name, description, leader_id) VALUES ($1, $2, $3) RETURNING *
		), m AS (
			INSERT INTO group_members (group_id, user_id)
			SELECT id, leader_id FROM g WHERE leader_id IS NOT NULL ON CONFLICT DO NOTHING
		)
		SELECT g.id, g.name, g.description, g.leader_id, COALESCE(u.username, '')
		FROM g LEFT JOIN users u ON u.id = g.leader_id`,
		p.Name, p.Description, p.LeaderID.val).
		Scan(&g.ID, &g.Name, &g.Description, &g.LeaderID, &g.LeaderUsername)
	if httpx.IsPGCode(err, "23505") || httpx.IsPGCode(err, "23503") {
		httpx.Error(w, http.StatusConflict, "group tidak dapat dibuat; nama sudah digunakan atau leader tidak ditemukan")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal membuat group")
		return
	}
	if g.LeaderID != nil {
		h.Sessions.RotateUser(r.Context(), *g.LeaderID)
	}
	httpx.JSON(w, http.StatusCreated, g)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	var p groupPayload
	if !decodeGroup(w, r, &p) {
		return
	}
	var g Group
	err := h.DB.QueryRow(r.Context(), `
		WITH g AS (
			UPDATE platform_groups
			SET name = $2, description = $3,
				leader_id = CASE WHEN $4 THEN $5 ELSE leader_id END,
				updated_at = now()
			WHERE id = $1
			RETURNING *
		), m AS (
			INSERT INTO group_members (group_id, user_id)
			SELECT id, leader_id FROM g WHERE $4 AND leader_id IS NOT NULL ON CONFLICT DO NOTHING
		)
		SELECT g.id, g.name, g.description, g.leader_id, COALESCE(u.username, '')
		FROM g LEFT JOIN users u ON u.id = g.leader_id`,
		id, p.Name, p.Description, p.LeaderID.set, p.LeaderID.val).
		Scan(&g.ID, &g.Name, &g.Description, &g.LeaderID, &g.LeaderUsername)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "group tidak ditemukan")
		return
	}
	if httpx.IsPGCode(err, "23505") || httpx.IsPGCode(err, "23503") {
		httpx.Error(w, http.StatusConflict, "group tidak dapat diperbarui; nama sudah digunakan atau leader tidak ditemukan")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal memperbarui group")
		return
	}
	if p.LeaderID.set && g.LeaderID != nil {
		h.Sessions.RotateUser(r.Context(), *g.LeaderID)
	}
	httpx.JSON(w, http.StatusOK, g)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	h.Sessions.RevokeGroup(r.Context(), id)
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM platform_groups WHERE id = $1`, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "group tidak dapat dihapus")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "group tidak ditemukan")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT u.id, u.username FROM group_members gm JOIN users u ON u.id = gm.user_id WHERE gm.group_id = $1 ORDER BY u.username`, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal mengambil anggota")
		return
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Member])
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal membaca anggota")
		return
	}
	if result == nil {
		result = []Member{}
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	gid, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	var p struct {
		UserID int64 `json:"user_id"`
	}
	if !httpx.Decode(w, r, &p) {
		return
	}
	if p.UserID <= 0 {
		httpx.Error(w, http.StatusBadRequest, "user_id tidak valid")
		return
	}
	_, err := h.DB.Exec(r.Context(), `INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, gid, p.UserID)
	if httpx.IsPGCode(err, "23505") || httpx.IsPGCode(err, "23503") {
		httpx.Error(w, http.StatusConflict, "anggota tidak dapat ditambahkan; group/user tidak ditemukan atau sudah menjadi anggota")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal menambahkan anggota")
		return
	}
	h.Sessions.RotateUser(r.Context(), p.UserID)
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	gid, ok := httpx.ID(w, r, "id")
	if !ok {
		return
	}
	uid, ok := httpx.ID(w, r, "userId")
	if !ok {
		return
	}
	var removed int
	err := h.DB.QueryRow(r.Context(), `
		WITH d AS (
			DELETE FROM group_members WHERE group_id = $1 AND user_id = $2 RETURNING user_id
		), u AS (
			UPDATE platform_groups SET leader_id = NULL, updated_at = now()
			WHERE id = $1 AND leader_id IN (SELECT user_id FROM d)
		)
		SELECT count(*) FROM d`, gid, uid).Scan(&removed)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "gagal menghapus anggota")
		return
	}
	if removed == 0 {
		httpx.Error(w, http.StatusNotFound, "keanggotaan tidak ditemukan")
		return
	}
	h.Sessions.RevokeUser(r.Context(), uid)
	w.WriteHeader(http.StatusNoContent)
}

func decodeGroup(w http.ResponseWriter, r *http.Request, p *groupPayload) bool {
	if !httpx.Decode(w, r, p) {
		return false
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	if p.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "nama group wajib diisi")
		return false
	}
	if p.LeaderID.val != nil && *p.LeaderID.val <= 0 {
		httpx.Error(w, http.StatusBadRequest, "leader_id tidak valid")
		return false
	}
	return true
}
