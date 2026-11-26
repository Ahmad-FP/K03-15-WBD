// Worker migration: mengambil job dari migration_jobs dan menjalankannya di database target.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Ahmad-FP/K03-15-WBD/internal/db"
	"github.com/Ahmad-FP/K03-15-WBD/internal/telemetry"
	"github.com/Ahmad-FP/K03-15-WBD/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// log JSON ke stdout, span OTel ke stderr supaya tidak tercampur
	slog.SetDefault(telemetry.NewLogger(os.Stdout))
	shutdown, err := telemetry.Init("worker", os.Stderr)
	if err != nil {
		fatal("gagal menyiapkan tracing", err)
	}
	defer shutdown(context.Background())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fatal("DATABASE_URL belum diatur", nil)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		fatal("DATABASE_URL tidak valid", err)
	}
	cfg.ConnConfig.Tracer = &telemetry.QueryTracer{DB: "platform"}
	concurrency := 4
	if n, err := strconv.Atoi(os.Getenv("WORKER_CONCURRENCY")); err == nil && n > 0 {
		concurrency = n
	}
	// tiap job yang jalan memegang satu koneksi khusus (pemegang lock), sisakan untuk query lain
	cfg.MaxConns = int32(concurrency*2 + 2)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fatal("gagal membuat pool database", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fatal("gagal terhubung ke database platform", err)
	}
	// aman dijalankan bersamaan dengan API: schema.sql diawali pg_advisory_xact_lock
	if _, err := pool.Exec(ctx, db.Schema); err != nil {
		fatal("gagal menyiapkan schema", err)
	}

	slog.Info("worker.started", "concurrency", concurrency)
	w := &worker.Worker{Pool: pool, Concurrency: concurrency, Handle: (&worker.Executor{Pool: pool}).Handle}
	w.Run(ctx)
	slog.Info("worker.stopped")
}

func fatal(msg string, err error) {
	if err != nil {
		slog.Error(msg, "error", err)
	} else {
		slog.Error(msg)
	}
	os.Exit(1)
}
