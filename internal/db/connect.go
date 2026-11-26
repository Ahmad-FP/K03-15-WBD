package db

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
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
	cfg.Tracer = &telemetry.QueryTracer{DB: "target"}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, ErrUnreachable
	}
	return conn, nil
}

// enkripsi DSN dengan kunci yang sama seperti saat admin menyimpan database (dipakai test worker)
func EncryptDSN(dsn string) ([]byte, error) {
	return encryptDSN(dsn)
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

// status dan pesan error koneksi yang ga bocorin detail credential
func ConnectStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, ErrUnreachable):
		return http.StatusBadGateway, "gagal terhubung ke database target, coba hubungi admin"
	}
	return http.StatusInternalServerError, "terjadi kesalahan saat membuka database target"
}

// tulis error koneksi sebagai response polos
func ConnectError(w http.ResponseWriter, err error) {
	status, msg := ConnectStatus(err)
	http.Error(w, msg, status)
}
