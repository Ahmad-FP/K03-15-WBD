package db

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound    = errors.New("database tidak ditemukan")
	ErrUnreachable = errors.New("database target tidak bisa dihubungi")
)

// buka koneksi ke database target pakai DSN yang disimpan admin
func Connect(ctx context.Context, app *pgxpool.Pool, id int64) (*pgx.Conn, error) {
	var encrypted []byte
	err := app.QueryRow(ctx, `SELECT encrypted_dsn FROM managed_databases WHERE id=$1`, id).Scan(&encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	dsn, err := decryptDSN(encrypted)
	if err != nil {
		return nil, err
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, ErrUnreachable
	}
	cfg.ConnectTimeout = 5 * time.Second
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, ErrUnreachable
	}
	return conn, nil
}

// ambil nama database buat judul halaman
func Name(ctx context.Context, app *pgxpool.Pool, id int64) (string, error) {
	var name string
	err := app.QueryRow(ctx, `SELECT name FROM managed_databases WHERE id=$1`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// ubah error koneksi jadi response yang ga bocorin detail credential
func ConnectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrUnreachable):
		http.Error(w, "gagal terhubung ke database target, coba hubungi admin", http.StatusBadGateway)
	default:
		http.Error(w, "terjadi kesalahan saat membuka database target", http.StatusInternalServerError)
	}
}
