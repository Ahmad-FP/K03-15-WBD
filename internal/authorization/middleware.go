package authorization

import (
	"context"
	"net/http"

	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type contextKey string

const effectiveAccessKey contextKey = "effective_database_access"

func EffectiveFromContext(r *http.Request) Level {
	v, _ := r.Context().Value(effectiveAccessKey).(Level)
	return v
}

func RequirePlatformAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !session.Current(r).Admin {
			httpx.Error(w, http.StatusForbidden, "akses ditolak")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RequireDatabaseAccess(pool *pgxpool.Pool, databaseID int64, required Level, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(), `SELECT a.access_level
			FROM group_members gm JOIN group_database_access a ON a.group_id = gm.group_id
			WHERE gm.user_id = $1 AND a.database_id = $2`, session.Current(r).UserID, databaseID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "gagal memeriksa hak akses")
			return
		}
		levels, err := pgx.CollectRows(rows, pgx.RowTo[Level])
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "gagal memeriksa hak akses")
			return
		}
		effective := Effective(levels)
		if !Allows(effective, required) {
			httpx.Error(w, http.StatusForbidden, "akses database ditolak")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), effectiveAccessKey, effective)))
	})
}
