package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/Ahmad-FP/K03-15-WBD/internal/auth"
	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/session"
	"github.com/Ahmad-FP/K03-15-WBD/internal/users"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newServer(pool *pgxpool.Pool) http.Handler {
	sessions := &session.Store{DB: pool}
	a := &auth.Handler{DB: pool, Sessions: sessions}
	u := &users.Handler{DB: pool, Sessions: sessions}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", a.LoginPage)
	mux.HandleFunc("POST /login", a.Login)
	mux.Handle("POST /logout", sessions.Require(a.Logout))
	mux.Handle("GET /{$}", sessions.Require(a.Home))

	// #WIP: Blocked (Authorization)
	mux.Handle("GET /admin/users", sessions.Require(u.List))
	mux.Handle("POST /admin/users", sessions.Require(u.Create))
	mux.Handle("POST /admin/users/{id}/password", sessions.Require(u.SetPassword))
	mux.Handle("POST /admin/users/{id}/admin", sessions.Require(u.SetAdmin))
	mux.Handle("POST /admin/users/{id}/delete", sessions.Require(u.Delete))

	return http.NewCrossOriginProtection().Handler(mux)
}

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	if _, err := pool.Exec(ctx, db.Schema); err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.ListenAndServe(":8080", newServer(pool)))
}
