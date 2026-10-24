package authorization

import (
	"context"
	"net/http"

	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
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
		sess := session.Current(r)
		if sess.UserID <= 0 || !sess.Admin {
			http.Error(w, "akses ditolak", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RequireDatabaseAccess(pool *pgxpool.Pool, databaseID int64, required Level, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := session.Current(r)
		var levels []Level
		rows, err := pool.Query(r.Context(), `SELECT a.access_level
			FROM group_members gm JOIN group_database_access a ON a.group_id = gm.group_id
			WHERE gm.user_id = $1 AND a.database_id = $2`, sess.UserID, databaseID)
		if err != nil {
			http.Error(w, "gagal memeriksa hak akses", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var l Level
			if err := rows.Scan(&l); err != nil {
				http.Error(w, "gagal memeriksa hak akses", http.StatusInternalServerError)
				return
			}
			levels = append(levels, l)
		}
		if rows.Err() != nil {
			http.Error(w, "gagal memeriksa hak akses", http.StatusInternalServerError)
			return
		}
		effective := Effective(levels)
		if !Allows(effective, required) {
			http.Error(w, "akses database ditolak", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), effectiveAccessKey, effective)))
	})
}
