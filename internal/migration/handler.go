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
	respond(w, http.StatusOK, list, err)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	m, err := Create(r.Context(), h.DB, dbID(r), session.Current(r).UserID, in)
	respond(w, http.StatusCreated, m, err)
}

func (h *Handler) Show(w http.ResponseWriter, r *http.Request) {
	mid, ok := httpx.ID(w, r, "mid")
	if !ok {
		return
	}
	m, err := GetDetail(r.Context(), h.DB, dbID(r), mid)
	respond(w, http.StatusOK, m, err)
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
	respond(w, http.StatusOK, m, err)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	mid, ok := httpx.ID(w, r, "mid")
	if !ok {
		return
	}
	respond(w, http.StatusNoContent, nil, Delete(r.Context(), h.DB, dbID(r), mid))
}

func (h *Handler) ListExecutions(w http.ResponseWriter, r *http.Request) {
	list, err := ListExecutions(r.Context(), h.DB, dbID(r), r.URL.Query().Get("hide_rollback") == "true")
	respond(w, http.StatusOK, list, err)
}

func (h *Handler) ShowExecution(w http.ResponseWriter, r *http.Request) {
	eid, ok := httpx.ID(w, r, "eid")
	if !ok {
		return
	}
	exec, err := GetExecution(r.Context(), h.DB, dbID(r), eid)
	respond(w, http.StatusOK, exec, err)
}

func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) { h.submitWith(w, r, "mid", Submit) }
func (h *Handler) Rollback(w http.ResponseWriter, r *http.Request) {
	h.submitWith(w, r, "mid", Rollback)
}
func (h *Handler) Retry(w http.ResponseWriter, r *http.Request) {
	h.submitWith(w, r, "eid", func(ctx context.Context, p *pgxpool.Pool, db, id, u int64) (int64, error) {
		return submitRecovery(ctx, p, db, id, u, "retry")
	})
}
func (h *Handler) Compensate(w http.ResponseWriter, r *http.Request) {
	h.submitWith(w, r, "eid", func(ctx context.Context, p *pgxpool.Pool, db, id, u int64) (int64, error) {
		return submitRecovery(ctx, p, db, id, u, "compensate")
	})
}

func (h *Handler) submitWith(w http.ResponseWriter, r *http.Request, key string, fn func(context.Context, *pgxpool.Pool, int64, int64, int64) (int64, error)) {
	targetID, ok := httpx.ID(w, r, key)
	if !ok {
		return
	}
	eid, err := fn(r.Context(), h.DB, dbID(r), targetID, session.Current(r).UserID)
	respond(w, http.StatusCreated, map[string]int64{"execution_id": eid}, err)
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
	respond(w, http.StatusNoContent, nil, Approve(r.Context(), h.DB, dbID(r), eid, session.Current(r).UserID, p.Decision))
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	eid, ok := httpx.ID(w, r, "eid")
	if !ok {
		return
	}
	respond(w, http.StatusNoContent, nil, Cancel(r.Context(), h.DB, dbID(r), eid))
}

func (h *Handler) Enqueue(w http.ResponseWriter, r *http.Request) {
	eid, ok := httpx.ID(w, r, "eid")
	if !ok {
		return
	}
	jobID, err := Enqueue(r.Context(), h.DB, dbID(r), eid)
	respond(w, http.StatusAccepted, struct {
		JobID       int64  `json:"job_id"`
		ExecutionID int64  `json:"execution_id"`
		Status      string `json:"status"`
	}{jobID, eid, "pending"}, err)
}

func dbID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func respond(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			httpx.Error(w, e.Status, e.Msg)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "terjadi kesalahan di server, coba lagi")
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	httpx.JSON(w, status, v)
}
