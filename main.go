package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/Ahmad-FP/K03-15-WBD/internal/access"
	"github.com/Ahmad-FP/K03-15-WBD/internal/authentication"
	"github.com/Ahmad-FP/K03-15-WBD/internal/authorization"
	"github.com/Ahmad-FP/K03-15-WBD/internal/database"
	"github.com/Ahmad-FP/K03-15-WBD/internal/groups"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/Ahmad-FP/K03-15-WBD/internal/users"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newServer(pool *pgxpool.Pool) http.Handler {
	sessions := &session.Store{DB: pool}
	authHandler := &authentication.Handler{DB: pool, Sessions: sessions}
	userHandler := &users.Handler{DB: pool, Sessions: sessions}
	groupHandler := &groups.Handler{DB: pool}
	databaseHandler := &database.Handler{DB: pool}
	accessHandler := &access.Handler{DB: pool}

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
	if _, err := pool.Exec(ctx, database.Schema); err != nil {
		log.Fatal("gagal menyiapkan schema: ", err)
	}
	log.Fatal(http.ListenAndServe(":8080", newServer(pool)))
}
