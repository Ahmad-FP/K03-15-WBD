package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"
)

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
