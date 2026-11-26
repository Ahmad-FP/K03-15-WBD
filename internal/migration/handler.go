package migration

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct{ DB *pgxpool.Pool }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := List(r.Context(), h.DB, dbID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	m, err := Create(r.Context(), h.DB, dbID(r), session.Current(r).UserID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, m)
}

func (h *Handler) Show(w http.ResponseWriter, r *http.Request) {
	mid, ok := httpx.ID(w, r, "mid")
	if !ok {
		return
	}
	m, err := GetDetail(r.Context(), h.DB, dbID(r), mid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	mid, ok := httpx.ID(w, r, "mid")
	if !ok {
		return
	}
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	m, err := Update(r.Context(), h.DB, dbID(r), mid, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	mid, ok := httpx.ID(w, r, "mid")
	if !ok {
		return
	}
	if err := Delete(r.Context(), h.DB, dbID(r), mid); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListExecutions(w http.ResponseWriter, r *http.Request) {
	list, err := ListExecutions(r.Context(), h.DB, dbID(r), r.URL.Query().Get("hide_rollback") == "true")
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list)
}

func (h *Handler) ShowExecution(w http.ResponseWriter, r *http.Request) {
	eid, ok := httpx.ID(w, r, "eid")
	if !ok {
		return
	}
	exec, err := GetExecution(r.Context(), h.DB, dbID(r), eid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, exec)
}

func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) { h.submitWith(w, r, "mid", Submit) }
func (h *Handler) Rollback(w http.ResponseWriter, r *http.Request) {
	h.submitWith(w, r, "mid", Rollback)
}
func (h *Handler) Retry(w http.ResponseWriter, r *http.Request) { h.submitWith(w, r, "eid", Retry) }
func (h *Handler) Compensate(w http.ResponseWriter, r *http.Request) {
	h.submitWith(w, r, "eid", Compensate)
}

func (h *Handler) submitWith(w http.ResponseWriter, r *http.Request, key string, fn func(context.Context, *pgxpool.Pool, int64, int64, int64) (int64, error)) {
	targetID, ok := httpx.ID(w, r, key)
	if !ok {
		return
	}
	eid, err := fn(r.Context(), h.DB, dbID(r), targetID, session.Current(r).UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]int64{"execution_id": eid})
}

func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) {
	eid, ok := httpx.ID(w, r, "eid")
	if !ok {
		return
	}
	var p struct {
		Decision string `json:"decision"`
	}
	if !httpx.Decode(w, r, &p) {
		return
	}
	if err := Approve(r.Context(), h.DB, dbID(r), eid, session.Current(r).UserID, p.Decision); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	eid, ok := httpx.ID(w, r, "eid")
	if !ok {
		return
	}
	if err := Cancel(r.Context(), h.DB, dbID(r), eid); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Enqueue(w http.ResponseWriter, r *http.Request) {
	eid, ok := httpx.ID(w, r, "eid")
	if !ok {
		return
	}
	jobID, err := Enqueue(r.Context(), h.DB, dbID(r), eid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, struct {
		JobID       int64  `json:"job_id"`
		ExecutionID int64  `json:"execution_id"`
		Status      string `json:"status"`
	}{jobID, eid, "pending"})
}

func dbID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Error
	if errors.As(err, &e) {
		httpx.Error(w, e.Status, e.Msg)
		return
	}
	httpx.Error(w, http.StatusInternalServerError, "terjadi kesalahan di server, coba lagi")
}
