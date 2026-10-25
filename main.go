package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/Ahmad-FP/K03-15-WBD/internal/access"
	"github.com/Ahmad-FP/K03-15-WBD/internal/admin"
	"github.com/Ahmad-FP/K03-15-WBD/internal/authentication"
	"github.com/Ahmad-FP/K03-15-WBD/internal/authorization"
	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/groups"
	"github.com/Ahmad-FP/K03-15-WBD/internal/migration"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
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
	adminPages := &admin.Handler{DB: pool}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", authHandler.LoginPage)
	mux.HandleFunc("POST /login", authHandler.Login)
	mux.Handle("POST /logout", sessions.Require(authHandler.Logout))
	mux.Handle("GET /{$}", sessions.Require(authHandler.Home))

	// Semua route admin harus melewati autentikasi session dan pemeriksaan role admin.
	admin := func(h http.HandlerFunc) http.Handler {
		protected := authorization.RequirePlatformAdmin(h)
		return sessions.Require(func(w http.ResponseWriter, r *http.Request) {
			protected.ServeHTTP(w, r)
		})
	}

	// cek session dulu, baru cek akses group ke database {id} di path
	dbAccess := func(required authorization.Level, h http.HandlerFunc) http.Handler {
		return sessions.Require(func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "id tidak valid", http.StatusBadRequest)
				return
			}
			authorization.RequireDatabaseAccess(pool, id, required, h).ServeHTTP(w, r)
		})
	}

	mux.Handle("GET /databases", sessions.Require(viewerHandler.List))
	mux.Handle("GET /databases/{id}", dbAccess(authorization.ViewOnly, viewerHandler.Overview))
	mux.Handle("GET /databases/{id}/tables/{schema}/{table}", dbAccess(authorization.ViewOnly, viewerHandler.Table))

	mux.Handle("GET /databases/{id}/migrations", dbAccess(authorization.ViewOnly, migrationHandler.List))
	mux.Handle("GET /databases/{id}/migrations/{mid}", dbAccess(authorization.ViewOnly, migrationHandler.Show))
	mux.Handle("GET /databases/{id}/migrations/new", dbAccess(authorization.ViewEdit, migrationHandler.New))
	mux.Handle("POST /databases/{id}/migrations", dbAccess(authorization.ViewEdit, migrationHandler.Create))
	mux.Handle("POST /databases/{id}/migrations/{mid}", dbAccess(authorization.ViewEdit, migrationHandler.Update))
	mux.Handle("POST /databases/{id}/migrations/{mid}/delete", dbAccess(authorization.ViewEdit, migrationHandler.Delete))
	mux.Handle("POST /databases/{id}/migrations/{mid}/run", dbAccess(authorization.ViewEdit, migrationHandler.Run))

	mux.Handle("GET /admin/groups", admin(adminPages.Groups))
	mux.Handle("GET /admin/databases", admin(adminPages.Databases))
	mux.Handle("GET /admin/users", admin(userHandler.List))
	mux.Handle("POST /admin/users", admin(userHandler.Create))
	mux.Handle("POST /admin/users/{id}/password", admin(userHandler.SetPassword))
	mux.Handle("POST /admin/users/{id}/admin", admin(userHandler.SetAdmin))
	mux.Handle("POST /admin/users/{id}/delete", admin(userHandler.Delete))

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

	return http.NewCrossOriginProtection().Handler(mux)
}

func main() {
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL belum diatur")
	}
	pool, err := pgxpool.New(ctx, dsn)
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
	log.Fatal(http.ListenAndServe(":8080", newServer(pool)))
}
