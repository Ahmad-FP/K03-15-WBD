package main

import (
	"context"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/Ahmad-FP/K03-15-WBD/internal/access"
	"github.com/Ahmad-FP/K03-15-WBD/internal/authentication"
	"github.com/Ahmad-FP/K03-15-WBD/internal/authorization"
	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/groups"
	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/Ahmad-FP/K03-15-WBD/internal/migration"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/Ahmad-FP/K03-15-WBD/internal/users"
	"github.com/Ahmad-FP/K03-15-WBD/internal/viewer"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newServer(pool *pgxpool.Pool) http.Handler {
	sessions := &session.Store{DB: pool}
	authHandler := &authentication.Handler{DB: pool, Sessions: sessions}
	userHandler := &users.Handler{DB: pool, Sessions: sessions}
	groupHandler := &groups.Handler{DB: pool, Sessions: sessions}
	databaseHandler := &db.Handler{DB: pool}
	accessHandler := &access.Handler{DB: pool, Sessions: sessions}
	viewerHandler := &viewer.Handler{DB: pool}
	migrationHandler := &migration.Handler{DB: pool}

	mux := http.NewServeMux()

	admin := func(h http.HandlerFunc) http.Handler {
		return sessions.Require(authorization.RequirePlatformAdmin(h).ServeHTTP)
	}
	dbAccess := func(required authorization.Level, h http.HandlerFunc) http.Handler {
		return sessions.Require(func(w http.ResponseWriter, r *http.Request) {
			id, ok := httpx.ID(w, r, "id")
			if !ok {
				return
			}
			authorization.RequireDatabaseAccess(pool, id, required, h).ServeHTTP(w, r)
		})
	}

	mux.HandleFunc("POST /api/auth/login", authHandler.Login)
	mux.Handle("POST /api/auth/logout", sessions.Require(authHandler.Logout))
	mux.Handle("GET /api/auth/me", sessions.Require(authHandler.Me))

	mux.Handle("GET /api/users", admin(userHandler.List))
	mux.Handle("POST /api/users", admin(userHandler.Create))
	mux.Handle("PUT /api/users/{id}/password", admin(userHandler.SetPassword))
	mux.Handle("PUT /api/users/{id}/admin", admin(userHandler.SetAdmin))
	mux.Handle("DELETE /api/users/{id}", admin(userHandler.Delete))

	mux.Handle("GET /api/groups", admin(groupHandler.List))
	mux.Handle("POST /api/groups", admin(groupHandler.Create))
	mux.Handle("PUT /api/groups/{id}", admin(groupHandler.Update))
	mux.Handle("DELETE /api/groups/{id}", admin(groupHandler.Delete))
	mux.Handle("GET /api/groups/{id}/members", admin(groupHandler.ListMembers))
	mux.Handle("POST /api/groups/{id}/members", admin(groupHandler.AddMember))
	mux.Handle("DELETE /api/groups/{id}/members/{userId}", admin(groupHandler.RemoveMember))

	mux.Handle("GET /api/db", admin(databaseHandler.List))
	mux.Handle("POST /api/db", admin(databaseHandler.Create))
	mux.Handle("PUT /api/db/{id}", admin(databaseHandler.Update))
	mux.Handle("DELETE /api/db/{id}", admin(databaseHandler.Delete))
	mux.Handle("POST /api/db/{id}/test", admin(databaseHandler.Test))

	mux.Handle("GET /api/db/{id}/access", admin(accessHandler.List))
	mux.Handle("PUT /api/db/{id}/access/{groupId}", admin(accessHandler.Set))
	mux.Handle("DELETE /api/db/{id}/access/{groupId}", admin(accessHandler.Delete))

	mux.Handle("GET /api/databases", sessions.Require(viewerHandler.List))
	mux.Handle("GET /api/databases/{id}", dbAccess(authorization.ViewOnly, viewerHandler.Overview))
	mux.Handle("GET /api/databases/{id}/tables/{schema}/{table}", dbAccess(authorization.ViewOnly, viewerHandler.Table))

	mux.Handle("GET /api/databases/{id}/migrations", dbAccess(authorization.ViewOnly, migrationHandler.List))
	mux.Handle("POST /api/databases/{id}/migrations", dbAccess(authorization.ViewEdit, migrationHandler.Create))
	mux.Handle("GET /api/databases/{id}/migrations/{mid}", dbAccess(authorization.ViewOnly, migrationHandler.Show))
	mux.Handle("PUT /api/databases/{id}/migrations/{mid}", dbAccess(authorization.ViewEdit, migrationHandler.Update))
	mux.Handle("DELETE /api/databases/{id}/migrations/{mid}", dbAccess(authorization.ViewEdit, migrationHandler.Delete))

	mux.Handle("GET /api/databases/{id}/executions", dbAccess(authorization.ViewOnly, migrationHandler.ListExecutions))
	mux.Handle("GET /api/databases/{id}/executions/{eid}", dbAccess(authorization.ViewOnly, migrationHandler.ShowExecution))
	mux.Handle("POST /api/databases/{id}/migrations/{mid}/submit", dbAccess(authorization.ViewEdit, migrationHandler.Submit))
	mux.Handle("POST /api/databases/{id}/migrations/{mid}/rollback", dbAccess(authorization.ViewEdit, migrationHandler.Rollback))
	mux.Handle("POST /api/databases/{id}/executions/{eid}/retry", dbAccess(authorization.ViewEdit, migrationHandler.Retry))
	mux.Handle("POST /api/databases/{id}/executions/{eid}/compensate", dbAccess(authorization.ViewEdit, migrationHandler.Compensate))
	mux.Handle("POST /api/databases/{id}/executions/{eid}/approve", dbAccess(authorization.ViewOnly, migrationHandler.Approve))
	mux.Handle("POST /api/databases/{id}/executions/{eid}/cancel", dbAccess(authorization.ViewEdit, migrationHandler.Cancel))
	mux.Handle("POST /api/databases/{id}/executions/{eid}/enqueue", dbAccess(authorization.ViewEdit, migrationHandler.Enqueue))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotFound, "endpoint tidak ditemukan")
	})
	mux.Handle("/", spaHandler("frontend/dist"))

	return http.NewCrossOriginProtection().Handler(mux)
}

func spaHandler(dir string) http.Handler {
	fsys := os.DirFS(dir)
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if info, err := fs.Stat(fsys, p); err != nil || info.IsDir() {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				files.ServeHTTP(w, r2)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}

func main() {
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL belum diatur")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatal(err)
	}

	// --- telemetry (Track 3): log JSON ke stdout, span OTel ke stderr, trace setiap query platform ---
	slog.SetDefault(telemetry.NewLogger(os.Stdout))
	shutdownTracing, err := telemetry.Init("api", os.Stderr)
	if err != nil {
		log.Fatal("gagal menyiapkan tracing: ", err)
	}
	defer shutdownTracing(context.Background())
	telemetry.SetActorFunc(func(ctx context.Context) string {
		if s, ok := session.FromContext(ctx); ok {
			return s.Username
		}
		return ""
	})
	poolConfig.ConnConfig.Tracer = &telemetry.QueryTracer{DB: "platform"}
	// --- akhir telemetry ---

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatal("gagal terhubung ke database aplikasi: ", err)
	}
	if _, err := pool.Exec(ctx, db.Schema); err != nil {
		log.Fatal("gagal menyiapkan schema: ", err)
	}
	log.Fatal(http.ListenAndServe(":8080", telemetry.Middleware(newServer(pool))))
}
