## Run

Prasyarat: Go >= 1.26.2, Node.js >= 20, PostgreSQL.

1. Build SPA frontend:

```sh
cd frontend && npm ci && npm run build && cd ..
```

2. Jalankan API server dan worker (pakai `DATABASE_URL` dan `DATABASE_CREDENTIALS_KEY` yang sama):

```sh
export DATABASE_URL=postgres://<user>:<password>@localhost:5432/app
export DATABASE_CREDENTIALS_KEY=$(openssl rand -hex 32)
go run .
```

Di terminal terpisah, jalankan worker:

```sh
export WORKER_CONCURRENCY=4
go run ./cmd/worker
```

Buka `http://localhost:8080` lalu login dengan akun default `admin` / `admin`.

Untuk development frontend dengan hot reload:

```sh
cd frontend && npm run dev
```

## Test

```sh
TEST_DATABASE_URL=postgres://user:password@localhost:5432/test go test -race ./...
```

## Session & Akses

- Session cookie `HttpOnly`, `SameSite=Lax`, `Secure` (HTTPS), proteksi CSRF cross-origin.
- Idle timeout 30 menit, absolute expiration 24 jam, rotasi berkala 15 menit (grace period 60 detik).
- Rotasi saat login, promosi admin, masuk group, perubahan akses/leader; dicabut saat logout, hapus user/group, cabut admin/akses, keluar group, atau demosi leader.
- Jika user tergabung di beberapa group untuk database yang sama, level tertinggi yang berlaku (`view_edit > view_only`). Platform Admin tidak otomatis punya akses data database.

## Database Viewer

Menampilkan schema, table/view, kolom, constraint, index, dan isi data (50 baris/halaman, urut primary key). Query berjalan dalam transaksi `READ ONLY` dengan `statement_timeout` 10 detik.

## Migration, Approval & Worker

- **Alur:** `Draft → Pending Review → Approved → Pending (Queue) → Running → Success / Failed / Partial / Partially Reverted`.
- **Approval:** setiap perubahan (`up`, `rollback`, `forward`, `retry`, `compensate`) wajib disetujui leader semua group yang memiliki akses ke database (`view_only` maupun `view_edit`) dan direvalidasi ulang oleh worker setelah lock diperoleh.
- **Immutability & History:** definisi migration yang pernah diajukan menjadi read-only. Semua attempt (berhasil maupun gagal) dicatat append-only di `migration_executions` beserta `sql_snapshot`, waktu, pengguna, dan status. Checkbox **Hide rollback** menyembunyikan pasangan eksekusi yang dibatalkan dan rollback suksesnya berdasarkan `execution_id` tanpa menghapus data riwayat.
- **Normal & Batch Mode:** `batch_size = 0` berjalan dalam 1 transaksi; `batch_size > 0` memproses per batch dengan cursor (`$1 = last_cursor`, `$2 = batch_size`) dan menyimpan checkpoint `_wbd_checkpoints` pada transaksi yang sama. Jika gagal di tengah, status menjadi `partial` dan dapat dilanjutkan (`retry`) atau dibatalkan per batch terbalik (`compensate` → `partially_reverted` bila terhenti).
- **Concurrency & Lock Recovery:** worker mengambil job via `FOR UPDATE SKIP LOCKED` dan memegang session advisory lock `pg_try_advisory_lock(3110, database_id)` lintas batch (1 migration per database, paralel untuk database berbeda). Karena lock bersifat session-level, jika worker mati/crash PostgreSQL melepas lock otomatis; worker lain memulihkan job `running` yatim saat start dan tiap 30 detik (`Recover`), lalu memeriksa `_wbd_checkpoints` sebelum eksekusi ulang agar tidak terjadi double commit.
- **Retry otomatis:** kegagalan sementara (koneksi putus, deadlock, lock unavailable) dicoba ulang hingga `max_attempts` (default 3) dengan backoff eksponensial (2s, 4s, … maks 1m) sebagai baris attempt baru.

## Benchmark

Dataset: tabel `items` berisi **2.000.000 baris** (`PostgreSQL 17`).

| Skenario | Hasil |
|---|---|
| `UPDATE items SET status = 'migrated'` tanpa batch | **16,0 detik** (2.000.000 baris) |
| Worker mode batch (`batch_size = 50000`) | **56,0 detik** (40 batch, 2.000.000 baris, `success`) |

Reproduksi:

```sh
WBD_BENCH_ROWS=2000000 WBD_BENCH_BATCH=50000 \
TEST_DATABASE_URL="postgres://user:pass@localhost:5432/test?sslmode=disable" \
go test ./internal/worker -run TestLargeDatasetBenchmark -v -timeout 30m
```

## Logging & Tracing

Log JSON terstruktur (`log/slog`) ditulis ke **stdout** dan span OpenTelemetry (console exporter) ke **stderr**. Credential, DSN, password SQL, dan session disamarkan (`[REDACTED]`). Trace context W3C (`traceparent`) dipropagasikan dari HTTP API → `migration_jobs` → worker → lock → batch/SQL query.

Contoh log JSON & span yang saling berkorelasi (`trace_id` sama):

```json
{"time":"2026-11-27T00:54:17.251974+07:00","level":"INFO","msg":"worker.job_claimed","trace_id":"ffb4ab5c023c8fc66624afcacf29a3fe","span_id":"4574f2d8784b6ad4","job_id":2,"execution_id":1,"database_id":1}
{"time":"2026-11-27T00:54:17.278412+07:00","level":"INFO","msg":"migration.attempt.end","event":"migration.attempt.end","database_id":1,"job_id":2,"migration_id":1,"execution_id":1,"req_id":1,"version":1,"operation":"up","attempt":1,"actor":"ani","status":"success","sql_snapshot":"WITH batch AS (...) SELECT id::text AS last_cursor FROM upd ORDER BY id","duration_ms":15.236,"batches_committed":3,"rows_processed":25,"trace_id":"ffb4ab5c023c8fc66624afcacf29a3fe","span_id":"62e0e5602970e080"}
```

```json
{"Name":"POST /api/databases/{id}/executions/{eid}/enqueue","SpanContext":{"TraceID":"ffb4ab5c023c8fc66624afcacf29a3fe","SpanID":"b80bbe78608f6b2d"}}
{"Name":"worker.job","SpanContext":{"TraceID":"ffb4ab5c023c8fc66624afcacf29a3fe","SpanID":"a5ee1058c1bcf8f9"},"Parent":{"TraceID":"ffb4ab5c023c8fc66624afcacf29a3fe","SpanID":"b80bbe78608f6b2d","Remote":true},"Attributes":[{"Key":"job.id","Value":{"Type":"INT64","Value":2}},{"Key":"execution.id","Value":{"Type":"INT64","Value":1}},{"Key":"database.id","Value":{"Type":"INT64","Value":1}}]}
{"Name":"worker.lock.held","SpanContext":{"TraceID":"ffb4ab5c023c8fc66624afcacf29a3fe","SpanID":"4574f2d8784b6ad4"},"Parent":{"TraceID":"ffb4ab5c023c8fc66624afcacf29a3fe","SpanID":"a5ee1058c1bcf8f9"}}
```
