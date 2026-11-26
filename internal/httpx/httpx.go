package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		Error(w, http.StatusBadRequest, "JSON tidak valid")
		return false
	}
	return true
}

func ID(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil || n <= 0 {
		Error(w, http.StatusBadRequest, key+" tidak valid")
		return 0, false
	}
	return n, true
}

func IsPGCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func Collect[T any](ctx context.Context, q Queryer, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[T])
	if list == nil {
		list = []T{}
	}
	return list, err
}

func WriteList[T any](w http.ResponseWriter, r *http.Request, q Queryer, errMsg, sql string, args ...any) {
	list, err := Collect[T](r.Context(), q, sql, args...)
	if err != nil {
		Error(w, http.StatusInternalServerError, errMsg)
		return
	}
	JSON(w, http.StatusOK, list)
}

func ExecFound(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, notFoundMsg, errMsg, sql string, args ...any) bool {
	tag, err := pool.Exec(r.Context(), sql, args...)
	if err != nil {
		Error(w, http.StatusInternalServerError, errMsg)
		return false
	}
	if tag.RowsAffected() == 0 {
		Error(w, http.StatusNotFound, notFoundMsg)
		return false
	}
	return true
}
